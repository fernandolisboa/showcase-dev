package project

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
	"github.com/fernandolisboa/showcase-dev/internal/runner"
	"github.com/fernandolisboa/showcase-dev/internal/store"
)

// fakePublishedStore is an in-memory PublishedStore for Source tests.
type fakePublishedStore struct {
	p   store.Project
	err error
}

func (f fakePublishedStore) GetPublishedProject(context.Context, string) (store.Project, error) {
	return f.p, f.err
}

func TestSourceResolvesPublishedManifest(t *testing.T) {
	manifest, _ := json.Marshal(runcontract.Manifest{
		Services: []runcontract.Service{
			{Name: "web", Repo: "r", Dockerfile: "Dockerfile", Port: 8080, Role: runcontract.RoleUI},
		},
	})
	src := NewSource(fakePublishedStore{p: store.Project{ID: "p1", Manifest: manifest}})

	got, err := src.Project(context.Background(), "p1")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(got.Manifest.Services) != 1 || got.Manifest.Services[0].Name != "web" {
		t.Errorf("manifest did not round-trip: %+v", got.Manifest)
	}
	// Images are empty here — the BuildingSource fills them at play time (slice 4).
	if got.Images == nil || len(got.Images) != 0 {
		t.Errorf("Images = %v, want an empty (non-nil) map", got.Images)
	}
}

func TestSourceMapsNotFoundToRunnerSentinel(t *testing.T) {
	src := NewSource(fakePublishedStore{err: store.ErrProjectNotFound})
	_, err := src.Project(context.Background(), "missing")
	if !errors.Is(err, runner.ErrProjectNotFound) {
		t.Fatalf("err = %v, want runner.ErrProjectNotFound (so FallbackSource defers to the fixture)", err)
	}
}

func TestSourcePropagatesStoreError(t *testing.T) {
	boom := errors.New("db down")
	src := NewSource(fakePublishedStore{err: boom})
	_, err := src.Project(context.Background(), "id")
	if errors.Is(err, runner.ErrProjectNotFound) || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store error propagated (not masked as not-found)", err)
	}
}

func TestSourcePropagatesCorruptManifest(t *testing.T) {
	src := NewSource(fakePublishedStore{p: store.Project{ID: "p1", Manifest: []byte(`{not json`)}})
	_, err := src.Project(context.Background(), "p1")
	if err == nil || errors.Is(err, runner.ErrProjectNotFound) {
		t.Fatalf("err = %v, want a decode error (a corrupt manifest must not silently fall back to the fixture)", err)
	}
}
