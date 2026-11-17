# bbkt CLI Demo
#
# Record with: scripts/demo-record.sh   (runs the leak preflight, then vhs)
#
# Runs against the throwaway "bbkt-test" demo workspace only. Credentials for
# that demo account come from $BBKT_DEMO_ENV (default ~/.config/bbkt-demo/env,
# never committed) and HOME is pointed at an empty temp dir, so no real stored
# profile can be read. If the env file is missing, commands fail instead of
# falling back to a real login.
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
Type `source "${BBKT_DEMO_ENV:-$HOME/.config/bbkt-demo/env}" || exit 1; export HOME="$(mktemp -d)"; unset BBKT_PROFILE BBKT_WORKSPACE BBKT_REPO; cd "$HOME"; PS1='$ '; clear`
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
