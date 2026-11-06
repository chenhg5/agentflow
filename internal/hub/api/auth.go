package api

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
)

// Guard wraps a handler with an access check.
type Guard func(http.Handler) http.Handler

// Open lets every request through.
func Open(next http.Handler) http.Handler { return next }

// BearerToken accepts "Authorization: Bearer <token>". It is how runners
// authenticate their reports.
func BearerToken(token string) Guard {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !equal(bearer(r), token) {
				writeError(w, http.StatusUnauthorized, "missing or wrong hub token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ViewerToken accepts the token as a Bearer token or as the password of HTTP
// basic auth, so a browser can sign in with its own prompt.
func ViewerToken(token string) Guard {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, password, _ := r.BasicAuth()
			if equal(bearer(r), token) || equal(password, token) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="agentflow hub", charset="UTF-8"`)
			http.Error(w, "sign in with the hub view token as the password", http.StatusUnauthorized)
		})
	}
}

func bearer(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

func equal(got, want string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// SameOrigin refuses state-changing requests a browser sent from another
// site: when Origin (or, failing that, Referer) is present it must name this
// Hub's own host. Requests without either come from scripts, not pages.
func SameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		source := r.Header.Get("Origin")
		if source == "" {
			source = r.Header.Get("Referer")
		}
		if source != "" {
			parsed, err := url.Parse(source)
			if err != nil || parsed.Host != r.Host {
				writeError(w, http.StatusForbidden, "cross-site request refused")
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, http.StatusForbidden, "cross-site request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}
