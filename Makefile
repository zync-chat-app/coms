.PHONY: run build keygen clean lint

run:
	go run cmd/server/main.go

build:
	go build -ldflags="-s -w" -o bin/coms cmd/server/main.go

keygen:
	go run cmd/server/keygen.go

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/ data/ logs/
