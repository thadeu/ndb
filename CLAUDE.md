# ndb — rules for Claude

## Git — no Claude attribution

These rules apply to every session: local, web, desktop and the iOS app.

- Never credit Claude in a commit or a PR. Do not add a `Co-Authored-By: Claude` trailer,
  a "Generated with Claude Code" line, or a session link. `.claude/settings.json` sets
  `attribution` to empty texts and `sessionUrl: false`.
- The commit author is always the maintainer (`thadeu`), never `Claude <noreply@anthropic.com>`.
  Before the first commit of a session, run `git config user.name` and `git config user.email`.
  If the name is `Claude` or the email is `noreply@anthropic.com`, set both to the
  identity of the maintainer's own commits (`git log --author=thadeu -1 --format='%an %ae'`).
- Do not rewrite old history to remove past credits.

## Git — workflow

- Fixes and small changes go directly on `main`. Larger features use a branch and a PR;
  the maintainer merges with squash.
- Do not watch CI and do not create tags: the maintainer runs `make tag`.
