package runner

import (
	"context"
	"errors"
	"testing"
)

func TestStubReportsNotImplemented(t *testing.T) {
	var r Runner = Stub{}

	if _, err := r.Provision(context.Background(), "proj", "sess"); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Provision err = %v, want ErrNotImplemented", err)
	}
	if err := r.Teardown(context.Background(), "sess"); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Teardown err = %v, want ErrNotImplemented", err)
	}
}
