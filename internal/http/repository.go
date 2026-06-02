package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
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

type repoTokenCreator interface {
	CreateRepoToken(ctx context.Context, input createRepoTokenInput) (repoTokenRecord, error)
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

type createRepoTokenInput struct {
	RepoID     string
	Scope      string
	Subject    string
	TTLSeconds int
}

type repoRecord struct {
	ID            string
	Namespace     string
	Name          string
	DefaultBranch string
	ArchivedAt    time.Time
}

type repoTokenRecord struct {
	ID            string
	RepoID        string
	RepoNamespace string
	RepoName      string
	Token         string
	TokenHash     string
	Scope         string
	Subject       string
	ExpiresAt     time.Time
	CreatedAt     time.Time
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

func (failingRepoStore) CreateRepoToken(ctx context.Context, input createRepoTokenInput) (repoTokenRecord, error) {
	return repoTokenRecord{}, errRepositoryNotImplemented
}

type memoryRepoStore struct {
	mu             sync.Mutex
	repos          map[string]repoRecord
	byName         map[string]string
	tokens         map[string]repoTokenRecord
	tokenIDsByHash map[string]string
	now            func() time.Time
}

func newMemoryRepoStore(now func() time.Time) *memoryRepoStore {
	if now == nil {
		now = time.Now
	}
	return &memoryRepoStore{
		repos:          map[string]repoRecord{},
		byName:         map[string]string{},
		tokens:         map[string]repoTokenRecord{},
		tokenIDsByHash: map[string]string{},
		now:            now,
	}
}

func (store *memoryRepoStore) CreateRepo(ctx context.Context, input createRepoInput) (repoRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	nameKey := input.Namespace + "/" + input.Name
	if id, ok := store.byName[nameKey]; ok {
		return store.repos[id], nil
	}

	repoID, err := newUUID()
	if err != nil {
		return repoRecord{}, err
	}
	repo := repoRecord{
		ID:            repoID,
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

func (store *memoryRepoStore) CreateRepoToken(ctx context.Context, input createRepoTokenInput) (repoTokenRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	repo, ok := store.repos[input.RepoID]
	if !ok {
		return repoTokenRecord{}, errRepoNotFound
	}

	var token string
	var tokenHash string
	for {
		generated, err := generateRepoToken()
		if err != nil {
			return repoTokenRecord{}, err
		}
		hash := hashRepoToken(generated)
		if _, exists := store.tokenIDsByHash[hash]; exists {
			continue
		}
		token = generated
		tokenHash = hash
		break
	}

	now := store.now()
	tokenID, err := newUUID()
	if err != nil {
		return repoTokenRecord{}, err
	}
	record := repoTokenRecord{
		ID:            tokenID,
		RepoID:        repo.ID,
		RepoNamespace: repo.Namespace,
		RepoName:      repo.Name,
		Token:         token,
		TokenHash:     tokenHash,
		Scope:         input.Scope,
		Subject:       input.Subject,
		ExpiresAt:     now.Add(time.Duration(input.TTLSeconds) * time.Second),
		CreatedAt:     now,
	}
	stored := record
	stored.Token = ""
	store.tokens[record.ID] = stored
	store.tokenIDsByHash[record.TokenHash] = record.ID
	return record, nil
}

func newUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	uuid := hex.EncodeToString(raw[:])
	return uuid[0:8] + "-" + uuid[8:12] + "-" + uuid[12:16] + "-" + uuid[16:20] + "-" + uuid[20:32], nil
}

func generateRepoToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "gtd_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func hashRepoToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
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
