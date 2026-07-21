package httpapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/sky-valley/gitrdone/internal/gitintent"
	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentfs"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

type intentRepositoryRegistry struct {
	mu          sync.Mutex
	storageRoot string
	repos       repoGetter
	gitStorage  repoGitStorage
	entries     map[string]intentRepositoryEntry
	closed      bool
}

type intentRepositoryEntry struct {
	repository *intent.Repository
	ledger     *intentfs.Ledger
}

func newIntentRepositoryRegistry(storageRoot string, repos repoGetter, gitStorage repoGitStorage) *intentRepositoryRegistry {
	storageRoot = newFilesystemGitStorage(storageRoot).root
	return &intentRepositoryRegistry{
		storageRoot: storageRoot,
		repos:       repos,
		gitStorage:  gitStorage,
		entries:     make(map[string]intentRepositoryEntry),
	}
}

func (registry *intentRepositoryRegistry) Resolve(ctx context.Context, controlRepoID string) (intentservice.Repository, error) {
	repoID, ok := parseRepoControlID(controlRepoID)
	if !ok {
		return nil, intentservice.ErrRepositoryNotFound
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closed {
		return nil, errors.New("intent repository registry is closed")
	}
	if entry, found := registry.entries[repoID]; found {
		return entry.repository, nil
	}

	repo, err := registry.repos.GetRepo(ctx, getRepoInput{ID: repoID})
	if errors.Is(err, errRepoNotFound) {
		return nil, intentservice.ErrRepositoryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load repo metadata: %w", err)
	}
	return registry.openLocked(ctx, repo)
}

func (registry *intentRepositoryRegistry) Bootstrap(ctx context.Context, controlRepoID string, content intent.ContentRef) (intent.Revision, error) {
	repoID, ok := parseRepoControlID(controlRepoID)
	if !ok {
		return intent.Revision{}, intentservice.ErrRepositoryNotFound
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closed {
		return intent.Revision{}, errors.New("intent repository registry is closed")
	}
	if entry, found := registry.entries[repoID]; found {
		current := entry.repository.CurrentIntent()
		if current.Content == content {
			return current, nil
		}
		return intent.Revision{}, intentservice.ErrRepositoryAlreadyInitialized
	}

	repo, err := registry.repos.GetRepo(ctx, getRepoInput{ID: repoID})
	if errors.Is(err, errRepoNotFound) {
		return intent.Revision{}, intentservice.ErrRepositoryNotFound
	}
	if err != nil {
		return intent.Revision{}, fmt.Errorf("load repo metadata: %w", err)
	}
	gitDir, err := registry.gitStorage.BareRepoPath(ctx, repo.ID)
	if err != nil {
		return intent.Revision{}, fmt.Errorf("locate bare repo: %w", err)
	}
	gitRepository, err := gitintent.OpenRepository(ctx, gitDir, "refs/heads/"+repo.DefaultBranch)
	if err != nil {
		return intent.Revision{}, fmt.Errorf("open git intent repository: %w", err)
	}
	if err := gitRepository.Bootstrap(ctx, content); err != nil {
		if errors.Is(err, gitintent.ErrTrunkAlreadyInitialized) {
			return intent.Revision{}, intentservice.ErrRepositoryAlreadyInitialized
		}
		return intent.Revision{}, fmt.Errorf("bootstrap git intent repository: %w", err)
	}
	repository, err := registry.openLocked(ctx, repo)
	if err != nil {
		return intent.Revision{}, err
	}
	return repository.CurrentIntent(), nil
}

func (registry *intentRepositoryRegistry) openLocked(ctx context.Context, repo repoRecord) (*intent.Repository, error) {
	gitDir, err := registry.gitStorage.BareRepoPath(ctx, repo.ID)
	if err != nil {
		return nil, fmt.Errorf("locate bare repo: %w", err)
	}
	gitRepository, err := gitintent.OpenRepository(ctx, gitDir, "refs/heads/"+repo.DefaultBranch)
	if err != nil {
		return nil, fmt.Errorf("open git intent repository: %w", err)
	}
	initial, err := gitRepository.Current(ctx)
	if err != nil {
		return nil, fmt.Errorf("read initial trunk projection: %w", err)
	}

	ledgerDirectory := filepath.Join(registry.storageRoot, "intent", repo.ID)
	if err := os.MkdirAll(ledgerDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("create intent ledger directory: %w", err)
	}
	ledger, err := intentfs.Open(filepath.Join(ledgerDirectory, "ledger.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("open intent ledger: %w", err)
	}
	repository, err := intent.OpenRepository(ctx, initial, ledger, gitRepository, gitRepository)
	if err != nil {
		_ = ledger.Close()
		return nil, fmt.Errorf("open intent repository: %w", err)
	}
	registry.entries[repo.ID] = intentRepositoryEntry{repository: repository, ledger: ledger}
	return repository, nil
}

func (registry *intentRepositoryRegistry) Close() error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closed {
		return nil
	}
	registry.closed = true
	var closeErr error
	for repoID, entry := range registry.entries {
		if err := entry.ledger.Close(); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("close intent ledger for repo %s: %w", repoID, err))
		}
	}
	registry.entries = nil
	return closeErr
}
