package cli

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testHTTPToken = "test-token-0123456789abcdef"

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`

func newTestHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "bbkt-test", Version: "0"}, nil)
	ts := httptest.NewServer(newHTTPHandler(s, testHTTPToken))
	t.Cleanup(ts.Close)
	return ts
}

func postInitialize(t *testing.T, ts *httptest.Server, authz, hostHeader string) (status int, body string) {
	t.Helper()
	return postInitializeTo(t, ts.Client(), ts.URL, authz, hostHeader)
}

func postInitializeTo(t *testing.T, client *http.Client, url, authz, hostHeader string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(initializeBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	if hostHeader != "" {
		req.Host = hostHeader
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func TestHTTPHandlerAuth(t *testing.T) {
	ts := newTestHTTPServer(t)

	tests := []struct {
		name  string
		authz string
		want  int
	}{
		{"no credential", "", http.StatusUnauthorized},
		{"wrong token", "Bearer wrong-token-0123456789abcdef", http.StatusUnauthorized},
		{"token prefix", "Bearer " + testHTTPToken[:len(testHTTPToken)-1], http.StatusUnauthorized},
		{"basic scheme", "Basic " + testHTTPToken, http.StatusUnauthorized},
		{"correct token", "Bearer " + testHTTPToken, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := postInitialize(t, ts, tt.authz, "")
			if status != tt.want {
				t.Fatalf("status = %d, want %d (body %q)", status, tt.want, body)
			}
			if tt.want == http.StatusOK && !strings.Contains(body, `"serverInfo"`) {
				t.Fatalf("initialize response missing serverInfo: %q", body)
			}
		})
	}
}

// TestHTTPHandlerKeepsDNSRebindingProtection guards that the SDK's localhost
// protection stays on: a loopback connection carrying a foreign Host header is
// rejected even with a valid token.
func TestHTTPHandlerKeepsDNSRebindingProtection(t *testing.T) {
	ts := newTestHTTPServer(t)
	status, body := postInitialize(t, ts, "Bearer "+testHTTPToken, "attacker.example:80")
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (body %q)", status, http.StatusForbidden, body)
	}
}

func TestMCPHostFlagDefaultsToLoopback(t *testing.T) {
	f := mcpCmd.Flags().Lookup("host")
	if f == nil {
		t.Fatal("mcp command has no --host flag")
	}
	if f.DefValue != "127.0.0.1" {
		t.Fatalf("--host default = %q, want 127.0.0.1", f.DefValue)
	}
	if f := mcpCmd.Flags().Lookup("allow-remote"); f == nil || f.DefValue != "false" {
		t.Fatal("--allow-remote must exist and default to false")
	}

	cfg, err := resolveHTTPConfig(f.DefValue, 8080, false, "")
	if err != nil {
		t.Fatalf("default config: %v", err)
	}
	if cfg.addr != "127.0.0.1:8080" {
		t.Fatalf("default listen addr = %q, want 127.0.0.1:8080", cfg.addr)
	}
	if cfg.remote {
		t.Fatal("default bind reported as remote")
	}
}

func TestResolveHTTPConfigGeneratesTokenOnLoopback(t *testing.T) {
	cfg, err := resolveHTTPConfig("127.0.0.1", 8080, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.generated || len(cfg.token) != 64 {
		t.Fatalf("want generated 64-hex-char token, got generated=%v len=%d", cfg.generated, len(cfg.token))
	}
	other, err := resolveHTTPConfig("127.0.0.1", 8080, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if other.token == cfg.token {
		t.Fatal("generated tokens repeat")
	}
}

func TestResolveHTTPConfigUsesEnvToken(t *testing.T) {
	cfg, err := resolveHTTPConfig("localhost", 9000, false, "  "+testHTTPToken+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.generated || cfg.token != testHTTPToken {
		t.Fatalf("token = %q generated=%v, want configured token", cfg.token, cfg.generated)
	}
	if _, err := resolveHTTPConfig("127.0.0.1", 9000, false, "short"); err == nil {
		t.Fatal("short token accepted")
	}
}

func TestResolveHTTPConfigRemote(t *testing.T) {
	tests := []struct {
		name        string
		host        string
		allowRemote bool
		token       string
		wantErr     string
		wantAddr    string
	}{
		{"all interfaces without opt-in", "0.0.0.0", false, testHTTPToken, "--allow-remote", ""},
		{"empty host without opt-in", "", false, testHTTPToken, "--allow-remote", ""},
		{"lan ip without opt-in", "192.168.1.10", false, testHTTPToken, "--allow-remote", ""},
		{"opt-in without token", "0.0.0.0", true, "", "BBKT_MCP_HTTP_TOKEN", ""},
		{"opt-in with blank token", "0.0.0.0", true, "   ", "BBKT_MCP_HTTP_TOKEN", ""},
		{"opt-in with token", "0.0.0.0", true, testHTTPToken, "", "0.0.0.0:8080"},
		{"ipv6 any with token", "::", true, testHTTPToken, "", "[::]:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := resolveHTTPConfig(tt.host, 8080, tt.allowRemote, tt.token)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want mention of %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !cfg.remote || cfg.generated || cfg.addr != tt.wantAddr {
				t.Fatalf("cfg = %+v, want remote configured-token bind on %s", cfg, tt.wantAddr)
			}
		})
	}
}

func TestResolveHTTPConfigRejectsBadPort(t *testing.T) {
	for _, p := range []int{-1, 65536} {
		if _, err := resolveHTTPConfig("127.0.0.1", p, false, ""); err == nil {
			t.Errorf("port %d accepted", p)
		}
	}
}

func TestMCPRemoteFlagsRequirePort(t *testing.T) {
	t.Cleanup(func() {
		allowRemote = false
		RootCmd.SetArgs(nil)
		RootCmd.SetOut(nil)
		RootCmd.SetErr(nil)
	})
	RootCmd.SetArgs([]string{"mcp", "--allow-remote"})
	RootCmd.SetOut(io.Discard)
	RootCmd.SetErr(io.Discard)
	err := RootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--port") {
		t.Fatalf("err = %v, want refusal mentioning --port", err)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for h, want := range map[string]bool{
		"127.0.0.1":   true,
		"127.0.0.2":   true,
		"::1":         true,
		"[::1]":       true,
		"localhost":   true,
		"LOCALHOST":   true,
		"":            false,
		"0.0.0.0":     false,
		"::":          false,
		"10.0.0.5":    false,
		"example.com": false,
	} {
		if got := isLoopbackHost(h); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", h, got, want)
		}
	}
}

func TestHTTPHandlerSendsBearerChallenge(t *testing.T) {
	ts := newTestHTTPServer(t)
	for name, authz := range map[string]string{"missing": "", "wrong": "Bearer wrong-token-0123456789abcdef"} {
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(initializeBody))
			if err != nil {
				t.Fatal(err)
			}
			if authz != "" {
				req.Header.Set("Authorization", authz)
			}
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			if got := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
				t.Fatalf("WWW-Authenticate = %q, want Bearer challenge", got)
			}
		})
	}
}

// TestServeHTTPWiring exercises the listener and server that serveHTTP
// composes: it must bind loopback, print the banner to the given writer, and
// serve the token-gated handler rather than the bare MCP handler.
func TestServeHTTPWiring(t *testing.T) {
	cfg := &httpConfig{addr: "127.0.0.1:0", token: testHTTPToken, generated: true}
	var banner bytes.Buffer
	ln, err := listenHTTP(cfg, &banner)
	if err != nil {
		t.Fatal(err)
	}
	if !isLoopbackAddr(ln.Addr()) {
		t.Fatalf("listening on %s, want loopback", ln.Addr())
	}
	if !strings.Contains(banner.String(), ln.Addr().String()) || !strings.Contains(banner.String(), testHTTPToken) {
		t.Fatalf("banner missing address or generated token: %q", banner.String())
	}

	s := mcp.NewServer(&mcp.Implementation{Name: "bbkt-test", Version: "0"}, nil)
	srv := newHTTPServer(s, cfg)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve returned %v", err)
		}
	})

	url := "http://" + ln.Addr().String()
	if status, body := postInitializeTo(t, http.DefaultClient, url, "", ""); status != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401 (body %q)", status, body)
	}
	status, body := postInitializeTo(t, http.DefaultClient, url, "Bearer "+testHTTPToken, "")
	if status != http.StatusOK || !strings.Contains(body, `"serverInfo"`) {
		t.Fatalf("correct token: status = %d body %q, want initialize success", status, body)
	}
}

// TestListenHTTPRefusesNonLoopbackWhenLocal guards against a "loopback" host
// (for example a tampered localhost entry) resolving to a non-loopback
// address and skipping the --allow-remote gate.
func TestListenHTTPRefusesNonLoopbackWhenLocal(t *testing.T) {
	var banner bytes.Buffer
	ln, err := listenHTTP(&httpConfig{addr: "0.0.0.0:0", token: testHTTPToken}, &banner)
	if err == nil {
		_ = ln.Close()
		t.Fatal("non-loopback bind accepted for a config marked loopback")
	}
	if !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("err = %v, want non-loopback refusal", err)
	}
	if banner.Len() != 0 {
		t.Fatalf("banner printed for a refused bind: %q", banner.String())
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	for a, want := range map[net.Addr]bool{
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}: true,
		&net.TCPAddr{IP: net.IPv6loopback}:       true,
		&net.TCPAddr{IP: net.IPv4zero}:           false,
		&net.TCPAddr{IP: net.IPv4(10, 0, 0, 5)}:  false,
		&net.UnixAddr{Name: "/tmp/x"}:            false,
	} {
		if got := isLoopbackAddr(a); got != want {
			t.Errorf("isLoopbackAddr(%v) = %v, want %v", a, got, want)
		}
	}
}
