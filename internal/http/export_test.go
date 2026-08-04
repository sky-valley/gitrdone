package httpapi

import (
	"net/http"

	"github.com/sky-valley/gitrdone/internal/intentservice"
)

// NewTestServerWithClose exposes the in-process intent service only to package
// tests. Repository amendments are judgement behavior, not a public HTTP
// command.
func NewTestServerWithClose(config Config) (http.Handler, *intentservice.Service, func() error) {
	repos := newMemoryRepoStore(nil)
	gitStorage := newFilesystemGitStorage(config.StorageRoot)
	repos.gitStorage = gitStorage
	resources, err := buildServer(config, PendingRuntimeConfig{}, repos, newMemoryIdempotencyStore(nil), gitStorage)
	if err != nil {
		panic(err)
	}
	return resources.handler, resources.judgement, resources.close
}
