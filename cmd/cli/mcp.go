package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/zach-snell/bbkt/internal/bitbucket"
	mcpserver "github.com/zach-snell/bbkt/internal/mcp"
)

const (
	// defaultHTTPHost keeps the HTTP transport off the network unless the user
	// explicitly asks for a remote bind.
	defaultHTTPHost = "127.0.0.1"
	// httpTokenEnv names the env var holding the bearer token HTTP clients must send.
	httpTokenEnv = "BBKT_MCP_HTTP_TOKEN" //nolint:gosec // env var name, not a credential
	// minHTTPTokenLen rejects trivially guessable configured tokens.
	minHTTPTokenLen = 16
	// httpSessionTimeout closes MCP sessions that have been idle this long.
	httpSessionTimeout = 30 * time.Minute
)

var (
	port        int
	host        string
	allowRemote bool
	noAuth      bool
)

var mcpCmd = &cobra.Command{
	Use:     "mcp",
	GroupID: groupMCP,
	Short:   "Start the Bitbucket MCP Server (for Claude Desktop, Cursor, etc.)",
	Long: `Starts the Model Context Protocol (MCP) server for Bitbucket.

By default the server speaks stdio — the right mode for local MCP
clients (Claude Desktop, Cursor). Use --port to switch to the HTTP
Streamable transport.

HTTP transport (--port):
  - Binds 127.0.0.1 by default. --host changes the bind address; a
    non-loopback host additionally requires --allow-remote and an
    explicit BBKT_MCP_HTTP_TOKEN.
  - Every request must carry "Authorization: Bearer <token>". The token
    comes from BBKT_MCP_HTTP_TOKEN (at least 16 characters); on loopback,
    if it is unset, a random token is generated and printed once to stderr.
  - The transport is cleartext HTTP (no TLS). Anything beyond loopback
    should sit behind a TLS-terminating reverse proxy.

Credentials are resolved in this order: BITBUCKET_ACCESS_TOKEN env,
then BITBUCKET_USERNAME + BITBUCKET_API_TOKEN env, then the stored
profile at ~/.config/bbkt/credentials.json (selected by --profile,
BBKT_PROFILE, or active_profile).

At startup the server introspects the credential's granted scopes and
drops a tool only when the credential lacks that tool's read scope. A
read-only credential therefore still exposes the write actions inside
the multiplexed manage_* tools (they fail at the API instead), so scope
filtering is not a write guard. For agents, use least-privilege
credentials, ideally a Bitbucket repository or workspace access token
via BITBUCKET_ACCESS_TOKEN. If scope introspection fails, the server
refuses to start unless BBKT_SKIP_SCOPE_CHECK=1.

BITBUCKET_DISABLED_TOOLS (comma-separated) removes tools outright:
manage_workspaces, manage_repositories, manage_refs, manage_commits,
manage_pull_requests, manage_pr_comments, manage_source,
manage_pipelines, manage_issues, bitbucket_api. Unknown names are
rejected at startup.

bitbucket_api is a raw passthrough to any Bitbucket v2 endpoint. It
allows only GET/HEAD unless BBKT_API_ALLOW_WRITE=1 is set.`,
	Example: `  bbkt mcp                              # stdio (default)
  bbkt mcp --port 8080                  # HTTP on 127.0.0.1:8080; token printed to stderr
  BBKT_MCP_HTTP_TOKEN=$(openssl rand -hex 32) bbkt mcp --port 8080
  BBKT_MCP_HTTP_TOKEN=... bbkt mcp --port 8080 --host 0.0.0.0 --allow-remote
  bbkt --profile work mcp               # use the "work" profile
  bbkt mcp --no-auth                    # start without creds (tools return auth-required)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if port == 0 && (cmd.Flags().Changed("host") || allowRemote) {
			return errors.New("--host and --allow-remote only apply to the HTTP transport; set --port")
		}
		return runServer()
	},
}

func init() {
	RootCmd.AddCommand(mcpCmd)
	// No -p shorthand: it collides with the global persistent --profile (-p).
	mcpCmd.Flags().IntVar(&port, "port", 0, "Port to listen on for HTTP Streamable transport (bearer token required; see --help)")
	mcpCmd.Flags().StringVar(&host, "host", defaultHTTPHost, "Address to bind the HTTP transport to (non-loopback requires --allow-remote and BBKT_MCP_HTTP_TOKEN)")
	mcpCmd.Flags().BoolVar(&allowRemote, "allow-remote", false, "Allow binding the HTTP transport to a non-loopback address (cleartext; put it behind TLS)")
	mcpCmd.Flags().BoolVar(&noAuth, "no-auth", false, "Start server without authentication (tools will return auth-required errors when called)")
}

// httpConfig is the validated configuration for the HTTP Streamable transport.
type httpConfig struct {
	addr      string // host:port to listen on
	token     string // bearer token clients must present
	generated bool   // token was generated rather than configured
	remote    bool   // bind address is not loopback
}

// unbracket strips the brackets from an IPv6 literal such as "[::1]".
func unbracket(h string) string {
	return strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
}

// isLoopbackHost reports whether h names a loopback interface. An empty host
// means "all interfaces" and is therefore not loopback.
func isLoopbackHost(h string) bool {
	h = unbracket(h)
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// resolveHTTPConfig validates the HTTP transport flags and picks the bearer
// token. A non-loopback bind needs both allowRemoteBind and an explicit
// envToken; on loopback a missing envToken is replaced by a random one.
func resolveHTTPConfig(bindHost string, bindPort int, allowRemoteBind bool, envToken string) (*httpConfig, error) {
	if bindPort < 1 || bindPort > 65535 {
		return nil, fmt.Errorf("invalid --port %d: must be between 1 and 65535", bindPort)
	}
	cfg := &httpConfig{
		addr:   net.JoinHostPort(unbracket(bindHost), strconv.Itoa(bindPort)),
		remote: !isLoopbackHost(bindHost),
	}
	envToken = strings.TrimSpace(envToken)

	if cfg.remote {
		if !allowRemoteBind {
			return nil, fmt.Errorf("refusing to bind the MCP HTTP transport to non-loopback host %q without --allow-remote", bindHost)
		}
		if envToken == "" {
			return nil, fmt.Errorf("binding the MCP HTTP transport to non-loopback host %q requires an explicit %s", bindHost, httpTokenEnv)
		}
	}

	if envToken != "" {
		if len(envToken) < minHTTPTokenLen {
			return nil, fmt.Errorf("%s must be at least %d characters", httpTokenEnv, minHTTPTokenLen)
		}
		cfg.token = envToken
		return cfg, nil
	}

	tok, err := generateHTTPToken()
	if err != nil {
		return nil, err
	}
	cfg.token = tok
	cfg.generated = true
	return cfg, nil
}

// generateHTTPToken returns 32 random bytes, hex encoded.
func generateHTTPToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating MCP HTTP token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// staticTokenVerifier accepts exactly one bearer token. Both sides are hashed
// before the constant-time compare so neither content nor length leaks.
func staticTokenVerifier(want string) auth.TokenVerifier {
	wantSum := sha256.Sum256([]byte(want))
	return func(_ context.Context, got string, _ *http.Request) (*auth.TokenInfo, error) {
		gotSum := sha256.Sum256([]byte(got))
		if subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) != 1 {
			return nil, auth.ErrInvalidToken
		}
		// RequireBearerToken rejects a zero Expiration; the static token never
		// expires, so report a rolling window. UserID binds sessions to the
		// authenticated caller.
		return &auth.TokenInfo{UserID: "bbkt-mcp-http", Expiration: time.Now().Add(time.Hour)}, nil
	}
}

// newHTTPHandler builds the HTTP Streamable handler for s, gated by a bearer
// token. The SDK's DNS-rebinding (localhost) and cross-origin protections stay
// at their enabled defaults.
func newHTTPHandler(s *mcp.Server, token string) http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return s
	}, &mcp.StreamableHTTPOptions{
		JSONResponse:   false,
		SessionTimeout: httpSessionTimeout,
	})
	authed := auth.RequireBearerToken(staticTokenVerifier(token), nil)(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authed.ServeHTTP(&bearerChallengeWriter{ResponseWriter: w}, r)
	})
}

// bearerChallengeWriter adds the RFC 6750 "WWW-Authenticate: Bearer"
// challenge to 401 responses, which the SDK middleware omits when no OAuth
// resource metadata URL is configured.
type bearerChallengeWriter struct {
	http.ResponseWriter
}

func (w *bearerChallengeWriter) WriteHeader(code int) {
	if code == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="bbkt"`)
	}
	w.ResponseWriter.WriteHeader(code)
}

