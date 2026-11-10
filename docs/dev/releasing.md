# Releasing

One tag `vX.Y.Z` releases everything. Version 0.1.0 is used in all packages.

| Artifact | Name | Workflow |
|---|---|---|
| Daemon binaries + checksums | GitHub Release | `release.yml` |
| Console binaries | GitHub Release (`ndb-ui-*`) | `release.yml` (after the daemon) |
| Go SDK | tag `sdk/go/vX.Y.Z`, module `github.com/thadeu/ndb/sdk/go` | `release.yml` |
| Go Redis adapter | tag `sdk/go/ndbredis/vX.Y.Z`, module `github.com/thadeu/ndb/sdk/go/ndbredis` (needs the Go SDK of the same release) | `release.yml` |
| Images | `ghcr.io/thadeu/ndb`, `ghcr.io/thadeu/ndb-ui` (two jobs at the same time) | `ghcr.yml` |
| Gems | `ndb` (pushed first), then `ndb-rails` (depends on it) | `publish.yml` (job `gems`) |
| npm | `@thadeu/ndb` | `publish.yml` (job `npm`, at the same time as `gems`) |
| Docs site | Cloudflare Pages (project `ndb-docs`, via wrangler) | `docs.yml` (only when the site changes; run it by hand to show a new version at once) |

**The tests run once, in CI** (`ci.yml`, on every push to `main` and every pull request). `make tag` pushes `main`, waits on this machine for the CI run of the release commit (`gh run watch`, no Actions minutes), and pushes the tag only when it passed. So `release.yml`, `ghcr.yml` and `publish.yml` do not test again; only the console is tested in `release.yml`, as CI does not test it. `NO_WAIT=1 make tag patch` pushes the tag without waiting.

## One-time setup

1. npm: create the scope `@thadeu` if needed. On npmjs.com, open the package `@thadeu/ndb` (for a first publish, use the account settings to add a trusted publisher, or publish once with a token). Add a trusted publisher: GitHub Actions, owner `thadeu`, repository `ndb`, workflow `publish.yml`. Alternative: set the repository secret `NPM_TOKEN`; the workflow uses it when it is set.
2. RubyGems: on rubygems.org, add two pending trusted publishers, one for `ndb` and one for `ndb-rails`. Both use owner `thadeu`, repository `ndb`, workflow `publish.yml`. Alternative: set the repository secret `GEM_HOST_API_KEY`; the workflow uses it when it is set.
3. Cloudflare Pages: create the project `ndb-docs` (or let the first `wrangler pages deploy` create it), attach the custom domain, and add the repo secrets `CLOUDFLARE_API_TOKEN` (Pages: Edit) and `CLOUDFLARE_ACCOUNT_ID`.
4. ghcr: after the first image push, open the package `ndb` under the profile Packages and set visibility to Public. Link it to the repository.
5. Settings, Actions, General: allow workflows to have read and write permission (the Go tag step pushes a tag).

## Versions

- **minor** (1.6.0 → 1.7.0): a feature, an API addition, or a performance or resource change (memory, disk, CPU) of the daemon or an SDK.
- **patch** (1.6.0 → 1.6.1): a bug fix only, with no new behavior.
- **major**: routes or keys change; the CHANGELOG has a migration table.

`make tag minor` or `make tag patch` bumps the SDK versions, the Go SDK version that `sdk/go/ndbredis/go.mod` needs, and the CHANGELOG.

## Per release

1. Update the version: `sdk/ts/package.json`, `sdk/ruby/lib/ndb/version.rb`, `sdk/rails/lib/ndb/rails/version.rb`. The publish workflows fail when a version differs from the tag. When the `ndb` minor version changes, update the `ndb` requirement in `sdk/rails/ndb-rails.gemspec`.
2. Move the `[Unreleased]` entries in `CHANGELOG.md` to the new version with the date.
3. Run the heavy checks on your machine (CI runs `-short`): `make race` or `make test`, and `make bench` (see [development.md](development.md)).
4. `make tag patch|minor|major`: it does steps 1 and 2, commits "Release X.Y.Z", pushes `main`, waits for CI and pushes the tag. By hand: `git tag vX.Y.Z && git push origin vX.Y.Z`, after CI passed on that commit.

## Verify

- Release: `gh release view v0.1.0`. Download one archive, check it with `sha256sum -c checksums.txt`, run `ndb -version` (prints `ndb 0.1.0`).
- Go tags: `git ls-remote --tags origin 'sdk/go/*'` lists `sdk/go/vX.Y.Z` and `sdk/go/ndbredis/vX.Y.Z`; then in a temp module `go get github.com/thadeu/ndb/sdk/go@vX.Y.Z` and `go get github.com/thadeu/ndb/sdk/go/ndbredis@vX.Y.Z`.
- Image: `docker run --rm ghcr.io/thadeu/ndb:0.1.0 -version`.
- npm: `npm view @thadeu/ndb version`; the package page shows the provenance badge.
- Gems: `gem search -r '^ndb$'`, `gem search -r ndb-rails`, or `gem install ndb-rails -v 0.1.0` (it installs `ndb` too).
- Docs: open the `ndb-docs` Cloudflare Pages URL (or its custom domain).

## If a publish fails

Workflows never run two at a time. CI, docs and the images cancel the run in progress when a newer push or tag comes. Release and publish finish the run in progress (a gem or a package version can be pushed only once), and of the tags that wait, only the newest runs: push the tags of two releases close together and only the second publishes.

A tag cannot be reused for a different build. Fix the cause and re-run the failed workflow in GitHub Actions (`publish.yml` can also run by hand: a version already on the registry is skipped).

When `make tag` stops because CI failed on the release commit, `main` has the release commit but the tag is not on origin. Fix the cause on `main`, then make a new patch release, or move the tag to the fixed commit before you push it. If the code must change, use a new patch version. A published npm or gem version cannot be replaced.
