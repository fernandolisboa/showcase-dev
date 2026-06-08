package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNormalizeHostPort(t *testing.T) {
	cases := []struct {
		in, defaultPort, want string
		ok                    bool
	}{
		{"example.com:443", "", "example.com:443", true},
		{"example.com", "80", "example.com:80", true},
		{"10.0.0.1:8080", "", "10.0.0.1:8080", true},
		{"example.com", "", "", false},       // no port, no default
		{"example.com:0", "", "", false},     // port out of range
		{"example.com:99999", "", "", false}, // port out of range
		{"example.com:abc", "", "", false},   // non-numeric port
		{"", "80", "", false},                // empty
		{"ex ample.com:80", "", "", false},   // space in host
		{"evil.com/path:80", "", "", false},  // path smuggled into host
	}
	for _, c := range cases {
		got, ok := normalizeHostPort(c.in, c.defaultPort)
		if ok != c.ok || got != c.want {
			t.Errorf("normalizeHostPort(%q, %q) = (%q, %v), want (%q, %v)", c.in, c.defaultPort, got, ok, c.want, c.ok)
		}
	}
}

func TestParseAllowlistFailsClosed(t *testing.T) {
	allow := parseAllowlist("a.com:443, b.com:80 ,,garbage,c.com:0")
	if !allow["a.com:443"] || !allow["b.com:80"] {
		t.Error("valid entries should be allowed")
	}
	if len(allow) != 2 {
		t.Errorf("malformed entries must be dropped, got %v", allow)
	}
	if len(parseAllowlist("")) != 0 {
		t.Error("empty allow-list must deny everything (empty set)")
	}
}

// proxyClient builds an http.Client that routes everything through the proxy.
func proxyClient(proxyURL string) *http.Client {
	u, _ := url.Parse(proxyURL)
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(u)},
		Timeout:   5 * time.Second,
	}
}

func TestProxyForwardsAllowedHTTPAndDeniesOthers(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}))
	defer backend.Close()
	backendHost := strings.TrimPrefix(backend.URL, "http://") // 127.0.0.1:port

	px := httptest.NewServer(&proxy{allow: map[string]bool{backendHost: true}})
	defer px.Close()
	client := proxyClient(px.URL)

	// Allowed: reaches the backend through the proxy.
	resp, err := client.Get(backend.URL)
	if err != nil {
		t.Fatalf("allowed GET through proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "hello" {
		t.Errorf("allowed GET = %d %q, want 200 hello", resp.StatusCode, body)
	}

	// Denied: a host not on the allow-list gets 403 from the proxy, never reaching out.
	resp, err = client.Get("http://denied.example.com/")
	if err != nil {
		t.Fatalf("denied GET through proxy: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("denied GET status = %d, want 403", resp.StatusCode)
	}
}

func TestProxyConnectTunnelsAllowedAndDeniesOthers(t *testing.T) {
	// A raw TCP echo upstream stands in for "an allowed external host:port".
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	upstream := ln.Addr().String()

	px := httptest.NewServer(&proxy{allow: map[string]bool{upstream: true}})
	defer px.Close()
	pxAddr := strings.TrimPrefix(px.URL, "http://")

	// Allowed CONNECT: tunnel established, raw bytes echo back.
	if status, conn, br := doConnect(t, pxAddr, upstream); status != "200" {
		t.Fatalf("allowed CONNECT status = %q, want 200", status)
	} else {
		defer conn.Close()
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatalf("write through tunnel: %v", err)
		}
		// Read via br (not conn): any bytes the proxy buffered after the status line
		// live in br, so all tunnel reads must go through it.
		buf := make([]byte, 4)
		if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
			t.Errorf("tunnel echo = %q, err %v; want ping", buf, err)
		}
	}

	// Denied CONNECT: a non-allowed target is refused (403), no tunnel.
	if status, conn, _ := doConnect(t, pxAddr, "127.0.0.1:9"); status == "200" {
		conn.Close()
		t.Error("CONNECT to a non-allowed host must be denied, got 200")
	} else if !strings.HasPrefix(status, "403") {
		t.Errorf("denied CONNECT status = %q, want 403", status)
	}
}

// doConnect issues a raw CONNECT to the proxy and returns the status token, the
// (possibly tunneled) connection, and the buffered reader to read it through.
func doConnect(t *testing.T, proxyAddr, target string) (string, net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	if _, err := io.WriteString(conn, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		t.Fatalf("malformed CONNECT response: %q", line)
	}
	// Drain the remaining response header lines so the buffered reader is left
	// positioned exactly at the start of the tunnel payload.
	for {
		h, err := br.ReadString('\n')
		if err != nil || h == "\r\n" || h == "\n" {
			break
		}
	}
	return fields[1], conn, br
}