// Flush keeps SSE streaming working through the wrapper.
func (w *bearerChallengeWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *bearerChallengeWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// listenHTTP opens the listener for cfg and prints the startup banner to
// stderrW. When cfg claims a loopback bind, the address actually bound is
// checked too: a hostname such as "localhost" goes through the system
// resolver and must not silently land on a non-loopback interface, which
// would bypass the --allow-remote and explicit-token requirements.
func listenHTTP(cfg *httpConfig, stderrW io.Writer) (net.Listener, error) {
	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return nil, fmt.Errorf("HTTP listen on %s: %w", cfg.addr, err)
	}
	if !cfg.remote && !isLoopbackAddr(ln.Addr()) {
		_ = ln.Close()
		return nil, fmt.Errorf("refusing to serve: %s resolved to non-loopback address %s; use an IP literal or --allow-remote", cfg.addr, ln.Addr())
	}

	_, _ = fmt.Fprintf(stderrW, "Starting Bitbucket MCP Server on http://%s/ (HTTP Streamable)\n", ln.Addr())
	if cfg.remote {
		_, _ = fmt.Fprintln(stderrW, "\nWARNING: listening on a non-loopback address over cleartext HTTP (no TLS).")
		_, _ = fmt.Fprintln(stderrW, "WARNING: the bearer token and all Bitbucket data cross the network unencrypted.")
		_, _ = fmt.Fprintln(stderrW, "WARNING: put this server behind a TLS-terminating reverse proxy.")
		_, _ = fmt.Fprintln(stderrW)
	}
	if cfg.generated {
		_, _ = fmt.Fprintf(stderrW, "\nGenerated a random bearer token for this run (%s is unset):\n\n  %s\n\n", httpTokenEnv, cfg.token)
		_, _ = fmt.Fprintln(stderrW, "Clients must send:  Authorization: Bearer <token>")
		_, _ = fmt.Fprintf(stderrW, "Set %s to keep a stable token across restarts.\n\n", httpTokenEnv)
	}
	return ln, nil
}

