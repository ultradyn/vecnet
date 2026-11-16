package cli

import (
	"bytes"
	"path/filepath"
	"runtime"
)

func launcherManagedUnit(raw []byte) bool {
	if runtime.GOOS == "windows" {
		return launcherWindowsManagedUnit(raw)
	}
	return bytes.HasPrefix(raw, []byte("# Managed by vecnet launcher install."))
}
func (a *app) launcherManagerInstall(p launcherPaths, unit []byte, enable, start bool) error {
	if err := a.launcherRun("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if enable {
		if err := a.launcherRun("systemctl", "--user", "enable", filepath.Base(p.Unit)); err != nil {
			return err
		}
	}
	if start {
		return a.launcherRun("systemctl", "--user", "restart", filepath.Base(p.Unit))
	}
	return nil
}
func (a *app) launcherManagerReload(meta launcherManaged, intent *launcherRepairIntent) error {
	if runtime.GOOS == "windows" {
		return a.launcherWindowsManagerReload(meta, intent)
	}
	_, err := a.launcherManager("daemon-reload")
	return err
}
func (a *app) launcherManagerRestart(meta launcherManaged) error {
	if runtime.GOOS == "windows" {
		return a.launcherWindowsManagerRestart(meta)
	}
	_, err := a.launcherManager("restart", filepath.Base(meta.UnitPath))
	return err
}

func (a *app) launcherPrepareManagerInstall(p launcherPaths, unit []byte, enable, start bool) (func() error, error) {
	if runtime.GOOS == "windows" {
		return a.launcherWindowsPrepareInstall(p, unit, enable, start)
	}
	return func() error { return a.launcherManagerInstall(p, unit, enable, start) }, nil
}
