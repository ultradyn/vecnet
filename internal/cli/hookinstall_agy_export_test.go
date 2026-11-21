package cli

import "testing"

// SetAgyInstallOSForTest pins the agy installer's host-OS gate for one test,
// so the external installer tests exercise the qualified install path on
// every host (only the dedicated refusal test checks the Windows gate).
func SetAgyInstallOSForTest(t *testing.T, goos string) {
	t.Helper()
	old := agyInstallOS
	agyInstallOS = goos
	t.Cleanup(func() { agyInstallOS = old })
}
