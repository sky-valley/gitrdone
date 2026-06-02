package httpapi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type repoCreator interface {
	CreateRepo(ctx context.Context, input createRepoInput) (repoRecord, error)
}

type repoGetter interface {
	GetRepo(ctx context.Context, input getRepoInput) (repoRecord, error)
}

type repoArchiver interface {
	ArchiveRepo(ctx context.Context, input archiveRepoInput) (repoRecord, error)
}

type createRepoInput struct {
	Namespace     string
	Name          string
	DefaultBranch string
}

type getRepoInput struct {
	ID string
}

type archiveRepoInput struct {
	ID string
}

type repoRecord struct {
	ID            string
	Namespace     string
	Name          string
	DefaultBranch string
	ArchivedAt    time.Time
}

var errRepositoryNotImplemented = errors.New("repository not implemented")
var errRepoNotFound = errors.New("repo not found")

type failingRepoStore struct{}

func (failingRepoStore) CreateRepo(ctx context.Context, input createRepoInput) (repoRecord, error) {
	return repoRecord{}, errRepositoryNotImplemented
}

func (failingRepoStore) GetRepo(ctx context.Context, input getRepoInput) (repoRecord, error) {
	return repoRecord{}, errRepositoryNotImplemented
}

func (failingRepoStore) ArchiveRepo(ctx context.Context, input archiveRepoInput) (repoRecord, error) {
	return repoRecord{}, errRepositoryNotImplemented
}

type memoryRepoStore struct {
	mu     sync.Mutex
	repos  map[string]repoRecord
	byName map[string]string
	nextID int64
	now    func() time.Time
}

func newMemoryRepoStore(now func() time.Time) *memoryRepoStore {
	if now == nil {
		now = time.Now
	}
	return &memoryRepoStore{
		repos:  map[string]repoRecord{},
		byName: map[string]string{},
		now:    now,
	}
}

func (store *memoryRepoStore) CreateRepo(ctx context.Context, input createRepoInput) (repoRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	nameKey := input.Namespace + "/" + input.Name
	if id, ok := store.byName[nameKey]; ok {
		return store.repos[id], nil
	}

	store.nextID++
	repo := repoRecord{
		ID:            fmt.Sprintf("repo_%d", store.nextID),
		Namespace:     input.Namespace,
		Name:          input.Name,
		DefaultBranch: input.DefaultBranch,
	}
	store.repos[repo.ID] = repo
	store.byName[nameKey] = repo.ID
	return repo, nil
}

func (store *memoryRepoStore) GetRepo(ctx context.Context, input getRepoInput) (repoRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	repo, ok := store.repos[input.ID]
	if !ok {
		return repoRecord{}, errRepoNotFound
	}
	return repo, nil
}

func (store *memoryRepoStore) ArchiveRepo(ctx context.Context, input archiveRepoInput) (repoRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	repo, ok := store.repos[input.ID]
	if !ok {
		return repoRecord{}, errRepoNotFound
	}
	if repo.ArchivedAt.IsZero() {
		repo.ArchivedAt = store.now()
		store.repos[repo.ID] = repo
	}
	return repo, nil
}
