package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryIdempotencyStoreReplaysSameRequest(t *testing.T) {
	store := newMemoryIdempotencyStore(func() time.Time {
		return time.Date(2026, 6, 2, 15, 0, 0, 0, time.UTC)
	})
	input := idempotencyInput{
		Scope:       "POST /v1/repos/repo_00000000-0000-4000-8000-000000000001/tokens",
		Key:         "differ:import:imp_123:source-read-token",
		RequestHash: "request-a",
	}
	createCalls := 0

	first, err := store.Do(context.Background(), input, func(context.Context) (createRepoTokenResponse, error) {
		createCalls++
		return createRepoTokenResponse{Token: "gtd_first", Scope: "read"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Do(context.Background(), input, func(context.Context) (createRepoTokenResponse, error) {
		createCalls++
		return createRepoTokenResponse{Token: "gtd_second", Scope: "read"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", createCalls)
	}
	if first.Response.Token != "gtd_first" {
		t.Fatalf("first token = %q, want gtd_first", first.Response.Token)
	}
	if first.Replayed {
		t.Fatal("first result was marked replayed")
	}
	if second.Response.Token != "gtd_first" {
		t.Fatalf("second token = %q, want original gtd_first", second.Response.Token)
	}
	if !second.Replayed {
		t.Fatal("second result was not marked replayed")
	}
}

func TestMemoryIdempotencyStoreConflictsOnChangedRequest(t *testing.T) {
	store := newMemoryIdempotencyStore(time.Now)
	input := idempotencyInput{
		Scope:       "POST /v1/repos/repo_00000000-0000-4000-8000-000000000001/tokens",
		Key:         "differ:run_123:push-token",
		RequestHash: "request-a",
	}
	_, err := store.Do(context.Background(), input, func(context.Context) (createRepoTokenResponse, error) {
		return createRepoTokenResponse{Token: "gtd_first"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	conflicting := input
	conflicting.RequestHash = "request-b"
	_, err = store.Do(context.Background(), conflicting, func(context.Context) (createRepoTokenResponse, error) {
		return createRepoTokenResponse{Token: "gtd_second"}, nil
	})

	if !errors.Is(err, errIdempotencyConflict) {
		t.Fatalf("error = %v, want errIdempotencyConflict", err)
	}
}

func TestMemoryIdempotencyStoreScopesKeys(t *testing.T) {
	store := newMemoryIdempotencyStore(time.Now)
	firstScope := idempotencyInput{
		Scope:       "POST /v1/repos/repo_00000000-0000-4000-8000-000000000001/tokens",
		Key:         "differ:shared:reader-token",
		RequestHash: "request-a",
	}
	secondScope := firstScope
	secondScope.Scope = "POST /v1/repos/repo_00000000-0000-4000-8000-000000000002/tokens"

	first, err := store.Do(context.Background(), firstScope, func(context.Context) (createRepoTokenResponse, error) {
		return createRepoTokenResponse{Token: "gtd_first"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Do(context.Background(), secondScope, func(context.Context) (createRepoTokenResponse, error) {
		return createRepoTokenResponse{Token: "gtd_second"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if first.Response.Token != "gtd_first" {
		t.Fatalf("first token = %q, want gtd_first", first.Response.Token)
	}
	if second.Response.Token != "gtd_second" {
		t.Fatalf("second token = %q, want gtd_second", second.Response.Token)
	}
}

func TestMemoryIdempotencyStoreExpiresResponseCache(t *testing.T) {
	now := time.Date(2026, 6, 2, 15, 0, 0, 0, time.UTC)
	store := newMemoryIdempotencyStore(func() time.Time {
		return now
	})
	input := idempotencyInput{
		Scope:       "POST /v1/repos/repo_00000000-0000-4000-8000-000000000001/tokens",
		Key:         "differ:run_123:push-token",
		RequestHash: "request-a",
	}

	first, err := store.Do(context.Background(), input, func(context.Context) (createRepoTokenResponse, error) {
		return createRepoTokenResponse{Token: "gtd_first"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(defaultIdempotencyRecordTTL + time.Second)
	second, err := store.Do(context.Background(), input, func(context.Context) (createRepoTokenResponse, error) {
		return createRepoTokenResponse{Token: "gtd_second"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if first.Response.Token != "gtd_first" {
		t.Fatalf("first token = %q, want gtd_first", first.Response.Token)
	}
	if second.Response.Token != "gtd_second" {
		t.Fatalf("second token = %q, want gtd_second after expiry", second.Response.Token)
	}
	if second.Replayed {
		t.Fatal("expired result was marked replayed")
	}
}
