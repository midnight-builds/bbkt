# bbkt CLI Demo
#
# Record with: scripts/demo-record.sh   (runs the leak preflight, then vhs)
#
# Runs against the throwaway "bbkt-test" demo workspace only. The record
# script copies just the "demo" profile into an empty temp HOME
# ($BBKT_DEMO_HOME), so no other stored profile can be read. Run directly
# without that variable and the tape exits instead of using a real login.
Output demo.gif

Set Shell zsh
Set FontSize 15
Set Width 1180
Set Height 720
Set Padding 20
Set Theme "Catppuccin Mocha"
Set TypingSpeed 40ms
Set WindowBar Colorful

Hide
Type `[ -n "$BBKT_DEMO_HOME" ] || exit 1; [ -z "$BBKT_DEMO_PATH" ] || export PATH="$BBKT_DEMO_PATH:$PATH"; export HOME="$BBKT_DEMO_HOME"; unset BBKT_PROFILE BBKT_WORKSPACE BBKT_REPO BITBUCKET_USERNAME BITBUCKET_API_TOKEN BITBUCKET_ACCESS_TOKEN BITBUCKET_OAUTH_CLIENT_ID BITBUCKET_OAUTH_CLIENT_SECRET; cd "$HOME"; unsetopt PROMPT_SP 2>/dev/null; PS1='$ '; clear`
Enter
Show

Sleep 500ms

Type "bbkt workspaces list"
Sleep 400ms
Enter
Sleep 3s

Type "bbkt prs list -R bbkt-test/bbkt"
Sleep 400ms
Enter
Sleep 4.5s

Hide
Type "clear"
Enter
Show

Type "bbkt prs comments list -R bbkt-test/bbkt 2"
Sleep 400ms
Enter
Sleep 5s

Hide
Type "clear"
Enter
Show

Type "bbkt source read -R bbkt-test/bbkt internal/bitbucket/retry.go --ref feat/retry-rate-limits"
Sleep 400ms
Enter
Sleep 5s
