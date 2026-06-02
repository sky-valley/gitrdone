package httpapi

import (
	"net/url"
	"strings"
)

func repoGitURL(baseURL string, repoID string) string {
	return strings.TrimRight(baseURL, "/") + "/git/repos/" + formatRepoControlID(repoID) + ".git"
}

func repoGitURLWithToken(baseURL string, repoID string, token string) string {
	raw := repoGitURL(baseURL, repoID)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	parsed.User = url.UserPassword("x-access-token", token)
	return parsed.String()
}
