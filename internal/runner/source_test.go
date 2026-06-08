package runner

import (
	"context"
	"errors"
	"testing"
)

// stubSource returns a fixed Project or error, recording whether it was called.
type stubSource struct {
	p      Project
	err    error
	called bool
}

func (s *stubSource) Project(context.Context, string) (Project, error) {
	s.called = true
	return s.p, s.err
}

func TestFallbackSourcePrimaryHitDoesNotConsultSecondary(t *testing.T) {
	primary := &stubSource{p: Project{Images: map[string]string{"web": "img"}}}
	secondary := &stubSource{}
	got, err := FallbackSource{Primary: primary, Secondary: secondary}.Project(context.Background(), "id")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.Images["web"] != "img" {
		t.Errorf("returned %+v, want the primary's project", got)
	}
	if secondary.called {
		t.Error("secondary was consulted even though primary resolved the Project")
	}
}

func TestFallbackSourceFallsBackOnNotFound(t *testing.T) {
	primary := &stubSource{err: ErrProjectNotFound}
	secondary := &stubSource{p: Project{Images: map[string]string{"web": "fixture"}}}
	got, err := FallbackSource{Primary: primary, Secondary: secondary}.Project(context.Background(), "id")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !secondary.called {
		t.Error("secondary was not consulted after primary ErrProjectNotFound")
	}
	if got.Images["web"] != "fixture" {
		t.Errorf("returned %+v, want the secondary's project", got)
	}
}

func TestFallbackSourcePropagatesOtherErrors(t *testing.T) {
	boom := errors.New("db down")
	primary := &stubSource{err: boom}
	secondary := &stubSource{}
	_, err := FallbackSource{Primary: primary, Secondary: secondary}.Project(context.Background(), "id")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the primary's error propagated", err)
	}
	if secondary.called {
		t.Error("secondary was consulted on a non-not-found error; the fault would be masked")
	}
}
