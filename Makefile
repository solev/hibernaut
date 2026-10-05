VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/solev/hibernaut/internal/version.Version=$(VERSION)
STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@v0.8.1
SHELL_SCRIPTS := azure/*.sh vm/*.sh $(filter-out %.go,$(wildcard vm/lib/*)) vm/idle-deallocate/idle-deallocate \
	images/dev/packages images/dev/configure images/dev/start images/dev/ready images/dev/machine-init \
	images/dev/hib-term.sh images/dev/trust-ca images/dev/tools

.PHONY: build build-linux lint test test-host test-host-remote generate deploy up down e2e e2e-slow

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

# Tests that need root on a Linux host. Every hook test runs under sudo. Of the machines tests,
# only those named TestHost do, because the rest expect an ordinary user.
test-host:
	go test -tags host -exec sudo ./internal/egress/hook/
	go test -tags host -exec sudo -run '^TestHost' ./internal/machines/

# Cross-compiles the host tests, runs them on the Azure host and removes the binaries.
test-host-remote:
	@set -e; \
	dir=$$(mktemp -d -t hibernaut-host-test.XXXXXX); \
	remote=/tmp/hibernaut-host-test-$$$$; \
	trap 'rm -rf "$$dir"' EXIT; \
	GOOS=linux GOARCH=amd64 go test -c -tags host -o "$$dir/hook.test" ./internal/egress/hook; \
	GOOS=linux GOARCH=amd64 go test -c -tags host -o "$$dir/machines.test" ./internal/machines; \
	azure/start.sh || { echo "Azure host is not reachable. Run azure/start.sh and retry." >&2; exit 1; }; \
	target=$$(bash -c 'source azure/common.sh; printf "%s@%s" "$$VM_USER" "$$(vm_ip)"'); \
	scp -r "$$dir" "$$target:$$remote"; \
	azure/ssh.sh "status=0; sudo $$remote/hook.test -test.v || status=1; \
		sudo $$remote/machines.test -test.v -test.run '^TestHost' || status=1; \
		rm -rf $$remote; exit \$$status"

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
# deletes machines labeled hib-test=e2e or named e2e-*.
e2e: up
	go test -tags e2e -count=1 -v -timeout 40m ./e2e/...

# Also reboots the VM, then deallocates and starts it, which adds about 5 minutes, and keeps a
# busy machine running past an hour, which adds about 65. TestTrim adds its pauses, at most the
# rest of the hour, two restarts, two saves and a fork.
e2e-slow: up
	HIB_E2E_SLOW=1 go test -tags e2e -count=1 -v -timeout 180m ./e2e/...
