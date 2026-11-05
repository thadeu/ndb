# Security policy

## Supported versions

ndb is pre-release (`v0.x`). Only the latest release gets security fixes.

## Report a problem

Please do not open a public issue for a security problem.

Use [GitHub private vulnerability reporting](https://github.com/thadeu/ndb/security/advisories/new) for this repository. Say what you found, how to reproduce it, and which version you used.

You can expect an answer in a few days. We fix the problem, publish a release, and then say what happened. We credit you if you want.

## Know before you deploy

- The TCP listener has no authentication yet. Bind it to a private address only (for example a WireGuard address), behind a firewall.
- The unix socket is protected by file permissions. Use `-unix-mode 0660` and a shared group.
- `data_dir` holds all data in clear text. Protect it with disk permissions and disk encryption.
