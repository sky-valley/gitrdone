package httpapi

import (
	"context"
	"errors"
	"sync"
	"time"
)

const defaultIdempotencyRecordTTL = 24 * time.Hour

var errIdempotencyConflict = errors.New("idempotency key conflict")

type idempotencyDoer interface {
	Do(ctx context.Context, input idempotencyInput, create func() (createRepoTokenResponse, error)) (idempotencyResult, error)
}

type idempotencyInput struct {
	Scope       string
	Key         string
	RequestHash string
}

type idempotencyResult struct {
	Response createRepoTokenResponse
	Replayed bool
}

type idempotencyLookupKey struct {
	Scope string
	Key   string
}

type idempotencyRecord struct {
	RequestHash string
	Response    createRepoTokenResponse
	ExpiresAt   time.Time
}

type memoryIdempotencyStore struct {
	mu      sync.Mutex
	records map[idempotencyLookupKey]idempotencyRecord
	now     func() time.Time
	ttl     time.Duration
}

func newMemoryIdempotencyStore(now func() time.Time) *memoryIdempotencyStore {
	if now == nil {
		now = time.Now
	}
	return &memoryIdempotencyStore{
		records: map[idempotencyLookupKey]idempotencyRecord{},
		now:     now,
		ttl:     defaultIdempotencyRecordTTL,
	}
}

func (store *memoryIdempotencyStore) Do(ctx context.Context, input idempotencyInput, create func() (createRepoTokenResponse, error)) (idempotencyResult, error) {
	if err := ctx.Err(); err != nil {
		return idempotencyResult{}, err
	}
	if input.Key == "" {
		response, err := create()
		return idempotencyResult{Response: response}, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	key := idempotencyLookupKey{
		Scope: input.Scope,
		Key:   input.Key,
	}
	now := store.now()
	if record, ok := store.records[key]; ok {
		if now.Before(record.ExpiresAt) {
			if record.RequestHash != input.RequestHash {
				return idempotencyResult{}, errIdempotencyConflict
			}
			return idempotencyResult{
				Response: record.Response,
				Replayed: true,
			}, nil
		}
		delete(store.records, key)
	}

	response, err := create()
	if err != nil {
		return idempotencyResult{}, err
	}
	store.records[key] = idempotencyRecord{
		RequestHash: input.RequestHash,
		Response:    response,
		ExpiresAt:   now.Add(store.ttl),
	}
	return idempotencyResult{Response: response}, nil
}
