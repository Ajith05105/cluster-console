// Package web holds the React page and serves it.
//
// The page is built by Vite into plain files (HTML, JavaScript, CSS). Those
// files are copied into this package's dist/ folder when the container image
// is built, and the go:embed line below packs them inside the program. The
// result is one program file that serves both the API and the page, with
// nothing else to install.
//
// In this repository dist/ holds only a placeholder page, so that the Go code
// builds and tests without the front end. The real files are put there by
// backend/Dockerfile.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// "all:" includes every file in dist/, including ones whose names start with
// a dot or underscore, which go:embed would otherwise skip.
//
//go:embed all:dist
var files embed.FS

// Handler returns the thing that serves the page.
func Handler() http.Handler {
	// Look inside dist/ so that "/index.html" means dist/index.html.
	dist, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err) // cannot happen: dist is embedded above
	}
	return handlerFor(dist)
}

// handlerFor serves a folder of built page files. It is separate from
// Handler so the tests can hand it a made-up folder.
func handlerFor(dist fs.FS) http.Handler {
	fileServer := http.FileServerFS(dist)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Which file is being asked for? "/" means the page itself.
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		// If there is no such file, serve the page anyway. It is a
		// single-page app: whatever the address, the page is the same.
		// The exception is anything under assets/, where a missing file
		// is a real "not found" (an old page asking for an old script).
		if info, err := fs.Stat(dist, name); err != nil || info.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				http.NotFound(w, r)
				return
			}
			name = "index.html"
		}

		if strings.HasPrefix(name, "assets/") {
			// Vite puts a fingerprint of the contents in each asset's file
			// name, so a given name never changes and browsers may keep it.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			// The page itself must always be fetched fresh, so that after
			// an upgrade the browser picks up the new asset names.
			w.Header().Set("Cache-Control", "no-cache")
		}

		request := r.Clone(r.Context())
		request.URL.Path = "/" + name
		if name == "index.html" {
			// The file server answers "/index.html" with a redirect to
			// "/". Asking it for "/" serves the same file directly.
			request.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, request)
	})
}
