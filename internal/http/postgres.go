package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var postgresMigrations = []string{
	`CREATE TABLE IF NOT EXISTS repos (
		id uuid PRIMARY KEY,
		namespace text NOT NULL,
		name text NOT NULL,
		default_branch text NOT NULL,
		archived_at timestamptz,
		created_at timestamptz NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS repos_namespace_name_key ON repos(namespace, name)`,
	`CREATE TABLE IF NOT EXISTS repo_tokens (
		id uuid PRIMARY KEY,
		repo_id uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
		token_hash text NOT NULL,
		scope text NOT NULL,
		subject text NOT NULL,
		expires_at timestamptz NOT NULL,
		created_at timestamptz NOT NULL,
		revoked_at timestamptz,
		last_used_at timestamptz
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS repo_tokens_token_hash_key ON repo_tokens(token_hash)`,
	`CREATE INDEX IF NOT EXISTS repo_tokens_repo_created_id_idx ON repo_tokens(repo_id, created_at, id)`,
	`CREATE TABLE IF NOT EXISTS idempotency_records (
		scope text NOT NULL,
		key text NOT NULL,
		request_hash text NOT NULL,
		response jsonb NOT NULL,
		expires_at timestamptz NOT NULL,
		PRIMARY KEY (scope, key)
	)`,
	`CREATE INDEX IF NOT EXISTS idempotency_records_expires_at_idx ON idempotency_records(expires_at)`,
}

func openPostgresPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, databaseURL)
}

func migratePostgres(ctx context.Context, db *pgxpool.Pool) error {
	for _, migration := range postgresMigrations {
		if _, err := db.Exec(ctx, migration); err != nil {
			return err
		}
	}
	return nil
}

type postgresRepoStore struct {
	db         *pgxpool.Pool
	gitStorage repoGitStorage
	now        func() time.Time
}

type postgresQueryer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type postgresTxContextKey struct{}

func contextWithPostgresTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, postgresTxContextKey{}, tx)
}

func postgresTxFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(postgresTxContextKey{}).(pgx.Tx)
	return tx, ok
}

func newPostgresRepoStore(db *pgxpool.Pool, now func() time.Time) *postgresRepoStore {
	if now == nil {
		now = time.Now
	}
	return &postgresRepoStore{
		db:         db,
		gitStorage: noopRepoGitStorage{},
		now:        now,
	}
}

func (store *postgresRepoStore) CreateRepo(ctx context.Context, input createRepoInput) (repoRecord, error) {
	tx, err := store.db.Begin(ctx)
	if err != nil {
		return repoRecord{}, err
	}
	defer rollbackTx(ctx, tx)

	repo, found, err := selectRepoByName(ctx, tx, input.Namespace, input.Name)
	if err != nil {
		return repoRecord{}, err
	}
	if found {
		if err := tx.Commit(ctx); err != nil {
			return repoRecord{}, err
		}
		return repo, nil
	}

	repoID, err := newUUID()
	if err != nil {
		return repoRecord{}, err
	}
	repo = repoRecord{
		ID:            repoID,
		Namespace:     input.Namespace,
		Name:          input.Name,
		DefaultBranch: input.DefaultBranch,
	}
	if err := insertRepo(ctx, tx, repo, store.now()); err != nil {
		return repoRecord{}, err
	}
	if err := store.gitStorage.InitBareRepo(ctx, repoID, input.DefaultBranch); err != nil {
		_ = store.gitStorage.DeleteBareRepo(ctx, repoID)
		return repoRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = store.gitStorage.DeleteBareRepo(ctx, repoID)
		return repoRecord{}, err
	}
	return repo, nil
}

func (store *postgresRepoStore) GetRepo(ctx context.Context, input getRepoInput) (repoRecord, error) {
	return getRepoWithQueryer(ctx, store.db, input)
}

