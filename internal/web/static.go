package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// staticFS holds the stylesheets, icons and the bundled scripts.
//
// dist/ is produced by `go run ./tools/assets` from web/src and is committed, so a
// fresh clone builds without any JavaScript toolchain. CI rebuilds it and fails if
// the committed copy is stale.
//
//go:embed all:static
var staticFS embed.FS

// StaticPrefix is the URL path the static assets are mounted under.
const StaticPrefix = "/static/"

// staticHandler serves the embedded assets.
//
// Asset URLs carry a build-specific cache-busting query, so responses can be marked
// immutable and cached for a year. The service worker relies on this too.
func staticHandler() (http.Handler, error) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	fileServer := http.FileServer(http.FS(sub))
	return http.StripPrefix(StaticPrefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Directory listings would expose nothing secret but are still noise.
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		fileServer.ServeHTTP(w, r)
	})), nil
}
