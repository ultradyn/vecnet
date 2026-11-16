//go:build !windows

package localruntime

import (
	"context"
	"errors"
)

func windowsHome(string) (string, error) { return "", errors.New("Windows runtime unavailable") }
func readWindows(string) (Result, error) { return Result{}, errors.New("Windows runtime unavailable") }
func ensureWindows(context.Context, Options) (Result, error) {
	return Result{}, errors.New("Windows runtime unavailable")
}

func addPlatformAssets(map[string][]byte) {}
