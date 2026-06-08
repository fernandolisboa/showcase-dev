package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TraefikMetrics polls Traefik's Prometheus metrics endpoint and reports the
// per-Session request count — the idle activity signal for the teardown reaper
// (ADR-0006). It reads the front-door request counter Traefik already keeps, so
// the control plane observes Guest activity WITHOUT sitting on the Guest data
// path (ADR-0004/0009): live traffic still flows Guest → Traefik → backend
// directly, and this is a side-channel pull, the same kind of call Traefik makes
// against /traefik.
//
// The endpoint embeds Session ids in its service labels, so it must stay
// Session-unreachable: Traefik binds the metrics entryPoint to a static IP on its
// own network and is published to host loopback only (deploy/traefik), so only the
// control plane scrapes it — a container on a Session network cannot (#27,
// ADR-0004). Enforced by the adversarial integration test in internal/runner.
type TraefikMetrics struct {
	url    string
	client *http.Client
}

// NewTraefikMetrics builds a metrics poller for the given Prometheus endpoint.
func NewTraefikMetrics(url string) *TraefikMetrics {
	return &TraefikMetrics{
		url:    url,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// RequestCounts returns the total Traefik request count per Session id, summed
// across that Session's UI and API services. A Session absent from the result has
// served no requests yet (count 0). The error is non-nil only when the endpoint
// could not be scraped, in which case the reaper skips idle checks for that tick.
func (m *TraefikMetrics) RequestCounts(ctx context.Context) (map[string]uint64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.url, nil)
	if err != nil {
		return nil, fmt.Errorf("traefik metrics request: %w", err)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrape traefik metrics: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("traefik metrics: status %d", resp.StatusCode)
	}
	return parseServiceRequestCounts(resp.Body)
}

// serviceLabel matches a Session service name in a metric line's service="" label
// and captures the Session id. Service names are minted in proxy/config.go as
// "s-<id>-ui" and "s-<id>-api-<n>"; Traefik appends an "@http" provider suffix in
// the metric label. The id alphabet is the base32 set NewSessionID uses.
var serviceLabel = regexp.MustCompile(`service="s-([a-z2-7]+)-(?:ui|api-\d+)(?:@[^"]*)?"`)

// parseServiceRequestCounts sums the traefik_service_requests_total counter per
// Session id from a Prometheus text exposition. It tolerates unrelated metric
// lines and Traefik's label ordering; only the service label and trailing value
// matter.
func parseServiceRequestCounts(r io.Reader) (map[string]uint64, error) {
	counts := map[string]uint64{}
	sc := bufio.NewScanner(r)
	// Metric lines are short, but raise the buffer so a long label set can't
	// truncate a line into a parse miss.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "traefik_service_requests_total{") {
			continue
		}
		match := serviceLabel.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		// The sample value is the last whitespace-separated field on the line.
		// Traefik's Prometheus exposition emits no trailing OpenMetrics timestamp,
		// so the last field is the value, not a timestamp.
		fields := strings.Fields(line)
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		counts[match[1]] += uint64(v)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read traefik metrics: %w", err)
	}
	return counts, nil
}
