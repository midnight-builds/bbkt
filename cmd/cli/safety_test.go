package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestTokenTailMasksShort(t *testing.T) {
	if got := tokenTail("123456789"); got != "****" {
		t.Errorf("9-char token: got %q", got)
	}
	if got := tokenTail("0123456789abcdef"); got != "...cdef" {
		t.Errorf("16-char token: got %q", got)
	}
}

func TestConfirmRepoDeleteBoundedRead(t *testing.T) {
	var out bytes.Buffer
	err := confirmRepoDelete("ws/repo", false, false, true, io.LimitReader(zeroReader{}, 1<<30), &out)
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Errorf("expected abort, got %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// runDelete executes "repos delete" through the real command tree. Callers
// only use argument sets that must fail before any client is constructed, so
// no network request can be made.
func runDelete(t *testing.T, args ...string) error {
	t.Helper()
	t.Setenv("BITBUCKET_ACCESS_TOKEN", "tok")
	t.Setenv("BBKT_WORKSPACE", "envws")
	t.Setenv("BBKT_REPO", "envrepo")
	RootCmd.SetArgs(append([]string{"repos", "delete"}, args...))
	RootCmd.SetOut(io.Discard)
	RootCmd.SetErr(io.Discard)
	defer RootCmd.SetArgs(nil)
	defer func() { _ = reposDeleteCmd.Flags().Set("yes", "false") }()
	return RootCmd.Execute()
}

func TestReposDeleteWiring(t *testing.T) {
	if stdinIsTerminal() {
		t.Skip("stdin is a terminal")
	}
	// No args: BBKT_WORKSPACE/BBKT_REPO must not be used as a target.
	if err := runDelete(t); err == nil {
		t.Error("no args: expected error")
	}
	// Explicit target, non-tty stdin, no --yes: refused by the confirmation
	// gate (which runs before the client is built).
	err := runDelete(t, "ws/repo")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("no --yes: err=%v", err)
	}
}

func TestSelectStatusSource(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name   string
		env    map[string]string
		stored bool
		want   statusSource
	}{
		{"none", nil, false, statusSource{Kind: "none"}},
		{"stored only", nil, true, statusSource{Kind: "stored"}},
		{"env token shadows stored", map[string]string{"BITBUCKET_ACCESS_TOKEN": "abcdefghijklmnopqrst"}, true,
			statusSource{Kind: "env-token", Fingerprint: "...qrst", Shadowed: true}},
		{"env token no stored", map[string]string{"BITBUCKET_ACCESS_TOKEN": "short"}, false,
			statusSource{Kind: "env-token", Fingerprint: "****"}},
		{"env basic", map[string]string{"BITBUCKET_USERNAME": "u", "BITBUCKET_API_TOKEN": "0123456789abcdefghij"}, true,
			statusSource{Kind: "env-basic", Fingerprint: "...ghij", Shadowed: true}},
		{"token wins over basic", map[string]string{"BITBUCKET_ACCESS_TOKEN": "abcdefghijklmnopqrst", "BITBUCKET_USERNAME": "u", "BITBUCKET_API_TOKEN": "0123456789abcdefghij"}, false,
			statusSource{Kind: "env-token", Fingerprint: "...qrst"}},
		{"partial basic ignored", map[string]string{"BITBUCKET_USERNAME": "u"}, true, statusSource{Kind: "stored"}},
	}
	for _, c := range cases {
		if got := selectStatusSource(env(c.env), c.stored); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}
