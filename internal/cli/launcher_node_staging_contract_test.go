package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/platform"
)

func launcherFixturePhysicalTemp(t *testing.T) string {
	t.Helper()
	parent, err := platform.Canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return parent
}

func TestLauncherFixtureNodeJobLifecycle(t *testing.T) {
	if _, err := exec.LookPath(platform.Exe("node")); err != nil {
		t.Fatal("genuine Node required for the staging contract:", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base, err := platform.Canonical(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := platform.MkdirTemp(base, "vecnet-ci-go-native-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	// Alias the real test binary, not Node. Exercise the same JS helper and
	// explicit TestMain utility entry used by suite.mjs with real native bytes.
	binary := filepath.Join(root, platform.Exe("cli.test"))
	if err := os.Link(exe, binary); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Clean(filepath.Join(cwd, "..", "..", "scripts", "ci", "launcher-node-fixture.mjs"))
	job := launcherFixturePhysicalTemp(t)
	if err := os.Mkdir(filepath.Join(job, ".ci"), 0o700); err != nil {
		t.Fatal(err)
	}
	inventory, err := json.Marshal([]map[string]string{{"pkg": "github.com/XertroV/vecnet/internal/cli", "binary": binary, "temporary": root, "directory": cwd}})
	if err != nil {
		t.Fatal(err)
	}
	jobCommand := func(action string) *exec.Cmd {
		t.Helper()
		script := `import {pathToFileURL} from 'node:url';
const m=await import(pathToFileURL(process.env.CI_NODE_HELPER));
const inventory=JSON.parse(process.env.CI_NODE_INVENTORY);
if(process.argv[1]==='prepare'){await m.prepareLauncherNode(inventory);console.log(await m.borrowedLauncherNode(inventory));}
else if(process.argv[1]==='borrow'){console.log(await m.borrowedLauncherNode(inventory));}
else if(process.argv[1]==='invalid-owner'){
inventory[0].temporary=process.cwd();inventory[0].binary=process.cwd()+'/cli.test';
await m.cleanupLauncherNode(inventory);
}
else {await m.cleanupLauncherNode(inventory);}`
		cmd := exec.CommandContext(t.Context(), platform.Exe("node"), "--input-type=module", "-e", script, action)
		cmd.Dir = job
		cmd.Env = append(os.Environ(), "CI_NODE_HELPER="+helper, "CI_NODE_INVENTORY="+string(inventory))
		return cmd
	}
	dir := filepath.Join(root, "launcher-node-fixture")
	command := func(args ...string) *exec.Cmd {
		t.Helper()
		return exec.CommandContext(t.Context(), exe, args...)
	}
	stage := jobCommand("prepare")
	output, err := stage.Output()
	if err != nil {
		t.Fatalf("stage utility: %v: %q", err, output)
	}
	t.Cleanup(func() {
		if err := launcherFixtureNodeUtility([]string{"cleanup", dir}, io.Discard); err != nil {
			t.Error(err)
		}
	})
	node := filepath.Join(dir, platform.Exe("node"))
	if string(output) != node+"\n" {
		t.Fatalf("unexpected staging output: %q", output)
	}
	if output, err := jobCommand("invalid-owner").CombinedOutput(); err == nil || !bytes.Contains(output, []byte("invalid native launcher Node fixture owner")) {
		t.Fatalf("invalid owner metadata did not fail closed: %v: %s", err, output)
	}
	for range 2 {
		borrower := command("-test.run=^TestLauncherFixtureNodeIsolation$", "-test.v", "-test.count=1")
		borrower.Env = append(os.Environ(), "CI_LAUNCHER_NODE_FIXTURE="+node)
		output, err := borrower.CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("--- PASS: TestLauncherFixtureNodeIsolation")) || bytes.Contains(output, []byte("prepared private image")) {
			t.Fatalf("borrower failed or prepared a replacement: %v: %s", err, output)
		}
		// Borrower TestMain has exited, including all its fixture cleanup.
		if _, err := borrowLauncherFixtureNode(node); err != nil {
			t.Fatal("borrower exit changed/deleted job image:", err)
		}
	}
	// Receipt corruption must reject borrowing, while cleanup must ignore that
	// path and remove only the image inside the native bundle's owned directory.
	if err := os.WriteFile(filepath.Join(job, ".ci", "launcher-node-fixture"), []byte(job+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := jobCommand("borrow").CombinedOutput(); err == nil || !bytes.Contains(output, []byte("invalid shared launcher Node fixture receipt")) {
		t.Fatalf("invalid receipt was accepted: %v: %s", err, output)
	}
	cleanup := jobCommand("cleanup")
	if output, err := cleanup.CombinedOutput(); err != nil {
		t.Fatalf("cleanup utility: %v: %s", err, output)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("job image survived owner cleanup: %v", err)
	}
	if _, err := os.Stat(job); err != nil {
		t.Fatal("cleanup followed corrupted receipt:", err)
	}
	if err := launcherFixtureNodeUtility([]string{"cleanup", dir}, io.Discard); err != nil {
		t.Fatal("cleanup of absent image:", err)
	}
	// An explicitly invalid shared path must fail the real consumer process,
	// rather than silently preparing another image after owner cleanup.
	borrower := command("-test.run=^TestLauncherFixtureNodeIsolation$", "-test.v")
	borrower.Env = append(os.Environ(), "CI_LAUNCHER_NODE_FIXTURE="+node)
	if output, err := borrower.CombinedOutput(); err == nil || !bytes.Contains(output, []byte("--- FAIL: TestLauncherFixtureNodeIsolation")) {
		t.Fatalf("missing shared image did not fail closed: %v: %s", err, output)
	}
}

type launcherFixtureBrokenWriter struct{}

func (launcherFixtureBrokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestLauncherFixtureNodeStageFailureCleanup(t *testing.T) {
	dir := filepath.Join(launcherFixturePhysicalTemp(t), "launcher-node-fixture")
	var output bytes.Buffer
	// Failure before any copy must not publish a path or leave partial state.
	t.Setenv("PATH", t.TempDir())
	if err := launcherFixtureNodeUtility([]string{"prepare", dir}, &output); err == nil || output.Len() != 0 {
		t.Fatalf("missing Node preparation: err=%v output=%q", err, output.String())
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed preparation leaked its image: %v", err)
	}
}

func TestLauncherFixtureNodeStagePublicationFailure(t *testing.T) {
	dir := filepath.Join(launcherFixturePhysicalTemp(t), "launcher-node-fixture")
	if err := launcherFixtureNodeUtility([]string{"prepare", dir}, launcherFixtureBrokenWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("lost publication failure:", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed publication leaked prepared image: %v", err)
	}
}

func TestLauncherFixtureNodeBorrowRejectsInvalid(t *testing.T) {
	parent := launcherFixturePhysicalTemp(t)
	for _, path := range []string{"", "relative-node", filepath.Join(parent, "missing", platform.Exe("node")), parent + "\n"} {
		if _, err := borrowLauncherFixtureNode(path); err == nil {
			t.Errorf("accepted invalid shared path %q", path)
		}
	}
	node, err := copyLauncherFixtureNode(filepath.Join(parent, "private node"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := borrowLauncherFixtureNode(node); err != nil {
		t.Fatal(err)
	}
	// A second link invalidates the image even though its genuine bytes match.
	alias := filepath.Join(parent, "alias")
	if err := os.Link(node, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := borrowLauncherFixtureNode(node); err == nil {
		t.Fatal("accepted a multi-link shared image")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(node, []byte("not genuine Node"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := borrowLauncherFixtureNode(node); err == nil {
		t.Fatal("accepted a private single-link image with wrong source bytes")
	}
	// Cleanup refuses arbitrary directories and aliases, preserving their data.
	outside := filepath.Join(parent, "outside")
	if err := platform.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := launcherFixtureNodeUtility([]string{"cleanup", outside}, io.Discard); err == nil {
		t.Fatal("accepted an arbitrary cleanup target")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("invalid cleanup removed outside data:", err)
	}
	if err := launcherFixtureNodeUtility([]string{"invalid", outside}, io.Discard); err == nil || !strings.Contains(err.Error(), "prepare|cleanup") {
		t.Fatal("accepted invalid utility mode:", err)
	}
}
