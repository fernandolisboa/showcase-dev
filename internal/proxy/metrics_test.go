package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseServiceRequestCountsSumsPerSession(t *testing.T) {
	// A representative slice of Traefik's Prometheus exposition: two sessions, the
	// first with a UI plus two API services and several status codes; unrelated
	// metric lines and comments interleaved.
	const exposition = `# HELP traefik_service_requests_total How many HTTP requests processed.
# TYPE traefik_service_requests_total counter
traefik_service_requests_total{code="200",method="GET",protocol="http",service="s-abc-ui@http"} 10
traefik_service_requests_total{code="404",method="GET",protocol="http",service="s-abc-ui@http"} 2
traefik_service_requests_total{code="200",method="POST",protocol="http",service="s-abc-api-0@http"} 5
traefik_service_requests_total{code="200",method="GET",protocol="http",service="s-abc-api-1@http"} 3
traefik_service_requests_total{code="200",method="GET",protocol="http",service="s-xyz-ui@http"} 7
traefik_entrypoint_requests_total{code="200",entrypoint="web"} 999
traefik_service_requests_total{code="200",method="GET",protocol="http",service="booting@http"} 42
`
	got, err := parseServiceRequestCounts(strings.NewReader(exposition))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["abc"] != 20 { // 10 + 2 + 5 + 3
		t.Errorf("abc = %d, want 20", got["abc"])
	}
	if got["xyz"] != 7 {
		t.Errorf("xyz = %d, want 7", got["xyz"])
	}
	if _, ok := got["booting"]; ok {
		t.Error("the shared booting service must not be attributed to a session")
	}
	if len(got) != 2 {
		t.Errorf("got %d sessions, want 2: %v", len(got), got)
	}
}

func TestParseServiceRequestCountsHandlesFloatAndEmpty(t *testing.T) {
	got, err := parseServiceRequestCounts(strings.NewReader(
		`traefik_service_requests_total{service="s-q-ui@http"} 1.0` + "\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["q"] != 1 {
		t.Errorf("q = %d, want 1", got["q"])
	}

	empty, err := parseServiceRequestCounts(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parse empty: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("empty exposition should yield no counts, got %v", empty)
	}
}

func TestTraefikMetricsRequestCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`traefik_service_requests_total{service="s-abc-ui@http"} 4` + "\n"))
	}))
	defer srv.Close()

	m := NewTraefikMetrics(srv.URL)
	got, err := m.RequestCounts(context.Background())
	if err != nil {
		t.Fatalf("RequestCounts: %v", err)
	}
	if got["abc"] != 4 {
		t.Errorf("abc = %d, want 4", got["abc"])
	}
}

func TestTraefikMetricsErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if _, err := NewTraefikMetrics(srv.URL).RequestCounts(context.Background()); err == nil {
		t.Error("expected an error on a non-200 metrics response")
	}
}
