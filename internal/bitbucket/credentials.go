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
			return err
		}
		return writeProfileStore(path, store)
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
		return nil, fmt.Errorf("override profile '%s' not found in store", override)
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

// APITokenLogin prompts the user for email + API Token and stores them.
func APITokenLogin(profileName string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("Atlassian API Token Authentication (Basic Auth)")
	fmt.Println("=============================================")
	fmt.Println()
	fmt.Println("Create an API Token at:")
	fmt.Println("  https://id.atlassian.com/manage-profile/security/api-tokens")
	fmt.Println()
	fmt.Println("IMPORTANT: Use the \"Create API token with scopes\" button (NOT the")
	fmt.Println("plain \"Create API token\" button). Bitbucket Cloud REST API requires")
	fmt.Println("scoped tokens since Sep 2025 — unscoped tokens authenticate to")
	fmt.Println("Atlassian but are rejected by Bitbucket's auth layer.")
	fmt.Println()
	fmt.Println("Recommended Scopes:")
	fmt.Println("  read:account, read:user:bitbucket, read:workspace:bitbucket,")
	fmt.Println("  read:repository:bitbucket, write:repository:bitbucket,")
	fmt.Println("  read:pullrequest:bitbucket, write:pullrequest:bitbucket,")
	fmt.Println("  read:pipeline:bitbucket, write:pipeline:bitbucket")
	fmt.Println()
	fmt.Println("Tools are dynamically disabled if your token omits specific scopes.")
	fmt.Println("To explicitly deny the MCP server access to tools despite having full scopes, use:")
	fmt.Println("  export BITBUCKET_DISABLED_TOOLS=\"manage_repositories,manage_refs\"")
	fmt.Println()

	fmt.Print("Atlassian email: ")
	email, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading email: %w", err)
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return fmt.Errorf("email is required")
	}

	fmt.Print("API Token: ")
	token, err := readSecretLine(reader, os.Stdin)
	if err != nil {
		return fmt.Errorf("reading API Token: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("API Token is required")
	}

	// Verify credentials by hitting the user API
	fmt.Println("\nVerifying credentials...")
	client := NewClient(email, token, "")
	userData, scopesStr, err := client.GetWithScopes("/user")
	if err != nil {
		var authErr *AuthError
		switch {
		case errors.As(err, &authErr) && authErr.IsClassicTokenRejection():
			return fmt.Errorf("%s\n\n(underlying error: %w)", classicTokenHelp, err)
		case errors.As(err, &authErr) && authErr.StatusCode == 403:
			// 403 with X-OAuth-Scopes still populates scopesStr — proceed with
			// the scopes we got, just skip the user-name lookup.
			fmt.Println("\nToken verified (403 on /user — token has Bitbucket scopes but lacks read:account; user display name unavailable).")
		default:
			return fmt.Errorf("credential verification failed: %s\n\n(underlying error: %w)\n\nCheck that your email and API Token are correct", describeAuthFailure(err), err)
		}
	}

	if userData != nil {
		var user struct {
			DisplayName string `json:"display_name"`
			Nickname    string `json:"nickname"`
		}
		if jsonErr := json.Unmarshal(userData, &user); jsonErr == nil {
			name := user.DisplayName
			if name == "" {
				name = user.Nickname
			}
			fmt.Printf("Authenticated as: %s\n", name)
		}
	}

	creds := &Credentials{
		ProfileName:          profileName,
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

	path, _ := CredentialsPath()
	fmt.Printf("\nCredentials saved to: %s\n", path)
	fmt.Println("You can now use the Bitbucket MCP server.")
	return nil
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
