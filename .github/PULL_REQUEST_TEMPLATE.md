## What and why

<!-- What does this change, and why? Link the issue if there is one. -->

## How it was tested

<!-- Tests added, commands run. Tests come first (TDD). -->

## Checklist

- [ ] Tests pass: `nice -n 10 go test -p 2 -race -parallel=2 -timeout 300s ./...`
- [ ] `gofmt -l .` and `GOOS=linux go vet ./...` are clean
- [ ] Docs in `docs/dev/` match the new behavior
- [ ] SDK changes: tests of the changed SDK pass
