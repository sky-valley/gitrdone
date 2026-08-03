package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMemoryRepoStoreCreateGetArchive(t *testing.T) {
	now := time.Date(2026, 6, 1, 18, 0, 0, 0, time.UTC)
	store := newMemoryRepoStore(func() time.Time {
		return now
	})

	created, err := store.CreateRepo(context.Background(), createRepoInput{
		Namespace:     "fixture",
		Name:          "project-alpha",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("created id is empty")
	}
	assertCanonicalUUIDV4(t, created.ID)
	if strings.HasPrefix(created.ID, "repo_") {
		t.Fatalf("store id = %q, want raw UUID without repo_ prefix", created.ID)
	}
	if created.Namespace != "fixture" {
		t.Fatalf("namespace = %q, want fixture", created.Namespace)
	}
	if created.Name != "project-alpha" {
		t.Fatalf("name = %q, want project-alpha", created.Name)
	}
	if created.DefaultBranch != "main" {
		t.Fatalf("defaultBranch = %q, want main", created.DefaultBranch)
	}
	if !created.ArchivedAt.IsZero() {
		t.Fatalf("archivedAt = %s, want zero", created.ArchivedAt)
	}

	got, err := store.GetRepo(context.Background(), getRepoInput{ID: created.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got != created {
		t.Fatalf("got = %#v, want %#v", got, created)
	}

	archived, err := store.ArchiveRepo(context.Background(), archiveRepoInput{ID: created.ID})
	if err != nil {
		t.Fatal(err)
	}
	if archived.ID != created.ID {
		t.Fatalf("archived id = %q, want %q", archived.ID, created.ID)
	}
	if archived.ArchivedAt != now {
		t.Fatalf("archivedAt = %s, want %s", archived.ArchivedAt, now)
	}

	again, err := store.ArchiveRepo(context.Background(), archiveRepoInput{ID: created.ID})
	if err != nil {
		t.Fatal(err)
	}
	if again.ArchivedAt != now {
		t.Fatalf("second archive changed archivedAt to %s, want %s", again.ArchivedAt, now)
	}
}

func TestMemoryRepoStoreCreateRepoIsIdempotentByExternalName(t *testing.T) {
	store := newMemoryRepoStore(time.Now)

	first, err := store.CreateRepo(context.Background(), createRepoInput{
		Namespace:     "fixture",
		Name:          "project-alpha",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateRepo(context.Background(), createRepoInput{
		Namespace:     "fixture",
		Name:          "project-alpha",
		DefaultBranch: "trunk",
	})
	if err != nil {
		t.Fatal(err)
	}

	if second != first {
		t.Fatalf("second create = %#v, want existing %#v", second, first)
	}
}

func TestMemoryRepoStoreReturnsNotFoundForUnknownRepoID(t *testing.T) {
	store := newMemoryRepoStore(time.Now)

	_, err := store.GetRepo(context.Background(), getRepoInput{ID: "repo_missing"})
	if !errors.Is(err, errRepoNotFound) {
		t.Fatalf("GetRepo error = %v, want errRepoNotFound", err)
	}

	_, err = store.ArchiveRepo(context.Background(), archiveRepoInput{ID: "repo_missing"})
	if !errors.Is(err, errRepoNotFound) {
		t.Fatalf("ArchiveRepo error = %v, want errRepoNotFound", err)
	}
}

func TestMemoryRepoStoreCreateRepoToken(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	store := newMemoryRepoStore(func() time.Time {
		return now
	})

	repo, err := store.CreateRepo(context.Background(), createRepoInput{
		Namespace:     "fixture",
		Name:          "project-token",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}

	token, err := store.CreateRepoToken(context.Background(), createRepoTokenInput{
		RepoID:     repo.ID,
		Scope:      "readwrite",
		Subject:    "bootstrap-job-abc",
		TTLSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}

	if token.ID == "" {
		t.Fatal("token id is empty")
	}
	assertCanonicalUUIDV4(t, token.ID)
	if strings.HasPrefix(token.ID, "token_") {
		t.Fatalf("token id = %q, want raw UUID without token_ prefix", token.ID)
	}
	if token.RepoID != repo.ID {
		t.Fatalf("repoID = %q, want %q", token.RepoID, repo.ID)
	}
	if token.Token == "" {
		t.Fatal("token value is empty")
	}
	if token.TokenHash == "" {
		t.Fatal("token hash is empty")
	}
	if token.TokenHash == token.Token {
		t.Fatal("token hash stored plaintext token")
	}
	if token.Scope != "readwrite" {
		t.Fatalf("scope = %q, want readwrite", token.Scope)
	}
	if token.Subject != "bootstrap-job-abc" {
		t.Fatalf("subject = %q, want bootstrap-job-abc", token.Subject)
	}
	if token.CreatedAt != now {
		t.Fatalf("createdAt = %s, want %s", token.CreatedAt, now)
	}
	if token.ExpiresAt != now.Add(time.Hour) {
		t.Fatalf("expiresAt = %s, want %s", token.ExpiresAt, now.Add(time.Hour))
	}
	if !token.RevokedAt.IsZero() {
		t.Fatalf("revokedAt = %s, want zero", token.RevokedAt)
	}
	if !token.LastUsedAt.IsZero() {
		t.Fatalf("lastUsedAt = %s, want zero", token.LastUsedAt)
	}

	stored := store.tokens[token.ID]
	if stored.Token != "" {
		t.Fatalf("stored token kept plaintext: %q", stored.Token)
	}
	if stored.TokenHash != token.TokenHash {
		t.Fatalf("stored token hash = %q, want %q", stored.TokenHash, token.TokenHash)
	}
	if store.tokenIDsByHash[token.TokenHash] != token.ID {
		t.Fatalf("token hash lookup did not point at %q", token.ID)
	}
}

func TestMemoryRepoStoreListRevokeAndAuditRepoTokens(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	store := newMemoryRepoStore(func() time.Time {
		return now
	})
	store.gitStorage = fixedRepoGitStorage{path: "/tmp/repo.git"}
	repo, err := store.CreateRepo(context.Background(), createRepoInput{
		Namespace:     "fixture",
		Name:          "project-token-lifecycle",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateRepoToken(context.Background(), createRepoTokenInput{
		RepoID:     repo.ID,
		Scope:      "read",
		Subject:    "reader-job",
		TTLSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := store.ListRepoTokens(context.Background(), listRepoTokensInput{RepoID: repo.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("tokens length = %d, want 1", len(tokens))
	}
	if tokens[0].ID != token.ID {
		t.Fatalf("token id = %q, want %q", tokens[0].ID, token.ID)
	}
	if tokens[0].Token != "" {
		t.Fatalf("listed token kept plaintext: %q", tokens[0].Token)
	}

	now = now.Add(time.Minute)
	grant, err := store.AuthorizeGitAccess(context.Background(), authorizeGitAccessInput{
		RepoID:    repo.ID,
		Token:     token.Token,
		Operation: gitOperationRead,
	})
	if err != nil {
		t.Fatal(err)
	}
	if grant.Subject != "reader-job" {
		t.Fatalf("grant subject = %q, want reader-job", grant.Subject)
	}
	if grant.CanonicalRef != "refs/heads/main" {
		t.Fatalf("grant canonical ref = %q, want refs/heads/main", grant.CanonicalRef)
	}
	tokens, err = store.ListRepoTokens(context.Background(), listRepoTokensInput{RepoID: repo.ID})
	if err != nil {
		t.Fatal(err)
	}
	if tokens[0].LastUsedAt != now {
		t.Fatalf("lastUsedAt = %s, want %s", tokens[0].LastUsedAt, now)
	}

	now = now.Add(time.Minute)
	revoked, err := store.RevokeRepoToken(context.Background(), revokeRepoTokenInput{
		RepoID:  repo.ID,
		TokenID: token.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if revoked.RevokedAt != now {
		t.Fatalf("revokedAt = %s, want %s", revoked.RevokedAt, now)
	}
	revokedAgain, err := store.RevokeRepoToken(context.Background(), revokeRepoTokenInput{
		RepoID:  repo.ID,
		TokenID: token.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if revokedAgain.RevokedAt != now {
		t.Fatalf("second revoke changed revokedAt to %s, want %s", revokedAgain.RevokedAt, now)
	}

	_, err = store.AuthorizeGitAccess(context.Background(), authorizeGitAccessInput{
		RepoID:    repo.ID,
		Token:     token.Token,
		Operation: gitOperationRead,
	})
	if !errors.Is(err, errRepoTokenInvalid) {
		t.Fatalf("AuthorizeGitAccess error = %v, want errRepoTokenInvalid", err)
	}
}

func TestMemoryRepoStoreReviewScopeCanInspectAndReviewButCannotPropose(t *testing.T) {
	store := newMemoryRepoStore(nil)
	store.gitStorage = fixedRepoGitStorage{path: "/tmp/repo.git"}
	repo, err := store.CreateRepo(context.Background(), createRepoInput{
		Namespace:     "fixture",
		Name:          "review-authority",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateRepoToken(context.Background(), createRepoTokenInput{
		RepoID:     repo.ID,
		Scope:      "review",
		Subject:    "Noam+GitRDone@Company.Example",
		TTLSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}

	gitGrant, err := store.AuthorizeGitAccess(context.Background(), authorizeGitAccessInput{
		RepoID: repo.ID, Token: token.Token, Operation: gitOperationRead,
	})
	if err != nil {
		t.Fatalf("review token read: %v", err)
	}
	if gitGrant.Subject != "noam+gitrdone@company.example" {
		t.Fatalf("git subject = %q, want reviewer email", gitGrant.Subject)
	}
	if _, err := store.AuthorizeGitAccess(context.Background(), authorizeGitAccessInput{
		RepoID: repo.ID, Token: token.Token, Operation: gitOperationWrite,
	}); !errors.Is(err, errRepoTokenForbidden) {
		t.Fatalf("review token write error = %v, want errRepoTokenForbidden", err)
	}
	for _, capability := range []repoCapability{repoCapabilityInspect, repoCapabilityReview} {
		grant, err := store.AuthorizeRepoAccess(context.Background(), authorizeRepoAccessInput{
			RepoID: repo.ID, Token: token.Token, Capability: capability,
		})
		if err != nil {
			t.Fatalf("review token capability %q: %v", capability, err)
		}
		if grant.Subject != "noam+gitrdone@company.example" {
			t.Fatalf("capability %q subject = %q, want reviewer email", capability, grant.Subject)
		}
	}
	if _, err := store.AuthorizeRepoAccess(context.Background(), authorizeRepoAccessInput{
		RepoID: repo.ID, Token: token.Token, Capability: repoCapabilityPropose,
	}); !errors.Is(err, errRepoTokenForbidden) {
		t.Fatalf("review token propose error = %v, want errRepoTokenForbidden", err)
	}
	if _, err := store.CreateRepoToken(context.Background(), createRepoTokenInput{
		RepoID: repo.ID, Scope: "review", Subject: "control-api", TTLSeconds: 3600,
	}); !errors.Is(err, errRepoTokenReviewSubjectInvalid) {
		t.Fatalf("non-email review subject error = %v, want errRepoTokenReviewSubjectInvalid", err)
	}
	for _, scope := range []string{"read", "write", "readwrite"} {
		ordinary, err := store.CreateRepoToken(context.Background(), createRepoTokenInput{
			RepoID: repo.ID, Scope: scope, Subject: "noam@company.example", TTLSeconds: 3600,
		})
		if err != nil {
			t.Fatalf("create %s token: %v", scope, err)
		}
		if _, err := store.AuthorizeRepoAccess(context.Background(), authorizeRepoAccessInput{
			RepoID: repo.ID, Token: ordinary.Token, Capability: repoCapabilityReview,
		}); !errors.Is(err, errRepoTokenForbidden) {
			t.Fatalf("%s token review error = %v, want errRepoTokenForbidden", scope, err)
		}
	}
}

func TestMemoryRepoStoreTokenLifecycleRequiresMatchingRepo(t *testing.T) {
	store := newMemoryRepoStore(time.Now)
	firstRepo, err := store.CreateRepo(context.Background(), createRepoInput{
		Namespace:     "fixture",
		Name:          "project-token-one",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondRepo, err := store.CreateRepo(context.Background(), createRepoInput{
		Namespace:     "fixture",
		Name:          "project-token-two",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateRepoToken(context.Background(), createRepoTokenInput{
		RepoID:     firstRepo.ID,
		Scope:      "read",
		Subject:    "reader-job",
		TTLSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.RevokeRepoToken(context.Background(), revokeRepoTokenInput{
		RepoID:  secondRepo.ID,
		TokenID: token.ID,
	})
	if !errors.Is(err, errRepoTokenNotFound) {
		t.Fatalf("RevokeRepoToken error = %v, want errRepoTokenNotFound", err)
	}
}

func TestMemoryRepoStoreCreateRepoTokenRequiresExistingRepo(t *testing.T) {
	store := newMemoryRepoStore(time.Now)

	_, err := store.CreateRepoToken(context.Background(), createRepoTokenInput{
		RepoID:     "repo_missing",
		Scope:      "read",
		Subject:    "bootstrap-job-abc",
		TTLSeconds: 3600,
	})
	if !errors.Is(err, errRepoNotFound) {
		t.Fatalf("CreateRepoToken error = %v, want errRepoNotFound", err)
	}
}

type fixedRepoGitStorage struct {
	path string
}

func (storage fixedRepoGitStorage) InitBareRepo(ctx context.Context, repoID string, defaultBranch string) error {
	return nil
}

func (storage fixedRepoGitStorage) DeleteBareRepo(ctx context.Context, repoID string) error {
	return nil
}

func (storage fixedRepoGitStorage) BareRepoPath(ctx context.Context, repoID string) (string, error) {
	return storage.path, nil
}

func assertCanonicalUUIDV4(t *testing.T, value string) {
	t.Helper()
	if len(value) != 36 {
		t.Fatalf("uuid = %q, length %d, want 36", value, len(value))
	}
	dashIndexes := map[int]bool{8: true, 13: true, 18: true, 23: true}
	for index, char := range value {
		if dashIndexes[index] {
			if char != '-' {
				t.Fatalf("uuid = %q, want dash at index %d", value, index)
			}
			continue
		}
		if !isLowerHex(char) {
			t.Fatalf("uuid = %q, want lowercase hex at index %d", value, index)
		}
	}
	if value[14] != '4' {
		t.Fatalf("uuid = %q, want version 4", value)
	}
	switch value[19] {
	case '8', '9', 'a', 'b':
	default:
		t.Fatalf("uuid = %q, want RFC 4122 variant", value)
	}
}

func isLowerHex(char rune) bool {
	return (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')
}
