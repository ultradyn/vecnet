// Package cliprogram implements synthetic CLI subprocesses without application initialization.
package cliprogram

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

type Spec struct {
	Kind   string
	Log    string
	Calls  string
	Marker string
	Exit   int
}

// ReadSpec loads the sidecar belonging to this executable pathname.
func ReadSpec(exe string) (Spec, error) {
	var spec Spec
	raw, err := os.ReadFile(exe + ".cli-fixture.json")
	if err != nil {
		return spec, err
	}
	err = json.Unmarshal(raw, &spec)
	return spec, err
}

// Run preserves the fixture's process arguments, streams, working directory and exit status.
func Run(spec Spec) int {
	switch spec.Kind {
	case "clipboard":
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			panic(err)
		}
		if len(raw) < 2 || raw[0] != 0xff || raw[1] != 0xfe || len(raw)%2 != 0 {
			panic("Windows clipboard fixture requires BOM-prefixed UTF-16LE")
		}
		units := make([]uint16, (len(raw)-2)/2)
		for i := range units {
			units[i] = binary.LittleEndian.Uint16(raw[2+2*i:])
		}
		if err := os.WriteFile(spec.Log, []byte(string(utf16.Decode(units))), 0600); err != nil {
			panic(err)
		}
		if spec.Calls != "" {
			f, err := os.OpenFile(spec.Calls, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				panic(err)
			}
			fmt.Fprintln(f, "called")
			f.Close()
		}
	case "claude":
		cwd, err := os.Getwd()
		if err != nil {
			panic(err)
		}
		cwd, err = filepath.EvalSymlinks(cwd)
		if err != nil {
			panic(err)
		}
		f, err := os.OpenFile(spec.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(f, "cwd=%s argv=%s\n", cwd, strings.Join(os.Args[1:], " "))
		f.Close()
	case "argv":
		raw, err := json.Marshal(os.Args[1:])
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(spec.Log, raw, 0600); err != nil {
			panic(err)
		}
	case "git-tracking":
		if len(os.Args) > 3 && os.Args[3] == "rev-parse" {
			fmt.Println(filepath.ToSlash(os.Args[2]))
			return 0
		}
		if len(os.Args) <= 3 || os.Args[3] != "ls-files" {
			panic("unexpected git fixture arguments")
		}
	case "git-timeout":
		time.Sleep(5 * time.Second)
	case "git-non-repository":
		fmt.Fprintln(os.Stderr, "fatal: not a git repository")
	case "git-failure":
	case "never-run":
		if err := os.WriteFile(spec.Marker, nil, 0600); err != nil {
			panic(err)
		}
	default:
		panic("unknown CLI fixture program kind")
	}
	return spec.Exit
}
