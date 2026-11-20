# Development

## Running from Source

```bash
git clone https://github.com/jaimegago/petri.git
cd petri
make build
./petri init
```

`make build` stamps the binary with the commit it was built from, and `-dirty`
when the tree differed from it: `petri --version` prints `petri version
0.1.0+b46fec5`, and `petri serve` declares the same string as its
`provider_version`. A bare `go build ./cmd/petri/` carries the commit only in a
plain clone and reports `0.1.0+unknown` from a git worktree or a submodule
checkout (ADR 0021).

## Running Tests

```bash
# Unit tests
go test ./...

# Integration tests (requires Docker + kind)
go test -tags integration ./...

# Linting
go vet ./...
go fmt ./...
```

## Adding a Company

1. Add the company definition to `configs/companies.yaml`
2. Add IaC templates in `templates/terraform/<provider>/` or `templates/pulumi/<provider>/`
3. Add GitOps templates in `templates/gitops/<tool>/`
4. Create app profiles in `pkg/companies/<company-name>.go`
5. Register the company in `pkg/companies/registry.go`

See [petri-architecture.md](petri-architecture.md) for the company configuration schema.
