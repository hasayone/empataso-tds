package admin

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// gzipPool reuses gzip writers at BestSpeed across responses.
var gzipPool = sync.Pool{
	New: func() any {
		z, err := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		if err != nil {
			// BestSpeed is a valid level; NewWriterLevel fails only on a bad level.
			panic(err)
		}
		return z
	},
}

// gzipMiddleware compresses a response when the client accepts gzip.
// next is the downstream handler. Upgrade requests are passed through unchanged.
// It returns the wrapping handler. It does not return an error.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !gzipEligible(r) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.finish()
		next.ServeHTTP(gw, r)
	})
}

// gzipEligible reports whether gzipMiddleware compresses the response to r:
// the client accepts gzip and the request is neither a protocol switch nor a request for an
// event stream, whose chunks must reach the client as they are written.
// It does not return an error.
func gzipEligible(r *http.Request) bool {
	return acceptsGzip(r) && !upgradeRequest(r) && !aiWantsSSE(r)
}

// addVary adds token to the Vary header of h unless a value already lists it.
// It does not return an error.
func addVary(h http.Header, token string) {
	for _, v := range h.Values("Vary") {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return
			}
		}
	}
	h.Add("Vary", token)
}

// acceptsGzip reports whether Accept-Encoding lists a gzip coding token.
// r is the request. It returns false when the header is missing or has no gzip token.
// It does not return an error.
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		token, _, _ := strings.Cut(part, ";")
		if strings.EqualFold(strings.TrimSpace(token), "gzip") {
			return true
		}
	}
	return false
}

// upgradeRequest reports whether r is a protocol switch such as a websocket.
// It returns true when Upgrade is set or Connection lists the upgrade token.
// It does not return an error.
func upgradeRequest(r *http.Request) bool {
	if r.Header.Get("Upgrade") != "" {
		return true
	}
	for _, part := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(part), "upgrade") {
			return true
		}
	}
	return false
}

// gzipResponseWriter delays the status line until the first body byte or the end of the request.
// ResponseWriter is the downstream writer. status is the code from WriteHeader, or 0 when unset.
// wroteHeader is true after the handler called WriteHeader. sent is true after the downstream
// WriteHeader. zw is the pooled compressor once a body is being gzipped.
type gzipResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	sent        bool
	zw          *gzip.Writer
}

// Header returns the downstream header map so the handler can set headers before the body.
// It does not return an error.
func (g *gzipResponseWriter) Header() http.Header {
	return g.ResponseWriter.Header()
}

// WriteHeader remembers code until the first body byte or finish.
// code is the HTTP status. A later call is ignored, matching net/http.
// It returns nothing and does not return an error.
func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.sent || g.wroteHeader {
		return
	}
	g.status = code
	g.wroteHeader = true
}

// Write sends p and starts gzip on the first non-empty chunk.
// p is one body chunk. An empty p before compression starts does not send headers.
// It returns the number of bytes accepted and any downstream or gzip write error.
func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		if g.zw != nil {
			return g.zw.Write(p)
		}
		return 0, nil
	}
	g.begin()
	if g.zw != nil {
		return g.zw.Write(p)
	}
	return g.ResponseWriter.Write(p)
}

// Flush pushes buffered gzip bytes, then flushes the downstream writer when it implements Flusher.
// A flush before the first body byte does nothing: flushing early would commit headers
// before Content-Encoding can be set. It returns nothing and does not return an error.
func (g *gzipResponseWriter) Flush() {
	if g.zw != nil {
		_ = g.zw.Flush()
	}
	if !g.sent {
		return
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// begin sends the remembered status and, unless Content-Encoding is already set, starts gzip.
// The first call sets Content-Encoding, adds Vary, and drops Content-Length.
// Later calls do nothing. It returns nothing and does not return an error.
func (g *gzipResponseWriter) begin() {
	if g.sent {
		return
	}
	g.sent = true
	if g.status == 0 {
		g.status = http.StatusOK
	}
	h := g.Header()
	if h.Get("Content-Encoding") != "" {
		g.ResponseWriter.WriteHeader(g.status)
		return
	}
	h.Set("Content-Encoding", "gzip")
	addVary(h, "Accept-Encoding")
	h.Del("Content-Length")
	g.ResponseWriter.WriteHeader(g.status)
	zw := gzipPool.Get().(*gzip.Writer)
	zw.Reset(g.ResponseWriter)
	g.zw = zw
}

// finish closes the gzip writer and returns it to the pool.
// When the handler sent no body, it writes the remembered status without Content-Encoding.
// It returns nothing. A gzip close error is discarded after the footer write is attempted.
func (g *gzipResponseWriter) finish() {
	if g.zw != nil {
		zw := g.zw
		g.zw = nil
		_ = zw.Close()
		zw.Reset(io.Discard)
		gzipPool.Put(zw)
		return
	}
	if g.sent {
		return
	}
	status := g.status
	if status == 0 {
		status = http.StatusOK
	}
	g.ResponseWriter.WriteHeader(status)
	g.sent = true
}
