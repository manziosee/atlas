# Contributing to Atlas

Thanks for helping build Atlas! This project is meant to be read by other
engineers, so clarity matters as much as correctness.

## Ground rules

1. **Open an issue first** for anything bigger than a bug fix, so we can agree
   on the approach before you write code.
2. **Architecture changes need an ADR.** Copy
   [`docs/adr/0000-template.md`](docs/adr/0000-template.md), number it, and
   include it in your PR.
3. **Security first.** Never commit secrets. Report vulnerabilities privately
   (see [SECURITY.md](SECURITY.md)).

## Development loop

```bash
make env       # create .env with generated secrets
make test      # race-enabled tests
make lint      # golangci-lint (gosec included)
make vuln      # govulncheck
make up        # 3-node cluster + Prometheus + Grafana
make chaos     # kill a node and watch the cluster heal
```

## Pull requests

- Keep PRs focused, one logical change each.
- Add tests. Protocol changes need a multi-node test (see
  `internal/membership/memberlist_test.go` for the in-memory network harness).
- CI must be green: lint, race tests, govulncheck, gitleaks, CodeQL, manifest
  validation and image CVE scan.
- Use [Conventional Commits](https://www.conventionalcommits.org/):
  `feat(membership): add indirect probes`.

## Code style

- `gofmt` + `goimports` (local prefix `github.com/manziosee/atlas`).
- Errors are wrapped with context (`fmt.Errorf("transport: listen: %w", err)`).
- Packages under `internal/` own one concern each. Avoid import cycles by
  depending on small interfaces (see `membership.Transport`).
- Structured logging only (`log/slog`), never `fmt.Println` in the node.
