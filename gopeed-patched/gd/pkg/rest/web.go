package rest

import (
	"bytes"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// webStaticHandler serves the embedded panel (mygopeed wasm UI) on the same
// port and the same router as the REST API, so the whole product needs only
// one listening port — no Caddy/nginx sidecar is required any more.
//
// Behavior:
//   - client accepts gzip and a sibling "*.gz" asset exists -> send the
//     pre-compressed bytes as-is (app.wasm is 20MB raw vs 5MB gzipped, and
//     re-compressing it on every request would burn CPU on small NAS boxes);
//   - otherwise fall back to the regular file server wrapped in
//     gzipMiddleware, which compresses on the fly.
type webStaticHandler struct {
	fileServer http.Handler
	fsys       fs.FS
	lastMod    time.Time
}

func newWebStaticHandler(fsys fs.FS) http.Handler {
	h := &webStaticHandler{
		fsys:    fsys,
		lastMod: time.Now(),
	}

	// The embedded assets never change after build, so pin Last-Modified to
	// the executable mtime: a rebuilt image always invalidates client caches.
	if exe, err := os.Executable(); err == nil {
		if fi, err := os.Stat(exe); err == nil {
			h.lastMod = fi.ModTime()
		}
	}

	h.fileServer = gzipMiddleware(http.FileServer(newEmbedCacheFileSystem(http.FS(fsys))))
	return h
}

func (h *webStaticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.fileServer.ServeHTTP(w, r)
		return
	}

	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if data, ok := h.readPrecompressed(name); ok {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Add("Vary", "Accept-Encoding")
			w.Header().Set("Content-Type", contentTypeByPath(name))
			// The panel busts its own cache with "?v=" on app.wasm, so keep
			// revalidation cheap instead of caching blindly.
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeContent(w, r, name, h.lastMod, bytes.NewReader(data))
			return
		}
	}

	h.fileServer.ServeHTTP(w, r)
}

// readPrecompressed returns the bytes of "<name>.gz" when that asset exists.
func (h *webStaticHandler) readPrecompressed(name string) ([]byte, bool) {
	if strings.HasSuffix(name, ".gz") {
		return nil, false
	}

	f, err := h.fsys.Open(name + ".gz")
	if err != nil {
		return nil, false
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

// contentTypeByPath fixes up the types Go's mime table gets wrong or misses,
// most importantly application/wasm which WebAssembly.instantiateStreaming
// requires for its fast path.
func contentTypeByPath(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".wasm":
		return "application/wasm"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	}

	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
