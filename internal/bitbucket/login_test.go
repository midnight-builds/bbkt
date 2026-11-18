package bitbucket

import (
	"os"
	"strings"
	"testing"
)

// withStdin points os.Stdin at a pipe carrying input for the test's duration.
func withStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig; _ = r.Close() })
}

func TestAPITokenLogin_NoTerminalExplainsHowToPipe(t *testing.T) {
	sandboxHome(t)
	withStdin(t, "")

	err := APITokenLogin(APITokenLoginOptions{Profile: "demo"})
	if err == nil {
		t.Fatal("expected an error without a terminal")
	}
	for _, want := range []string{"interactive terminal", "--with-token", "--profile demo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if _, statErr := os.Stat(credentialsPathForTest(t)); !os.IsNotExist(statErr) {
		t.Error("credentials file was written despite the failed login")
	}
}

func TestAPITokenLogin_WithTokenNeedsEmail(t *testing.T) {
	sandboxHome(t)
	withStdin(t, "tok\n")

	err := APITokenLogin(APITokenLoginOptions{Profile: "ci", TokenFromStdin: true})
	if err == nil || !strings.Contains(err.Error(), "--email") {
		t.Fatalf("err = %v, want a message asking for --email", err)
	}
}

func TestAPITokenLogin_WithTokenRejectsEmptyToken(t *testing.T) {
	sandboxHome(t)
	withStdin(t, "\n")

	err := APITokenLogin(APITokenLoginOptions{Profile: "ci", Email: "a@b.c", TokenFromStdin: true})
	if err == nil || !strings.Contains(err.Error(), "token is required") {
		t.Fatalf("err = %v, want 'token is required'", err)
	}
}

func TestClassicTokenHelp_UsesSharedScopeLists(t *testing.T) {
	for _, s := range append(append([]string{}, ReadOnlyScopes...), WriteScopes...) {
		if !strings.Contains(classicTokenHelp, s) {
			t.Errorf("classicTokenHelp is missing scope %q", s)
		}
	}
	if strings.Contains(scopeLines(ReadOnlyScopes), "write:") {
		t.Error("read-only scope list contains a write scope")
	}
}

func TestDescribeAccess(t *testing.T) {
	cases := []struct{ scopes, want string }{
		{"", "unknown"},
		{"read:account read:repository:bitbucket", "read-only"},
		{"read:repository:bitbucket write:pullrequest:bitbucket", "read + write"},
		{"read:repository:bitbucket delete:repository:bitbucket", "read + admin/delete"},
		{"read:repository:bitbucket,admin:repository:bitbucket", "read + admin/delete"},
		{"write:repository:bitbucket admin:repository:bitbucket", "read + write + admin/delete"},
	}
	for _, c := range cases {
		got, note := describeAccess(c.scopes)
		if got != c.want {
			t.Errorf("describeAccess(%q) = %q, want %q", c.scopes, got, c.want)
		}
		if (got == "read-only" || got == "unknown") != (len(note) == 0) {
			t.Errorf("describeAccess(%q): note %v does not match level %q", c.scopes, note, got)
		}
	}
}

func credentialsPathForTest(t *testing.T) string {
	t.Helper()
	p, err := CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
