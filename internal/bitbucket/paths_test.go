package bitbucket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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
		{"x/../../pullrequests/5/merge?", "x%2F..%2F..%2Fpullrequests%2F5%2Fmerge%3F", false},
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
		{"a?b", "", true},
		{"a#b", "", true},
		{"a%2e%2e/b", "", true},
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

func TestRejectDotSegments(t *testing.T) {
	for _, p := range []string{"/a/../b", "/a/./b", "/a/%2e%2e/b", "/a/%2E", "/a/.."} {
		if rejectDotSegments(p) == nil {
			t.Errorf("rejectDotSegments(%q) = nil, want error", p)
		}
	}
	for _, p := range []string{"/a/b?x=../y", "/a/..b/c", "/a/b..c", "/repositories/w/r/diff/a..b"} {
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
		{"stop pipeline", func() error {
			return c.StopPipeline(StopPipelineArgs{Workspace: "w", RepoSlug: "r", PipelineUUID: "x/../../pullrequests/5/merge?"})
		}, "/repositories/w/r/pipelines/x%2F..%2F..%2Fpullrequests%2F5%2Fmerge%3F/stopPipeline"},
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
		}, "/repositories/w/r/src/feature%2Fx/dir/my%20file.go"},
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
		{"file history", func() error {
			_, err := c.GetFileHistory(GetFileHistoryArgs{Workspace: "w", RepoSlug: "r", Path: "a/b c.go"})
			return err
		}, "/repositories/w/r/filehistory/HEAD/a/b%20c.go"},
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
		"diff query": func() error {
			_, err := c.GetDiff(GetDiffArgs{Workspace: "w", RepoSlug: "r", Spec: "a?x=1"})
			return err
		},
		"diffstat percent": func() error {
			_, err := c.GetDiffStat(GetDiffStatArgs{Workspace: "w", RepoSlug: "r", Spec: "a%2f..%2fb"})
			return err
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
			_, err := c.ListDirectory(ListDirectoryArgs{Workspace: "w", RepoSlug: "r", Path: "a/./b"})
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("", "", "tok")
	c.baseURL = srv.URL
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Get("/x"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
