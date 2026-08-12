//darak:local-state — reads the OPERATOR-SUPPLIED -brand-logo file, once, at
// startup. The lint check in internal/lint forbids path-resolving calls in the
// server because they re-run permission resolution against the calling process,
// which is root; that reasoning does not reach here, because the path is a
// command-line flag, it is read before the listener is open, and no request can
// steer it. There is nobody to route it through a helper AS, either: the file
// belongs to whoever installed the server, not to a user.

package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Brand is what the interface calls this installation.
//
// It exists because the thing in the corner of the page is a company's file
// server, not a product. An operator points -brand-logo at their own mark and
// the interface stops looking like somebody else's software.
//
// The logo is read ONCE, at startup, into memory. Two reasons. The process is
// root and resolves no path of its own at request time -- that is the helper's
// job, with the requesting user's credentials -- so a per-request read here
// would be the one exception to the rule the whole design rests on. And a bad
// path should stop the server on the line that configured it rather than
// produce a broken image for every visitor.
type Brand struct {
	// Name is the wordmark, the document title, and the logo's alt text.
	Name string
	// Logo is the image bytes, empty when none was configured.
	Logo []byte
	// LogoType is its media type, derived from the file extension.
	LogoType string
	// LogoTag is an ETag over the bytes.
	LogoTag string
	// LogoTime is the file's mtime, for If-Modified-Since.
	LogoTime time.Time
}

// DefaultBrandName is what the corner says when nothing is configured.
const DefaultBrandName = "파일 서버"

// maxLogoBytes bounds what is held in memory and inlined into every page load.
// A wordmark that does not fit in this is not a wordmark.
const maxLogoBytes = 1 << 20 // 1 MiB

// logoTypes is an allowlist rather than a sniff.
//
// Deliberately not http.DetectContentType: the point of failing here is to tell
// the operator that the file they named is not a picture, and sniffing turns
// that into a silently served application/octet-stream.
var logoTypes = map[string]string{
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
	".avif": "image/avif",
	".ico":  "image/x-icon",
}

// LoadBrand reads the configured logo. An empty path is not an error -- it is
// the default, and it means the interface draws its built-in mark.
func LoadBrand(name, logoPath string) (Brand, error) {
	if name == "" {
		name = DefaultBrandName
	}
	b := Brand{Name: name}
	if logoPath == "" {
		return b, nil
	}

	ext := strings.ToLower(filepath.Ext(logoPath))
	mediaType, ok := logoTypes[ext]
	if !ok {
		kinds := make([]string, 0, len(logoTypes))
		for k := range logoTypes {
			kinds = append(kinds, k)
		}
		return b, fmt.Errorf("brand logo %q: unsupported extension %q (one of %s)",
			logoPath, ext, strings.Join(kinds, " "))
	}

	st, err := os.Stat(logoPath)
	if err != nil {
		return b, fmt.Errorf("brand logo: %w", err)
	}
	if st.Size() > maxLogoBytes {
		return b, fmt.Errorf("brand logo %q: %d bytes, over the %d limit",
			logoPath, st.Size(), maxLogoBytes)
	}
	data, err := os.ReadFile(logoPath)
	if err != nil {
		return b, fmt.Errorf("brand logo: %w", err)
	}
	if len(data) == 0 {
		return b, errors.New("brand logo: file is empty")
	}

	sum := sha256.Sum256(data)
	b.Logo = data
	b.LogoType = mediaType
	b.LogoTag = `"` + hex.EncodeToString(sum[:8]) + `"`
	b.LogoTime = st.ModTime()
	return b, nil
}

// handleBranding answers what to draw in the corner.
//
// Unauthenticated, because the login page is the first thing anyone sees and it
// should carry the mark too. What it discloses is the name of the company whose
// server you have just been asked to sign in to, which the hostname already
// said.
func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	// Not cached. It is one small response per page load, and an operator who
	// restarts with a new logo should see it, not spend ten minutes doubting the
	// flag they just set.
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{
		"name": s.cfg.Brand.Name,
		"logo": len(s.cfg.Brand.Logo) > 0,
	})
}

func (s *Server) handleBrandingLogo(w http.ResponseWriter, r *http.Request) {
	if len(s.cfg.Brand.Logo) == 0 {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", s.cfg.Brand.LogoType)
	h.Set("ETag", s.cfg.Brand.LogoTag)
	// An hour, revalidated: the bytes only change when the process restarts, and
	// the ETag makes the revalidation free.
	h.Set("Cache-Control", "public, max-age=3600, must-revalidate")
	// An SVG is a document, and a document served from this origin could carry a
	// script -- not when drawn in an <img>, which is how the interface uses it,
	// but a link straight to this URL renders it as a page. The operator picked
	// the file, so this is not a defence against a hostile upload; it is a
	// defence against one careless SVG becoming same-origin script.
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	// The name argument is only ServeContent's hint for guessing a Content-Type,
	// and one is already set above, so it is unused here. What is wanted from
	// ServeContent is If-None-Match and If-Modified-Since.
	http.ServeContent(w, r, "logo", s.cfg.Brand.LogoTime, bytes.NewReader(s.cfg.Brand.Logo))
}
