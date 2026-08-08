package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serve runs one request through Compress and returns the recorder.
func serve(t *testing.T, req *http.Request, h http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Compress(h).ServeHTTP(rec, req)
	return rec
}

func gzipRequest(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Header.Set("Accept-Encoding", "gzip")
	return r
}

// A listing is the reason this exists: repetitive JSON, and large enough that
// the difference is the whole point.
func TestAListingIsCompressed(t *testing.T) {
	body := `{"entries":[` + strings.Repeat(`{"name":"f.txt","dir":false,"size":10,"mode":"0600"},`, 500) + `{}]}`

	rec := serve(t, gzipRequest("GET", "/api/files/homes/alice"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, body)
	})

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q, want it to mention Accept-Encoding", got)
	}
	// The stored length described the uncompressed body and is now a lie.
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want it removed", got)
	}

	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Error("the decompressed body is not what the handler wrote")
	}
	if rec.Body.Len() >= len(body) {
		t.Errorf("compressed to %d bytes from %d — no saving", rec.Body.Len(), len(body))
	}
}

// THE trap. http.ServeContent answers a Range by seeking to an offset in the
// stored file. Compressing that response makes the offsets describe something
// the client never receives, and a resumed download reassembles into garbage.
func TestARangeRequestIsNeverCompressed(t *testing.T) {
	for _, header := range []string{"Range", "If-Range"} {
		req := gzipRequest("GET", "/api/files/homes/alice/big.txt")
		req.Header.Set(header, "bytes=100-200")

		rec := serve(t, req, func(w http.ResponseWriter, r *http.Request) {
			// A text file is in the compressible list, so only the Range check
			// can be what saves this.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, strings.Repeat("x", 4096))
		})

		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("%s: Content-Encoding = %q, want none", header, got)
		}
		if rec.Body.Len() != 4096 {
			t.Errorf("%s: body is %d bytes, want the 4096 written", header, rec.Body.Len())
		}
	}
}

// 206 is the partial response itself; 304 and 204 have no body to compress.
func TestOnlyAPlain200IsCompressed(t *testing.T) {
	for _, status := range []int{
		http.StatusPartialContent,
		http.StatusNotModified,
		http.StatusNoContent,
		http.StatusNotFound,
	} {
		rec := serve(t, gzipRequest("GET", "/api/files/x"), func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, strings.Repeat("a", 4096))
		})
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("status %d: Content-Encoding = %q, want none", status, got)
		}
	}
}

// A file download must arrive byte for byte. Most of what this server stores is
// already compressed, and a second pass would cost CPU to make it bigger.
func TestFileBytesArePassedThrough(t *testing.T) {
	payload := bytes.Repeat([]byte{0x1f, 0x8b, 0x42, 0x00}, 1024)

	rec := serve(t, gzipRequest("GET", "/api/files/homes/alice/photo.jpg"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(payload)
	})

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Error("the bytes changed on the way out")
	}
}

