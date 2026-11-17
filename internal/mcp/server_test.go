package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zach-snell/bbkt/internal/bitbucket"
)

const fullScopes = "read:repository:bitbucket, write:repository:bitbucket, read:pullrequest:bitbucket, write:pullrequest:bitbucket, read:pipeline:bitbucket, write:pipeline:bitbucket, read:issue:bitbucket, write:issue:bitbucket"

// fakeAPI is an httptest-backed Bitbucket API. /user answers scope
// introspection: with the X-OAuth-Scopes header set to scopes when non-empty,
// or with a bare 401 when scopes is empty. Every request is counted.
type fakeAPI struct {
	srv      *httptest.Server
	requests atomic.Int64
	paths    chan string
}

func newFakeAPI(t *testing.T, scopes string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{paths: make(chan string, 64)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		select {
		case f.paths <- r.Method + " " + r.URL.Path:
		default:
		}
		if r.URL.Path == "/user" {
			if scopes == "" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"type":"error","error":{"message":"Unauthorized"}}`))
				return
			}
			w.Header().Set("X-OAuth-Scopes", scopes)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) client() *bitbucket.Client {
	c := bitbucket.NewClient("", "", "test-token")
	c.SetBaseURLForTesting(f.srv.URL)
	return c
}

// clearPolicyEnv makes each test independent of the developer's environment.
func clearPolicyEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"BITBUCKET_DISABLED_TOOLS", "BBKT_SKIP_SCOPE_CHECK", "BBKT_API_ALLOW_WRITE", "BBKT_WORKSPACE", "BBKT_REPO"} {
		t.Setenv(k, "")
	}
}

// connect serves s over in-memory transports and returns a connected client session.
func connect(t *testing.T, s *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cl := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := cl.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func listToolNames(t *testing.T, s *mcp.Server) []string {
	t.Helper()
	res, err := connect(t, s).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func sorted(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

func without(all []string, drop ...string) []string {
	var out []string
	for _, n := range all {
		if !slices.Contains(drop, n) {
			out = append(out, n)
		}
	}
	return sorted(out)
}

func TestToolNames_MatchesExpectedSet(t *testing.T) {
	want := []string{
		"bitbucket_api", "manage_commits", "manage_issues", "manage_pipelines",
		"manage_pr_comments", "manage_pull_requests", "manage_refs",
		"manage_repositories", "manage_source", "manage_workspaces",
	}
	if got := sorted(toolNames()); !slices.Equal(got, want) {
		t.Fatalf("toolNames() = %v, want %v", got, want)
	}
}

func TestListTools_FullScopesNoDenyList(t *testing.T) {
	clearPolicyEnv(t)
	s, err := newServer(newFakeAPI(t, fullScopes).client())
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	if got, want := listToolNames(t, s), sorted(toolNames()); !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestListTools_UnauthenticatedMatchesToolSet(t *testing.T) {
	clearPolicyEnv(t)
	if got, want := listToolNames(t, NewUnauthenticated()), sorted(toolNames()); !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestListTools_DisabledToolsRemoved(t *testing.T) {
	clearPolicyEnv(t)
	// Whitespace and empty entries (trailing/double commas) are tolerated.
	t.Setenv("BITBUCKET_DISABLED_TOOLS", " bitbucket_api, manage_source,,")
	s, err := newServer(newFakeAPI(t, fullScopes).client())
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	want := without(toolNames(), "bitbucket_api", "manage_source")
	if got := listToolNames(t, s); !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestNewServer_UnknownDisabledToolFails(t *testing.T) {
	clearPolicyEnv(t)
	t.Setenv("BITBUCKET_DISABLED_TOOLS", "manage_source,manage_pullrequests,bogus")
	api := newFakeAPI(t, fullScopes)
	s, err := newServer(api.client())
	if err == nil {
		t.Fatal("newServer succeeded with unknown deny-list entries, want error")
	}
	if s != nil {
		t.Error("newServer returned a server alongside the error")
	}
	msg := err.Error()
	for _, want := range []string{`"manage_pullrequests"`, `"bogus"`, "valid names", "manage_pull_requests", "bitbucket_api"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
	if strings.Contains(msg, `"manage_source"`) {
		t.Errorf("error %q flags the valid entry manage_source", msg)
	}
	if n := api.requests.Load(); n != 0 {
		t.Errorf("deny-list validation made %d API request(s), want 0", n)
	}
}

func TestParseDisabledTools_EmptyIsEmpty(t *testing.T) {
	for _, raw := range []string{"", ",", " , ,"} {
		got, err := parseDisabledTools(raw)
		if err != nil {
			t.Fatalf("parseDisabledTools(%q): %v", raw, err)
		}
		if len(got) != 0 {
			t.Errorf("parseDisabledTools(%q) = %v, want empty", raw, got)
		}
	}
}

func TestNewServer_ScopeIntrospectionFailureFailsClosed(t *testing.T) {
	clearPolicyEnv(t)
	s, err := newServer(newFakeAPI(t, "").client())
	if err == nil {
		t.Fatal("newServer succeeded although scope introspection failed, want error")
	}
	if s != nil {
		t.Error("newServer returned a server alongside the error")
	}
	if !strings.Contains(err.Error(), "BBKT_SKIP_SCOPE_CHECK") {
		t.Errorf("error %q lacks the BBKT_SKIP_SCOPE_CHECK hint", err)
	}
	var authErr *bitbucket.AuthError
	if !errors.As(err, &authErr) {
		t.Errorf("error %q does not wrap the underlying *bitbucket.AuthError", err)
	}
}

func TestNewServer_SkipScopeCheckRegistersAllTools(t *testing.T) {
	for _, v := range []string{"1", "true", "YES", "on"} {
		t.Run(v, func(t *testing.T) {
			clearPolicyEnv(t)
			t.Setenv("BBKT_SKIP_SCOPE_CHECK", v)
			t.Setenv("BITBUCKET_DISABLED_TOOLS", "manage_issues")
			s, err := newServer(newFakeAPI(t, "").client())
			if err != nil {
				t.Fatalf("newServer: %v", err)
			}
			if got, want := listToolNames(t, s), without(toolNames(), "manage_issues"); !slices.Equal(got, want) {
				t.Fatalf("tools = %v, want %v", got, want)
			}
		})
	}
}

func TestNewServer_SkipScopeCheckRejectsOtherValues(t *testing.T) {
	clearPolicyEnv(t)
	t.Setenv("BBKT_SKIP_SCOPE_CHECK", "0")
	if _, err := newServer(newFakeAPI(t, "").client()); err == nil {
		t.Fatal("BBKT_SKIP_SCOPE_CHECK=0 skipped the scope check, want error")
	}
}

func TestListTools_ReadOnlyScopesDropTools(t *testing.T) {
	clearPolicyEnv(t)
	// Token can read repositories and pull requests only: pipelines and issues
	// tools must not be offered.
	s, err := newServer(newFakeAPI(t, "read:repository:bitbucket, read:pullrequest:bitbucket").client())
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	want := without(toolNames(), "manage_pipelines", "manage_issues")
	if got := listToolNames(t, s); !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestBitbucketAPI_WriteBlockedWithoutOptIn(t *testing.T) {
	clearPolicyEnv(t)
	api := newFakeAPI(t, fullScopes)
	s, err := newServer(api.client())
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	cs := connect(t, s)
	before := api.requests.Load() // the /user introspection call

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "bitbucket_api",
		Arguments: map[string]any{
			"path":   "/repositories/ws/repo",
			"method": "DELETE",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("DELETE via bitbucket_api succeeded without BBKT_API_ALLOW_WRITE, want tool error")
	}
	if text := resultText(res); !strings.Contains(text, "BBKT_API_ALLOW_WRITE") {
		t.Errorf("result %q does not explain the BBKT_API_ALLOW_WRITE opt-in", text)
	}
	if after := api.requests.Load(); after != before {
		t.Errorf("blocked write reached the API: %d request(s) after the call, want 0", after-before)
	}
}

func TestBitbucketAPI_ReadReachesServer(t *testing.T) {
	clearPolicyEnv(t)
	api := newFakeAPI(t, fullScopes)
	s, err := newServer(api.client())
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	cs := connect(t, s)
	<-api.paths // drain the /user introspection call

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "bitbucket_api",
		Arguments: map[string]any{"path": "/repositories/ws/repo"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("GET via bitbucket_api failed: %s", resultText(res))
	}
	if got := <-api.paths; got != "GET /repositories/ws/repo" {
		t.Errorf("server saw %q, want GET /repositories/ws/repo", got)
	}
}

func TestResolveScope_EnvFallback(t *testing.T) {
	tests := []struct {
		name                 string
		envWorkspace, envRep string
		inWorkspace, inRepo  string
		wantWorkspace        string
		wantRepo             string
	}{
		{name: "no env, no args"},
		{name: "workspace env", envWorkspace: "envws", wantWorkspace: "envws"},
		{name: "repo env plain", envWorkspace: "envws", envRep: "envrepo", wantWorkspace: "envws", wantRepo: "envrepo"},
		{name: "repo env compound", envRep: "cws/crepo", wantWorkspace: "cws", wantRepo: "crepo"},
		{name: "compound does not override workspace env", envWorkspace: "envws", envRep: "cws/crepo", wantWorkspace: "envws", wantRepo: "crepo"},
		{name: "args win", envWorkspace: "envws", envRep: "cws/crepo", inWorkspace: "argws", inRepo: "argrepo", wantWorkspace: "argws", wantRepo: "argrepo"},
		{name: "explicit workspace with compound repo env", envRep: "cws/crepo", inWorkspace: "argws", wantWorkspace: "argws", wantRepo: "crepo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BBKT_WORKSPACE", tt.envWorkspace)
			t.Setenv("BBKT_REPO", tt.envRep)
			ws, repo := ResolveScope(tt.inWorkspace, tt.inRepo)
			if ws != tt.wantWorkspace || repo != tt.wantRepo {
				t.Errorf("ResolveScope(%q, %q) = (%q, %q), want (%q, %q)",
					tt.inWorkspace, tt.inRepo, ws, repo, tt.wantWorkspace, tt.wantRepo)
			}
		})
	}
}

func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
