package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type deliverySource struct {
	Config   string `json:"config"`
	Bindings string `json:"bindings"`
}
type deliveryRegistry struct {
	Version int              `json:"version"`
	Sources []deliverySource `json:"sources"`
}
type deliveryDescriptor struct {
	Version int    `json:"version"`
	Hub     string `json:"hub"`
}
type deliveryOwner struct {
	Hub       string    `json:"hub"`
	PID       int       `json:"pid"`
	Birth     string    `json:"birth"`
	Digest    string    `json:"binary_digest"`
	Launcher  string    `json:"launcher"`
	StartedAt time.Time `json:"started_at"`
	Managed   bool      `json:"managed"`
}
type deliveryPaths struct{ Root, State, Registry, Lease, Owner, Start, Descriptor, Hub, Slug string }

// Walk with directory descriptors: no symlink component is followed, including
// ancestors. Existing directory modes are checked, never silently repaired.
func deliveryPrivateDir(path string) error {
	if !filepath.IsAbs(path) {
		return errCodexDelivery
	}
	fd, err := unix.Open("/", unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return errCodexDelivery
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	for _, part := range parts {
		err = unix.Mkdirat(fd, part, 0700)
		if err != nil && !errors.Is(err, unix.EEXIST) {
			unix.Close(fd)
			return errCodexDelivery
		}
		next, e := unix.Openat(fd, part, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return errCodexDelivery
		}
		fd = next
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&0777 != 0700 {
		return errCodexDelivery
	}
	return nil
}

func deliveryPrivateFile(path string, create bool) (*os.File, error) {
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if create {
		flags |= unix.O_CREAT
	}
	fd, err := unix.Open(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		unix.Close(fd)
		return nil, errCodexDelivery
	}
	return os.NewFile(uintptr(fd), path), nil
}
func deliveryRead(path string, dest any) error {
	file, err := deliveryPrivateFile(path, false)
	if err != nil {
		return err
	}
	defer file.Close()
	// The registry is metadata, never credential TOML or arbitrary large input.
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	if err := decoder.Decode(dest); err != nil {
		return errCodexDelivery
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errCodexDelivery
	}
	return nil
}
func deliveryPublish(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return errCodexDelivery
	}
	return writeFileAtomic(path, append(raw, '\n'))
}
func deliveryLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}
func deliveryLockWait(ctx context.Context, path string) (*os.File, error) {
	file, err := deliveryPrivateFile(path, true)
	if err != nil {
		return nil, errCodexDelivery
	}
	for {
		if ctx.Err() != nil {
			file.Close()
			return nil, errCodexDelivery
		}
		locked, err := deliveryLock(file)
		if err != nil {
			file.Close()
			return nil, errCodexDelivery
		}
		if locked {
			return file, nil
		}
		if !sleepCtx(ctx, 10*time.Millisecond) {
			file.Close()
			return nil, errCodexDelivery
		}
	}
}
func (a *app) codexDeliveryPaths(hub string) (deliveryPaths, error) {
	var p deliveryPaths
	var err error
	p.Hub, p.Slug, err = codexDeliveryHub(hub)
	if err != nil {
		return p, err
	}
	data := a.getenv("XDG_DATA_HOME")
	if data == "" {
		if a.getenv("HOME") == "" {
			return p, errCodexDelivery
		}
		data = filepath.Join(a.getenv("HOME"), ".local", "share")
	}
	runtime := a.getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(data) || !filepath.IsAbs(runtime) {
		return p, errCodexDelivery
	}
	// XDG_RUNTIME_DIR must already exist; do not invent a shared /tmp fallback.
	if st, err := os.Lstat(runtime); err != nil || !st.IsDir() {
		return p, errCodexDelivery
	}
	if err := deliveryPrivateDir(runtime); err != nil {
		return p, err
	}
	p.Root = filepath.Join(data, "vecnet", "codex-delivery")
	p.State = filepath.Join(p.Root, p.Slug)
	for _, dir := range []string{filepath.Join(data, "vecnet"), p.Root, p.State, filepath.Join(runtime, "vecnet")} {
		if err := deliveryPrivateDir(dir); err != nil {
			return p, err
		}
	}
	p.Registry = filepath.Join(p.Root, "registry.json")
	p.Lease = filepath.Join(runtime, "vecnet", "delivery-"+p.Slug+".lock")
	p.Owner = filepath.Join(runtime, "vecnet", "delivery-"+p.Slug+".json")
	p.Start = filepath.Join(runtime, "vecnet", "start-"+p.Slug+".lock")
	p.Descriptor = filepath.Join(p.State, "descriptor.json")
	return p, nil
}
func deliveryRegister(ctx context.Context, p deliveryPaths, config, bindings string) error {
	if !filepath.IsAbs(config) || !filepath.IsAbs(bindings) {
		return errCodexDelivery
	}
	config = filepath.Clean(config)
	bindings = filepath.Clean(bindings)
	lock, err := deliveryLockWait(ctx, filepath.Join(p.Root, "registry.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	reg := deliveryRegistry{Version: 1, Sources: []deliverySource{}}
	if err := deliveryRead(p.Registry, &reg); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errCodexDelivery
	}
	if reg.Version != 1 || reg.Sources == nil {
		return errCodexDelivery
	}
	source := deliverySource{config, bindings}
	seen := map[deliverySource]bool{}
	sources := make([]deliverySource, 0, len(reg.Sources)+1)
	for _, s := range append(reg.Sources, source) {
		if !filepath.IsAbs(s.Config) || !filepath.IsAbs(s.Bindings) {
			return errCodexDelivery
		}
		if !seen[s] {
			sources = append(sources, s)
			seen[s] = true
		}
	}
	reg.Sources = sources
	if err := deliveryPublish(p.Registry, reg); err != nil {
		return errCodexDelivery
	}
	var desc deliveryDescriptor
	if err := deliveryRead(p.Descriptor, &desc); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return errCodexDelivery
		}
		return deliveryPublish(p.Descriptor, deliveryDescriptor{1, p.Hub})
	}
	if desc.Version != 1 || desc.Hub != p.Hub {
		return errCodexDelivery
	}
	return nil
}
func deliveryGeneration(m deliveryOwner, p deliveryPaths, r codexDeliveryRelease) bool {
	return m.Hub == p.Hub && m.Digest == r.Digest && m.Launcher == r.Entry
}
func (a *app) deliverySystemctl(ctx context.Context, args ...string) (string, error) {
	path, err := a.codexDeliveryExecutable("systemctl")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, append([]string{"--user"}, args...)...)
	cmd.Env = a.codexDeliveryEnv(a.getenv("VECNET_BIN"))
	cmd.WaitDelay = 100 * time.Millisecond
	var out deliveryOutput
	cmd.Stdout = &out
	err = cmd.Run()
	return strings.TrimSpace(out.String()), err
}
func (a *app) ensureCodexDeliveryPlatform(ctx context.Context, hub, config, bindings string, r codexDeliveryRelease) error {
	p, err := a.codexDeliveryPaths(hub)
	if err != nil {
		return err
	}
	if err := deliveryRegister(ctx, p, config, bindings); err != nil {
		return err
	}
	start, err := deliveryLockWait(ctx, p.Start)
	if err != nil {
		return err
	}
	defer start.Close()
	lease, err := deliveryPrivateFile(p.Lease, true)
	if err != nil {
		return errCodexDelivery
	}
	defer lease.Close()
	locked, err := deliveryLock(lease)
	if err != nil {
		return errCodexDelivery
	}
	unit := "vecnet-delivery@" + p.Slug + ".service"
	installed := false
	// A managed same-generation lease is already in its final lifecycle.
	// Detached owners also check whether installation made a unit available.
	// Metadata is a generation hint, never proof for signaling.
	var owner deliveryOwner
	sameGenerationOwner := false
	if !locked {
		if deliveryRead(p.Owner, &owner) != nil || owner.Hub != p.Hub {
			return errCodexDelivery
		}
		sameGenerationOwner = deliveryGeneration(owner, p, r) && deliveryOwnerHoldsLease(p, lease, owner)
		if sameGenerationOwner && owner.Managed {
			return nil
		}
	}
	state, systemdErr := a.deliverySystemctl(ctx, "show", "--property=LoadState", "--value", unit)
	installed = systemdErr == nil && state == "loaded"
	if sameGenerationOwner && !installed {
		return nil
	}
	if !locked {
		// A crashed leader can leave a bounded claim child holding its open-file
		// description. Wait, never signal a guessed PID or bypass that fence.
		if !deliveryOwnerRunning(owner) {
			for !locked {
				if !sleepCtx(ctx, 10*time.Millisecond) {
					return errCodexDelivery
				}
				locked, err = deliveryLock(lease)
				if err != nil {
					return errCodexDelivery
				}
			}
		} else {
			pidfd, err := deliveryOwnerPidfd(p, lease, owner)
			if err != nil {
				return err
			}
			if installed && owner.Managed {
				unix.Close(pidfd)
				if _, err := a.deliverySystemctl(ctx, "restart", unit); err != nil {
					return errCodexDelivery
				}
				return deliveryAwaitOwner(ctx, p, r, lease)
			}
			err = unix.PidfdSendSignal(pidfd, unix.SIGTERM, nil, 0)
			unix.Close(pidfd)
			if err != nil {
				return errCodexDelivery
			}
			for !locked {
				if !sleepCtx(ctx, 10*time.Millisecond) {
					return errCodexDelivery
				}
				locked, err = deliveryLock(lease)
				if err != nil {
					return errCodexDelivery
				}
			}
		}
	}
	// Start serialization is retained while the new process acquires this same
	// inode. Releasing a lock is closing/unlocking only, never unlinking.
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_UN); err != nil {
		return errCodexDelivery
	}
	if installed {
		if _, err := a.deliverySystemctl(ctx, "start", unit); err != nil {
			return errCodexDelivery
		}
	} else {
		cmd := exec.Command(r.Binary, "codex", "deliver", "--hub", p.Hub)
		cmd.Env = a.codexDeliveryEnv(r.Binary)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return errCodexDelivery
		}
		// Reap on the normal path without becoming a supervisor; parent exit has
		// no effect on the execed runner or its inherited lease.
		go cmd.Wait()
	}
	return deliveryAwaitOwner(ctx, p, r, lease)
}
func deliveryAwaitOwner(ctx context.Context, p deliveryPaths, r codexDeliveryRelease, lease *os.File) error {
	for {
		if ctx.Err() != nil {
			return errCodexDelivery
		}
		// Do not take a free lease while waiting for a child to acquire it:
		// that would turn our own starter into a successful busy contender.
		var owner deliveryOwner
		if deliveryRead(p.Owner, &owner) == nil && deliveryGeneration(owner, p, r) {
			if deliveryOwnerHoldsLease(p, lease, owner) {
				return nil
			}
		}
		if !sleepCtx(ctx, 10*time.Millisecond) {
			return errCodexDelivery
		}
	}
}

