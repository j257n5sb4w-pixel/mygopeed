package rest

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (g gzipResponseWriter) Write(b []byte) (int, error) {
	return g.Writer.Write(b)
}

// WriteHeader drops Content-Length, since the gzip-compressed body length
// differs from the original one. Keeping the original length makes the client
// truncate the response or hang waiting for bytes that never arrive.
func (g gzipResponseWriter) WriteHeader(code int) {
	g.ResponseWriter.Header().Del("Content-Length")
	g.ResponseWriter.Header().Set("Content-Encoding", "gzip")
	g.ResponseWriter.WriteHeader(code)
}

func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		// HEAD 响应没有 body，加 Content-Encoding 会让客户端以为有压缩数据
		if r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		w.Header().Del("Content-Length")

		gz := gzip.NewWriter(w)
		defer gz.Close()

		next.ServeHTTP(gzipResponseWriter{Writer: gz, ResponseWriter: w}, r)
	})
}
