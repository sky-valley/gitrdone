package httpapi

import (
	"context"
	"errors"
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
