VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/solev/hibernaut/internal/version.Version=$(VERSION)
STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@v0.8.1
SHELL_SCRIPTS := azure/*.sh vm/*.sh $(filter-out %.go,$(wildcard vm/lib/*)) vm/idle-deallocate/idle-deallocate

.PHONY: build build-linux lint test test-host generate deploy up down e2e e2e-slow

build:
	go build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

# hibd for the VM.
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/linux-amd64/hibd ./cmd/hibd

lint:
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "Not gofmt'ed:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	go vet -tags e2e,host ./...
	$(STATICCHECK) ./...
	$(STATICCHECK) -tags e2e,host ./...
	shellcheck $(SHELL_SCRIPTS)

test:
	go test -race ./...

# Tests that need a Linux host with systemd.
test-host:
	go test -tags host ./...

generate:
	go generate ./...

# Installs this checkout's host pieces on the VM.
deploy:
	azure/deploy.sh

# Starts the VM, deploys, installs E2B if needed and waits until it's healthy.
up:
	azure/up.sh

down:
	azure/down.sh

# Readies the VM (azure/up.sh), then tests it from here through an SSH tunnel. Creates and
# deletes machines labeled hib-test=e2e.
e2e: up
	go test -tags e2e -count=1 -v ./e2e/...

# Also reboots the VM, then deallocates and starts it, which adds about 5 minutes.
e2e-slow: up
	HIB_E2E_SLOW=1 go test -tags e2e -count=1 -v -timeout 60m ./e2e/...
