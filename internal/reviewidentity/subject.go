package reviewidentity

import (
	"net/mail"
	"strings"
)

const maxSubjectBytes = 128

func Canonical(subject string) (string, bool) {
	canonical := strings.ToLower(strings.TrimSpace(subject))
	if len(canonical) < 1 || len(canonical) > maxSubjectBytes {
		return "", false
	}
	address, err := mail.ParseAddress(canonical)
	if err != nil || address.Address != canonical {
		return "", false
	}
	return canonical, true
}
