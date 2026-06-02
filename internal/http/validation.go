package httpapi

import (
	"fmt"
	"strings"
)

const maxControlInputLength = 128

func validateRepoSlug(field string, value string) string {
	if len(value) < 1 || len(value) > maxControlInputLength {
		return repoSlugError(field)
	}
	for _, char := range value {
		if isASCIILetter(char) || isASCIIDigit(char) || char == '.' || char == '_' || char == '-' {
			continue
		}
		return repoSlugError(field)
	}
	return ""
}

func repoSlugError(field string) string {
	return fmt.Sprintf("%s must be 1-128 characters and contain only letters, numbers, dot, underscore, or dash", field)
}

func validateDefaultBranch(value string) string {
	if len(value) < 1 || len(value) > maxControlInputLength {
		return "defaultBranch must be a valid branch name"
	}
	if strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") {
		return "defaultBranch must be a valid branch name"
	}
	if strings.Contains(value, "//") || strings.Contains(value, "..") {
		return "defaultBranch must be a valid branch name"
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return "defaultBranch must be a valid branch name"
		}
	}
	for _, char := range value {
		if isASCIILetter(char) || isASCIIDigit(char) || char == '.' || char == '_' || char == '-' || char == '/' {
			continue
		}
		return "defaultBranch must be a valid branch name"
	}
	return ""
}

func validateTokenSubject(value string) string {
	if len(value) < 1 || len(value) > maxControlInputLength {
		return tokenSubjectError()
	}
	for _, char := range value {
		if isASCIILetter(char) || isASCIIDigit(char) {
			continue
		}
		switch char {
		case '.', '_', '-', '/', ':', '@':
			continue
		default:
			return tokenSubjectError()
		}
	}
	return ""
}

func tokenSubjectError() string {
	return "subject must be 1-128 characters and contain only letters, numbers, dot, underscore, dash, slash, colon, or at sign"
}

func isASCIILetter(char rune) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

func isASCIIDigit(char rune) bool {
	return char >= '0' && char <= '9'
}
