package httpapi

import "strings"

func repoGitURL(baseURL string, repoID string) string {
	return strings.TrimRight(baseURL, "/") + "/git/repos/" + formatRepoControlID(repoID) + ".git"
}
