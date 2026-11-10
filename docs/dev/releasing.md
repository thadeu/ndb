# Releasing

One tag `vX.Y.Z` releases everything. Version 0.1.0 is used in all packages.

| Artifact | Name | Workflow |
|---|---|---|
| Daemon binaries + checksums | GitHub Release | `release.yml` |
| Console binaries | GitHub Release (`ndb-ui-*`) | `release.yml` (after the daemon) |
| Go SDK | tag `sdk/go/vX.Y.Z`, module `github.com/thadeu/ndb/sdk/go` | `release.yml` |
| Go Redis adapter | tag `sdk/go/ndbredis/vX.Y.Z`, module `github.com/thadeu/ndb/sdk/go/ndbredis` (needs the Go SDK of the same release) | `release.yml` |
| Images | `ghcr.io/thadeu/ndb`, `ghcr.io/thadeu/ndb-ui` (two jobs at the same time), then `ghcr.io/thadeu/ndb-stack` (built FROM the two images of the same version) | `ghcr.yml` |
| Gems | `ndb` (pushed first), then `ndb-rails` (depends on it) | `publish.yml` (job `gems`) |
| npm | `@thadeu/ndb` | `publish.yml` (job `npm`, at the same time as `gems`) |
| Docs site | Cloudflare Pages (project `ndb-docs`, via wrangler) | `docs.yml` (only when the site changes; run it by hand to show a new version at once) |

**One run does it all.** `make tag` pushes `main` and the tag, and ends at once. CI (`ci.yml`) tests the release commit; when the tests pass and the commit has a tag `v*`, its last jobs call `release.yml`, `ghcr.yml` and `publish.yml` for that tag, at the same time, in the same run. A failed test publishes nothing. So the tests run once, a push to `main` with no tag runs only the tests, and only the console is tested in `release.yml` (CI does not test it). The run of a release commit is never cancelled by a newer push.

Each of the three can also run by hand (Actions, Run workflow, with the tag), for example to finish a publish that stopped halfway.

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
4. `make tag patch|minor|major`: it does steps 1 and 2, commits "Release X.Y.Z" and pushes `main` and the tag. CI then tests and publishes. By hand: `git tag vX.Y.Z` on a commit, then `git push origin main vX.Y.Z` (the CI run of a commit with a tag publishes it).

## Verify

- Release: `gh release view v0.1.0`. Download one archive, check it with `sha256sum -c checksums.txt`, run `ndb -version` (prints `ndb 0.1.0`).
- Go tags: `git ls-remote --tags origin 'sdk/go/*'` lists `sdk/go/vX.Y.Z` and `sdk/go/ndbredis/vX.Y.Z`; then in a temp module `go get github.com/thadeu/ndb/sdk/go@vX.Y.Z` and `go get github.com/thadeu/ndb/sdk/go/ndbredis@vX.Y.Z`.
- Image: `docker run --rm ghcr.io/thadeu/ndb:0.1.0 -version`.
- npm: `npm view @thadeu/ndb version`; the package page shows the provenance badge.
- Gems: `gem search -r '^ndb$'`, `gem search -r ndb-rails`, or `gem install ndb-rails -v 0.1.0` (it installs `ndb` too).
- Docs: open the `ndb-docs` Cloudflare Pages URL (or its custom domain).

## If a publish fails

**A release is never cancelled.** A push to `main` cancels the older test run of `main`, but the run of a release commit has a group of its own: no push cancels it. Two releases run in order: `release.yml`, `ghcr.yml` and `publish.yml` run one at a time, and a newer release waits for the older one to end. So each version is published in full, and `latest` (npm, images) is the newest one.

GitHub keeps only **one** run waiting in a group. With release A running and release B waiting, a release C that comes then takes the place of B: B is not published. This happens only with three tags in a few minutes. To publish B, run `release`, `ghcr` and `publish` by hand (Actions, Run workflow) with its tag.

A tag cannot be reused for a different build. Fix the cause and re-run the failed workflow in GitHub Actions (`publish.yml` can also run by hand: a version already on the registry is skipped).

When CI fails on the release commit, nothing is published, but the tag is on origin. Fix the cause on `main` and make a new patch release. If the code must change, use a new patch version. A published npm or gem version cannot be replaced.
