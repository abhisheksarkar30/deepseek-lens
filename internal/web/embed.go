// Package web embeds the dashboard's static assets — vanilla HTML/CSS/JS,
// no build step, no node_modules (CLAUDE.md / the bead's rationale: a build
// toolchain would break the single-binary `go build` deploy story). Files is
// mounted by internal/api.New so the served binary never reads these off
// disk at runtime.
package web

import "embed"

//go:embed index.html app.js style.css
var Files embed.FS
