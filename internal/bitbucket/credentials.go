package bitbucket

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
)

// AuthType distinguishes between credential storage methods.
type AuthType string

const (
	AuthTypeAPIToken AuthType = "api_token"
	AuthTypeOAuth    AuthType = "oauth"
)

// DisabledToolsExample is the BITBUCKET_DISABLED_TOOLS value shown during
// onboarding. Every entry must be a registered MCP tool name, or the server
// refuses to start; internal/mcp tests enforce this.
const DisabledToolsExample = "manage_repositories,manage_pipelines"

// ProfileStore holds multiple authentication profiles
type ProfileStore struct {
	ActiveProfile string                  `json:"active_profile"`
	Profiles      map[string]*Credentials `json:"profiles"`
}

// Credentials holds persisted authentication data.
// Supports both API Token (Basic Auth) and OAuth 2.0 (Bearer Auth).
type Credentials struct {
	ProfileName string    `json:"-"`
	AuthType    AuthType  `json:"auth_type"`
	CreatedAt   time.Time `json:"created_at"`

	// API Token fields (auth_type=api_token)
	Email    string `json:"email,omitempty"`
	APIToken string `json:"api_token,omitempty"`

	// OAuth fields (auth_type=oauth)
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Scopes       string `json:"scopes,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`

	// Derived cache data
	AccessibleWorkspaces []string `json:"accessible_workspaces,omitempty"`
}

// IsOAuth returns true if these credentials use OAuth.
func (c *Credentials) IsOAuth() bool {
	return c.AuthType == AuthTypeOAuth
}

// IsAPIToken returns true if these credentials use an API token.
func (c *Credentials) IsAPIToken() bool {
	return c.AuthType == AuthTypeAPIToken
}

// IsExpired returns true if OAuth access token is expired (with 5 min buffer).
func (c *Credentials) IsExpired() bool {
	if !c.IsOAuth() {
		return false
	}
	expiry := c.CreatedAt.Add(time.Duration(c.ExpiresIn) * time.Second)
	return time.Now().After(expiry.Add(-5 * time.Minute))
}

// CredentialsPath returns the path to the credentials file.
func CredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home dir: %w", err)
	}
	return filepath.Join(home, ".config", "bbkt", "credentials.json"), nil
}

// SaveProfileStore persists the entire ProfileStore to disk, replacing
// whatever is there. It is a blind overwrite: changes another process made
// since store was loaded are lost. To modify the store, use
// UpdateProfileStore, which reads and writes under the credentials lock.
func SaveProfileStore(store *ProfileStore) error {
	path, err := CredentialsPath()
	if err != nil {
		return err
	}

	return withCredentialsLock(path, func() error {
		return writeProfileStore(path, store)
	})
}

// SaveProfile saves a single credential under its ProfileName.
// If it is the first profile being saved, it is automatically marked as active.
func SaveProfile(creds *Credentials) error {
	if creds.ProfileName == "" {
		creds.ProfileName = "default"
	}
	return UpdateProfileStore(func(store *ProfileStore) error {
		store.Profiles[creds.ProfileName] = creds
		if store.ActiveProfile == "" {
			store.ActiveProfile = creds.ProfileName
		}
		return nil
	})
}

// UpdateProfileStore performs a locked read-modify-write of the profile
// store: it loads the current store (or an empty one if no credentials file
// exists yet), applies fn, and writes the result back atomically. Any load
// error other than "file does not exist" is returned without writing, so an
// unreadable or corrupt file is never silently replaced.
func UpdateProfileStore(fn func(*ProfileStore) error) error {
	path, err := CredentialsPath()
	if err != nil {
		return err
	}
	return withCredentialsLock(path, func() error {
		store, _, err := readProfileStore(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			store = &ProfileStore{Profiles: make(map[string]*Credentials)}
		case err != nil:
			return fmt.Errorf("refusing to overwrite existing credentials: %w", err)
		}
		if err := fn(store); err != nil {
			if errors.Is(err, errSkipWrite) {
				return nil
			}
			return err
		}
		return writeProfileStore(path, store)
	})
}

// errSkipWrite, returned from an UpdateProfileStore callback, aborts the
// update without writing and without reporting an error.
var errSkipWrite = errors.New("skip credentials write")

