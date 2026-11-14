// Package connectors supplies the source distribution carried by every CLI.
package connectors

import (
	"embed"
	"io/fs"
)

// Embed launcher sources within the distributable root Go module. The obsolete
// empty launcher module fence prevented embedding and was removed for go install.
//
//go:embed launcher/src launcher/schema launcher/package.json launcher/systemd/vecnet-launcher-profile.service.in
var embeddedLauncher embed.FS

// Launcher is rooted at the launcher's package, preserving the existing API.
var Launcher, _ = fs.Sub(embeddedLauncher, "launcher")

// HubClient retains connector-relative paths. Runtime dependencies are Node
// builtins and these relative sources, without tests or node_modules.
//
//go:embed hub-client/src hub-client/package.json
var HubClient embed.FS
