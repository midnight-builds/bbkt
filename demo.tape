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
Set FontSize 16
Set Width 950
Set Height 600
Set Padding 20
Set Theme "Catppuccin Mocha"
Set TypingSpeed 40ms
Set WindowBar Colorful

Env PATH "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"

Hide
Type `[ -n "$BBKT_DEMO_HOME" ] || exit 1; export HOME="$BBKT_DEMO_HOME"; unset BBKT_PROFILE BBKT_WORKSPACE BBKT_REPO BITBUCKET_USERNAME BITBUCKET_API_TOKEN BITBUCKET_ACCESS_TOKEN BITBUCKET_OAUTH_CLIENT_ID BITBUCKET_OAUTH_CLIENT_SECRET; cd "$HOME"; PS1='$ '; clear`
Enter
Show

Sleep 500ms

Type "bbkt workspaces list"
Sleep 400ms
Enter
Sleep 2s

Type "bbkt repos list bbkt-test"
Sleep 400ms
Enter
Sleep 2s

Type "bbkt source tree bbkt-test bbkt"
Sleep 400ms
Enter
Sleep 2s

Type "bbkt prs list -R bbkt-test/bbkt --state ALL"
Sleep 400ms
Enter
Sleep 2s

Type "bbkt repos get bbkt-test bbkt"
Sleep 400ms
Enter
Sleep 3s
