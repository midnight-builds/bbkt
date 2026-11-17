package bitbucket

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// isolatedCredsPath points HOME at a fresh temp dir so tests never touch the
// real ~/.config, and returns the credentials path inside it.
func isolatedCredsPath(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path, err := CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func writeRaw(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile's mode is filtered by umask; force the exact mode.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func loadOnDisk(t *testing.T, path string) ProfileStore {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s ProfileStore
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("credentials file is not valid JSON: %v\n%s", err, data)
	}
	return s
}

func TestSaveProfile_NotExistStartsFresh(t *testing.T) {
	path := isolatedCredsPath(t)

	if err := SaveProfile(&Credentials{ProfileName: "work", AuthType: AuthTypeAPIToken, APIToken: "t"}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	s := loadOnDisk(t, path)
	if s.ActiveProfile != "work" || len(s.Profiles) != 1 || s.Profiles["work"].APIToken != "t" {
		t.Errorf("unexpected store: %+v", s)
	}
}

// A corrupt/half-written file must not be treated as "no profiles": that
// silently wiped every saved profile (BBKT-SEC-11).
func TestSaveProfile_CorruptFileIsNotOverwritten(t *testing.T) {
	path := isolatedCredsPath(t)
	corrupt := `{"active_profile": "work", "profiles": {"work": {"api_tok`
	writeRaw(t, path, corrupt, 0o600)

	err := SaveProfile(&Credentials{ProfileName: "other", AuthType: AuthTypeAPIToken})
	if err == nil {
		t.Fatal("SaveProfile succeeded over a corrupt credentials file; want error")
	}
	if !strings.Contains(err.Error(), "parsing") {
		t.Errorf("error should explain the parse failure, got: %v", err)
	}

	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != corrupt {
		t.Errorf("corrupt file was modified:\n got %q\nwant %q", got, corrupt)
	}
}

func TestSaveProfile_PreservesOtherProfiles(t *testing.T) {
	path := isolatedCredsPath(t)

	if err := SaveProfile(&Credentials{ProfileName: "work", AuthType: AuthTypeAPIToken, APIToken: "w"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProfile(&Credentials{ProfileName: "personal", AuthType: AuthTypeOAuth, AccessToken: "p"}); err != nil {
		t.Fatal(err)
	}
	// Overwrite one profile; the other must survive untouched.
	if err := SaveProfile(&Credentials{ProfileName: "personal", AuthType: AuthTypeOAuth, AccessToken: "p2"}); err != nil {
		t.Fatal(err)
	}

	s := loadOnDisk(t, path)
	if s.ActiveProfile != "work" {
		t.Errorf("ActiveProfile = %q, want work", s.ActiveProfile)
	}
	if len(s.Profiles) != 2 || s.Profiles["work"].APIToken != "w" || s.Profiles["personal"].AccessToken != "p2" {
		t.Errorf("profiles not preserved: %+v", s.Profiles)
	}
}

func assertAllProfiles(t *testing.T, path string, names []string) {
	t.Helper()
	s := loadOnDisk(t, path)
	for _, n := range names {
		if _, ok := s.Profiles[n]; !ok {
			t.Errorf("profile %q lost by concurrent saves", n)
		}
	}
	if len(s.Profiles) != len(names) {
		t.Errorf("profile count = %d, want %d", len(s.Profiles), len(names))
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestSaveProfile_ConcurrentGoroutinesKeepEveryProfile(t *testing.T) {
	path := isolatedCredsPath(t)

	const n = 32
	names := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		names[i] = fmt.Sprintf("p%02d", i)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = SaveProfile(&Credentials{ProfileName: names[i], AuthType: AuthTypeAPIToken, APIToken: names[i]})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("SaveProfile(%s): %v", names[i], err)
		}
	}
	assertAllProfiles(t, path, names)
}

const helperProfilesEnv = "BBKT_TEST_SAVE_PROFILES"

// TestHelperSaveProfiles is not a real test: it is the child process body for
// TestSaveProfile_ConcurrentProcessesKeepEveryProfile.
func TestHelperSaveProfiles(t *testing.T) {
	list := os.Getenv(helperProfilesEnv)
	if list == "" {
		t.Skip("helper process only")
	}
	for _, name := range strings.Split(list, ",") {
		if err := SaveProfile(&Credentials{ProfileName: name, AuthType: AuthTypeAPIToken, APIToken: name}); err != nil {
			t.Fatalf("SaveProfile(%s): %v", name, err)
		}
	}
}

// Separate processes (e.g. several MCP servers refreshing OAuth tokens) must
// also serialize through the file lock.
func TestSaveProfile_ConcurrentProcessesKeepEveryProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns subprocesses")
	}
	path := isolatedCredsPath(t)
	home := os.Getenv("HOME")

	const procs, perProc = 6, 5
	var names []string
	cmds := make([]*exec.Cmd, procs)
	outs := make([]*bytes.Buffer, procs)
	for p := range procs {
		var mine []string
		for j := range perProc {
			mine = append(mine, fmt.Sprintf("proc%d-%d", p, j))
		}
		names = append(names, mine...)
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSaveProfiles$", "-test.count=1")
		cmd.Env = append(os.Environ(), helperProfilesEnv+"="+strings.Join(mine, ","), "HOME="+home, "USERPROFILE="+home)
		outs[p] = new(bytes.Buffer)
		cmd.Stdout, cmd.Stderr = outs[p], outs[p]
		cmds[p] = cmd
	}
	for _, c := range cmds {
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for p, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("helper %d failed: %v\n%s", p, err, outs[p])
		}
	}
	assertAllProfiles(t, path, names)
}

