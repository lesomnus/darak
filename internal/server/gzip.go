package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Compress wraps a handler so text responses go out gzipped.
//
// The listing is what motivates this: a directory of 10,000 entries is 948 KB
// of JSON whose every row repeats the same six keys, and it gzips to 36 KB.
// Nothing about the shape of the response needed to change to get a 26x
// reduction on the wire.
//
// The decision is made at WriteHeader, from the CONTENT TYPE THE HANDLER SET,
// not from the route. It has to be: GET /api/files/<path> is one route that
// answers with JSON for a directory and with the file's own bytes for a file,
// and only one of those may be touched.
func Compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r.Header.Get("Accept-Encoding")) || rangeRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

// rangeRequest reports whether the client asked for part of the body.
//
// This is the trap. http.ServeContent serves file downloads and share links,
// and it honours Range by seeking to a byte offset IN THE STORED FILE. Compress
// that and the offsets no longer describe what the client receives: a resumed
// download silently reassembles into corruption. If-Range has the same problem.
//
// A Range request is therefore passed through untouched. It costs nothing --
// the responses worth compressing here are listings, and nobody asks for byte
// 500 of a directory listing.
func rangeRequest(r *http.Request) bool {
	return r.Header.Get("Range") != "" || r.Header.Get("If-Range") != ""
}

// acceptsGzip parses Accept-Encoding, including the q parameter.
//
// The parameter is not decoration: RFC 9110 §12.5.3 gives `gzip;q=0` the
// meaning "gzip is NOT acceptable", and it is how a client turns compression
// off while still naming what it would take. Reading the name and discarding
// the qvalue inverts that — the one client that went to the trouble of
// refusing gets a body it has no decoder attached for, and prints binary.
//
// `*` is the other half of the same rule: a wildcard with a non-zero q accepts
// gzip without naming it.
func acceptsGzip(header string) bool {
	accepted := false
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		name = strings.TrimSpace(name)
		if !strings.EqualFold(name, "gzip") && name != "*" {
			continue
		}
		if qZero(params) {
			// An explicit refusal. A later wildcard must not undo a specific
			// `gzip;q=0`, so this returns rather than continuing.
			if strings.EqualFold(name, "gzip") {
				return false
			}
			continue
		}
		if strings.EqualFold(name, "gzip") {
			return true // a named, non-refused gzip is the strongest signal
		}
		accepted = true // `*`, unless a later `gzip;q=0` overrules it
	}
	return accepted
}

// qZero reports whether the parameters say q=0, in any of the spellings the
// grammar allows: q=0, q=0., q=0.0, q=0.000.
func qZero(params string) bool {
	for _, p := range strings.Split(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "0" || (strings.HasPrefix(v, "0.") && strings.Trim(v[2:], "0") == "") {
			return true
		}
	}
	return false
}

// compressible is matched against the media type, so parameters like
// "; charset=utf-8" do not have to be repeated here.
//
// Deliberately a list rather than "anything that is not an image": an unknown
// type is far more likely to be already-compressed bytes (an archive, a video,
// an office document) than something worth another pass.
var compressible = map[string]bool{
	"application/json":       true,
	"application/javascript": true,
	"text/javascript":        true,
	"text/html":              true,
	"text/css":               true,
	"text/plain":             true,
	"text/markdown":          true,
	"image/svg+xml":          true,
}

// tooSmallToBother is the point below which a gzip header, trailer and the
// round trip through the compressor cost more than they save.
const tooSmallToBother = 1024

type gzipWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
}

func (w *gzipWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if w.shouldCompress(status) {
		h := w.Header()
		// The stored length describes the uncompressed body, and after this it
		// describes nothing. Leaving it makes the response unparseable.
		h.Del("Content-Length")
		// And the offsets it invites are into a representation that will never
		// be served: rangeRequest() declines to compress a Range request, so a
		// client that took this up would receive gzip bytes, resume with
		// `Range:`, be answered with IDENTITY bytes, and splice the two into a
		// corrupt file. http.ServeContent sets this header before we get here,
		// so it has to be removed rather than merely not added.
		h.Del("Accept-Ranges")
		h.Set("Content-Encoding", "gzip")
		// A cache that keeps one copy for every client must be told that the
		// body depends on what the client said it accepts.
		h.Add("Vary", "Accept-Encoding")
		w.gz = getWriter(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipWriter) shouldCompress(status int) bool {
	// 204 and 304 have no body at all; anything but 200 here is an error page
	// small enough not to matter, and 206 is the partial response this must
	// never touch.
	if status != http.StatusOK {
		return false
	}
	h := w.Header()
	if h.Get("Content-Encoding") != "" {
		return false // the handler already encoded it
	}
	// A download is left alone even when its type is compressible.
	//
	// Compressing it is legal and the bytes arrive intact, but Content-Length
	// has to go, and that is the number the browser's download UI counts
	// towards -- a 2 GB log would download with no progress and no estimate.
	// On a file server that is a worse trade than the bandwidth is worth.
	// `attachment` is exactly the marker for "this is a file being handed
	// over" (serveFile and serveShared set it; writeJSON and the UI do not).
	if strings.Contains(strings.ToLower(h.Get("Content-Disposition")), "attachment") {
		return false
	}
	mediaType, _, _ := strings.Cut(h.Get("Content-Type"), ";")
	if !compressible[strings.ToLower(strings.TrimSpace(mediaType))] {
		return false
	}
	// Only when the handler declared a length can this be known up front; a
	// streamed response of unknown size is compressed on the assumption that a
	// handler which did not bound it expects it to be large.
	if n, err := strconv.Atoi(h.Get("Content-Length")); err == nil && n < tooSmallToBother {
		return false
	}
	return true
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		// net/http would infer 200 here, and the decision above has to be made
		// before any byte of the body is written.
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// ReadFrom keeps sendfile(2) available on the download path.
//
// This is the cost of wrapping a ResponseWriter: net/http's own writer
// implements io.ReaderFrom, and that is what lets http.ServeContent hand a
// *os.File to (*net.TCPConn).ReadFrom and have the kernel move the bytes. A
// wrapper that does not forward it makes io.Copy fall back to a 32 KiB
// userspace buffer -- measured at 0 sendfile calls and ~65,000 syscalls for a
// 2 GB file, against ~500 with it.
//
// It bites hardest here precisely because downloads are NOT compressed: the
// wrapper is doing nothing for them except getting in the way.
func (w *gzipWriter) ReadFrom(r io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		// Compressed: every byte has to go through the compressor, so there is
		// no fast path to preserve.
		return io.Copy(w.gz, r)
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(w.ResponseWriter, r)
}

// Unwrap lets http.ResponseController reach the writer underneath, so a
// deadline or a flush set through it applies to the real connection.
func (w *gzipWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush keeps streaming handlers streaming. Without it the wrapper silently
// swallows the http.Flusher interface and a progressive response is buffered
// until the handler returns.
func (w *gzipWriter) Flush() {
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *gzipWriter) close() {
	if w.gz != nil {
		_ = w.gz.Close()
		putWriter(w.gz)
		w.gz = nil
	}
}

// Compressors are pooled: a listing request allocates a 64 KiB window and the
// deflate tables, and a file browser makes one of these per navigation.
var writers = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

func getWriter(w http.ResponseWriter) *gzip.Writer {
	gz := writers.Get().(*gzip.Writer)
	gz.Reset(w)
	return gz
}

func putWriter(gz *gzip.Writer) {
	gz.Reset(nil) // drop the reference to the response writer
	writers.Put(gz)
}
