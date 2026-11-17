# Security Policy

## Supported versions

Only the latest release of bbkt receives security fixes. Please upgrade
before reporting an issue, and include the output of `bbkt --version`.

## Reporting a vulnerability

Please do not open a public issue for security problems. Report privately
through GitHub private vulnerability reporting: open the repository's
**Security** tab and choose **Report a vulnerability**.

Include a description, reproduction steps, the affected version, and the
impact you expect. You can expect an acknowledgement within 7 days and a
status update or fix timeline within 30 days. Reporters are credited in the
release notes unless they prefer otherwise.

## Scope

In scope:

- The `bbkt` CLI
- The MCP server (`bbkt mcp`)
- The installer (`install.sh`) and release artifacts

Out of scope: vulnerabilities in Bitbucket Cloud itself, in third-party MCP
clients, or issues that require an already-compromised local account.

## Note on regulatory status

bbkt is a non-commercial, open-source project maintained by an individual;
the maintainer is tracking the EU Cyber Resilience Act for any future
obligations.
