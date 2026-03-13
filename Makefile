BINARY := petri
CMD := ./cmd/petri

.PHONY: run dev test build clean install

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
