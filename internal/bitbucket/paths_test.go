package bitbucket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPathSegment(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"main", "main", false},
		{"{abcd-1234}", "%7Babcd-1234%7D", false},
		{"feature/x", "feature%2Fx", false},
		{"x/../../pullrequests/5/merge?", "", true},
		{"../../x", "", true},
		{"a/..", "", true},
		{"feature/..x", "feature%2F..x", false},
		{"a b", "a%20b", false},
		{"", "", true},
		{".", "", true},
		{"..", "", true},
		{"...", "...", false},
	}
	for _, tt := range tests {
		got, err := pathSegment("x", tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("pathSegment(%q) = %q, %v; want %q err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestPathMulti(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"a1b2c3", "a1b2c3", false},
		{"main..feature", "main..feature", false},
		{"feature/x..main", "feature/x..main", false},
		{"dir/my file.go", "dir/my%20file.go", false},
		{"dir/", "dir/", false},
		{"", "", true},
		{"..", "", true},
		{"a/../b", "", true},
		{"a/./b", "", true},
		{"a/..", "", true},
		{"a?b", "a%3Fb", false},
		{"docs/C#.md", "docs/C%23.md", false},
		{"100%.txt", "100%25.txt", false},
		{"a%2e%2e/b", "a%252e%252e/b", false},
		{"a\nb", "", true},
		{"a\x00b", "", true},
	}
	for _, tt := range tests {
		got, err := pathMulti("x", tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("pathMulti(%q) = %q, %v; want %q err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestPathUUID(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"{abcd-1234}", "%7Babcd-1234%7D", false},
		{"x/../../pullrequests/5/merge?", "", true},
		{"a/b", "", true},
		{`a\b`, "", true},
		{"..", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		got, err := pathUUID("x", tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("pathUUID(%q) = %q, %v; want %q err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestRejectDotSegments(t *testing.T) {
	for _, p := range []string{"/a/../b", "/a/./b", "/a/%2e%2e/b", "/a/%2E", "/a/..", "/a/x%2F..%2F..%2Fb/c", "/a/%2e%2e%2fb"} {
		if rejectDotSegments(p) == nil {
			t.Errorf("rejectDotSegments(%q) = nil, want error", p)
		}
	}
	for _, p := range []string{"/a/b?x=../y", "/a/..b/c", "/a/b..c", "/repositories/w/r/diff/a..b", "/a/feature%2Fx/b", "/a/x%2F..y"} {
		if err := rejectDotSegments(p); err != nil {
			t.Errorf("rejectDotSegments(%q) = %v", p, err)
		}
	}
}

// recordingClient returns a client whose server records the escaped path and
// request count.
func recordingClient(t *testing.T) (c *Client, gotPath *string, count *int) {
	t.Helper()
	var mu sync.Mutex
	var got string
	var n int
	c = newBearerClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Slash refs are first resolved to a commit via /refs/branches/{name};
		// answer that lookup with a fixed hash and record only the real call.
		if strings.Contains(r.URL.EscapedPath(), "/refs/branches/") {
			_, _ = w.Write([]byte(`{"target":{"hash":"c0ffee"}}`))
			return
		}
		mu.Lock()
		got = r.URL.EscapedPath()
		n++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"values":[]}`))
	})
	return c, &got, &n
}

func TestClientPaths_Escaped(t *testing.T) {
	c, got, _ := recordingClient(t)
	tests := []struct {
		name string
		call func() error
		want string
	}{
		{"get pipeline uuid", func() error {
			_, err := c.GetPipeline(GetPipelineArgs{Workspace: "w", RepoSlug: "r", PipelineUUID: "{ab-12}"})
			return err
		}, "/repositories/w/r/pipelines/%7Bab-12%7D"},
		{"steps", func() error {
			_, err := c.ListPipelineSteps(ListPipelineStepsArgs{Workspace: "w", RepoSlug: "r", PipelineUUID: "{p}"})
			return err
		}, "/repositories/w/r/pipelines/%7Bp%7D/steps"},
		{"step log", func() error {
			_, err := c.GetPipelineStepLog(GetPipelineStepLogArgs{Workspace: "w", RepoSlug: "r", PipelineUUID: "{p}", StepUUID: "{s}"})
			return err
		}, "/repositories/w/r/pipelines/%7Bp%7D/steps/%7Bs%7D/log"},
		{"diff", func() error {
			_, err := c.GetDiff(GetDiffArgs{Workspace: "w", RepoSlug: "r", Spec: "feature/x..main"})
			return err
		}, "/repositories/w/r/diff/feature/x..main"},
		{"diffstat", func() error {
			_, err := c.GetDiffStat(GetDiffStatArgs{Workspace: "w", RepoSlug: "r", Spec: "a1b2c3"})
			return err
		}, "/repositories/w/r/diffstat/a1b2c3"},
		{"file content", func() error {
			_, _, err := c.GetFileContent(GetFileContentArgs{Workspace: "w", RepoSlug: "r", Path: "dir/my file.go", Ref: "feature/x"})
			return err
		}, "/repositories/w/r/src/c0ffee/dir/my%20file.go"},
		{"file content default ref", func() error {
			_, _, err := c.GetFileContent(GetFileContentArgs{Workspace: "w", RepoSlug: "r", Path: "a.go"})
			return err
		}, "/repositories/w/r/src/HEAD/a.go"},
		{"list dir root", func() error {
			_, err := c.ListDirectory(ListDirectoryArgs{Workspace: "w", RepoSlug: "r"})
			return err
		}, "/repositories/w/r/src/HEAD/"},
		{"list dir ref root", func() error {
			_, err := c.ListDirectory(ListDirectoryArgs{Workspace: "w", RepoSlug: "r", Ref: "main"})
			return err
		}, "/repositories/w/r/src/main/"},
		{"list dir path", func() error {
			_, err := c.ListDirectory(ListDirectoryArgs{Workspace: "w", RepoSlug: "r", Ref: "main", Path: "a/b"})
			return err
		}, "/repositories/w/r/src/main/a/b"},
		{"diff query chars escaped", func() error {
			_, err := c.GetDiff(GetDiffArgs{Workspace: "w", RepoSlug: "r", Spec: "a?x=1"})
			return err
		}, "/repositories/w/r/diff/a%3Fx=1"},
		{"diffstat percent escaped", func() error {
			_, err := c.GetDiffStat(GetDiffStatArgs{Workspace: "w", RepoSlug: "r", Spec: "a%2f..%2fb"})
			return err
		}, "/repositories/w/r/diffstat/a%252f..%252fb"},
		{"file with hash", func() error {
			_, _, err := c.GetFileContent(GetFileContentArgs{Workspace: "w", RepoSlug: "r", Path: "docs/C#.md", Ref: "main"})
			return err
		}, "/repositories/w/r/src/main/docs/C%23.md"},
		{"file history", func() error {
			_, err := c.GetFileHistory(GetFileHistoryArgs{Workspace: "w", RepoSlug: "r", Path: "a/b c.go"})
			return err
		}, "/repositories/w/r/filehistory/HEAD/a/b%20c.go"},
		// "." and "/" mean the repo root, as they did before path validation.
		{"list dir dot", func() error {
			_, err := c.ListDirectory(ListDirectoryArgs{Workspace: "w", RepoSlug: "r", Path: "."})
			return err
		}, "/repositories/w/r/src/HEAD/"},
		{"list dir slash", func() error {
			_, err := c.ListDirectory(ListDirectoryArgs{Workspace: "w", RepoSlug: "r", Path: "/"})
			return err
		}, "/repositories/w/r/src/HEAD/"},
		{"list dir dot-slash subdir", func() error {
			_, err := c.ListDirectory(ListDirectoryArgs{Workspace: "w", RepoSlug: "r", Path: "./src/"})
			return err
		}, "/repositories/w/r/src/HEAD/src/"},
		{"file content dot-slash", func() error {
			_, _, err := c.GetFileContent(GetFileContentArgs{Workspace: "w", RepoSlug: "r", Path: "./a/./b.go"})
			return err
		}, "/repositories/w/r/src/HEAD/a/b.go"},
		{"file history leading slash", func() error {
			_, err := c.GetFileHistory(GetFileHistoryArgs{Workspace: "w", RepoSlug: "r", Path: "/a.go"})
			return err
		}, "/repositories/w/r/filehistory/HEAD/a.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			*got = ""
			if err := tt.call(); err != nil {
				t.Fatal(err)
			}
			if *got != tt.want {
				t.Errorf("path = %q, want %q", *got, tt.want)
			}
		})
	}
}

func TestClientPaths_TraversalRejected(t *testing.T) {
	c, _, n := recordingClient(t)
	calls := map[string]func() error{
		"pipeline dotdot": func() error {
			_, err := c.GetPipeline(GetPipelineArgs{Workspace: "w", RepoSlug: "r", PipelineUUID: ".."})
			return err
		},
		"stop dot": func() error {
			return c.StopPipeline(StopPipelineArgs{Workspace: "w", RepoSlug: "r", PipelineUUID: "."})
		},
		"step dotdot": func() error {
			_, err := c.GetPipelineStepLog(GetPipelineStepLogArgs{Workspace: "w", RepoSlug: "r", PipelineUUID: "{p}", StepUUID: ".."})
			return err
		},
		"diff traversal": func() error {
			_, err := c.GetDiff(GetDiffArgs{Workspace: "w", RepoSlug: "r", Spec: "a/../../pullrequests/5"})
			return err
		},
		"stop pipeline attack string": func() error {
			return c.StopPipeline(StopPipelineArgs{Workspace: "w", RepoSlug: "r", PipelineUUID: "x/../../pullrequests/5/merge?"})
		},
		"pipeline slash": func() error {
			_, err := c.GetPipeline(GetPipelineArgs{Workspace: "w", RepoSlug: "r", PipelineUUID: "a/b"})
			return err
		},
		"delete branch encoded traversal": func() error {
			return c.DeleteBranch(DeleteBranchArgs{Workspace: "w", RepoSlug: "r", Name: "../../x"})
		},
		"file traversal": func() error {
			_, _, err := c.GetFileContent(GetFileContentArgs{Workspace: "w", RepoSlug: "r", Path: "../../x"})
			return err
		},
		"file ref dotdot": func() error {
			_, _, err := c.GetFileContent(GetFileContentArgs{Workspace: "w", RepoSlug: "r", Path: "a", Ref: ".."})
			return err
		},
		"list dir traversal": func() error {
			_, err := c.ListDirectory(ListDirectoryArgs{Workspace: "w", RepoSlug: "r", Path: "a/../b"})
			return err
		},
		"list dir leading dotdot": func() error {
			_, err := c.ListDirectory(ListDirectoryArgs{Workspace: "w", RepoSlug: "r", Path: "./../x"})
			return err
		},
		"history of root": func() error {
			_, err := c.GetFileHistory(GetFileHistoryArgs{Workspace: "w", RepoSlug: "r", Path: "."})
			return err
		},
		"history traversal": func() error {
			_, err := c.GetFileHistory(GetFileHistoryArgs{Workspace: "w", RepoSlug: "r", Path: "a/.."})
			return err
		},
		"repo slug dotdot": func() error {
			_, err := c.GetCommit(GetCommitArgs{Workspace: "w", RepoSlug: "..", Commit: "abc"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if *n != 0 {
		t.Errorf("server received %d requests, want 0", *n)
	}
}

func TestDo_ConcurrentRequests(t *testing.T) {
	sandboxHome(t)

	var mu sync.Mutex
	var refreshes int
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		refreshes++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh","refresh_token":"r2","expires_in":3600,"token_type":"bearer"}`))
	}))
	t.Cleanup(tokenSrv.Close)
	withTokenEndpoint(t, tokenSrv)

	var failOnce sync.Once
	var badAuth []string
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		force := false
		if r.URL.Path == "/once" {
			failOnce.Do(func() { force = true })
		}
		if force {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if auth != "Bearer fresh" {
			mu.Lock()
			badAuth = append(badAuth, auth)
			mu.Unlock()
		}
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(apiSrv.Close)

	creds := &Credentials{
		ProfileName:  "default",
		AuthType:     AuthTypeOAuth,
		AccessToken:  "stale",
		RefreshToken: "r1",
		ClientID:     "cid",
		ClientSecret: "csec",
		ExpiresIn:    3600,
		CreatedAt:    time.Now().Add(-2 * time.Hour), // expired
	}
	if err := SaveProfile(creds); err != nil {
		t.Fatal(err)
	}
	c := NewClientFromCredentials(creds)
	c.baseURL = apiSrv.URL

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := "/x"
			if i == 0 {
				p = "/once" // exercises the 401 retry path concurrently
			}
			if _, err := c.Get(p); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(badAuth) != 0 {
		t.Errorf("requests carried a non-refreshed bearer token: %v", badAuth)
	}
	if refreshes == 0 {
		t.Error("expected at least one token refresh")
	}
}

func TestResolveSlashRef(t *testing.T) {
	var paths []string
	c := newBearerClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		switch {
		case strings.HasSuffix(r.URL.EscapedPath(), "/refs/branches/release%2Fv2"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.EscapedPath(), "/refs/tags/release%2Fv2"):
			_, _ = w.Write([]byte(`{"target":{"hash":"abc123"}}`))
		default:
			http.NotFound(w, r)
		}
	})

	if got, err := c.resolveSlashRef("w", "r", "main"); err != nil || got != "main" || len(paths) != 0 {
		t.Fatalf("plain ref: got %q, %v, %d requests; want main unchanged with no request", got, err, len(paths))
	}
	got, err := c.resolveSlashRef("w", "r", "release/v2")
	if err != nil || got != "abc123" {
		t.Fatalf("tag with slash: got %q, %v; want abc123 (falls back from branches to tags)", got, err)
	}
	if _, err := c.resolveSlashRef("w", "r", "no/such"); err == nil || !strings.Contains(err.Error(), "no branch or tag") {
		t.Fatalf("missing ref: err = %v", err)
	}
	if _, err := c.resolveSlashRef("w", "r", "a/../b"); err == nil {
		t.Fatal("dot segments in a ref must be rejected before any request")
	}
}
