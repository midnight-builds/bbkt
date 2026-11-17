package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zach-snell/bbkt/internal/bitbucket"
)

var (
	useOAuth      bool
	authEmail     string
	authWithToken bool
)

var authCmd = &cobra.Command{
	Use:     "auth",
	GroupID: groupAuth,
	Short:   "Authenticate with Bitbucket Cloud",
	Long: `Set up credentials for accessing Bitbucket Cloud.

By default, prompts for an Atlassian API token (Basic Auth). Create
one at:
  https://id.atlassian.com/manage-profile/security/api-tokens

Use the "Create API token with scopes" button — Bitbucket REST API
requires scoped tokens since Sep 2025. You'll be prompted for your
Atlassian email (used as the auth "username") and the token.

For an OAuth 2.0 browser flow (requires a workspace OAuth consumer),
use --oauth.

Credentials are written to ~/.config/bbkt/credentials.json (0600).
Pass --profile to save under a named profile (e.g. "work") so you
can switch with 'bbkt --profile work ...' or 'bbkt profile use work'.`,
	Example: `  bbkt auth                          # API token, saved to "default" profile
  bbkt auth --profile work           # API token, saved to "work" profile
  bbkt auth --oauth                  # OAuth 2.0 browser flow
  printf '%s\n' "$TOKEN" | bbkt auth --profile ci --email you@example.com --with-token`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Profile name comes from the persistent --profile flag (root.go);
		// default to "default" when saving creds so we always write somewhere.
		profile, _ := cmd.Flags().GetString("profile")
		if profile == "" {
			profile = "default"
		}
		if useOAuth {
			return runOAuthLogin(profile)
		}
		if err := bitbucket.APITokenLogin(bitbucket.APITokenLoginOptions{
			Profile:        profile,
			Email:          authEmail,
			TokenFromStdin: authWithToken,
		}); err != nil {
			return fmt.Errorf("auth failed: %w", err)
		}
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:     "status",
	GroupID: groupAuth,
	Short:   "Show current authentication status",
	RunE: func(cmd *cobra.Command, args []string) error {
		runStatus()
		return nil
	},
}

var logoutCmd = &cobra.Command{
	Use:     "logout",
	GroupID: groupAuth,
	Short:   "Log out and remove stored credentials",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLogout(cmd)
	},
}

func init() {
	RootCmd.AddCommand(authCmd)
	RootCmd.AddCommand(statusCmd)
	RootCmd.AddCommand(logoutCmd)

	authCmd.Flags().BoolVar(&useOAuth, "oauth", false, "Authenticate via OAuth 2.0 (opens browser)")
	authCmd.Flags().StringVar(&authEmail, "email", "", "Atlassian account email (skips the email prompt)")
	authCmd.Flags().BoolVar(&authWithToken, "with-token", false, "Read the API token from stdin, no prompts (needs --email); for scripts and CI")
	authCmd.MarkFlagsMutuallyExclusive("oauth", "with-token")
	// Note: --profile is inherited from RootCmd as a persistent flag and read in RunE.

	// `bbkt auth status` and `bbkt auth logout` are natural things to type;
	// without aliases they silently fall through to the interactive login.
	authCmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show current authentication status (alias for `bbkt status`)",
		RunE: func(cmd *cobra.Command, args []string) error {
			runStatus()
			return nil
		},
	})
	authCmd.AddCommand(&cobra.Command{
		Use:   "logout",
		Short: "Log out and remove stored credentials (alias for `bbkt logout`)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogout(cmd)
		},
	})
}

func runOAuthLogin(profile string) error {
	clientID := os.Getenv("BITBUCKET_OAUTH_CLIENT_ID")
	clientSecret := os.Getenv("BITBUCKET_OAUTH_CLIENT_SECRET")

	if clientID == "" || clientSecret == "" {
		// Print the long-form setup instructions to stderr — these are
		// onboarding guidance, not the error itself.
		fmt.Fprintf(os.Stderr, "Create an OAuth consumer at:\n")
		fmt.Fprintf(os.Stderr, "  Bitbucket > Workspace Settings > OAuth consumers > Add consumer\n")
		fmt.Fprintf(os.Stderr, "  Check \"This is a private consumer\" (required for refresh tokens)\n")
		fmt.Fprintf(os.Stderr, "  Callback URL: http://localhost:%d/callback\n", bitbucket.DefaultOAuthCallbackPort)
		fmt.Fprintf(os.Stderr, "    (override the port with BBKT_OAUTH_CALLBACK_PORT if %d is in use)\n", bitbucket.DefaultOAuthCallbackPort)
		fmt.Fprintf(os.Stderr, "  Scopes: repository, repository:write, pullrequest, pullrequest:write,\n")
		fmt.Fprintf(os.Stderr, "          pipeline, pipeline:write, account\n\n")
		return fmt.Errorf("OAuth credentials required: set BITBUCKET_OAUTH_CLIENT_ID and BITBUCKET_OAUTH_CLIENT_SECRET")
	}

	if err := bitbucket.OAuthLogin(clientID, clientSecret, profile); err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	return nil
}

