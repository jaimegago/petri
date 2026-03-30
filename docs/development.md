# Development

## Running from Source

```bash
git clone https://github.com/yourusername/petri.git
cd petri
go build ./cmd/petri/
./petri init
```

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
