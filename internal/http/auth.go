package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func controlAuth(expected string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !matchesControlBearer(r, expected) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func matchesControlBearer(r *http.Request, expected string) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && got != "" && expected != "" && subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func repoTokenFromRequest(r *http.Request) string {
	username, password, ok := r.BasicAuth()
	if ok {
		if password != "" {
			return password
		}
		return username
	}

	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(token)
}
