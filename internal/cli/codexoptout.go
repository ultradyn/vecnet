package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type codexOptOut struct {
	Config  string `json:"config"`
	Profile string `json:"profile"`
	As      string `json:"as"`
}
type codexOptOuts struct {
	Version  int           `json:"version"`
	Disabled []codexOptOut `json:"disabled"`
}

// All readers/writers hold bindings.json.lock. This is intentionally separate
// from the strict cross-language authority schema: opt-out grants no ownership.
func readCodexOptOuts(authority string) (codexOptOuts, error) {
	file := codexOptOuts{Version: 1, Disabled: []codexOptOut{}}
	raw, err := readPrivateCodexStatus(authority + ".optout")
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, err
	}
	if err := validateCodexStateJSON(raw); err != nil {
		return file, err
	}
	file = codexOptOuts{} // existing files must supply both version and disabled
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return file, err
	}
	if file.Version != 1 || file.Disabled == nil || len(file.Disabled) > 1024 {
		return file, usageErr("invalid Codex opt-out state")
	}
	for _, row := range file.Disabled {
		if !filepath.IsAbs(row.Config) || row.Profile == "" || strings.ContainsAny(row.Profile, "\x00\r\n") || (row.As != "" && !hookID.MatchString(row.As)) {
			return file, usageErr("invalid Codex opt-out target")
		}
	}
	return file, nil
}
func (f codexOptOuts) disabled(config, profile, as string) bool {
	return slices.Contains(f.Disabled, codexOptOut{config, profile, as})
}
func (f *codexOptOuts) disable(config, profile, as string) {
	if !f.disabled(config, profile, as) {
		f.Disabled = append(f.Disabled, codexOptOut{config, profile, as})
	}
}
func (f *codexOptOuts) enable(config, profile, as string) {
	f.Disabled = slices.DeleteFunc(f.Disabled, func(row codexOptOut) bool { return row == (codexOptOut{config, profile, as}) })
}
func (f codexOptOuts) write(authority string) error {
	if len(f.Disabled) > 1024 {
		return usageErr("Codex opt-out limit reached")
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return writeCodexDurable(authority+".optout", raw)
}

func (a *app) checkCodexEnrollmentOptOut(config, profile, as string) error {
	path, err := codexBindingsPath(a.getenv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	unlock, err := lockPath(a.ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	file, err := readCodexOptOuts(path)
	if err != nil {
		return err
	}
	if file.disabled(config, profile, as) {
		return errCodexOptOut
	}
	return nil
}

var errCodexOptOut = errors.New("Codex enrollment disabled by unbind; use explicit addressed codex bind/setup to re-enable")
