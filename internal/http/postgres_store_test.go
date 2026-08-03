package httpapi

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPostgresMigrationsUseNativeUUIDs(t *testing.T) {
	schema := strings.Join(postgresMigrations, "\n")
	for _, want := range []string{
		"id uuid PRIMARY KEY",
		"repo_id uuid NOT NULL REFERENCES repos(id)",
		"CREATE UNIQUE INDEX IF NOT EXISTS repo_tokens_token_hash_key ON repo_tokens(token_hash)",
		"PRIMARY KEY (scope, key)",
	} {
		if !strings.Contains(schema, want) {
			t.Fatalf("postgres migrations missing %q:\n%s", want, schema)
		}
	}
}

func TestPostgresRepoStoreContract(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("GITRDONE_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set GITRDONE_TEST_DATABASE_URL to run the Postgres store contract")
	}

	ctx := context.Background()
	db, err := openPostgresPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
	})
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "TRUNCATE repo_tokens, repos, idempotency_records RESTART IDENTITY CASCADE"); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 6, 2, 16, 0, 0, 0, time.UTC)
	store := newPostgresRepoStore(db, func() time.Time {
		return now
	})
	store.gitStorage = fixedRepoGitStorage{path: "/tmp/repo.git"}

	repo, err := store.CreateRepo(ctx, createRepoInput{
		Namespace:     "fixture",
		Name:          "postgres-contract",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalUUIDV4(t, repo.ID)

	again, err := store.CreateRepo(ctx, createRepoInput{
		Namespace:     "fixture",
		Name:          "postgres-contract",
		DefaultBranch: "trunk",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again != repo {
		t.Fatalf("idempotent create = %#v, want %#v", again, repo)
	}

	token, err := store.CreateRepoToken(ctx, createRepoTokenInput{
		RepoID:     repo.ID,
		Scope:      "read",
		Subject:    "reader-job",
		TTLSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if token.Token == "" {
		t.Fatal("token value is empty")
	}

	now = now.Add(time.Minute)
	grant, err := store.AuthorizeGitAccess(ctx, authorizeGitAccessInput{
		RepoID:    repo.ID,
		Token:     token.Token,
		Operation: gitOperationRead,
	})
	if err != nil {
		t.Fatal(err)
	}
	if grant.Subject != "reader-job" {
		t.Fatalf("subject = %q, want reader-job", grant.Subject)
	}
	if grant.CanonicalRef != "refs/heads/main" {
		t.Fatalf("canonical ref = %q, want refs/heads/main", grant.CanonicalRef)
	}
	intentGrant, err := store.AuthorizeRepoAccess(ctx, authorizeRepoAccessInput{
		RepoID:     repo.ID,
		Token:      token.Token,
		Capability: repoCapabilityInspect,
	})
	if err != nil {
		t.Fatal(err)
	}
	if intentGrant.Subject != "reader-job" {
		t.Fatalf("intent grant subject = %q, want reader-job", intentGrant.Subject)
	}
	_, err = store.AuthorizeRepoAccess(ctx, authorizeRepoAccessInput{
		RepoID:     repo.ID,
		Token:      token.Token,
		Capability: repoCapabilityPropose,
	})
	if !errors.Is(err, errRepoTokenForbidden) {
		t.Fatalf("read token propose error = %v, want errRepoTokenForbidden", err)
	}

	tokens, err := store.ListRepoTokens(ctx, listRepoTokensInput{RepoID: repo.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("tokens length = %d, want 1", len(tokens))
	}
	if tokens[0].Token != "" {
		t.Fatalf("listed token kept plaintext: %q", tokens[0].Token)
	}
	if !tokens[0].LastUsedAt.Equal(now) {
		t.Fatalf("lastUsedAt = %s, want %s", tokens[0].LastUsedAt, now)
	}

	now = now.Add(time.Minute)
	revoked, err := store.RevokeRepoToken(ctx, revokeRepoTokenInput{
		RepoID:  repo.ID,
		TokenID: token.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !revoked.RevokedAt.Equal(now) {
		t.Fatalf("revokedAt = %s, want %s", revoked.RevokedAt, now)
	}
	_, err = store.AuthorizeGitAccess(ctx, authorizeGitAccessInput{
		RepoID:    repo.ID,
		Token:     token.Token,
		Operation: gitOperationRead,
	})
	if !errors.Is(err, errRepoTokenInvalid) {
		t.Fatalf("AuthorizeGitAccess error = %v, want errRepoTokenInvalid", err)
	}
	reviewer, err := store.CreateRepoToken(ctx, createRepoTokenInput{
		RepoID: repo.ID, Scope: "review", Subject: "Noam+GitRDone@Company.Example", TTLSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("create review token: %v", err)
	}
	if reviewer.Subject != "noam+gitrdone@company.example" {
		t.Fatalf("review subject = %q, want canonical email", reviewer.Subject)
	}
	if _, err := store.AuthorizeRepoAccess(ctx, authorizeRepoAccessInput{
		RepoID: repo.ID, Token: reviewer.Token, Capability: repoCapabilityReview,
	}); err != nil {
		t.Fatalf("authorize review token: %v", err)
	}
	if _, err := store.CreateRepoToken(ctx, createRepoTokenInput{
		RepoID: repo.ID, Scope: "review", Subject: "control-api", TTLSeconds: 3600,
	}); !errors.Is(err, errRepoTokenReviewSubjectInvalid) {
		t.Fatalf("Postgres non-email review subject error = %v, want errRepoTokenReviewSubjectInvalid", err)
	}

	archived, err := store.ArchiveRepo(ctx, archiveRepoInput{ID: repo.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !archived.ArchivedAt.Equal(now) {
		t.Fatalf("archivedAt = %s, want %s", archived.ArchivedAt, now)
	}
}

func TestPostgresTokenIdempotencyRollsBackTokenWhenReplayRecordFails(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("GITRDONE_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set GITRDONE_TEST_DATABASE_URL to run the Postgres store contract")
	}

	ctx := context.Background()
	db, err := openPostgresPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
	})
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "ALTER TABLE idempotency_records DROP COLUMN IF EXISTS forced_failure"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(ctx, "ALTER TABLE idempotency_records DROP COLUMN IF EXISTS forced_failure")
	})
	if _, err := db.Exec(ctx, "TRUNCATE repo_tokens, repos, idempotency_records RESTART IDENTITY CASCADE"); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 6, 2, 16, 0, 0, 0, time.UTC)
	store := newPostgresRepoStore(db, func() time.Time {
		return now
	})
	store.gitStorage = fixedRepoGitStorage{path: "/tmp/repo.git"}

	repo, err := store.CreateRepo(ctx, createRepoInput{
		Namespace:     "fixture",
		Name:          "postgres-idempotency-rollback",
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(ctx, "ALTER TABLE idempotency_records ADD COLUMN forced_failure text NOT NULL"); err != nil {
		t.Fatal(err)
	}

	idempotency := newPostgresIdempotencyStore(db, func() time.Time {
		return now
	})
	_, err = idempotency.Do(ctx, idempotencyInput{
		Scope:       createRepoTokenIdempotencyScope(repo.ID),
		Key:         "request-1",
		RequestHash: "same-request",
	}, func(createCtx context.Context) (createRepoTokenResponse, error) {
		token, err := store.CreateRepoToken(createCtx, createRepoTokenInput{
			RepoID:     repo.ID,
			Scope:      "read",
			Subject:    "reader-job",
			TTLSeconds: 3600,
		})
		if err != nil {
			return createRepoTokenResponse{}, err
		}
		return createRepoTokenResponse{
			ID:        formatRepoTokenControlID(token.ID),
			Token:     token.Token,
			ExpiresAt: token.ExpiresAt.Format(time.RFC3339),
			GitURL:    repoGitURL("https://gitrdone.test", token.RepoID),
			Scope:     token.Scope,
			Subject:   token.Subject,
		}, nil
	})
	if err == nil {
		t.Fatal("idempotency Do error = nil, want forced insert failure")
	}

	var tokenCount int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM repo_tokens WHERE repo_id = $1::uuid", repo.ID).Scan(&tokenCount); err != nil {
		t.Fatal(err)
	}
	if tokenCount != 0 {
		t.Fatalf("persisted token count = %d, want 0", tokenCount)
	}
}
