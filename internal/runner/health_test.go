package runner

import "testing"

func TestParseComposePSNDJSON(t *testing.T) {
	// Newer Compose emits one JSON object per line.
	out := []byte(`{"Service":"db","State":"running","Health":"healthy"}
{"Service":"web","State":"running","Health":""}
`)
	got, err := parseComposePS(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d services, want 2: %+v", len(got), got)
	}
	if got[0].Service != "db" || got[0].State != "running" || got[0].Health != "healthy" {
		t.Errorf("unexpected first service: %+v", got[0])
	}
}

func TestParseComposePSArray(t *testing.T) {
	// Older Compose emits a single JSON array.
	out := []byte(`[{"Service":"web","State":"exited","Health":""}]`)
	got, err := parseComposePS(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].State != "exited" {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestParseComposePSSkipsNoise(t *testing.T) {
	// A stray warning on the combined stream must not break parsing.
	out := []byte(`time="..." level=warning msg="something"
{"Service":"web","State":"running","Health":"healthy"}`)
	got, err := parseComposePS(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Service != "web" {
		t.Errorf("unexpected: %+v", got)
	}

	empty, err := parseComposePS([]byte("  \n "))
	if err != nil {
		t.Fatalf("parse empty: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("blank output should yield no services, got %+v", empty)
	}
}

func TestHealthyFromPS(t *testing.T) {
	cases := []struct {
		name     string
		services []composePS
		want     bool
	}{
		{"all running, mix of healthy and no-healthcheck", []composePS{
			{State: "running", Health: "healthy"}, {State: "running", Health: ""}}, true},
		{"empty means stack gone", nil, false},
		{"a container exited", []composePS{
			{State: "running", Health: "healthy"}, {State: "exited", Health: ""}}, false},
		{"a container restarting", []composePS{{State: "restarting", Health: ""}}, false},
		{"unhealthy healthcheck", []composePS{{State: "running", Health: "unhealthy"}}, false},
		{"still starting", []composePS{{State: "running", Health: "starting"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := healthyFromPS(tc.services); got != tc.want {
				t.Errorf("healthyFromPS = %v, want %v", got, tc.want)
			}
		})
	}
}
