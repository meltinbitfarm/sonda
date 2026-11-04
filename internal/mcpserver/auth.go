package mcpserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// MinTokenLen is the shortest SONDA_MCP_TOKEN accepted (e.g. openssl rand -hex 32 gives 64).
const MinTokenLen = 32

// RequireToken lets a request through only if it carries the token, as
// `Authorization: Bearer <token>` or as `?token=<token>` (for Claude's
// remote connectors, which only take a URL). A4: the comparison is
// constant-time on SHA-256 digests, so neither content nor length leaks;
// failures get 401. The token is removed from the URL before next runs, so
// nothing downstream can log it.
func RequireToken(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		got, fromHeader := bearer(r)
		if !fromHeader {
			got = r.URL.Query().Get("token")
		}
		sum := sha256.Sum256([]byte(got))
		if subtle.ConstantTimeCompare(sum[:], want[:]) != 1 || got == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="sonda"`)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}` + "\n"))
			return
		}
		if r.URL.RawQuery != "" {
			r = r.Clone(r.Context())
			q := r.URL.Query()
			q.Del("token")
			r.URL.RawQuery = q.Encode()
			r.RequestURI = r.URL.RequestURI()
		}
		next.ServeHTTP(w, r)
	})
}

// bearer returns the Bearer credential and whether an Authorization header
// with the Bearer scheme (case-insensitive) was present.
func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, cred, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return strings.TrimSpace(cred), true
}