// statusSource describes which credential source is in effect.
type statusSource struct {
	Kind        string // "env-token", "env-basic", "stored", "none"
	Fingerprint string // last 4 of the env token, when applicable
	Shadowed    bool   // a stored profile exists but env vars take precedence
}

// selectStatusSource mirrors the precedence used by getClient and the MCP
// server: BITBUCKET_ACCESS_TOKEN, then BITBUCKET_USERNAME + BITBUCKET_API_TOKEN,
// then the stored profile.
func selectStatusSource(getenv func(string) string, haveStored bool) statusSource {
	token := getenv("BITBUCKET_ACCESS_TOKEN")
	user := getenv("BITBUCKET_USERNAME")
	pass := getenv("BITBUCKET_API_TOKEN")
	switch {
	case token != "":
		return statusSource{Kind: "env-token", Fingerprint: tokenTail(token), Shadowed: haveStored}
	case user != "" && pass != "":
		return statusSource{Kind: "env-basic", Fingerprint: tokenTail(pass), Shadowed: haveStored}
	case haveStored:
		return statusSource{Kind: "stored"}
	}
	return statusSource{Kind: "none"}
}

func tokenTail(t string) string {
	if len(t) < 16 {
		return "****"
	}
	return "..." + t[len(t)-4:]
}

func runStatus() {
	creds, err := bitbucket.LoadCredentials()
	src := selectStatusSource(os.Getenv, err == nil)

	switch src.Kind {
	case "env-token":
		fmt.Println("Authenticated via BITBUCKET_ACCESS_TOKEN environment variable")
		fmt.Printf("  Token:   %s\n", src.Fingerprint)
	case "env-basic":
		fmt.Println("Authenticated via BITBUCKET_USERNAME + BITBUCKET_API_TOKEN environment variables")
		fmt.Printf("  User:    %s\n", os.Getenv("BITBUCKET_USERNAME"))
		fmt.Printf("  Token:   %s\n", src.Fingerprint)
	case "none":
		fmt.Println("Not authenticated. Run: bbkt auth")
		return
	}
	if src.Shadowed {
		note := "a stored profile exists"
		if err == nil && creds.ProfileName != "" {
			note = fmt.Sprintf("stored profile %q exists", creds.ProfileName)
		}
		fmt.Printf("  Note:    %s but is shadowed by the environment variables above\n", note)
		return
	}
	if src.Kind != "stored" {
		return
	}

	path, _ := bitbucket.CredentialsPath()

	switch {
	case creds.IsAPIToken():
		fmt.Println("Authenticated via API Token (Basic Auth)")
		fmt.Printf("  Profile: %s\n", creds.ProfileName)
		fmt.Printf("  Email:   %s\n", creds.Email)
		fmt.Printf("  Token:   %s\n", tokenTail(creds.APIToken))
		if creds.Scopes != "" {
			fmt.Printf("  Scopes:  %s\n", creds.Scopes)
		}
		fmt.Printf("  Stored:  %s\n", creds.CreatedAt.Format("2006-01-02 15:04:05"))
		fmt.Printf("  File:    %s\n", path)

	case creds.IsOAuth():
		fmt.Println("Authenticated via OAuth 2.0 (Bearer Auth)")
		fmt.Printf("  Scopes:  %s\n", creds.Scopes)
		fmt.Printf("  Stored:  %s\n", creds.CreatedAt.Format("2006-01-02 15:04:05"))
		if creds.IsExpired() {
			fmt.Println("  Status:  expired (will auto-refresh)")
		} else {
			fmt.Println("  Status:  valid")
		}
		fmt.Printf("  File:    %s\n", path)
	}
}

func runLogout(cmd *cobra.Command) error {
	if err := bitbucket.RemoveCredentials(); err != nil {
		return fmt.Errorf("removing credentials: %w", err)
	}
	PrintOrJSON(cmd, map[string]any{"logged_out": true}, func() {
		fmt.Println("Logged out. Credentials removed.")
	})
	return nil
}
