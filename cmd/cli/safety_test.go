package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestResolveDeleteTarget(t *testing.T) {
	t.Setenv("BBKT_WORKSPACE", "envws")
	t.Setenv("BBKT_REPO", "envrepo")
	ok := []struct {
		args   []string
		ws, rs string
	}{
		{[]string{"ws", "repo"}, "ws", "repo"},
		{[]string{"ws/repo"}, "ws", "repo"},
	}
	for _, c := range ok {
		ws, rs, err := resolveDeleteTarget(c.args)
		if err != nil || ws != c.ws || rs != c.rs {
			t.Errorf("%v: got %q %q %v", c.args, ws, rs, err)
		}
	}
	bad := [][]string{nil, {}, {"ws"}, {""}, {"", "repo"}, {"ws", ""}, {"ws/"}, {"/repo"}, {"a/b/c"}, {"ws", "a/b"}, {" ", "x"}}
	for _, args := range bad {
		if _, _, err := resolveDeleteTarget(args); err == nil {
			t.Errorf("%q: expected error (no inference allowed)", args)
		}
	}
}

func TestConfirmRepoDelete(t *testing.T) {
	const target = "ws/repo"
	cases := []struct {
		name         string
		yes, js, tty bool
		input        string
		wantErr      string
	}{
		{"yes skips prompt", true, false, false, "", ""},
		{"yes with json", true, true, false, "", ""},
		{"non-tty refused", false, false, false, "ws/repo\n", "--yes"},
		{"json refused even on tty", false, true, true, "ws/repo\n", "--yes"},
		{"tty match", false, false, true, "ws/repo\n", ""},
		{"tty mismatch", false, false, true, "repo\n", "aborted"},
		{"tty empty", false, false, true, "", "aborted"},
	}
	for _, c := range cases {
		var out bytes.Buffer
		err := confirmRepoDelete(target, c.yes, c.js, c.tty, strings.NewReader(c.input), &out)
		if c.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: err %v, want containing %q", c.name, err, c.wantErr)
		}
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
		{"env token shadows stored", map[string]string{"BITBUCKET_ACCESS_TOKEN": "abcdefghijkl"}, true,
			statusSource{Kind: "env-token", Fingerprint: "...ijkl", Shadowed: true}},
		{"env token no stored", map[string]string{"BITBUCKET_ACCESS_TOKEN": "short"}, false,
			statusSource{Kind: "env-token", Fingerprint: "****"}},
		{"env basic", map[string]string{"BITBUCKET_USERNAME": "u", "BITBUCKET_API_TOKEN": "0123456789"}, true,
			statusSource{Kind: "env-basic", Fingerprint: "...6789", Shadowed: true}},
		{"token wins over basic", map[string]string{"BITBUCKET_ACCESS_TOKEN": "abcdefghijkl", "BITBUCKET_USERNAME": "u", "BITBUCKET_API_TOKEN": "0123456789"}, false,
			statusSource{Kind: "env-token", Fingerprint: "...ijkl"}},
		{"partial basic ignored", map[string]string{"BITBUCKET_USERNAME": "u"}, true, statusSource{Kind: "stored"}},
	}
	for _, c := range cases {
		if got := selectStatusSource(env(c.env), c.stored); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}