func TestSaveProfile_TightensExistingModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes not applicable")
	}
	path := isolatedCredsPath(t)
	writeRaw(t, path, `{"active_profile":"a","profiles":{"a":{"auth_type":"api_token"}}}`, 0o644)
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	stderrOut = new(bytes.Buffer)
	t.Cleanup(func() { stderrOut = os.Stderr })

	if err := SaveProfile(&Credentials{ProfileName: "b", AuthType: AuthTypeAPIToken}); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("credentials file mode = %#o, want 0600", got)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("config dir mode = %#o, want 0700", got)
	}
	if s := loadOnDisk(t, path); len(s.Profiles) != 2 {
		t.Errorf("profiles = %+v, want a and b", s.Profiles)
	}
}

func TestLoadProfileStore_WarnsOnLooseMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes not applicable")
	}
	path := isolatedCredsPath(t)
	writeRaw(t, path, `{"active_profile":"a","profiles":{"a":{"auth_type":"api_token"}}}`, 0o644)
	buf := new(bytes.Buffer)
	stderrOut = buf
	t.Cleanup(func() { stderrOut = os.Stderr })

	if _, err := LoadProfileStore(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "warning:") || !strings.Contains(out, "chmod 600") {
		t.Errorf("expected loose-permission warning, got %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("warning should be a single line, got %q", out)
	}

	// Not repeated on subsequent loads in the same process.
	buf.Reset()
	if _, err := LoadProfileStore(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("warning repeated: %q", buf.String())
	}
}

func TestLoadProfileStore_NoWarningAt0600(t *testing.T) {
	path := isolatedCredsPath(t)
	writeRaw(t, path, `{"active_profile":"a","profiles":{"a":{"auth_type":"api_token"}}}`, 0o600)
	buf := new(bytes.Buffer)
	stderrOut = buf
	t.Cleanup(func() { stderrOut = os.Stderr })

	if _, err := LoadProfileStore(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected warning: %q", buf.String())
	}
}

func TestSaveProfile_RefusesSymlinkedCredentialsFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on windows")
	}
	path := isolatedCredsPath(t)
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	const original = `{"active_profile":"a","profiles":{"a":{"auth_type":"api_token"}}}`
	writeRaw(t, target, original, 0o600)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	err := SaveProfile(&Credentials{ProfileName: "b", AuthType: AuthTypeAPIToken})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("SaveProfile through symlink: err = %v, want symlink refusal", err)
	}
	if err := SaveProfileStore(&ProfileStore{}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("SaveProfileStore through symlink: err = %v, want symlink refusal", err)
	}

	got, rerr := os.ReadFile(target)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != original {
		t.Errorf("symlink target was modified: %s", got)
	}
	fi, lerr := os.Lstat(path)
	if lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink should be left in place, lstat = %v, %v", fi, lerr)
	}
}

// Piped stdin (scripts, tests) must keep working as plain line input; masked
// reading only applies to a real terminal.
func TestReadSecretLine_PipeReadsPlainLine(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if _, err := w.WriteString("s3cret-token\n"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	got, err := readSecretLine(bufio.NewReader(r), r)
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cret-token\n" {
		t.Errorf("readSecretLine = %q, want %q", got, "s3cret-token\n")
	}
}

// A legacy single-credential file is migrated to the profile format on load.
func TestLoadProfileStore_MigratesLegacyFile(t *testing.T) {
	path := isolatedCredsPath(t)
	writeRaw(t, path, `{"auth_type":"api_token","email":"me@example.com","api_token":"tok"}`, 0o600)

	store, err := LoadProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	if store.ActiveProfile != "default" || store.Profiles["default"] == nil || store.Profiles["default"].Email != "me@example.com" {
		t.Fatalf("migrated store = %+v", store)
	}

	disk := loadOnDisk(t, path)
	if disk.ActiveProfile != "default" || disk.Profiles["default"] == nil || disk.Profiles["default"].APIToken != "tok" {
		t.Errorf("on-disk file not migrated: %+v", disk)
	}
}

// Migration on load must not clobber profiles saved concurrently, and must
// not deadlock against writers holding the lock.
func TestLoadProfileStore_LegacyMigrationRacesWithSaves(t *testing.T) {
	path := isolatedCredsPath(t)
	writeRaw(t, path, `{"auth_type":"api_token","email":"me@example.com","api_token":"tok"}`, 0o600)

	const n = 10
	names := []string{"default"}
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := range n {
		name := fmt.Sprintf("p%d", i)
		names = append(names, name)
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs <- SaveProfile(&Credentials{ProfileName: name, AuthType: AuthTypeAPIToken})
		}()
		go func() {
			defer wg.Done()
			_, err := LoadProfileStore()
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertAllProfiles(t, path, names)
}