// A zombie still has birth ticks but can no longer own or drain a sender.
func deliveryOwnerRunning(m deliveryOwner) bool {
	if m.PID <= 1 || m.Birth == "" || procStart(m.PID) != m.Birth {
		return false
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", m.PID))
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 {
		return false
	}
	fields := strings.Fields(string(raw[i+1:]))
	return len(fields) > 0 && fields[0] != "Z" && fields[0] != "X"
}

// pidfd binds the signal target before checking birth and lock ownership.
// Linux without pidfd fails closed for replacement; starts still work.
func deliveryOwnerPidfd(p deliveryPaths, lease *os.File, m deliveryOwner) (int, error) {
	if m.PID <= 1 {
		return -1, errCodexDelivery
	}
	pidfd, err := unix.PidfdOpen(m.PID, 0)
	if err != nil {
		return -1, errCodexDelivery
	}
	if !deliveryOwnerHoldsLease(p, lease, m) {
		unix.Close(pidfd)
		return -1, errCodexDelivery
	}
	return pidfd, nil
}
func deliveryOwnerHoldsLease(p deliveryPaths, lease *os.File, m deliveryOwner) bool {
	if m.Hub != p.Hub || !deliveryOwnerRunning(m) {
		return false
	}
	var lockStat, processStat unix.Stat_t
	if unix.Fstat(int(lease.Fd()), &lockStat) != nil || unix.Stat(fmt.Sprintf("/proc/%d/fd/3", m.PID), &processStat) != nil || lockStat.Dev != processStat.Dev || lockStat.Ino != processStat.Ino {
		return false
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/fdinfo/3", m.PID))
	if err != nil {
		return false
	}
	// fdinfo reports locks of this open-file description, unlike a PID file
	// or merely opening the same inode. Require its exclusive FLOCK record.
	identity := fmt.Sprintf("%02x:%02x:%d", unix.Major(lockStat.Dev), unix.Minor(lockStat.Dev), lockStat.Ino)
	held := false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 8 && fields[0] == "lock:" && fields[2] == "FLOCK" && fields[4] == "WRITE" && fields[6] == identity {
			held = true
		}
	}
	return held && deliveryOwnerRunning(m)
}

var deliveryHubSlug = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (a *app) cmdCodexDeliveryPlatform(args []string) error {
	spec := map[string]flagKind{"hub": strFlag, "hub-id": strFlag}
	if args[0] == "delivery-refresh" {
		spec["config"] = strFlag
		spec["bindings"] = strFlag
	}
	o, err := parseFlags("codex "+args[0], args[1:], spec)
	if err != nil {
		return err
	}
	if len(o.pos) != 0 || o.has("hub") == o.has("hub-id") {
		return usageErr("codex delivery: pass exactly one of --hub or --hub-id")
	}
	hub := o.str("hub")
	if o.has("hub-id") {
		slug := o.str("hub-id")
		if !deliveryHubSlug.MatchString(slug) {
			return errCodexDelivery
		}
		data := a.getenv("XDG_DATA_HOME")
		if data == "" {
			if a.getenv("HOME") == "" {
				return errCodexDelivery
			}
			data = filepath.Join(a.getenv("HOME"), ".local/share")
		}
		root := filepath.Join(data, "vecnet", "codex-delivery")
		if deliveryPrivateDir(root) != nil || deliveryPrivateDir(filepath.Join(root, slug)) != nil {
			return errCodexDelivery
		}
		var desc deliveryDescriptor
		if deliveryRead(filepath.Join(root, slug, "descriptor.json"), &desc) != nil || desc.Version != 1 {
			return errCodexDelivery
		}
		var got string
		hub, got, err = codexDeliveryHub(desc.Hub)
		if err != nil || got != slug || hub != desc.Hub {
			return errCodexDelivery
		}
	}
	switch args[0] {
	case "delivery-refresh":
		config := o.str("config")
		bindings := o.str("bindings")
		if o.has("config") != o.has("bindings") {
			return usageErr("delivery-refresh: --config and --bindings must be supplied together")
		}
		if !o.has("config") {
			p, e := a.codexDeliveryPaths(hub)
			if e != nil {
				return e
			}
			var registry deliveryRegistry
			if deliveryRead(p.Registry, &registry) != nil || registry.Version != 1 || len(registry.Sources) == 0 {
				return errCodexDelivery
			}
			// Installer refresh preserves custom registered sources; it must
			// not fabricate a new source from the installer's default config.
			config, bindings = registry.Sources[0].Config, registry.Sources[0].Bindings
		}
		return a.ensureCodexDelivery(a.ctx, hub, config, bindings)
	case "delivery-status":
		p, err := a.codexDeliveryPaths(hub)
		if err != nil {
			return err
		}
		lease, err := deliveryPrivateFile(p.Lease, true)
		if err != nil {
			return errCodexDelivery
		}
		defer lease.Close()
		locked, err := deliveryLock(lease)
		if err != nil {
			return errCodexDelivery
		}
		// The lock alone determines liveness. Never expose config or process argv.
		return json.NewEncoder(a.stdout).Encode(struct {
			Hub     string `json:"hub"`
			Running bool   `json:"running"`
		}{p.Hub, !locked})
	case "deliver":
		return a.runCodexDelivery(hub, o.has("hub-id"))
	}
	return errCodexDelivery
}
func (a *app) runCodexDelivery(hub string, managed bool) error {
	r, err := a.codexDeliveryRelease()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 6*time.Second)
	defer cancel()
	if err := a.checkCodexDeliveryNode(ctx, r); err != nil {
		return err
	}
	p, err := a.codexDeliveryPaths(hub)
	if err != nil {
		return err
	}
	// The descriptor is registered by ensure, never by the Node runner.
	var desc deliveryDescriptor
	if deliveryRead(p.Descriptor, &desc) != nil || desc.Version != 1 || desc.Hub != p.Hub {
		return errCodexDelivery
	}
	lease, err := deliveryPrivateFile(p.Lease, true)
	if err != nil {
		return errCodexDelivery
	}
	defer lease.Close()
	locked, err := deliveryLock(lease)
	if err != nil {
		return errCodexDelivery
	}
	if !locked {
		if managed {
			var m deliveryOwner
			if deliveryRead(p.Owner, &m) != nil || !deliveryOwnerHoldsLease(p, lease, m) {
				// Restart=on-failure must retry after a bounded orphan claim writer
				// releases FD3; a clean contender exit would strand the service.
				return errCodexDelivery
			}
		}
		return nil
	}
	birth := procStart(os.Getpid())
	if birth == "" {
		return errCodexDelivery
	}
	owner := deliveryOwner{Hub: p.Hub, PID: os.Getpid(), Birth: birth, Digest: r.Digest, Launcher: r.Entry, StartedAt: time.Now().UTC(), Managed: managed}
	if err := deliveryPublish(p.Owner, owner); err != nil {
		return errCodexDelivery
	}
	env := a.codexDeliveryEnv(r.Binary)
	argv := []string{r.Node, "--disable-warning=ExperimentalWarning", r.Entry, "run", "--hub", p.Hub, "--state-dir", p.State, "--registry", p.Registry}
	// FD3 is the sole lifecycle channel. All other Go descriptors stay CLOEXEC.
	fd := int(lease.Fd())
	if fd == 3 {
		_, err = unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0)
	} else {
		err = unix.Dup3(fd, 3, 0)
	}
	if err != nil {
		return errCodexDelivery
	}
	if err = unix.Exec(r.Node, argv, env); err != nil {
		if fd != 3 {
			unix.Close(3)
		}
		return errCodexDelivery
	}
	return nil
}
