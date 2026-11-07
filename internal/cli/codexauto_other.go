//go:build !linux

package cli

import "context"

func (a *app) startCodexAuto(string)       {}
func (a *app) cmdCodexAuto([]string) error { return errCodexDelivery }
func (a *app) updateCodexAuto(string, string, codexBinding, *codexBinding, Profile) error {
	return errCodexDelivery
}
func (a *app) codexAdmission(context.Context, string, string, string, bool) (func(), []codexBinding, error) {
	return func() {}, nil, nil
}
