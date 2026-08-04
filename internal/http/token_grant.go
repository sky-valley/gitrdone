package httpapi

import (
	"errors"
	"strings"

	"github.com/sky-valley/gitrdone/internal/reviewidentity"
)

var errRepoTokenScopeInvalid = errors.New("repo token scope is invalid")
var errRepoTokenSubjectRequired = errors.New("repo token subject is required")
var errRepoTokenSubjectInvalid = errors.New("repo token subject is invalid")
var errRepoTokenReviewSubjectInvalid = errors.New("repo review token subject is invalid")

func normalizeCreateRepoTokenInput(input createRepoTokenInput) (createRepoTokenInput, error) {
	input.Scope = strings.TrimSpace(input.Scope)
	input.Subject = strings.TrimSpace(input.Subject)
	if !isRepoTokenScope(input.Scope) {
		return createRepoTokenInput{}, errRepoTokenScopeInvalid
	}
	if input.Subject == "" {
		return createRepoTokenInput{}, errRepoTokenSubjectRequired
	}
	if input.Scope == "review" {
		subject, valid := canonicalReviewSubject(input.Subject)
		if !valid {
			return createRepoTokenInput{}, errRepoTokenReviewSubjectInvalid
		}
		input.Subject = subject
		return input, nil
	}
	if validateTokenSubject(input.Subject) != "" {
		return createRepoTokenInput{}, errRepoTokenSubjectInvalid
	}
	return input, nil
}

func isRepoTokenScope(scope string) bool {
	return scope == "read" || scope == "write" || scope == "readwrite" || scope == "review"
}

func canonicalReviewSubject(subject string) (string, bool) {
	return reviewidentity.Canonical(subject)
}
