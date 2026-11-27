//go:build !ui

package web

import "embed"

// Embedded reports whether the binary embeds the web UI.
const Embedded = false

// FS holds a placeholder page that says the build does not include the web
// UI, with index.html at its root.
var FS = mustSub(headless, "headless")

//go:embed headless
var headless embed.FS
