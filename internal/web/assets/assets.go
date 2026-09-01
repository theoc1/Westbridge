// Package assets carries the built frontend inside the binary.
//
// The Vite build writes into dist/, which is gitignored except for a .gitkeep:
// the embed directive refuses an empty directory, and its "all:" prefix is
// what makes it match a dotfile. Without both, a fresh clone fails to build
// before the frontend has ever been compiled.
package assets

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// assetDir is Vite's output subdirectory for hashed bundles. Everything under
// it is content-addressed, so a request that misses there is always an error
// rather than a route the SPA could handle.
const assetDir = "assets"

// ErrNotBuilt reports that the binary was built without a frontend, i.e. the
// embedded dist/ has no index.html. The server degrades to an API-only mode
// rather than serving a blank page, so `go build ./...` without a prior
// `make frontend` still produces something usable.
var ErrNotBuilt = errors.New("assets: frontend has not been built into this binary")

// FS returns the built frontend rooted at dist/.
func FS() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}

// Built reports whether a frontend build is actually present.
func Built() bool {
	sub, err := FS()
	if err != nil {
		return false
	}
	_, err = fs.Stat(sub, "index.html")
	return err == nil
}

// Handler serves the built frontend as a single-page app: a request that
// matches a file is served from disk, and anything else falls back to
// index.html so client-side routing and deep links work.
//
// It returns ErrNotBuilt when no frontend was embedded, leaving the caller to
// decide what to do about it.
func Handler() (http.Handler, error) {
	sub, err := FS()
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return nil, ErrNotBuilt
	}

	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if name == "." || name == "/" {
			name = "index.html"
		}

		// http.FileServer answers /index.html with a 301 to "./", which is
		// noise no browser needs; serve the shell directly instead.
		if name != "index.html" {
			if info, statErr := fs.Stat(sub, name); statErr == nil && !info.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
		}

		// A missing file under assets/ is a broken build, not a client-side
		// route. Answering it with the SPA shell makes the browser report an
		// opaque module/MIME error instead of a plain 404, which is a much
		// worse thing to debug.
		if strings.HasPrefix(name, assetDir+"/") {
			http.NotFound(w, r)
			return
		}

		// Unknown path: hand back the SPA shell, so client-side routing and
		// deep links work.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(index)
	}), nil
}