func getRepoWithQueryer(ctx context.Context, queryer postgresQueryer, input getRepoInput) (repoRecord, error) {
	repo, err := scanRepo(queryer.QueryRow(ctx, `
		SELECT id::text, namespace, name, default_branch, archived_at
		FROM repos
		WHERE id = $1::uuid
	`, input.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return repoRecord{}, errRepoNotFound
	}
	return repo, err
}

func (store *postgresRepoStore) ArchiveRepo(ctx context.Context, input archiveRepoInput) (repoRecord, error) {
	repo, err := scanRepo(store.db.QueryRow(ctx, `
		UPDATE repos
		SET archived_at = COALESCE(archived_at, $2)
		WHERE id = $1::uuid
		RETURNING id::text, namespace, name, default_branch, archived_at
	`, input.ID, store.now()))
	if errors.Is(err, pgx.ErrNoRows) {
		return repoRecord{}, errRepoNotFound
	}
	return repo, err
}

func (store *postgresRepoStore) CreateRepoToken(ctx context.Context, input createRepoTokenInput) (repoTokenRecord, error) {
	queryer := postgresQueryer(store.db)
	if tx, ok := postgresTxFromContext(ctx); ok {
		queryer = tx
	}
	return store.createRepoTokenWithQueryer(ctx, queryer, input)
}

func (store *postgresRepoStore) createRepoTokenWithQueryer(ctx context.Context, queryer postgresQueryer, input createRepoTokenInput) (repoTokenRecord, error) {
	repo, err := getRepoWithQueryer(ctx, queryer, getRepoInput{ID: input.RepoID})
	if err != nil {
		return repoTokenRecord{}, err
	}

	for {
		generated, err := generateRepoToken()
		if err != nil {
			return repoTokenRecord{}, err
		}
		tokenHash := hashRepoToken(generated)
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
			Token:         generated,
			TokenHash:     tokenHash,
			Scope:         input.Scope,
			Subject:       input.Subject,
			ExpiresAt:     now.Add(time.Duration(input.TTLSeconds) * time.Second),
			CreatedAt:     now,
		}

		_, err = queryer.Exec(ctx, `
			INSERT INTO repo_tokens (id, repo_id, token_hash, scope, subject, expires_at, created_at)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7)
		`, record.ID, record.RepoID, record.TokenHash, record.Scope, record.Subject, record.ExpiresAt, record.CreatedAt)
		if err != nil {
			if isPostgresUniqueViolation(err) {
				continue
			}
			return repoTokenRecord{}, err
		}
		return record, nil
	}
}

