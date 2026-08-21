BINARY := petri
CMD := ./cmd/petri

SVC_DIR := images/svc
SVC_IMAGE := ghcr.io/jaimegago/svc
SVC_VERSION ?= 0.1.0

.PHONY: run dev test build clean install svc-test svc-image svc-push

run:
	go run $(CMD)

install:
	go install $(CMD)

test:
	go test ./...

build:
	go build -o $(BINARY) $(CMD)

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
