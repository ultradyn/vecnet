package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestContextErrorExitOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"canceled", context.Canceled, "interrupted"},
		{"wrapped-canceled", fmt.Errorf("local inventory update: %w", context.Canceled), "interrupted"},
		{"deadline", context.DeadlineExceeded, "context deadline exceeded"},
		{"scheduler-deadline", fmt.Errorf("launcher Task Scheduler register: %w", context.DeadlineExceeded), "launcher Task Scheduler register: context deadline exceeded"},
		{"git-deadline", fmt.Errorf("local project Git discovery: %w", context.DeadlineExceeded), "local project Git discovery: context deadline exceeded"},
		{"inventory-deadline", fmt.Errorf("local inventory update: %w", context.DeadlineExceeded), "local inventory update: context deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			a := &app{stdout: &out, stderr: &stderr}
			if code := a.exit(tc.err); code != exitUsage || out.Len() != 0 || stderr.String() != "vecnet: "+tc.want+"\n" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
			}
		})
	}
}

func TestLocalRegistryContextErrorLabels(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, operation := range []string{"git-discovery", "inventory-update"} {
			t.Run(operation+"/"+cause.Error(), func(t *testing.T) {
				searchPath := os.Getenv("PATH")
				a, _, home := localRegistryFixture(t)
				a.ctx = deadlineErrorsContext(t, cause)
				var err error
				label := "local inventory update"
				if operation == "git-discovery" {
					// Keep the executable discoverable when testing Git context errors.
					t.Setenv("PATH", searchPath)
					label = "local project Git discovery"
					_, err = a.localProjectIdentity()
				} else {
					err = a.mutateLocalRegistry(true, func(*localRegistryDocument) error {
						t.Fatal("expired context reached mutation")
						return nil
					})
				}
				deadlineErrorsRequireLabel(t, err, cause, label)
				if _, err := os.Stat(home); !os.IsNotExist(err) {
					t.Fatalf("expired operation created inventory HOME: %v", err)
				}
			})
		}
	}
	t.Run("inventory-lock", func(t *testing.T) {
		a, _, home := localRegistryFixture(t)
		dir, err := a.localRegistryDirectory(true)
		if err != nil {
			t.Fatal(err)
		}
		defer dir.Close()
		unlock, err := localRegistryLock(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		a.ctx = ctx
		err = a.mutateLocalRegistry(true, func(*localRegistryDocument) error {
			t.Fatal("contended lock reached mutation")
			return nil
		})
		deadlineErrorsRequireLabel(t, err, context.DeadlineExceeded, "local inventory update")
		if _, err := os.Stat(localRegistryInventory(home)); !os.IsNotExist(err) {
			t.Fatalf("lock timeout published inventory: %v", err)
		}
	})
	t.Run("before-publication", func(t *testing.T) {
		a, root, home := localRegistryFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a.ctx = ctx
		err := a.mutateLocalRegistry(true, func(doc *localRegistryDocument) error {
			doc.Projects = append(doc.Projects, localProject{Root: root, Directory: root, FirstSeen: localRegistryNow(), LastSeen: localRegistryNow(), Artifacts: []localArtifact{}})
			cancel()
			return nil
		})
		deadlineErrorsRequireLabel(t, err, context.Canceled, "local inventory update")
		if _, err := os.Stat(localRegistryInventory(home)); !os.IsNotExist(err) {
			t.Fatalf("cancellation published inventory: %v", err)
		}
	})
	t.Run("non-context-error", func(t *testing.T) {
		a, _, _ := localRegistryFixture(t)
		want := errors.New("mutation refused")
		if err := a.mutateLocalRegistry(true, func(*localRegistryDocument) error { return want }); err != want {
			t.Fatalf("non-context mutation error changed: %v", err)
		}
	})
}

func TestLauncherSchedulerContextErrorLabels(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows Task Scheduler boundary")
	}
	ctl, err := launcherNativeTaskController(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			_, err := ctl.Inspect(deadlineErrorsContext(t, cause), "vecnet-launcher-p-0123456789abcdef")
			deadlineErrorsRequireLabel(t, err, cause, "launcher Task Scheduler inspect")
		})
	}
}

func deadlineErrorsContext(t *testing.T, cause error) context.Context {
	t.Helper()
	if cause == context.Canceled {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	t.Cleanup(cancel)
	return ctx
}

func deadlineErrorsRequireLabel(t *testing.T, err, cause error, label string) {
	t.Helper()
	if !errors.Is(err, cause) || err.Error() != label+": "+cause.Error() {
		t.Fatalf("want wrapped %s: %v; got %v", label, cause, err)
	}
}