func (store *postgresRepoStore) ListRepoTokens(ctx context.Context, input listRepoTokensInput) ([]repoTokenRecord, error) {
	if _, err := store.GetRepo(ctx, getRepoInput{ID: input.RepoID}); err != nil {
		return nil, err
	}
	rows, err := store.db.Query(ctx, `
		SELECT
			t.id::text, t.repo_id::text, r.namespace, r.name, t.token_hash, t.scope, t.subject,
			t.expires_at, t.created_at, t.revoked_at, t.last_used_at
		FROM repo_tokens t
		JOIN repos r ON r.id = t.repo_id
		WHERE t.repo_id = $1::uuid
		ORDER BY t.created_at ASC, t.id ASC
	`, input.RepoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []repoTokenRecord
	for rows.Next() {
		token, err := scanRepoToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tokens, nil
}

func (store *postgresRepoStore) RevokeRepoToken(ctx context.Context, input revokeRepoTokenInput) (repoTokenRecord, error) {
	if _, err := store.GetRepo(ctx, getRepoInput{ID: input.RepoID}); err != nil {
		return repoTokenRecord{}, err
	}
	token, err := scanRepoToken(store.db.QueryRow(ctx, `
		UPDATE repo_tokens
		SET revoked_at = COALESCE(revoked_at, $3)
		WHERE id = $1::uuid AND repo_id = $2::uuid
		RETURNING
			id::text, repo_id::text,
			(SELECT namespace FROM repos WHERE id = repo_tokens.repo_id),
			(SELECT name FROM repos WHERE id = repo_tokens.repo_id),
			token_hash, scope, subject, expires_at, created_at, revoked_at, last_used_at
	`, input.TokenID, input.RepoID, store.now()))
	if errors.Is(err, pgx.ErrNoRows) {
		return repoTokenRecord{}, errRepoTokenNotFound
	}
	return token, err
}

func (store *postgresRepoStore) AuthorizeGitAccess(ctx context.Context, input authorizeGitAccessInput) (gitAccessGrant, error) {
	repo, err := store.GetRepo(ctx, getRepoInput{ID: input.RepoID})
	if err != nil {
		return gitAccessGrant{}, err
	}
	if !repo.ArchivedAt.IsZero() {
		return gitAccessGrant{}, errRepoArchived
	}
	if input.Token == "" {
		return gitAccessGrant{}, errRepoTokenInvalid
	}

	token, err := scanRepoToken(store.db.QueryRow(ctx, `
		SELECT
			t.id::text, t.repo_id::text, r.namespace, r.name, t.token_hash, t.scope, t.subject,
			t.expires_at, t.created_at, t.revoked_at, t.last_used_at
		FROM repo_tokens t
		JOIN repos r ON r.id = t.repo_id
		WHERE t.token_hash = $1
	`, hashRepoToken(input.Token)))
	if errors.Is(err, pgx.ErrNoRows) {
		return gitAccessGrant{}, errRepoTokenInvalid
	}
	if err != nil {
		return gitAccessGrant{}, err
	}
	if token.RepoID != repo.ID {
		return gitAccessGrant{}, errRepoTokenInvalid
	}
	if !token.RevokedAt.IsZero() {
		return gitAccessGrant{}, errRepoTokenInvalid
	}
	now := store.now()
	if !now.Before(token.ExpiresAt) {
		return gitAccessGrant{}, errRepoTokenInvalid
	}
	if !scopeAllowsGitOperation(token.Scope, input.Operation) {
		return gitAccessGrant{}, errRepoTokenForbidden
	}
	repoPath, err := store.gitStorage.BareRepoPath(ctx, repo.ID)
	if err != nil {
		return gitAccessGrant{}, err
	}
	if _, err := store.db.Exec(ctx, `
		UPDATE repo_tokens
		SET last_used_at = $2
		WHERE id = $1::uuid
	`, token.ID, now); err != nil {
		return gitAccessGrant{}, err
	}
	return gitAccessGrant{
		RepoID:   repo.ID,
		RepoPath: repoPath,
		Subject:  token.Subject,
	}, nil
}

type postgresIdempotencyStore struct {
	db  *pgxpool.Pool
	now func() time.Time
	ttl time.Duration
}

func newPostgresIdempotencyStore(db *pgxpool.Pool, now func() time.Time) postgresIdempotencyStore {
	if now == nil {
		now = time.Now
	}
	return postgresIdempotencyStore{
		db:  db,
		now: now,
		ttl: defaultIdempotencyRecordTTL,
	}
}

func (store postgresIdempotencyStore) Do(ctx context.Context, input idempotencyInput, create func(context.Context) (createRepoTokenResponse, error)) (idempotencyResult, error) {
	if err := ctx.Err(); err != nil {
		return idempotencyResult{}, err
	}
	if input.Key == "" {
		response, err := create(ctx)
		return idempotencyResult{Response: response}, err
	}

	tx, err := store.db.Begin(ctx)
	if err != nil {
		return idempotencyResult{}, err
	}
	defer rollbackTx(ctx, tx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`, input.Scope, input.Key); err != nil {
		return idempotencyResult{}, err
	}

	now := store.now()
	record, found, err := selectIdempotencyRecord(ctx, tx, input)
	if err != nil {
		return idempotencyResult{}, err
	}
	if found {
		if now.Before(record.ExpiresAt) {
			if record.RequestHash != input.RequestHash {
				return idempotencyResult{}, errIdempotencyConflict
			}
			if err := tx.Commit(ctx); err != nil {
				return idempotencyResult{}, err
			}
			return idempotencyResult{
				Response: record.Response,
				Replayed: true,
			}, nil
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM idempotency_records
			WHERE scope = $1 AND key = $2
		`, input.Scope, input.Key); err != nil {
			return idempotencyResult{}, err
		}
	}

	response, err := create(contextWithPostgresTx(ctx, tx))
	if err != nil {
		return idempotencyResult{}, err
	}
	rawResponse, err := json.Marshal(response)
	if err != nil {
		return idempotencyResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO idempotency_records (scope, key, request_hash, response, expires_at)
		VALUES ($1, $2, $3, $4, $5)
	`, input.Scope, input.Key, input.RequestHash, rawResponse, now.Add(store.ttl)); err != nil {
		return idempotencyResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return idempotencyResult{}, err
	}
	return idempotencyResult{Response: response}, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func selectRepoByName(ctx context.Context, tx pgx.Tx, namespace string, name string) (repoRecord, bool, error) {
	repo, err := scanRepo(tx.QueryRow(ctx, `
		SELECT id::text, namespace, name, default_branch, archived_at
		FROM repos
		WHERE namespace = $1 AND name = $2
	`, namespace, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return repoRecord{}, false, nil
	}
	if err != nil {
		return repoRecord{}, false, err
	}
	return repo, true, nil
}

func insertRepo(ctx context.Context, tx pgx.Tx, repo repoRecord, createdAt time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO repos (id, namespace, name, default_branch, created_at)
		VALUES ($1::uuid, $2, $3, $4, $5)
	`, repo.ID, repo.Namespace, repo.Name, repo.DefaultBranch, createdAt)
	return err
}

func scanRepo(row rowScanner) (repoRecord, error) {
	var repo repoRecord
	var archivedAt pgtype.Timestamptz
	if err := row.Scan(&repo.ID, &repo.Namespace, &repo.Name, &repo.DefaultBranch, &archivedAt); err != nil {
		return repoRecord{}, err
	}
	repo.ArchivedAt = timeFromPostgres(archivedAt)
	return repo, nil
}

func scanRepoToken(row rowScanner) (repoTokenRecord, error) {
	var token repoTokenRecord
	var revokedAt pgtype.Timestamptz
	var lastUsedAt pgtype.Timestamptz
	if err := row.Scan(
		&token.ID,
		&token.RepoID,
		&token.RepoNamespace,
		&token.RepoName,
		&token.TokenHash,
		&token.Scope,
		&token.Subject,
		&token.ExpiresAt,
		&token.CreatedAt,
		&revokedAt,
		&lastUsedAt,
	); err != nil {
		return repoTokenRecord{}, err
	}
	token.RevokedAt = timeFromPostgres(revokedAt)
	token.LastUsedAt = timeFromPostgres(lastUsedAt)
	return token, nil
}

func selectIdempotencyRecord(ctx context.Context, tx pgx.Tx, input idempotencyInput) (idempotencyRecord, bool, error) {
	var record idempotencyRecord
	var rawResponse []byte
	err := tx.QueryRow(ctx, `
		SELECT request_hash, response, expires_at
		FROM idempotency_records
		WHERE scope = $1 AND key = $2
		FOR UPDATE
	`, input.Scope, input.Key).Scan(&record.RequestHash, &rawResponse, &record.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotencyRecord{}, false, nil
	}
	if err != nil {
		return idempotencyRecord{}, false, err
	}
	if err := json.Unmarshal(rawResponse, &record.Response); err != nil {
		return idempotencyRecord{}, false, err
	}
	return record, true, nil
}

func timeFromPostgres(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}

func rollbackTx(ctx context.Context, tx pgx.Tx) {
	_ = tx.Rollback(ctx)
}

func isPostgresUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func NewPostgresServer(ctx context.Context, config Config, databaseURL string) (http.Handler, func() error, error) {
	db, err := openPostgresPool(ctx, databaseURL)
	if err != nil {
		return nil, nil, err
	}
	if err := migratePostgres(ctx, db); err != nil {
		db.Close()
		return nil, nil, err
	}
	repos := newPostgresRepoStore(db, nil)
	repos.gitStorage = newFilesystemGitStorage(config.StorageRoot)
	return NewServerWithStores(config, repos, newPostgresIdempotencyStore(db, nil)), func() error {
		db.Close()
		return nil
	}, nil
}
