package server

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
)

// setSecurityHeaders applies the response headers every reply carries, including early rejections.
func (s *Server) setSecurityHeaders(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("X-Content-Type-Options", "nosniff")
	// same-origin keeps share-preview tokens out of third-party Referer headers; no-referrer would make form
	// navigations send "Origin: null", which transportGuard rejects.
	header.Set("Referrer-Policy", "same-origin")
	if s.options.Auth != AuthPlatform {
		// The platform gateway may legitimately embed the app in its own page.
		header.Set("X-Frame-Options", "SAMEORIGIN")
	}
	if s.requestTLS(r) {
		header.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

// contentSecurityPolicy builds the policy for HTML documents; scriptHashes are ready-made 'sha256-...' sources.
// connect-src allows data: because the IronRDP loader fetches its inlined wasm module from a data: URL;
// frame-src allows https: for the browser-mode in-app viewer, which only embeds https pages.
func contentSecurityPolicy(scriptHashes []string) string {
	var script strings.Builder
	script.WriteString("script-src 'self' 'wasm-unsafe-eval'")
	for _, hash := range scriptHashes {
		script.WriteByte(' ')
		script.WriteString(hash)
	}
	return "default-src 'self'; " + script.String() +
		"; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self' data:; " +
		"media-src 'self' data: blob:; worker-src 'self' blob:; frame-src 'self' https:; object-src 'none'; " +
		"base-uri 'self'; form-action 'self'; frame-ancestors 'self'"
}

// inlineScriptHashes returns the CSP hash sources of every inline (no src attribute) script element in html.
func inlineScriptHashes(html []byte) []string {
	lower := asciiLower(html)
	var hashes []string
	seen := map[string]bool{}
	for pos := 0; ; {
		start := strings.Index(lower[pos:], "<script")
		if start < 0 {
			break
		}
		start += pos
		nameEnd := start + len("<script")
		if nameEnd < len(lower) && !strings.ContainsRune(" \t\r\n\f/>", rune(lower[nameEnd])) {
			pos = nameEnd
			continue
		}
		tagEnd, hasSrc := scanTag(lower, nameEnd)
		if tagEnd < 0 {
			break
		}
		closeAt := strings.Index(lower[tagEnd:], "</script")
		if closeAt < 0 {
			break
		}
		closeAt += tagEnd
		pos = closeAt + len("</script")
		if hasSrc {
			continue
		}
		sum := sha256.Sum256(html[tagEnd:closeAt])
		hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !seen[hash] {
			seen[hash] = true
			hashes = append(hashes, hash)
		}
	}
	return hashes
}

// asciiLower lowercases ASCII letters only, so indexes into the result match the original bytes.
func asciiLower(b []byte) string {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// scanTag walks the attributes of a start tag from index i and returns the index after its closing '>' (-1 when
// unterminated) and whether a src attribute is present. Quoted values may contain '>'.
func scanTag(s string, i int) (end int, hasSrc bool) {
	for i < len(s) {
		switch c := s[i]; {
		case c == '>':
			return i + 1, hasSrc
		case c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == '/':
			i++
		default:
			nameStart := i
			for i < len(s) && !strings.ContainsRune(" \t\r\n\f/>=", rune(s[i])) {
				i++
			}
			if s[nameStart:i] == "src" {
				hasSrc = true
			}
			for i < len(s) && strings.ContainsRune(" \t\r\n\f", rune(s[i])) {
				i++
			}
			if i < len(s) && s[i] == '=' {
				i++
				for i < len(s) && strings.ContainsRune(" \t\r\n\f", rune(s[i])) {
					i++
				}
				if i < len(s) && (s[i] == '"' || s[i] == '\'') {
					quote := s[i]
					i++
					for i < len(s) && s[i] != quote {
						i++
					}
					i++
				} else {
					for i < len(s) && !strings.ContainsRune(" \t\r\n\f>", rune(s[i])) {
						i++
					}
				}
			} else if i == nameStart {
				i++
			}
		}
	}
	return -1, hasSrc
}
