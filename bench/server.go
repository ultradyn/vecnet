//go:build linux

package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/XertroV/vecnet/internal/httpapi"
	"github.com/XertroV/vecnet/internal/hub"
	"github.com/XertroV/vecnet/internal/store"
)

type snapshot struct {
	RSS, MemoryCurrent                      int64
	CPUSeconds                              float64
	ActiveSSE, ActiveLongPoll               int64
	GOMAXPROCS, Cgroup, CPUQuota, MemoryMax string
}
type storage struct {
	DBBytes, WALBytes, PageBytes, BlobBytes int64
	Tables, Rows                            map[string]int64
	Method                                  string
}

func difference(a, b storage) storage {
	d := storage{DBBytes: b.DBBytes - a.DBBytes, WALBytes: b.WALBytes - a.WALBytes, PageBytes: b.PageBytes - a.PageBytes, BlobBytes: b.BlobBytes - a.BlobBytes, Tables: map[string]int64{}, Rows: map[string]int64{}, Method: b.Method}
	for k, v := range b.Tables {
		d.Tables[k] = v - a.Tables[k]
	}
	for k, v := range b.Rows {
		d.Rows[k] = v - a.Rows[k]
	}
	return d
}
func readSnapshot() snapshot {
	var s snapshot
	var r syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &r)
	s.CPUSeconds = float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
	b, _ := os.ReadFile("/proc/self/statm")
	f := strings.Fields(string(b))
	if len(f) > 1 {
		s.RSS, _ = strconv.ParseInt(f[1], 10, 64)
		s.RSS *= int64(os.Getpagesize())
	}
	s.GOMAXPROCS = os.Getenv("GOMAXPROCS")
	b, _ = os.ReadFile("/proc/self/cgroup")
	s.Cgroup = strings.TrimSpace(string(b))
	p := strings.TrimPrefix(s.Cgroup, "0::")
	b, _ = os.ReadFile(filepath.Join("/sys/fs/cgroup", p, "cpu.max"))
	s.CPUQuota = strings.TrimSpace(string(b))
	b, _ = os.ReadFile(filepath.Join("/sys/fs/cgroup", p, "memory.max"))
	s.MemoryMax = strings.TrimSpace(string(b))
	b, _ = os.ReadFile(filepath.Join("/sys/fs/cgroup", p, "memory.current"))
	s.MemoryCurrent, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return s
}
func serveHub(dir string) error {
	if dir == "" {
		return fmt.Errorf("temporary directory required")
	}
	defer os.RemoveAll(dir)
	db, e := store.Open(filepath.Join(dir, "hub.db"))
	if e != nil {
		return e
	}
	defer db.Close()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return e
	}
	defer ln.Close()
	cfg := hub.DefaultConfig()
	cfg.DataDir = dir
	cfg.PublicURL = "http://" + ln.Addr().String()
	h, e := hub.New(db, cfg, nil)
	if e != nil {
		return e
	}
	_, secret, _, e := h.EnsureOperator(context.Background(), "benchmark operator")
	if e != nil {
		return e
	}
	stack := httpapi.NewStack(h)
	deps := stack.Deps()
	// Only anonymous setup limiting is disabled; auth and production workers stay real.
	deps.Limiter = nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// systemd scope children may outlive the driver after terminal cancellation.
	// Stop and remove this private temp directory if the driver disappears.
	if parent, err := strconv.Atoi(os.Getenv("VECNET_BENCH_PARENT_PID")); err == nil && parent > 1 {
		go func() {
			tick := time.NewTicker(time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					if syscall.Kill(parent, 0) == syscall.ESRCH {
						cancel()
						return
					}
				}
			}
		}()
	}
	stop := make(chan struct{})
	deps.Stop = stop
	handler := httpapi.New(deps)
	var sse, poll atomic.Int64
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/bench/") {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+secret)) != 1 {
				w.WriteHeader(401)
				return
			}
			switch r.URL.Path {
			case "/bench/snapshot":
				s := readSnapshot()
				s.ActiveSSE = sse.Load()
				s.ActiveLongPoll = poll.Load()
				_ = json.NewEncoder(w).Encode(s)
			case "/bench/storage":
				// Keep control instrumentation separate from product HTTP paths.
				s, e := readStorage(db, dir)
				if e != nil {
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(s)
			case "/bench/waiters":
				_ = json.NewEncoder(w).Encode(map[string]int{"waiting": h.Log.Waiters(r.URL.Query().Get("peer"))})
			case "/bench/blob-cap":
				var v struct {
					Bytes int64 `json:"bytes"`
				}
				if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&v) != nil || v.Bytes < 1 || v.Bytes > 100<<20 {
					w.WriteHeader(400)
					return
				}
				h.Blobs.MaxSize = v.Bytes
				w.WriteHeader(204)
			case "/bench/stop":
				w.WriteHeader(204)
				go cancel()
			default:
				w.WriteHeader(404)
			}
			return
		}
		if r.URL.Path == "/v1/events" {
			counter := &poll
			if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
				counter = &sse
			}
			counter.Add(1)
			defer counter.Add(-1)
		}
		handler.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: mux}
	httpapi.ConfigureServer(srv)
	// StateClosed is emitted after a connection's handler has returned, even
	// when Shutdown times out and Close forcibly interrupts its socket.
	var connections sync.WaitGroup
	srv.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			connections.Add(1)
		case http.StateClosed, http.StateHijacked:
			connections.Done()
		}
	}
	workers := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() { defer close(workers); stack.Run(ctx) }()
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		close(stop)
		shutdown, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		if srv.Shutdown(shutdown) != nil {
			_ = srv.Close()
		}
	}()
	// Startup credentials travel only over the parent's private pipe.
	if e = json.NewEncoder(os.Stdout).Encode(map[string]string{"URL": cfg.PublicURL, "Secret": secret}); e != nil {
		cancel()
		<-workers
		<-shutdownDone
		return e
	}
	e = srv.Serve(ln)
	cancel()
	<-workers
	<-shutdownDone
	connections.Wait()
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}
func fileBytes(p string) (int64, error) {
	f, e := os.Stat(p)
	if e != nil {
		return 0, e
	}
	return f.Size(), nil
}
func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func readStorage(db *store.DB, dir string) (storage, error) {
	s := storage{Tables: map[string]int64{}, Rows: map[string]int64{}}
	var busy, log, checkpoint int
	if e := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpoint); e != nil {
		return s, e
	}
	if busy != 0 {
		return s, fmt.Errorf("checkpoint busy")
	}
	var pages, size int64
	if e := db.QueryRow("PRAGMA page_count").Scan(&pages); e != nil {
		return s, e
	}
	if e := db.QueryRow("PRAGMA page_size").Scan(&size); e != nil {
		return s, e
	}
	s.PageBytes = pages * size
	var statErr error
	s.DBBytes, statErr = fileBytes(filepath.Join(dir, "hub.db"))
	if statErr != nil {
		return s, statErr
	}
	s.WALBytes, statErr = fileBytes(filepath.Join(dir, "hub.db-wal"))
	if statErr != nil && !os.IsNotExist(statErr) {
		return s, statErr
	}
	if e := filepath.WalkDir(filepath.Join(dir, "blobs"), func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			n, err := fileBytes(p)
			if err != nil {
				return err
			}
			s.BlobBytes += n
		}
		return nil
	}); e != nil && !os.IsNotExist(e) {
		return s, e
	}
	rows, e := db.Query("SELECT name,sum(pgsize) FROM dbstat GROUP BY name")
	if e != nil && !strings.Contains(e.Error(), "no such table: dbstat") && !strings.Contains(e.Error(), "no such module: dbstat") {
		return s, e
	}
	if e == nil {
		s.Method = "dbstat allocated B-tree pages (tables and indexes separate)"
		for rows.Next() {
			var name string
			var n int64
			if e = rows.Scan(&name, &n); e != nil {
				rows.Close()
				return s, e
			}
			s.Tables[name] = n
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return s, e
		}
	}
	objects, e := db.Query("SELECT name,type FROM sqlite_master WHERE type IN ('table','index') ORDER BY name")
	if e != nil {
		return s, e
	}
	type object struct{ name, kind string }
	var names []object
	for objects.Next() {
		var o object
		if e = objects.Scan(&o.name, &o.kind); e != nil {
			objects.Close()
			return s, e
		}
		names = append(names, o)
	}
	e = objects.Err()
	objects.Close()
	if e != nil {
		return s, e
	}
	for _, o := range names {
		if o.kind != "table" {
			continue
		}
		var n int64
		if e = db.QueryRow("SELECT count(*) FROM " + quoteIdent(o.name)).Scan(&n); e != nil {
			return s, e
		}
		s.Rows[o.name] = n
	}
	if s.Method != "" {
		return s, nil
	}
	s.Method = "estimate: actual cell byte lengths + 16B/row + 2B/column; indexes sum key bytes + 16B/entry + 2B/key (not allocated pages)"
	for _, o := range names {
		pragma := "PRAGMA table_info(" + quoteIdent(o.name) + ")"
		if o.kind == "index" {
			pragma = "PRAGMA index_info(" + quoteIdent(o.name) + ")"
		}
		r, e := db.Query(pragma)
		if e != nil {
			return s, e
		}
		var cols []string
		for r.Next() {
			if o.kind == "table" {
				var cid, nn, pk int
				var name, typ string
				var def any
				e = r.Scan(&cid, &name, &typ, &nn, &def, &pk)
				cols = append(cols, name)
			} else {
				var seq, cid int
				var name *string
				e = r.Scan(&seq, &cid, &name)
				if name != nil {
					cols = append(cols, *name)
				}
			}
			if e != nil {
				r.Close()
				return s, e
			}
		}
		e = r.Err()
		r.Close()
		if e != nil {
			return s, e
		}
		if len(cols) == 0 {
			continue
		}
		table := o.name
		predicate := ""
		if o.kind == "index" {
			if e = db.QueryRow("SELECT tbl_name FROM sqlite_master WHERE name=?", o.name).Scan(&table); e != nil {
				return s, e
			}
			var definition *string
			if e = db.QueryRow("SELECT sql FROM sqlite_master WHERE name=?", o.name).Scan(&definition); e != nil {
				return s, e
			}
			if definition != nil {
				if m := regexp.MustCompile(`(?is)\bWHERE\b(.*)$`).FindStringSubmatch(*definition); len(m) > 0 {
					predicate = " WHERE " + m[1]
				}
			}
		}
		expr := []string{strconv.Itoa(16 + 2*len(cols))}
		for _, col := range cols {
			expr = append(expr, "coalesce(length(cast("+quoteIdent(col)+" AS BLOB)),0)")
		}
		var n int64
		if e = db.QueryRow("SELECT coalesce(sum(" + strings.Join(expr, "+") + "),0) FROM " + quoteIdent(table) + predicate).Scan(&n); e != nil {
			return s, e
		}
		s.Tables[o.name] = n
	}
	return s, nil
}
