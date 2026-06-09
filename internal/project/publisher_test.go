package project

import (
	"context"
	"testing"
	"time"

	"github.com/fernandolisboa/showcase-dev/internal/runcontract"
	"github.com/fernandolisboa/showcase-dev/internal/store"
)

// blockingBuilder blocks inside BuildProject until released or its context is cancelled,
// so a test can hold a build "in flight" and then cancel it via Shutdown.
type blockingBuilder struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingBuilder) BuildProject(ctx context.Context, _, _ string, _ runcontract.Manifest) (string, error) {
	close(b.started)
	select {
	case <-b.release:
		return "sha", nil
	case <-ctx.Done():
		return "", ctx.Err()
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

	pub.Start(1, "me", "p1", runcontract.Manifest{})
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
		pub.Start(1, "me", "p1", runcontract.Manifest{})
		pub.wait()
		if p := fs.byOwner[1][0]; !p.Published || p.CommitSHA != "deadbeef" || p.BuildState != "published" {
			t.Errorf("a successful background build must settle to published: %+v", p)
		}
	})
	t.Run("failure", func(t *testing.T) {
		fs := newFakeStore()
		fs.byOwner[1] = []store.Project{{ID: "p1", OwnerID: 1, BuildState: "building"}}
		pub := NewPublisher(fs, &fakeBuilder{err: errPlaceholder}, discardLogger())
		pub.Start(1, "me", "p1", runcontract.Manifest{})
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
