package project

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
	"github.com/fernandolisboa/showcase-dev/internal/store"
)

// mapBuilder returns a fixed per-service commit map (a multi-repo build).
type mapBuilder struct{ commits map[string]string }

func (b mapBuilder) BuildProject(context.Context, string, string, runcontract.Manifest) (map[string]string, error) {
	return b.commits, nil
}

// TestPublisherStoresPerServiceCommits: a multi-repo build's per-service commits are
// persisted, and the headline commit_sha is the ui service's commit (#50).
func TestPublisherStoresPerServiceCommits(t *testing.T) {
	fs := newFakeStore()
	fs.byOwner[1] = []store.Project{{ID: "p1", OwnerID: 1, BuildState: "building"}}
	m := runcontract.Manifest{Services: []runcontract.Service{
		{Name: "web", Role: runcontract.RoleUI},
		{Name: "api", Role: runcontract.RoleAPI, PathPrefix: "/api"},
	}}
	pub := NewPublisher(fs, mapBuilder{commits: map[string]string{"web": "websha", "api": "apisha"}}, discardLogger())

	pub.Start(1, "me", "p1", m)
	pub.wait()

	p := fs.byOwner[1][0]
	if p.CommitSHA != "websha" { // headline = the ui service's commit
		t.Errorf("headline commit = %q, want the ui service's websha", p.CommitSHA)
	}
	var sc map[string]string
	if err := json.Unmarshal(p.ServiceCommits, &sc); err != nil {
		t.Fatalf("service_commits not valid json: %v (%s)", err, p.ServiceCommits)
	}
	if sc["web"] != "websha" || sc["api"] != "apisha" {
		t.Errorf("per-service commits not persisted: %v", sc)
	}
}

// uiManifest is a minimal manifest with one ui service, so headlineCommit resolves the
// published commit from the builder's per-service map.
func uiManifest() runcontract.Manifest {
	return runcontract.Manifest{Services: []runcontract.Service{{Name: "web", Role: runcontract.RoleUI}}}
}

// blockingBuilder blocks inside BuildProject until released or its context is cancelled,
// so a test can hold a build "in flight" and then cancel it via Shutdown.
type blockingBuilder struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingBuilder) BuildProject(ctx context.Context, _, _ string, m runcontract.Manifest) (map[string]string, error) {
	close(b.started)
	select {
	case <-b.release:
		commits := map[string]string{}
		for _, svc := range m.Services {
			commits[svc.Name] = "sha"
		}
		return commits, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestPublisherShutdownLeavesBuildingForSweep: a build aborted by Shutdown (baseCtx
// cancelled mid-build) must NOT be recorded as a failure — it stays 'building' so the
// recovery sweep reclaims it on restart, mirroring how a boot cancelled at shutdown is
// dropped rather than marked failed.
func TestPublisherShutdownLeavesBuildingForSweep(t *testing.T) {
	fs := newFakeStore()
	fs.byOwner[1] = []store.Project{{ID: "p1", OwnerID: 1, BuildState: "building"}}
	bb := &blockingBuilder{started: make(chan struct{}), release: make(chan struct{})}
	pub := NewPublisher(fs, bb, discardLogger())

	pub.Start(1, "me", "p1", uiManifest())
	<-bb.started // the build is now running and blocked

	if err := pub.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := fs.byOwner[1][0].BuildState; got != "building" {
		t.Errorf("a build aborted by shutdown must stay 'building' for the sweep, got %q", got)
	}
}

// TestPublisherBuildSettles: a completed background build settles the lifecycle —
// success → published, failure → failed.
func TestPublisherBuildSettles(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		fs := newFakeStore()
		fs.byOwner[1] = []store.Project{{ID: "p1", OwnerID: 1, BuildState: "building"}}
		pub := NewPublisher(fs, &fakeBuilder{commit: "deadbeef"}, discardLogger())
		pub.Start(1, "me", "p1", uiManifest())
		pub.wait()
		if p := fs.byOwner[1][0]; !p.Published || p.CommitSHA != "deadbeef" || p.BuildState != "published" {
			t.Errorf("a successful background build must settle to published: %+v", p)
		}
	})
	t.Run("failure", func(t *testing.T) {
		fs := newFakeStore()
		fs.byOwner[1] = []store.Project{{ID: "p1", OwnerID: 1, BuildState: "building"}}
		pub := NewPublisher(fs, &fakeBuilder{err: errPlaceholder}, discardLogger())
		pub.Start(1, "me", "p1", uiManifest())
		pub.wait()
		if p := fs.byOwner[1][0]; p.Published || p.BuildState != "failed" || p.BuildError == "" {
			t.Errorf("a failed background build must settle to failed: %+v", p)
		}
	})
}

// TestPublisherReapOnceReclaims: the recovery sweep reclaims a build stranded in
// 'building' (the staleness window itself is exercised against real Postgres in the store
// integration test; the fake reclaims any 'building' row).
func TestPublisherReapOnceReclaims(t *testing.T) {
	fs := newFakeStore()
	fs.byOwner[1] = []store.Project{{ID: "p1", OwnerID: 1, BuildState: "building"}}
	pub := NewPublisher(fs, &fakeBuilder{}, discardLogger())

	pub.reapOnce(context.Background(), time.Minute, time.Minute)
	if got := fs.byOwner[1][0].BuildState; got != "failed" {
		t.Errorf("reapOnce must reclaim a stuck 'building' row to 'failed', got %q", got)
	}
}

var errPlaceholder = errPublishBuild("RUN build failed")

type errPublishBuild string

func (e errPublishBuild) Error() string { return string(e) }
