// Command egress-proxy is the per-Session egress allow-list enforcer (ADR-0011):
// a small, auditable forward proxy that forwards plain HTTP and tunnels HTTPS
// (via CONNECT) ONLY to host:port pairs on its allow-list, and default-denies
// everything else. It is reachable by untrusted Owner code, so it must never be
// an open relay: the allow-list — rendered from the already-validated run
// contract into EGRESS_ALLOWLIST — is the single source of what may leave.
//
// It listens on :8888. The allow-list is EGRESS_ALLOWLIST=host1:443,host2:80.
// `-healthcheck` dials the listener and exits 0/1, for the container healthcheck.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const listenAddr = ":8888"

func main() {
	healthcheck := flag.Bool("healthcheck", false, "dial the local listener and exit 0 (up) or 1 (down)")
	flag.Parse()
	if *healthcheck {
		os.Exit(runHealthcheck())
	}

	allow := parseAllowlist(os.Getenv("EGRESS_ALLOWLIST"))
	log.Printf("egress-proxy: allow-list has %d destination(s)", len(allow))

	srv := &http.Server{
		Addr:    listenAddr,
		Handler: &proxy{allow: allow},
		// No write timeout: a CONNECT tunnel is long-lived. Read header timeout
		// bounds a client that opens a connection and never sends a request.
		ReadHeaderTimeout: 30 * time.Second,
	}
	log.Printf("egress-proxy: listening on %s", listenAddr)
	log.Fatal(srv.ListenAndServe())
}

// proxy enforces the allow-list. allow maps an exact "host:port" to true; a
// destination not present is denied. An empty map (e.g. an unparseable list)
// denies everything — fail closed.
type proxy struct {
	allow map[string]bool
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

// handleConnect tunnels arbitrary TCP (the HTTPS path) to an allowed host:port.
// The target is r.Host (always host:port for CONNECT); it is validated AFTER
// normalization, never trusting a Host header or the raw request line.
func (p *proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	dest, ok := normalizeHostPort(r.Host, "")
	if !ok || !p.allowed(dest) {
		p.deny(w, r.Method, dest)
		return
	}

	upstream, err := net.DialTimeout("tcp", dest, 15*time.Second)
	if err != nil {
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer client.Close()

	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	tunnel(client, upstream)
}

// handleHTTP forwards a plain-HTTP proxy request to an allowed host:port. In
// proxy mode the request URL is absolute, so the destination is r.URL.Host
// (defaulting to port 80) — again validated post-parse, not from a header.
func (p *proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() {
		http.Error(w, "egress-proxy: only absolute-URL proxy requests are accepted", http.StatusBadRequest)
		return
	}
	dest, ok := normalizeHostPort(r.URL.Host, "80")
	if !ok || !p.allowed(dest) {
		p.deny(w, r.Method, dest)
		return
	}

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	copyHeader(outReq.Header, r.Header)

	resp, err := http.DefaultTransport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *proxy) allowed(hostPort string) bool { return p.allow[hostPort] }

// deny logs the refusal by destination only (never path or body, so the audit
// trail isn't a side channel) and returns 403.
func (p *proxy) deny(w http.ResponseWriter, method, dest string) {
	log.Printf("egress-proxy: DENY %s %s (not in allow-list)", method, dest)
	http.Error(w, "egress-proxy: destination not in allow-list", http.StatusForbidden)
}

// parseAllowlist turns "host1:443,host2:80" into a set of exact host:port keys.
// Any malformed entry is skipped (fail closed for that entry); a wholly
// unparseable value yields an empty set, which denies everything.
func parseAllowlist(raw string) map[string]bool {
	allow := map[string]bool{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		dest, ok := normalizeHostPort(entry, "")
		if !ok {
			log.Printf("egress-proxy: ignoring malformed allow-list entry %q", entry)
			continue
		}
		allow[dest] = true
	}
	return allow
}

// normalizeHostPort splits a "host:port" (or "host" when defaultPort is set) into
// a canonical "host:port" key. It rejects empty host, missing/invalid port, and
// any leftover path/scheme — the strict parse that closes sloppy-parsing bypasses.
func normalizeHostPort(s, defaultPort string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		if defaultPort == "" {
			return "", false
		}
		host, port = s, defaultPort
	}
	if host == "" || strings.ContainsAny(host, "/\\ ") {
		return "", false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", false
	}
	return net.JoinHostPort(host, port), true
}

// tunnel copies bytes both ways and returns only when BOTH directions have
// finished. When one side closes its write half, we half-close the peer's write
// half so it sees EOF (rather than closing the whole connection, which would
// truncate the still-open direction). Waiting for both copies means the caller's
// deferred Close()s don't fire — and discard in-flight bytes — mid-transfer.
func tunnel(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		closeWrite(dst)
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}

// closeWrite half-closes the write side if the connection supports it (TCP
// does), signalling EOF to the peer without tearing down the read side.
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// copyHeader copies HTTP headers, skipping hop-by-hop headers.
func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		if hopByHop[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

var hopByHop = map[string]bool{
	"connection": true, "proxy-connection": true, "keep-alive": true,
	"proxy-authenticate": true, "proxy-authorization": true,
	"te": true, "trailer": true, "transfer-encoding": true, "upgrade": true,
}

// runHealthcheck dials the local listener; 0 = up, 1 = down.
func runHealthcheck() int {
	conn, err := net.DialTimeout("tcp", "127.0.0.1"+listenAddr, 2*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "egress-proxy: healthcheck failed:", err)
		return 1
	}
	_ = conn.Close()
	return 0
}
