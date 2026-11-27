// Package web holds the embedded web UI. Builds with the ui tag embed the
// Vite output in build/; other builds embed a placeholder page instead, so
// the server builds and tests without Node.
package web

import "io/fs"

// mustSub returns the subtree of fsys at dir, which the embed directive
// guarantees to exist.
func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
