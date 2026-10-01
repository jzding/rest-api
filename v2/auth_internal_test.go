// Copyright 2025 The Cloud Native Events Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package restapi

import (
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// captureRoundTripper records the last request it saw and returns a 204.
type captureRoundTripper struct {
	last *http.Request
}

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.last = req
	return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: make(http.Header)}, nil
}

func TestBearerTokenRoundTripper(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("  tok-123\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	capRT := &captureRoundTripper{}
	rt := &bearerTokenRoundTripper{base: capRT, tokenPath: tokenPath}

	req, err := http.NewRequest(http.MethodPost, "https://example.svc:9043/event", http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()
	if got := capRT.last.Header.Get("Authorization"); got != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer tok-123")
	}
	// Contract: the caller's original request must not be mutated.
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("original request header mutated: %q", got)
	}

	// Rotated token on disk is picked up on the next request (re-read per call).
	if err = os.WriteFile(tokenPath, []byte("tok-456"), 0o600); err != nil {
		t.Fatalf("rewrite token: %v", err)
	}
	resp, err = rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip after rotation: %v", err)
	}
	resp.Body.Close()
	if got := capRT.last.Header.Get("Authorization"); got != "Bearer tok-456" {
		t.Errorf("rotated Authorization = %q, want %q", got, "Bearer tok-456")
	}

	// Missing token file: request still proceeds, no Authorization header set.
	rtMissing := &bearerTokenRoundTripper{base: capRT, tokenPath: filepath.Join(dir, "absent")}
	resp, err = rtMissing.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip missing token: %v", err)
	}
	resp.Body.Close()
	if got := capRT.last.Header.Get("Authorization"); got != "" {
		t.Errorf("missing-token request set Authorization = %q, want empty", got)
	}

	// Plaintext http callback: the token is a cluster credential and must not be
	// sent over an unencrypted request, even when a valid token is available.
	if err = os.WriteFile(tokenPath, []byte("tok-789"), 0o600); err != nil {
		t.Fatalf("rewrite token: %v", err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, "http://example.svc:8080/event", http.NoBody)
	if err != nil {
		t.Fatalf("new http request: %v", err)
	}
	resp, err = rt.RoundTrip(httpReq)
	if err != nil {
		t.Fatalf("RoundTrip http: %v", err)
	}
	resp.Body.Close()
	if got := capRT.last.Header.Get("Authorization"); got != "" {
		t.Errorf("plaintext http push set Authorization = %q, want empty", got)
	}
}

func TestIsBlockedDialIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		// Allowed: loopback (RHT-0003 in-pod) and private (cluster Pod IPs).
		{"127.0.0.1", false},
		{"::1", false},
		{"10.0.0.5", false},
		{"172.16.3.4", false},
		{"192.168.1.10", false},
		{"fd12:3456::1", false},
		{"93.184.216.34", false}, // public
		// Blocked: link-local, cloud metadata, multicast, unspecified.
		{"169.254.0.1", true},
		{"169.254.169.254", true},
		{"fe80::1", true},
		{"fd00:ec2::254", true},
		{"224.0.0.1", true},
		{"0.0.0.0", true},
		{"::", true},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := isBlockedDialIP(ip); got != c.blocked {
			t.Errorf("isBlockedDialIP(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
	if !isBlockedDialIP(nil) {
		t.Errorf("isBlockedDialIP(nil) should be blocked")
	}
}

func TestValidateEndpointURI(t *testing.T) {
	valid := []string{
		"http://localhost:8080/event",
		"https://consumer.ptp.svc.cluster.local:9043/ack",
		"http://10.128.0.9:8080/callback",
	}
	for _, u := range valid {
		if err := validateEndpointURI(u); err != nil {
			t.Errorf("validateEndpointURI(%q) = %v, want nil", u, err)
		}
	}
	invalid := []string{
		"ftp://host/x",                  // bad scheme
		"http://",                       // empty host
		"://nope",                       // unparseable scheme
		"http://169.254.169.254/latest", // metadata
		"http://[fe80::1]/x",            // link-local
		"http://224.0.0.1/x",            // multicast
		"http://0.0.0.0/x",              // unspecified
	}
	for _, u := range invalid {
		if err := validateEndpointURI(u); err == nil {
			t.Errorf("validateEndpointURI(%q) = nil, want error", u)
		}
	}
}

func TestIsLoopbackRemoteAddr(t *testing.T) {
	yes := []string{"127.0.0.1:5000", "[::1]:5000", "localhost:5000", "127.0.0.1"}
	no := []string{"", "10.0.0.1:5000", "192.168.1.1:80", "example.com:80"}
	for _, a := range yes {
		if !isLoopbackRemoteAddr(a) {
			t.Errorf("isLoopbackRemoteAddr(%q) = false, want true", a)
		}
	}
	for _, a := range no {
		if isLoopbackRemoteAddr(a) {
			t.Errorf("isLoopbackRemoteAddr(%q) = true, want false", a)
		}
	}
}

func TestApplyTLSProfile(t *testing.T) {
	// Explicit profile is applied verbatim.
	c := &AuthConfig{
		TLSMinVersion:   "VersionTLS13",
		TLSCipherSuites: []string{"TLS_AES_128_GCM_SHA256", "bogus-name"},
	}
	cfg := &tls.Config{} //nolint:gosec // MinVersion set by ApplyTLSProfile under test
	c.ApplyTLSProfile(cfg)
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %x, want TLS13", cfg.MinVersion)
	}

	// No profile => default floor of TLS 1.2, existing MinVersion preserved.
	empty := &AuthConfig{}
	cfg2 := &tls.Config{} //nolint:gosec // MinVersion set by ApplyTLSProfile under test
	empty.ApplyTLSProfile(cfg2)
	if cfg2.MinVersion != tls.VersionTLS12 {
		t.Errorf("default MinVersion = %x, want TLS12", cfg2.MinVersion)
	}

	// Nil receiver must not panic.
	var nilCfg *AuthConfig
	nilCfg.ApplyTLSProfile(&tls.Config{}) //nolint:gosec // nil-receiver no-op path under test
}