// persistRefreshedToken writes a refreshed OAuth token back to its profile.
// Unlike SaveProfile it merges only the token fields into the profile as it
// is on disk now, so a long-lived process holding an older copy of creds
// cannot revert concurrent changes. If the profile is gone (logout) or was
// replaced by a different login, nothing is written.
func persistRefreshedToken(creds *Credentials) error {
	return UpdateProfileStore(func(store *ProfileStore) error {
		cur, ok := store.Profiles[creds.ProfileName]
		if !ok || cur == nil || !cur.IsOAuth() || cur.ClientID != creds.ClientID {
			return errSkipWrite
		}
		cur.AccessToken = creds.AccessToken
		cur.RefreshToken = creds.RefreshToken
		cur.TokenType = creds.TokenType
		cur.ExpiresIn = creds.ExpiresIn
		cur.Scopes = creds.Scopes
		cur.CreatedAt = creds.CreatedAt
		return nil
	})
}

// LoadProfileStore reads the persisted profile store from disk.
// It automatically migrates older single-credential files to the new ProfileStore format.
func LoadProfileStore() (*ProfileStore, error) {
	path, err := CredentialsPath()
	if err != nil {
		return nil, err
	}

	store, legacy, err := readProfileStore(path)
	if err != nil {
		return nil, err
	}

	if legacy {
		// Save the migrated format silently back to disk. Re-read under the
		// lock so a concurrent writer's newer contents are never clobbered.
		_ = withCredentialsLock(path, func() error {
			cur, stillLegacy, err := readProfileStore(path)
			if err != nil {
				return err
			}
			if !stillLegacy {
				store = cur
				return nil
			}
			return writeProfileStore(path, cur)
		})
	}

	return store, nil
}

// readProfileStore reads and parses the credentials file without taking the
// lock. legacy reports whether the file was in the old single-credential
// format (the returned store is already migrated in memory).
func readProfileStore(path string) (store *ProfileStore, legacy bool, err error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the fixed bbkt credentials location
	if err != nil {
		return nil, false, fmt.Errorf("reading credentials file: %w", err)
	}
	warnLoosePermissions(path)

	// Check if this is the old scalar Credentials format or the new ProfileStore format
	var check map[string]interface{}
	if err := json.Unmarshal(data, &check); err != nil {
		return nil, false, fmt.Errorf("parsing credentials file: %w", err)
	}

	if _, ok := check["profiles"]; !ok {
		// Old format migration
		var oldCreds Credentials
		if err := json.Unmarshal(data, &oldCreds); err != nil {
			return nil, false, fmt.Errorf("parsing legacy credentials: %w", err)
		}
		oldCreds.ProfileName = "default"

		return &ProfileStore{
			ActiveProfile: "default",
			Profiles: map[string]*Credentials{
				"default": &oldCreds,
			},
		}, true, nil
	}

	// New format
	var s ProfileStore
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, false, fmt.Errorf("parsing profile store: %w", err)
	}

	if s.Profiles == nil {
		s.Profiles = make(map[string]*Credentials)
	}

	for name, creds := range s.Profiles {
		creds.ProfileName = name
	}

	return &s, false, nil
}

// writeProfileStore atomically replaces the credentials file with store.
// The caller must hold the credentials lock. The data is written to a temp
// file in the same directory, fsynced and renamed into place, so readers
// never observe a partially written file. Afterwards the file is forced to
// 0600 and its directory to 0700, since create-time modes do not tighten an
// existing file. A symlinked credentials file is refused rather than followed.
func writeProfileStore(path string, store *ProfileStore) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write credentials: %s is a symlink; replace it with a regular file", path)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("checking credentials file: %w", err)
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling profile store: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp credentials file: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("setting temp credentials file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("writing credentials file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing credentials file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing credentials file: %w", err)
	}
	if err := replaceFile(tmpName, path); err != nil {
		return fmt.Errorf("replacing credentials file: %w", err)
	}
	committed = true

	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("setting credentials file mode: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("setting config dir mode: %w", err)
	}
	syncDir(dir)
	return nil
}

