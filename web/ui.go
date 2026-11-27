//go:build ui

package web

import "embed"

// Embedded reports whether the binary embeds the web UI.
const Embedded = true

// FS holds the files of the web UI, with index.html at its root.
var FS = mustSub(build, "build")

//go:embed all:build
var build embed.FS
