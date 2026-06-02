package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func controlAuth(expected string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || got == "" || expected == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}
