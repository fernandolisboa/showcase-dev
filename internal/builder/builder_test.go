package builder

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// fakeDocker records build/rm invocations so cache, invalidation, and eviction
// can be tested without real docker.
type fakeDocker struct {
	mu      sync.Mutex
	builds  []string // tags built
	removed []string // tags removed
	failOn  string   // if a build tag contains this, the build fails
}

// run stands in for the host-side docker ops the builder still makes directly —
// image rm (invalidation/eviction). The sandboxed build goes through build below.
func (f *fakeDocker) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(args) >= 2 && args[0] == "image" && args[1] == "rm" {
		f.removed = append(f.removed, args[len(args)-1])
	}
	return nil, nil
}

// build stands in for the sandboxed build backend: it records the built tag and
// fails when the tag matches failOn, so cache/invalidation/eviction are testable
// without a real BuildKit sandbox.
func (f *fakeDocker) build(_ context.Context, tag, _, _ string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn != "" && strings.Contains(tag, f.failOn) {
		return []byte("step 3/5: RUN false\nbuild failed"), errors.New("exit status 1")
	}
	f.builds = append(f.builds, tag)
	return []byte("built " + tag), nil
}

// ctxProvider returns a Context func that records how many times it was invoked
// (a cache hit must not materialise the context).
func ctxProvider(calls *int) func() (string, func(), error) {
	return func() (string, func(), error) {
		*calls++
		return t_tempdir, func() {}, nil
	}
}

// t_tempdir is a stand-in context dir; the fake docker never reads it.
const t_tempdir = "/tmp/ignored-by-fake"

func newTestBuilder(d *fakeDocker, max int) *Builder {
	b := New(max, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.run = d.run
	b.build = d.build
	return b
}

func spec(name, version string, ctxCalls *int) BuildSpec {
	return BuildSpec{ImageName: name, Version: version, Dockerfile: "Dockerfile", Context: ctxProvider(ctxCalls)}
}

func TestBuildCacheHitSkipsRebuild(t *testing.T) {
	d := &fakeDocker{}
	b := newTestBuilder(d, 10)
	var calls int

	tag1, err := b.Build(context.Background(), spec("showcase/x", "v1", &calls))
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	tag2, err := b.Build(context.Background(), spec("showcase/x", "v1", &calls))
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if tag1 != tag2 {
		t.Errorf("tags differ across a cache hit: %q vs %q", tag1, tag2)
	}
	if len(d.builds) != 1 {
		t.Errorf("expected 1 docker build, got %d", len(d.builds))
	}
	if calls != 1 {
		t.Errorf("cache hit must not materialise the build context; ctx calls = %d", calls)
	}
}

func TestBuildNewVersionRebuildsAndInvalidates(t *testing.T) {
	d := &fakeDocker{}
	b := newTestBuilder(d, 10)
	var calls int

	old, _ := b.Build(context.Background(), spec("showcase/x", "v1", &calls))
	nw, err := b.Build(context.Background(), spec("showcase/x", "v2", &calls))
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if old == nw {
		t.Error("a new version must produce a new tag")
	}
	if len(d.builds) != 2 {
		t.Errorf("expected 2 builds, got %d", len(d.builds))
	}
	if len(d.removed) != 1 || d.removed[0] != old {
		t.Errorf("a new version must invalidate the old image; removed = %v, want [%s]", d.removed, old)
	}
}

func TestBuildLRUEvictsColdestOverCap(t *testing.T) {
	d := &fakeDocker{}
	b := newTestBuilder(d, 2)
	var calls int

	aTag, _ := b.Build(context.Background(), spec("showcase/a", "v1", &calls)) // used=1
	b.Build(context.Background(), spec("showcase/b", "v1", &calls))            // used=2
	// Touch a so b becomes the coldest.
	b.Build(context.Background(), spec("showcase/a", "v1", &calls)) // hit, used=3
	b.Build(context.Background(), spec("showcase/c", "v1", &calls)) // used=4 -> evict b

	if len(d.removed) != 1 {
		t.Fatalf("expected 1 eviction, got %v", d.removed)
	}
	if strings.HasPrefix(d.removed[0], "showcase/a:") {
		t.Errorf("evicted the recently-used image %q; should have evicted showcase/b", d.removed[0])
	}
	if _, ok := b.cache["showcase/a"]; !ok {
		t.Error("recently-used showcase/a should remain cached")
	}
	if _, ok := b.cache["showcase/b"]; ok {
		t.Error("coldest showcase/b should have been evicted")
	}
	_ = aTag
}

func TestRebuildFailureKeepsPreviousImage(t *testing.T) {
	// A failed rebuild (new version) must leave the last-good image cached and
	// NOT invalidate it — the platform keeps serving the previous build.
	d := &fakeDocker{}
	b := newTestBuilder(d, 10)
	var calls int

	good, _ := b.Build(context.Background(), spec("showcase/x", "v1", &calls))

	d.failOn = "showcase/x" // the next build (v2) fails
	if _, err := b.Build(context.Background(), spec("showcase/x", "v2", &calls)); err == nil {
		t.Fatal("expected the rebuild to fail")
	}

	if len(d.removed) != 0 {
		t.Errorf("a failed rebuild must not invalidate the good image; removed = %v", d.removed)
	}
	if e, ok := b.cache["showcase/x"]; !ok || e.tag != good || e.version != "v1" {
		t.Errorf("cache should still hold the last-good v1 build, got %+v ok=%v", e, ok)
	}
}

func TestBuildFailureReturnsErrorWithOutput(t *testing.T) {
	d := &fakeDocker{failOn: "showcase/bad"}
	b := newTestBuilder(d, 10)
	var calls int

	_, err := b.Build(context.Background(), spec("showcase/bad", "v1", &calls))
	if err == nil {
		t.Fatal("expected a build error")
	}
	if !strings.Contains(err.Error(), "build failed") {
		t.Errorf("error should carry docker output, got %q", err)
	}
	if _, ok := b.cache["showcase/bad"]; ok {
		t.Error("a failed build must not be cached")
	}
}
