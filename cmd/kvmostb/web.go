package main

import (
	"embed"
	"io/fs"
	"net/http"
)

// webFS holds the embedded console. It uses no CDN assets, so the font stack
// falls back to system fonts.
//
//go:embed all:web
var webFS embed.FS

// webHandler serves the embedded web console.
func webHandler() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // embedded asset is a build-time constant
	}
	return http.FileServerFS(sub)
}
