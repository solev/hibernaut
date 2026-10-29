VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/solev/hibernaut/internal/version.Version=$(VERSION)
STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@v0.8.1
SHELL_SCRIPTS := azure/*.sh vm/*.sh $(filter-out %.go,$(wildcard vm/lib/*)) vm/idle-deallocate/idle-deallocate \
	images/dev/packages images/dev/configure images/dev/start images/dev/ready images/dev/machine-init \
	images/dev/hib-term.sh images/dev/trust-ca images/dev/tools

.PHONY: build build-linux lint test test-host test-host-remote test-linux-remote generate deploy up down e2e e2e-slow

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

# Tests that need root on a Linux host, and a few that need Linux. Every hook test runs under sudo.
# Of the machines, cleanup, statefile and taskstore tests, only those named TestHost do. The rest
# expect an ordinary user. Of the linkguard tests, only the TestGuard ones do, and they need ip,
# iptables, nft and conntrack. Of the linkadmit tests, the TestHost one and the run watcher's and
# control socket's do, to meet real cgroups, inotify and Unix sockets. The linkproxy abort tests
# run as an ordinary user, to see an abort reset both legs on Linux.
test-host:
	@for need in nft:nftables conntrack:conntrack; do \
		PATH="$$PATH:/usr/sbin:/sbin" command -v "$${need%%:*}" >/dev/null || \
			{ echo "test-host needs $${need%%:*}. Install the $${need#*:} package." >&2; exit 1; }; \
	done
	go test -tags host -exec sudo ./internal/egress/hook/
	go test -tags host -exec sudo -run '^TestHost' ./internal/machines/
	go test -tags host -exec sudo -run '^TestHost' ./internal/cleanup/
	go test -tags host -exec sudo -run '^TestHost' ./internal/statefile/
	go test -tags host -exec sudo -run '^TestHost' ./internal/tasks/internal/taskstore/
	go test -tags host -exec sudo -run '^TestGuard' ./internal/linkguard/
	go test -tags host -exec sudo -run '^(TestHost|TestRunWatcher|TestTheControlSocketAcrossProcesses)' ./internal/linkadmit/
	go test -run '^TestAbort' ./internal/linkproxy/

# Cross-compiles the host tests, runs them on the Azure host and removes the binaries. Each runs
# from its package's directory, as go test runs it, so the repo files it reads are where it looks.
# The host gets nftables and conntrack first if it lacks them, for the linkguard tests.
test-host-remote:
	@set -e; \
	dir=$$(mktemp -d -t hibernaut-host-test.XXXXXX); \
	remote=/tmp/hibernaut-host-test-$$$$; \
	trap 'rm -rf "$$dir"' EXIT; \
	for pkg in internal/egress/hook internal/machines internal/cleanup internal/statefile internal/tasks/internal/taskstore internal/linkguard internal/linkadmit internal/linkproxy; do \
		GOOS=linux GOARCH=amd64 go test -c -tags host -o "$$dir/$$pkg/" "./$$pkg"; \
	done; \
	mkdir -p "$$dir/vm/e2b"; \
	cp vm/e2b/sandbox-firewall.nft "$$dir/vm/e2b/"; \
	azure/start.sh || { echo "Azure host is not reachable. Run azure/start.sh and retry." >&2; exit 1; }; \
	target=$$(bash -c 'source azure/common.sh; printf "%s@%s" "$$VM_USER" "$$(vm_ip)"'); \
	scp -r "$$dir" "$$target:$$remote"; \
	azure/ssh.sh "{ command -v nft && command -v conntrack; } >/dev/null || \
		{ sudo apt-get update -qq && sudo apt-get install -y -qq nftables conntrack; } || { rm -rf $$remote; exit 1; }; \
		status=0; cd $$remote/internal/egress/hook && sudo ./hook.test -test.v || status=1; \
		cd $$remote/internal/machines && sudo ./machines.test -test.v -test.run '^TestHost' || status=1; \
		cd $$remote/internal/cleanup && sudo ./cleanup.test -test.v -test.run '^TestHost' || status=1; \
		cd $$remote/internal/statefile && sudo ./statefile.test -test.v -test.run '^TestHost' || status=1; \
		cd $$remote/internal/tasks/internal/taskstore && sudo ./taskstore.test -test.v -test.run '^TestHost' || status=1; \
		cd $$remote/internal/linkguard && sudo ./linkguard.test -test.v -test.run '^TestGuard' || status=1; \
		cd $$remote/internal/linkadmit && sudo ./linkadmit.test -test.v \
			-test.run '^(TestHost|TestRunWatcher|TestTheControlSocketAcrossProcesses)' || status=1; \
		cd $$remote/internal/linkproxy && ./linkproxy.test -test.v -test.run '^TestAbort' || status=1; \
		cd /; rm -rf $$remote; exit \$$status"

# Runs every package's unit tests on the Azure host as its ordinary user, under that user's umask
# and with the pinned Codex. The working tree's tracked files go to a fresh directory there, which
# is removed afterwards.
test-linux-remote:
	@set -e; \
	azure/start.sh || { echo "Azure host is not reachable. Run azure/start.sh and retry." >&2; exit 1; }; \
	target=$$(bash -c 'source azure/common.sh; printf "%s@%s" "$$VM_USER" "$$(vm_ip)"'); \
	remote=$$(ssh "$$target" mktemp -d /tmp/hibernaut-linux-test.XXXXXX); \
	trap 'ssh "$$target" "rm -rf $$remote"' EXIT; \
	git ls-files -z | xargs -0 sh -c 'for f; do [ ! -e "$$f" ] || printf "%s\0" "$$f"; done' sh | \
		COPYFILE_DISABLE=1 tar --no-xattrs --null -T - -czf - | ssh "$$target" "tar -xzf - -C $$remote"; \
	ssh "$$target" "cd $$remote && GOTOOLCHAIN=local GOFLAGS=-mod=readonly \
		HIB_CODEX=/usr/local/bin/codex HIB_REQUIRE_CODEX=1 /usr/local/go/bin/go test -race ./..."

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
# deletes machines labeled hib-test=e2e or named e2e-*. TestMachineToMachine adds about 30
# minutes.
e2e: up
	go test -tags e2e -count=1 -v -timeout 70m ./e2e/...

# Also reboots the VM, then deallocates and starts it, which adds about 5 minutes, and keeps a
# busy machine running past an hour, which adds about 65. TestTrim adds its pauses, at most the
# rest of the hour, two restarts, two saves and a fork. TestMachineToMachineSlow waits out idle
# timeouts and link leases, which adds about 60, and checking links across the reboot a few more.
# TestSSHAsE2BStarts restarts E2B and connects as it starts, which adds about a minute.
e2e-slow: up
	HIB_E2E_SLOW=1 go test -tags e2e -count=1 -v -timeout 270m ./e2e/...
