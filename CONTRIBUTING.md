# Contributing to ndb

Thank you for your help. Bug reports, docs fixes and pull requests are welcome.

## Before you start

- Search the [issues](https://github.com/thadeu/ndb/issues) first.
- For a large change, open an issue and describe the idea before you write code.
- Security problems: do not open a public issue. See [SECURITY.md](SECURITY.md).

## Set up

You need Go 1.26 or newer. For the SDKs: Ruby 3.4 with Bundler, and Node 24 with pnpm.

```bash
git clone https://github.com/thadeu/ndb && cd ndb
make build
make test
```

More commands are in [docs/dev/development.md](docs/dev/development.md).

## Rules

- **Test first.** Write the failing test, then the code.
- **Keep tests light.** Use `nice -n 10 go test -p 2 -race -parallel=2 -timeout 300s ./...`. Long fuzz runs belong in CI.
- **Format.** `gofmt -l .` must print nothing, and `GOOS=linux go vet ./...` must be clean. `make lint` runs golangci-lint.
- **Blank lines.** Put one blank line before `if`, `for`, `switch`, `return`, `defer` and `go`, after a closing `}`, and between groups of lines that do different things. The linter (`wsl`) checks it.
- **Simple.** Do the minimum that solves the problem. No speculative options.
- **Docs.** When a change alters a documented behavior, update the file in `docs/dev/` in the same pull request.
- **SDKs.** The SDKs only build JSON. Query meaning belongs in the daemon.

## Pull requests

1. Fork, and make a branch from `main`.
2. Make small commits. Use [Conventional Commits](https://www.conventionalcommits.org) for each message and the pull request title: `type(scope): summary`, with the types `feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `build`, `ci` and `chore` (for example `fix(server): ...`, `feat(query): ...`). `release: X.Y.Z` is only for `make tag`.
3. Run the tests of everything you touched (daemon and SDKs).
4. Open the pull request and fill in the template.

## License

By contributing, you agree that your work is licensed under the license of the part you change: AGPL-3.0 for the daemon, MIT for the SDKs in `sdk/`.
