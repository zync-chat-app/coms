.PHONY: run build keygen clean lint

run:
	CGO_ENABLED=1 go run -tags "fts5" cmd/server/main.go

build:
	CGO_ENABLED=1 go build -tags "fts5" -ldflags="-s -w" -o bin/coms cmd/server/main.go

keygen:
	go run cmd/server/keygen.go

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/ data/ logs/