// Package webapp embeds the frontend build output (frontend/dist) into the binary and serves it same-origin,
// so no separate web server sits in front (see deploy/platform/README.md). SPA history-mode routing:
// paths that are not found return index.html and the frontend router takes over; /api/ is the exception and
// always returns 404 JSON (there are no frontend pages there).
package webapp

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

//go:embed all:dist
var distFS embed.FS

// Handler: under /api/ returns 404 JSON (unregistered API); for other GET paths -> serve the static file if
// it exists, otherwise return index.html (SPA fallback). Adds gzip, since no
// reverse proxy in front compresses responses.
func Handler() http.Handler {
	root, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("webapp: embed dist: " + err.Error()) // a build-time problem; should never happen at runtime
	}
	fileServer := http.FileServerFS(root)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Any /api/ request reaching here is an unregistered API (this version lacks the feature, or the path
		// has a typo): return 404 JSON. Returning index.html + 200 would make the caller think it succeeded,
		// and it would only break when parsing JSON.
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			apierr.Write(w, http.StatusNotFound, apierr.New(api.ErrorCodeNotFound, api.ErrorReasonRouteNotFound, nil))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path := trimLeadingSlash(r.URL.Path)
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(root, path); err != nil {
			// SPA fallback. We cannot rewrite the request path to "/index.html" and hand it to
			// http.FileServerFS: net/http special-cases request paths that literally end in .../index.html
			// and 301-redirects them to "./", which is meaningless for the fake paths here (e.g.
			// /some/client-side/route), so read the file and write it back directly.
			w.Header().Set("Cache-Control", "no-cache")
			serveIndex(w, r, root)
			return
		}
		setCacheControl(w, path)
		fileServer.ServeHTTP(w, r)
	})
	return middleware.Compress(5)(h)
}

// MethodNotAllowed: the path is registered but the method is not (methods this version lacks also land here).
// Returns 405 JSON (like the 404 JSON above), with Allow listing the methods this path has; chi's default 405
// has an empty body.
func MethodNotAllowed(match func(method, path string) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			if match(m, r.URL.Path) {
				w.Header().Add("Allow", m)
			}
		}
		apierr.Write(w, http.StatusMethodNotAllowed, apierr.New(api.ErrorCodeInvalid, api.ErrorReasonMethodNotAllowed, nil))
	}
}

// setCacheControl: files in embed.FS have no ModTime (zero value), so net/http's conditional GET
// (If-Modified-Since/ETag) does not work and everything is resent in full each time. Vite-built /assets/*
// file names carry a content hash (the name changes only when the content does), so the browser can cache
// them for a year and never ask again; other files (index.html, unhashed brand/ images, etc.) are
// conservatively no-cache and checked every time.
func setCacheControl(w http.ResponseWriter, path string) {
	if strings.HasPrefix(path, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
}

// notBuiltMessage: dist/ commits only an empty .gitkeep (see the comment in ../.gitignore). In an environment
// that has never run `npm run build`, a missing index.html is normal, not an error, so serve this hard-coded
// placeholder page instead of a bare 404.
const notBuiltMessage = `<!doctype html><html><head><meta charset="utf-8"><title>BSOS Node Manager</title></head>` +
	`<body><p>The frontend is not built. Run <code>npm run build</code> in <code>frontend/</code>,` +
	` then rebuild the backend (see deploy/platform/README.md).</p></body></html>`

func serveIndex(w http.ResponseWriter, r *http.Request, root fs.FS) {
	f, err := root.Open("index.html")
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, notBuiltMessage)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}
