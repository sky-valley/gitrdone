package requestauth

import (
	"context"
	"net/http"
	"strings"
)

type subjectContextKey struct{}

func WithSubject(request *http.Request, subject string) *http.Request {
	ctx := context.WithValue(request.Context(), subjectContextKey{}, strings.TrimSpace(subject))
	return request.WithContext(ctx)
}

func Subject(request *http.Request) string {
	subject, _ := request.Context().Value(subjectContextKey{}).(string)
	return subject
}
