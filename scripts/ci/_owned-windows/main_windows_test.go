package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Use the actual owner entrypoint and a separate real child. This exercises
// CreateProcess, handle inheritance and Windows's argv parser, not a Go mirror
// of the escaping algorithm.
func TestNativeOwnerChildIO(t *testing.T) {
	if os.Getenv("CI_TEST_OWNER_CHILD") == "1" {
		switch os.Args[3] {
		case "owner":
			os.Args = os.Args[3:]
			main()
		case "wait":
			os.Stdout.WriteString("ready\n")
			time.Sleep(time.Minute)
		case "child":
			b := []byte{0}
			if n, err := os.Stdin.Read(b); n != 0 || err == nil {
				t.Fatalf("wanted NUL stdin, read %d bytes: %v", n, err)
			}
			if os.Getenv("CI_TEST_OWNER_VALUE") != "literal value" {
				t.Fatal("child did not inherit environment")
			}
			if _, err := os.Stat("cwd-marker"); err != nil {
				t.Fatal(err)
			}
			if err := json.NewEncoder(os.Stdout).Encode(os.Args[4:]); err != nil {
				t.Fatal(err)
			}
			os.Stderr.WriteString("child stderr")
			os.Exit(7)
		}
		t.Fatal("unexpected fixture role")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "cwd-marker"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	literal := []string{"", "Space with trailing\\", `backslash\"quote`, "🚀", "tab\tseparated"}
	fixture := []string{"-test.run=^TestNativeOwnerChildIO$", "--", "child"}
	data, err := json.Marshal(request{File: self, Args: append(fixture, literal...), Milliseconds: 5000})
	if err != nil {
		t.Fatal(err)
	}
	cmd := ownerTestCommand(self, data)
	cmd.Dir = cwd
	cmd.Env = append(cmd.Env, "CI_TEST_OWNER_VALUE=literal value")
	// Keep the control pipe open with no data: if the child inherits it instead
	// of NUL, the read hangs until the owner's deadline and cannot pass.
	control, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	out, err := cmd.Output()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 7 || string(exit.Stderr) != "child stderr" {
		t.Fatalf("expected child status and stderr, got %v, stdout %q, stderr %q", err, out, cmd.Stderr)
	}
	var args []string
	if err := json.Unmarshal(out, &args); err != nil || !reflect.DeepEqual(args, literal) {
		t.Fatalf("child args %q, want %q: %v", args, literal, err)
	}
}

func ownerTestCommand(self string, data []byte) *exec.Cmd {
	cmd := exec.Command(self, "-test.run=^TestNativeOwnerChildIO$", "--", "owner", base64.StdEncoding.EncodeToString(data))
	cmd.Env = append(os.Environ(), "CI_TEST_OWNER_CHILD=1")
	return cmd
}

func TestNativeOwnerControlPipe(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(request{File: self, Args: []string{"-test.run=^TestNativeOwnerChildIO$", "--", "wait"}, Milliseconds: 5000})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"line", "eof"} {
		t.Run(mode, func(t *testing.T) {
			cmd := ownerTestCommand(self, data)
			cmd.Stderr = os.Stderr
			control, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer control.Close()
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(stdout)
			ready, readErr := reader.ReadString('\n')
			if mode == "line" {
				_, err = io.WriteString(control, "cancel\n")
			} else {
				err = control.Close()
			}
			waitErr := cmd.Wait()
			if readErr != nil || ready != "ready\n" || err != nil {
				t.Fatalf("readiness %q %v, control %v, wait %v", ready, readErr, err, waitErr)
			}
			if exit, ok := waitErr.(*exec.ExitError); !ok || exit.ExitCode() != 143 {
				t.Fatalf("expected cancel status 143, got %v", waitErr)
			}
		})
	}
}
