package httpapi

import "strings"

const repoControlIDPrefix = "repo_"
const repoTokenControlIDPrefix = "token_"

func formatRepoControlID(id string) string {
	return repoControlIDPrefix + id
}

func parseRepoControlID(value string) (string, bool) {
	raw, ok := strings.CutPrefix(strings.TrimSpace(value), repoControlIDPrefix)
	if !ok || !isCanonicalUUID(raw) {
		return "", false
	}
	return raw, true
}

func formatRepoTokenControlID(id string) string {
	return repoTokenControlIDPrefix + id
}

func parseRepoTokenControlID(value string) (string, bool) {
	raw, ok := strings.CutPrefix(strings.TrimSpace(value), repoTokenControlIDPrefix)
	if !ok || !isCanonicalUUID(raw) {
		return "", false
	}
	return raw, true
}

func isCanonicalUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		switch index {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if !isLowerHexDigit(char) {
				return false
			}
		}
	}
	return true
}

func isLowerHexDigit(char rune) bool {
	return (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')
}