// syncDir best-effort fsyncs a directory so a rename into it is durable.
// Directories cannot be opened for sync on Windows, so it is skipped there.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	d, err := os.Open(dir) // #nosec G304 -- dir is the bbkt config directory
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// withCredentialsLock runs fn while holding an exclusive advisory lock on a
// sibling lock file, serializing credential writers across goroutines and
// processes (e.g. several MCP servers refreshing OAuth tokens at once).
func withCredentialsLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	lf, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- fixed sibling of the credentials file
	if err != nil {
		return fmt.Errorf("opening credentials lock: %w", err)
	}
	defer func() { _ = lf.Close() }()

	if err := lockFile(lf); err != nil {
		return fmt.Errorf("locking credentials file: %w", err)
	}
	defer func() { _ = unlockFile(lf) }()

	return fn()
}

// stderrOut is where credential-file warnings are printed; tests override it.
var stderrOut io.Writer = os.Stderr

// warnedPaths records credential files already warned about, so a long-lived
// MCP server does not repeat the warning on every load.
var warnedPaths sync.Map

// warnLoosePermissions prints a one-line warning if the credentials file is
// readable or writable by group/others. Windows has no POSIX modes, so it is
// skipped there.
func warnLoosePermissions(path string) {
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm()&0o077 == 0 {
		return
	}
	if _, already := warnedPaths.LoadOrStore(path, true); already {
		return
	}
	_, _ = fmt.Fprintf(stderrOut, "warning: %s has mode %#o and contains secrets; run: chmod 600 %s\n", path, fi.Mode().Perm(), path)
}

// LoadCredentials gets the active credential profile based on context.
// Priority:
//  1. BBKT_PROFILE environment variable (or --profile CLI flag equivalent)
//  2. A profile whose AccessibleWorkspaces contains the workspace of the
//     current directory's Bitbucket git remote
//  3. The configured 'ActiveProfile' in credentials.json
func LoadCredentials() (*Credentials, error) {
	store, err := LoadProfileStore()
	if err != nil {
		return nil, err
	}

	// 1. Explicit Override
	if override := os.Getenv("BBKT_PROFILE"); override != "" {
		if creds, ok := store.Profiles[override]; ok {
			return creds, nil
		}
		return nil, fmt.Errorf("no profile named %q (see 'bbkt profile list', or create it with 'bbkt auth --profile %s')", override, override)
	}

	// 2. Magic Context Inference
	if ws, _, err := GetLocalRepoInfo(); err == nil && ws != "" {
		for _, creds := range store.Profiles {
			for _, accessible := range creds.AccessibleWorkspaces {
				if strings.EqualFold(accessible, ws) {
					return creds, nil
				}
			}
		}
	}

	// 3. Fallback to Active Profile
	creds, ok := store.Profiles[store.ActiveProfile]
	if !ok {
		if len(store.Profiles) > 0 {
			// Panic recovery: just use whatever we have
			for _, first := range store.Profiles {
				return first, nil
			}
		}
		return nil, fmt.Errorf("active profile '%s' not found in store, and no other profiles found", store.ActiveProfile)
	}

	return creds, nil
}

// RemoveCredentials deletes the stored credentials file.
func RemoveCredentials() error {
	path, err := CredentialsPath()
	if err != nil {
		return err
	}

	return withCredentialsLock(path, func() error {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	})
}

// ReadOnlyScopes are enough for every read action (and for a demo or an
// agent that should only look). WriteScopes add creating, merging, commenting
// and triggering. Shared by the login guide and the auth error help so the
// two never drift apart.
var (
	ReadOnlyScopes = []string{
		"read:account", "read:user:bitbucket", "read:workspace:bitbucket",
		"read:repository:bitbucket", "read:pullrequest:bitbucket", "read:pipeline:bitbucket",
	}
	WriteScopes = []string{
		"write:repository:bitbucket", "write:pullrequest:bitbucket", "write:pipeline:bitbucket",
	}
)