// isLoopbackAddr reports whether a bound listener address is on loopback.
func isLoopbackAddr(a net.Addr) bool {
	tcp, ok := a.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}

// newHTTPServer returns the http.Server that serves s behind the bearer token.
func newHTTPServer(s *mcp.Server, cfg *httpConfig) *http.Server {
	return &http.Server{
		Handler:           newHTTPHandler(s, cfg.token),
		ReadHeaderTimeout: 3 * time.Second,
	}
}

// serveHTTP listens on cfg.addr and serves s until the listener fails. All
// operator-facing output goes to stderr so stdout stays clean.
func serveHTTP(s *mcp.Server, cfg *httpConfig) error {
	ln, err := listenHTTP(cfg, os.Stderr)
	if err != nil {
		return err
	}
	if err := newHTTPServer(s, cfg).Serve(ln); err != nil {
		return fmt.Errorf("HTTP server error: %w", err)
	}
	return nil
}

func runServer() error {
	// Validate the HTTP transport before loading credentials so a refused
	// bind never touches the Bitbucket API.
	var httpCfg *httpConfig
	if port != 0 {
		cfg, err := resolveHTTPConfig(host, port, allowRemote, os.Getenv(httpTokenEnv))
		if err != nil {
			return err
		}
		httpCfg = cfg
	}

	var s *mcp.Server

	if noAuth {
		s = mcpserver.NewUnauthenticated()
	} else {
		// Priority: env vars > stored credentials
		username := os.Getenv("BITBUCKET_USERNAME")
		password := os.Getenv("BITBUCKET_API_TOKEN")
		token := os.Getenv("BITBUCKET_ACCESS_TOKEN")

		if token != "" || (username != "" && password != "") {
			var err error
			if s, err = mcpserver.New(username, password, token); err != nil {
				return err
			}
		} else {
			creds, err := bitbucket.LoadCredentials()
			if err != nil {
				// Print onboarding hint to stderr as guidance, return the
				// terse error for cobra to surface.
				fmt.Fprintf(os.Stderr, "No credentials found. Either:\n")
				fmt.Fprintf(os.Stderr, "  1. Run: bbkt auth          (API token — recommended)\n")
				fmt.Fprintf(os.Stderr, "  2. Run: bbkt auth --oauth   (OAuth via browser)\n")
				fmt.Fprintf(os.Stderr, "  3. Set BITBUCKET_ACCESS_TOKEN env var\n")
				fmt.Fprintf(os.Stderr, "  4. Set BITBUCKET_USERNAME + BITBUCKET_API_TOKEN env vars\n\n")
				return fmt.Errorf("no credentials configured")
			}

			switch {
			case creds.IsAPIToken() || creds.IsOAuth():
				if s, err = mcpserver.NewFromCredentials(creds); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unknown auth type in stored credentials: %s", creds.AuthType)
			}
		}
	}

	if httpCfg != nil {
		return serveHTTP(s, httpCfg)
	}
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}
