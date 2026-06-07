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

func (f *fakeDocker) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case len(args) > 0 && args[0] == "build":
		tag := flagValue(args, "-t")
		if f.failOn != "" && strings.Contains(tag, f.failOn) {
			return []byte("step 3/5: RUN false\nbuild failed"), errors.New("exit status 1")
		}
		f.builds = append(f.builds, tag)
		return []byte("built " + tag), nil
	case len(args) >= 2 && args[0] == "image" && args[1] == "rm":
		f.removed = append(f.removed, args[len(args)-1])
		return nil, nil
	}
	return nil, nil
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
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
