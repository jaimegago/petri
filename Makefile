BINARY := petri
CMD := ./cmd/petri

SVC_DIR := images/svc
SVC_IMAGE := ghcr.io/jaimegago/svc
SVC_VERSION ?= 0.2.0

# What the binary is stamped with (internal/version, ADR 0021): the short
# commit, and -dirty when `git status --porcelain` prints anything, which
# counts an untracked file because one is compiled in like a modified one.
# Empty outside a git checkout, where the binary falls back to its own lookup.
VERSION_PKG := github.com/jaimegago/petri/internal/version
BUILD ?= $(shell git rev-parse --short HEAD 2>/dev/null)$(shell test -z "$$(git status --porcelain 2>/dev/null)" || echo -dirty)
LDFLAGS := -X $(VERSION_PKG).Build=$(BUILD)

.PHONY: run dev test build version clean install svc-test svc-image svc-push

run:
	go run $(CMD)

install:
	go install -ldflags "$(LDFLAGS)" $(CMD)

test:
	go test ./...

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

# Prints what a build made now would report.
version:
	@go run -ldflags "$(LDFLAGS)" $(CMD) --version

clean:
	rm -f $(BINARY)

# The application image (docs/application-image.md). A nested module, so the
# root `make test` does not cover it.
svc-test:
	cd $(SVC_DIR) && go vet ./... && go test ./...

svc-image:
	docker build --build-arg VERSION=$(SVC_VERSION) -t $(SVC_IMAGE):$(SVC_VERSION) $(SVC_DIR)

svc-push:
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(SVC_VERSION) -t $(SVC_IMAGE):$(SVC_VERSION) --push $(SVC_DIR)
