//go:build linux

// Standalone real-hub resource benchmark; never imported by product binaries.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type client struct {
	base, operator string
	http           *http.Client
}
type peer struct{ ID, Secret string }

type httpFailure struct {
	Method, Path string
	Status       int
}

func (e *httpFailure) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.Status)
}

// Omit response bodies, credentials and request bodies from errors.
func (c *client) call(secret, method, path string, body, out any) error {
	return c.callStatus(secret, method, path, body, out, 0)
}

func (c *client) callStatus(secret, method, path string, body, out any, expected int) error {
	return c.callContext(context.Background(), secret, method, path, body, out, expected)
}

func (c *client) callContext(ctx context.Context, secret, method, path string, body, out any, expected int) error {
	var r io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		r = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if e != nil {
		return errors.New("invalid benchmark request")
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	resp, e := c.http.Do(req)
	if e != nil {
		return fmt.Errorf("%s request failed: %T", method, e)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 || (expected != 0 && resp.StatusCode != expected) {
		return &httpFailure{method, strings.Split(path, "?")[0], resp.StatusCode}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	_, e = io.Copy(io.Discard, resp.Body)
	return e
}
func (c *client) peer(name string) (peer, error) {
	var inv struct {
		ID    string `json:"peer_id"`
		Token string `json:"enrol_token"`
	}
	if e := c.call(c.operator, "POST", "/v1/admin/peers", map[string]any{"kind": "session", "name": name}, &inv); e != nil {
		return peer{}, e
	}
	var p struct {
		ID     string `json:"peer_id"`
		Secret string `json:"secret"`
	}
	if e := c.call("", "POST", "/v1/enrol", map[string]string{"token": inv.Token}, &p); e != nil {
		return peer{}, e
	}
	e := c.call(c.operator, "PUT", "/v1/admin/peers/"+p.ID+"/limits", map[string]int{"sends_per_min": 10000000, "threads_per_hour": 10000000}, nil)
	return peer{p.ID, p.Secret}, e
}
func (c *client) snapshot() (s snapshot, e error) {
	e = c.call(c.operator, "GET", "/bench/snapshot", nil, &s)
	return
}
func (c *client) storage() (s storage, e error) {
	e = c.call(c.operator, "GET", "/bench/storage", nil, &s)
	return
}

type running struct {
	*client
	cmd *exec.Cmd
	dir string
}

func startHub(mode string) (*running, error) {
	dir, e := os.MkdirTemp("", "vecnet-bench-")
	if e != nil {
		return nil, e
	}
	exe, e := os.Executable()
	if e != nil {
		os.RemoveAll(dir)
		return nil, e
	}
	args := []string{exe, "-serve", "-data-dir", dir}
	env := append(os.Environ(), "VECNET_SQLITE_SYNCHRONOUS=full", "VECNET_BENCH_PARENT_PID="+strconv.Itoa(os.Getpid()))
	uid := strconv.Itoa(os.Getuid())
	env = append(env, "XDG_RUNTIME_DIR=/run/user/"+uid, "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/"+uid+"/bus")
	scope := []string{"systemd-run", "--user", "--scope", "--quiet"}
	if mode == "limited" {
		env = append(env, "GOMAXPROCS=1")
		scope = append(scope, "-p", "CPUQuota=100%", "-p", "MemoryMax=512M")
	} else {
		env = append(env, "GOMAXPROCS="+strconv.Itoa(runtime.NumCPU()))
	}
	args = append(append(scope, "--"), args...)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = os.Stderr
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		os.RemoveAll(dir)
		return nil, e
	}
	if e = cmd.Start(); e != nil {
		os.RemoveAll(dir)
		return nil, e
	}
	var info struct{ URL, Secret string }
	ready := make(chan error, 1)
	go func() { ready <- json.NewDecoder(pipe).Decode(&info) }()
	select {
	case e = <-ready:
	case <-time.After(30 * time.Second):
		e = errors.New("hub startup timeout")
	}
	if e != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cmd.Wait()
		os.RemoveAll(dir)
		return nil, errors.New("benchmark hub did not start")
	}
	tr := &http.Transport{MaxIdleConns: 100, MaxIdleConnsPerHost: 100, IdleConnTimeout: time.Second}
	r := &running{&client{info.URL, info.Secret, &http.Client{Transport: tr, Timeout: 2 * time.Minute}}, cmd, dir}
	s, e := r.snapshot()
	if e != nil || (mode == "limited" && (s.GOMAXPROCS != "1" || s.CPUQuota != "100000 100000" || s.MemoryMax != "536870912")) {
		r.close()
		return nil, errors.New("hub resource limits could not be verified")
	}
	return r, nil
}
func (r *running) close() {
	stopClient := *r.client
	stopClient.http = &http.Client{Transport: r.http.Transport, Timeout: 2 * time.Second}
	_ = stopClient.call(r.operator, "POST", "/bench/stop", nil, nil)
	done := make(chan struct{})
	go func() { r.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
	r.http.CloseIdleConnections()
	os.RemoveAll(r.dir)
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}
func main() {
	var fdLimit syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &fdLimit) == nil && fdLimit.Cur < min(fdLimit.Max, 32768) {
		fdLimit.Cur = min(fdLimit.Max, 32768)
		if e := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &fdLimit); e != nil {
			fmt.Fprintln(os.Stderr, "cannot raise benchmark file descriptor limit")
			os.Exit(1)
		}
	}
	serve := flag.Bool("serve", false, "private child hub mode")
	dir := flag.String("data-dir", "", "child temporary directory")
	mode := flag.String("mode", "limited", "limited (1 CPU / 512 MiB cgroup) or unconstrained")
	suite := flag.String("suite", "all", "all, sends, idle, delivery, blobs, compaction")
	compactionFocus := flag.String("compaction-focus", "matrix", "compaction matrix or supplement (full query plans and legacy snapshot costs)")
	out := flag.String("out", "bench/results.json", "output JSON (no credentials)")
	duration := flag.Duration("duration", 5*time.Second, "minimum send scenario duration")
	minimum := flag.Int("min-sends", 200, "minimum accepted sends/scenario")
	workers := flag.Int("workers", 4, "send concurrency")
	idleWindow := flag.Duration("idle-window", 32*time.Second, "idle sampling window (>=30s captures heartbeat and poll renewal)")
	counts := flag.String("idle-counts", "0,100,1000,5000", "idle concurrency counts")
	repeats := flag.Int("blob-repeats", 3, "uploads/downloads per size")
	flag.Parse()
	if *serve {
		if e := serveHub(*dir); e != nil {
			fmt.Fprintln(os.Stderr, "benchmark child failed")
			os.Exit(1)
		}
		return
	}
	if (*mode != "limited" && *mode != "unconstrained") || *workers < 1 || *minimum < 1 || *duration <= 0 || *repeats < 1 || *idleWindow <= 0 {
		fmt.Fprintln(os.Stderr, "invalid benchmark limits")
		os.Exit(2)
	}
	if *suite == "compaction" {
		if err := measureCompaction(*minimum, *out, *compactionFocus); err != nil {
			fmt.Fprintln(os.Stderr, "compaction failed:", err)
			os.Exit(1)
		}
		return
	}
	if *suite != "all" && *suite != "sends" && *suite != "idle" && *suite != "delivery" && *suite != "blobs" {
		fmt.Fprintln(os.Stderr, "invalid benchmark suite")
		os.Exit(2)
	}
	var idleCounts []int
	for _, v := range strings.Split(*counts, ",") {
		n, e := strconv.Atoi(v)
		if e != nil || n < 0 {
			fmt.Fprintln(os.Stderr, "invalid idle counts")
			os.Exit(2)
		}
		idleCounts = append(idleCounts, n)
	}
	result := map[string]any{"mode": *mode, "started_utc": time.Now().UTC(), "go": runtime.Version(), "client_gomaxprocs": runtime.GOMAXPROCS(0), "duration_seconds": duration.Seconds(), "minimum_sends": *minimum, "workers": *workers, "host": hostInfo()}
	run := func(name string, fn func(*client) (any, error)) error {
		fmt.Fprintln(os.Stderr, "benchmark:", *mode, name)
		r, e := startHub(*mode)
		if e != nil {
			return e
		}
		defer r.close()
		v, e := fn(r.client)
		result[name] = v
		if e != nil {
			result["error"] = e.Error()
		}
		if err := writeJSON(*out, result); err != nil {
			return err
		}
		return e
	}
	var err error
	if *suite == "all" || *suite == "sends" {
		var rows []any
		for _, size := range []int{200, 2048, 16384} {
			for _, fanout := range []int{1, 2, 5, 20} {
				if err != nil {
					break
				}
				r, e := startHub(*mode)
				if e != nil {
					err = e
					break
				}
				fmt.Fprintf(os.Stderr, "benchmark: %s sends %dB fanout=%d\n", *mode, size, fanout)
				v, e := measureSends(r.client, size, fanout, *workers, *duration, *minimum)
				r.close()
				rows = append(rows, v)
				result["sends"] = rows
				if e != nil {
					result["error"] = e.Error()
					err = e
				}
				if e = writeJSON(*out, result); e != nil {
					err = e
				}
			}
		}
	}
	if err == nil && (*suite == "all" || *suite == "delivery") {
		err = run("delivery", measureDelivery)
	}
	if err == nil && (*suite == "all" || *suite == "idle") {
		var idleReport map[string]any
		var rows []idleBlobIdleRow
		for _, protocol := range []string{"sse", "longpoll"} {
			r, e := startHub(*mode)
			if e != nil {
				err = e
				break
			}
			v, e := measureIdle(r.client, idleCounts, *idleWindow, protocol)
			r.close()
			if v != nil {
				part := v.(map[string]any)
				if idleReport == nil {
					idleReport = part
				}
				rows = append(rows, part["samples"].([]idleBlobIdleRow)...)
				idleReport["samples"] = rows
				result["idle"] = idleReport
			}
			if e != nil {
				result["error"] = e.Error()
				err = e
			}
			if e = writeJSON(*out, result); e != nil {
				err = e
			}
			if err != nil {
				break
			}
		}
	}
	if err == nil && (*suite == "all" || *suite == "blobs") {
		err = run("blobs", func(c *client) (any, error) { return measureBlobs(c, *repeats) })
	}
	if err != nil {
		result["error"] = err.Error()
		_ = writeJSON(*out, result)
		fmt.Fprintln(os.Stderr, "benchmark failed:", err)
		os.Exit(1)
	}
}
func hostInfo() map[string]string {
	m := map[string]string{}
	for _, p := range []string{"/proc/cpuinfo", "/proc/meminfo", "/proc/loadavg", "/proc/version"} {
		b, e := os.ReadFile(p)
		if e != nil {
			continue
		}
		s := string(b)
		if p == "/proc/cpuinfo" {
			for _, line := range strings.Split(s, "\n") {
				if strings.HasPrefix(line, "model name") {
					s = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
					break
				}
			}
		}
		m[p] = s
	}
	return m
}