// A download keeps its Content-Length, because that is the number the
// browser's progress bar counts towards. A compressible type is not enough to
// override that.
func TestADownloadKeepsItsLength(t *testing.T) {
	rec := serve(t, gzipRequest("GET", "/api/files/homes/alice/server.log"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''server.log`)
		w.Header().Set("Content-Length", "4096")
		_, _ = io.WriteString(w, strings.Repeat("log line\n", 456))
	})

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none for an attachment", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "4096" {
		t.Errorf("Content-Length = %q, want it kept so the download shows progress", got)
	}
}

func TestAClientThatDidNotAskGetsPlainBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/files/x", nil) // no Accept-Encoding
	Compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.Repeat("a", 4096))
	})).ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none", got)
	}
	if rec.Body.Len() != 4096 {
		t.Errorf("body is %d bytes, want 4096", rec.Body.Len())
	}
}

// "gzip" must be recognised inside a real Accept-Encoding, must not be found
// inside a token that merely contains it, and -- the part that is easy to get
// wrong -- q=0 means the client is REFUSING it, not asking for it.
func TestAcceptEncodingParsing(t *testing.T) {
	for header, want := range map[string]bool{
		"gzip":                   true,
		"gzip, deflate, br":      true,
		"deflate, gzip;q=1.0, *": true,
		" GZIP ":                 true,
		"gzip; q=0.5":            true,
		"*":                      true,
		"*;q=0.1":                true,
		"br":                     false,
		"":                       false,
		"identity":               false,
		"x-gzip-not-really":      false,
		"notgzip":                false,

		// RFC 9110 §12.5.3: q=0 means "not acceptable".
		"gzip;q=0":           false,
		"gzip;q=0.0":         false,
		"gzip;q=0.000":       false,
		"identity, gzip;q=0": false,
		"*;q=0":              false,
		// A wildcard must not resurrect an encoding that was refused by name.
		"*, gzip;q=0": false,
		// ...and a refused wildcard must not veto one asked for by name.
		"*;q=0, gzip": true,
	} {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

// Compressing switches the representation, so the byte offsets the header
// invites no longer describe anything the server will send. A client that
// resumed on them would splice gzip bytes onto identity bytes -- Range
// requests are never compressed -- and write a corrupt file.
func TestCompressingRetractsAcceptRanges(t *testing.T) {
	rec := serve(t, gzipRequest("GET", "/assets/index.js"), func(w http.ResponseWriter, r *http.Request) {
		// What http.ServeContent sets before the middleware sees the response.
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", "14000")
		_, _ = io.WriteString(w, strings.Repeat("function f(){};\n", 875))
	})

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "" {
		t.Errorf("Accept-Ranges = %q, want it retracted on a compressed body", got)
	}
}

// An uncompressed response keeps it: a download must stay resumable.
func TestAnUncompressedResponseKeepsAcceptRanges(t *testing.T) {
	rec := serve(t, gzipRequest("GET", "/api/files/homes/alice/movie.mkv"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-matroska")
		w.Header().Set("Accept-Ranges", "bytes")
		_, _ = w.Write(bytes.Repeat([]byte{7}, 4096))
	})
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want it kept so downloads resume", got)
	}
}

// Wrapping a ResponseWriter hides the optional interfaces of the one
// underneath. io.ReaderFrom is the one that matters here: it is how
// http.ServeContent reaches sendfile(2), and it is needed most on the
// downloads this middleware deliberately does NOT compress -- where the
// wrapper is otherwise pure overhead.
func TestTheDownloadFastPathSurvivesWrapping(t *testing.T) {
	var (
		sawReaderFrom bool
		sawUnwrap     bool
	)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawReaderFrom = w.(io.ReaderFrom)
		_, sawUnwrap = w.(interface{ Unwrap() http.ResponseWriter })
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(Compress(h))
	defer srv.Close()

	req, err := http.NewRequest("GET", srv.URL+"/api/files/homes/alice/movie.mkv", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit, so Go's transport does not add and transparently strip its own.
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if !sawReaderFrom {
		t.Error("the wrapped writer is not an io.ReaderFrom — ServeContent loses sendfile")
	}
	if !sawUnwrap {
		t.Error("the wrapped writer has no Unwrap — http.ResponseController cannot reach the connection")
	}
}

// ReadFrom is a second entry point into the body, so it has to make the same
// compression decision Write does rather than bypassing it.
func TestReadFromStillCompressesWhatItShould(t *testing.T) {
	body := strings.Repeat(`{"name":"f.txt","size":10},`, 400)

	rec := serve(t, gzipRequest("GET", "/api/files/homes/alice"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// io.Copy prefers the destination's ReadFrom, which is the path here.
		if _, err := io.Copy(w, strings.NewReader(body)); err != nil {
			t.Error(err)
		}
	})

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip through the ReadFrom path", got)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Error("the body written through ReadFrom did not survive the round trip")
	}
}

// A handler that already encoded its body must not have it encoded again.
func TestAnAlreadyEncodedBodyIsLeftAlone(t *testing.T) {
	rec := serve(t, gzipRequest("GET", "/api/files/x"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "br")
		_, _ = io.WriteString(w, strings.Repeat("a", 4096))
	})
	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Errorf("Content-Encoding = %q, want br untouched", got)
	}
}

// Below the threshold the gzip header and trailer cost more than they save --
// but only when the handler said how long the body was.
func TestATinyDeclaredBodyIsNotWorthCompressing(t *testing.T) {
	rec := serve(t, gzipRequest("GET", "/api/whoami"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "17")
		_, _ = io.WriteString(w, `{"user":"alice"}`+"\n")
	})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none for a 17-byte body", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "17" {
		t.Errorf("Content-Length = %q, want it kept at 17", got)
	}
}

// The pool hands the same compressor to the next request. A writer that kept a
// reference to the previous response would write one client's bytes into
// another's connection.
func TestPooledCompressorsDoNotLeakAcrossRequests(t *testing.T) {
	body := strings.Repeat("shared-secret-", 200)
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}

	for i := 0; i < 8; i++ {
		rec := serve(t, gzipRequest("GET", "/api/files/x"), handler)
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("request %d: not gzip: %v", i, err)
		}
		got, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if string(got) != body {
			t.Fatalf("request %d: body was not what this handler wrote", i)
		}
	}
}
