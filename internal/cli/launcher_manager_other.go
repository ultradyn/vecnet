//go:build !windows

package cli

import (
	"context"
	"errors"
)

func launcherNativeTaskController(*app) (launcherTaskController, error) {
	return nil, errors.New("Windows Task Scheduler unavailable")
}

func launcherNativeProfileTasks(context.Context) ([]string, error) {
	return nil, errors.New("Windows Task Scheduler unavailable")
}