// scopeLines formats scopes as indented, comma-separated lines of at most
// three entries each.
func scopeLines(scopes []string) string {
	var b strings.Builder
	for i := 0; i < len(scopes); i += 3 {
		end := min(i+3, len(scopes))
		b.WriteString("    " + strings.Join(scopes[i:end], ", "))
		if end < len(scopes) {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// APITokenLoginOptions configures APITokenLogin.
type APITokenLoginOptions struct {
	Profile string
	// Email is the Atlassian account email. When empty it is prompted for.
	Email string
	// TokenFromStdin reads the token from the first line of stdin with no
	// prompts or guide, for scripts and CI. Email must be set.
	TokenFromStdin bool
}

// errNoTerminal explains how to log in when there is no keyboard to type on.
func errNoTerminal(profile string) error {
	return fmt.Errorf(`bbkt auth needs an interactive terminal to prompt for your email and token.
Run it in a terminal, or pipe the token in:
  printf '%%s\n' "$TOKEN" | bbkt auth --profile %s --email you@example.com --with-token`, profile)
}

// APITokenLogin verifies an Atlassian email + scoped API token against
// Bitbucket and stores them under opts.Profile.
func APITokenLogin(opts APITokenLoginOptions) error {
	if opts.Profile == "" {
		opts.Profile = "default"
	}
	reader := bufio.NewReader(os.Stdin)
	interactive := !opts.TokenFromStdin

	if opts.TokenFromStdin && opts.Email == "" {
		return fmt.Errorf("--with-token needs --email (the Atlassian account email that owns the token)")
	}
	if interactive && !term.IsTerminal(os.Stdin.Fd()) {
		return errNoTerminal(opts.Profile)
	}

	if interactive {
		fmt.Println()
		fmt.Printf("Bitbucket login for profile %q (Atlassian API token)\n", opts.Profile)
		fmt.Println("=============================================")
		if store, err := LoadProfileStore(); err == nil {
			if _, exists := store.Profiles[opts.Profile]; exists {
				fmt.Printf("\nNote: profile %q already exists and will be replaced.\n", opts.Profile)
			}
		}
		fmt.Println()
		fmt.Println("1. Open https://id.atlassian.com/manage-profile/security/api-tokens")
		fmt.Println("   signed in as the account you want this profile to use.")
		fmt.Println("2. Click \"Create API token with scopes\" (not the plain \"Create API")
		fmt.Println("   token\" button: Bitbucket rejects unscoped tokens), choose")
		fmt.Println("   Bitbucket, and pick scopes:")
		fmt.Println()
		fmt.Println("   Read-only (enough to browse; recommended for AI agents and demos):")
		fmt.Print(scopeLines(ReadOnlyScopes))
		fmt.Println("   To also create/merge PRs, comment, push files or run pipelines, add:")
		fmt.Print(scopeLines(WriteScopes))
		fmt.Println()
		fmt.Println("   bbkt hides tools the token's scopes can't use.")
		fmt.Println("3. Copy the token, then answer the prompts below.")
		fmt.Println()
	}

	email := strings.TrimSpace(opts.Email)
	if email == "" {
		fmt.Print("Atlassian email: ")
		line, err := reader.ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && strings.TrimSpace(line) != "") {
			return fmt.Errorf("reading email: %w", err)
		}
		email = strings.TrimSpace(line)
		if email == "" {
			return fmt.Errorf("email is required")
		}
	}

	var token string
	if opts.TokenFromStdin {
		line, err := reader.ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && strings.TrimSpace(line) != "") {
			return fmt.Errorf("reading token from stdin: %w", err)
		}
		token = line
	} else {
		fmt.Print("API token (input hidden): ")
		t, err := readSecretLine(reader, os.Stdin)
		if err != nil {
			return fmt.Errorf("reading API token: %w", err)
		}
		token = t
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("API token is required")
	}

	// Verify credentials by hitting the user API
	if interactive {
		fmt.Println("\nVerifying with Bitbucket...")
	}
	client := NewClient(email, token, "")
	displayName := ""
	userData, scopesStr, err := client.GetWithScopes("/user")
	if err != nil {
		var authErr *AuthError
		switch {
		case errors.As(err, &authErr) && authErr.IsClassicTokenRejection():
			return fmt.Errorf("%s\n\n(underlying error: %w)", classicTokenHelp, err)
		case errors.As(err, &authErr) && authErr.StatusCode == 403:
			// 403 with X-OAuth-Scopes still populates scopesStr: proceed with
			// the scopes we got, just skip the user-name lookup.
			displayName = "(name unavailable: token lacks read:account)"
		default:
			return fmt.Errorf("credential verification failed: %s\n\n(underlying error: %w)\n\nCheck that your email and API token are correct", describeAuthFailure(err), err)
		}
	}
	if userData != nil {
		var user struct {
			DisplayName string `json:"display_name"`
			Nickname    string `json:"nickname"`
		}
		if jsonErr := json.Unmarshal(userData, &user); jsonErr == nil {
			displayName = user.DisplayName
			if displayName == "" {
				displayName = user.Nickname
			}
		}
	}

	creds := &Credentials{
		ProfileName:          opts.Profile,
		AuthType:             AuthTypeAPIToken,
		CreatedAt:            time.Now(),
		Email:                email,
		APIToken:             token,
		Scopes:               scopesStr,
		AccessibleWorkspaces: FetchAccessibleWorkspaces(client),
	}
	if err := SaveProfile(creds); err != nil {
		return fmt.Errorf("saving profile: %w", err)
	}

	active := ""
	if store, err := LoadProfileStore(); err == nil {
		active = store.ActiveProfile
	}
	printLoginSummary(creds, displayName, active)
	return nil
}

// printLoginSummary tells the user what was saved and how to use it.
func printLoginSummary(creds *Credentials, displayName, active string) {
	fmt.Printf("\nSaved profile %q", creds.ProfileName)
	if displayName != "" {
		fmt.Printf(" (%s)", displayName)
	}
	fmt.Println()
	if ws := creds.AccessibleWorkspaces; len(ws) > 0 {
		fmt.Printf("  Workspaces: %s\n", strings.Join(ws, ", "))
	} else {
		fmt.Println("  Workspaces: none visible (token may lack read:workspace:bitbucket)")
	}
	access := "read-only"
	if strings.Contains(creds.Scopes, "write:") {
		access = "read + write"
	}
	if creds.Scopes == "" {
		access = "unknown"
	}
	fmt.Printf("  Access:     %s\n", access)
	if path, err := CredentialsPath(); err == nil {
		fmt.Printf("  Stored in:  %s\n", path)
	}
	fmt.Println()
	if active == creds.ProfileName {
		fmt.Println("This is your active profile. Try: bbkt workspaces list")
		return
	}
	fmt.Printf("Your active profile is still %q. To use this one:\n", active)
	fmt.Printf("  bbkt --profile %s workspaces list    # one command\n", creds.ProfileName)
	fmt.Printf("  bbkt profile use %s                  # make it the default\n", creds.ProfileName)
	fmt.Printf("  BBKT_PROFILE=%s                      # in an MCP server's env\n", creds.ProfileName)
}

// readSecretLine reads one line of secret input. When in is a terminal (and
// nothing is already buffered from it) the input is read with echo disabled;
// otherwise, e.g. for pipes in scripts and tests, it is read as a plain line
// from reader.
func readSecretLine(reader *bufio.Reader, in *os.File) (string, error) {
	if reader.Buffered() == 0 && term.IsTerminal(in.Fd()) {
		restore := restoreTerminalOnSignal(in.Fd())
		b, err := term.ReadPassword(in.Fd())
		restore()
		fmt.Println()
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return reader.ReadString('\n')
}

// restoreTerminalOnSignal guards a no-echo read: if the process is
// interrupted (Ctrl-C) or terminated while echo is off, the terminal state is
// restored before exiting, so the user's shell is not left without echo.
// The returned func stops the guard once the read has finished.
func restoreTerminalOnSignal(fd uintptr) (stop func()) {
	state, err := term.GetState(fd)
	if err != nil {
		return func() {}
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			_ = term.Restore(fd, state)
			fmt.Println()
			os.Exit(130)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(sigCh)
		close(done)
	}
}

// FetchAccessibleWorkspaces retrieves all workspace slugs the client can access.
func FetchAccessibleWorkspaces(client *Client) []string {
	var slugs []string
	res, err := client.ListWorkspaces(ListWorkspacesArgs{Pagelen: 100})
	if err == nil && res != nil {
		for _, w := range res.Values {
			slugs = append(slugs, w.Slug)
		}
	}
	return slugs
}
