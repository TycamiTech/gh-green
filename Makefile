.PHONY: all build build-linux test run-dry clean

BINARY_NAME=gh-green
BINARY_WIN=gh-green.exe

all: test build-linux

# Build stripped production binary for Linux amd64 (recommended for servers & crontab)
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $(BINARY_NAME) main.go

# Build local OS binary
build:
	go build -o $(BINARY_NAME) main.go

# Run unit tests
test:
	go test -v ./...

# Run dry-run simulation
run-dry:
	go run main.go --dry-run --now

# Clean built binaries
clean:
	@rm -f $(BINARY_NAME) $(BINARY_WIN)

