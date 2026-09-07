package engine

import (
	"net/http"
	"os"
	"path"
	"strings"
)

// static.go — hardened SPA static file serving (security assessment M4).
//
// Serves ONLY the built web/dist tree (never the source web/ directory,
// which may carry source maps, un-minified code, or dev artifacts), with:
//   - SPA fallback: unknown non-file paths serve index.html (client routing)
//   - Cache-Control: hashed assets (/assets/*) cache for 1 year immutable;
//     everything else (HTML, favicon) is no-cache so deploys land instantly
//   - path.Clean containment: the resolved file must stay inside the root
//     (defense in depth — http.FileServer already guards traversal)
//   - the handler is wrapped by wrapPublicBrowserMiddleware at registration
//     time, so security headers / rate limit / logging / recovery apply

const (
	staticAssetsMaxAge = "max-age=31536000, immutable"
	staticNoCache      = "no-cache"
)

// spaHandler returns an http.HandlerFunc serving rootDir with SPA fallback.
func (e *Engine) spaHandler(rootDir string) http.HandlerFunc {
	fs := http.FileServer(http.Dir(rootDir))
	return func(w http.ResponseWriter, r *http.Request) {
		// Only GET/HEAD make sense for static assets.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Containment check: clean the URL path and verify the resolved
		// file stays under the root before touching the filesystem.
		urlPath := path.Clean("/" + r.URL.Path)
		if strings.Contains(urlPath, "..") {
			http.NotFound(w, r)
			return
		}
		full := path.Join(rootDir, urlPath)

		st, err := os.Stat(full)
		switch {
		case err == nil && st.IsDir():
			// Directory request: serve index.html at that prefix if it
			// exists (sub-app builds), else the SPA entrypoint.
			indexPath := path.Join(full, "index.html")
			if _, ierr := os.Stat(indexPath); ierr != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", staticNoCache)
			http.ServeFile(w, r, indexPath)
			return
		case err == nil:
			// Real file: hashed build assets get long-lived caching.
			if strings.HasPrefix(urlPath, "/assets/") {
				w.Header().Set("Cache-Control", staticAssetsMaxAge)
			} else {
				w.Header().Set("Cache-Control", staticNoCache)
			}
			fs.ServeHTTP(w, r)
			return
		}

		// Unknown path → SPA fallback to index.html (client-side routing),
		// but never for asset-shaped paths (a missing JS chunk should 404,
		// not return HTML that fails to parse).
		if strings.HasPrefix(urlPath, "/assets/") {
			http.NotFound(w, r)
			return
		}
		indexPath := path.Join(rootDir, "index.html")
		if _, ierr := os.Stat(indexPath); ierr != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", staticNoCache)
		http.ServeFile(w, r, indexPath)
	}
}
