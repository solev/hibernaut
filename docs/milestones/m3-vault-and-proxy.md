# M3 Vault, egress proxy, leak scan

Status: **in progress** · Spec: §4.1, §4.2, §4.5, §8 (leak scan) · Spikes: S1 (first), S8

**Goal:** machines use secrets without ever holding them, and we can prove it.

## Design

The M3 code is built against this section. Code that has to differ updates it in the same commit.

**Files on the host:**

| Path | Owner, mode | Holds |
| --- | --- | --- |
| `/etc/hibernaut/vault.key` | root, 0600 | The 32-byte AES-256-GCM key. The first `hibd secret add` makes it. |
| `/var/lib/hibernaut/vault.json` | root, 0644 | Each secret's name, variable, sites and placeholder in plain text, and its real value sealed with the key. The plain fields are the sealed value's additional data, so editing a binding makes the value unreadable. |
| `/etc/hibernaut/ca.key`, `/etc/hibernaut/ca.crt` | root, 0600 and 0644 | The Hibernaut CA. `hibd ca init` makes it once, from `vm/setup-host.sh`. |
| `/var/log/hibernaut/audit.jsonl` | root, 0644 | The audit log, one JSON object per request with a swap. It rotates to `audit.jsonl.1`. |

**Who runs what:**

- `hib secret add` and `hib secret rm` run `sudo -n hibd secret …` on the host, because sealing a
  value needs the key. The value travels on stdin, never in an argument.
- `hib secret ls` and `hib log` run `hibd` as the login user. The files they read hold no real
  value.
- `hibd proxy` runs as root in its own unit, `hibd-proxy.service`, so a crash in the proxy leaves
  the busy watcher running. On a clean stop, and in `ExecStopPost` after a crash, it removes its
  iptables rule, and machines go back to E2B's path. They hold only placeholders, so that's safe.
- `hibd leakscan` runs as root (`sudo hibd leakscan`). It needs the real values and E2B's files.

**Packages:**

- `internal/vault`: the vault file, its key, and placeholders.
- `internal/envblock`: renders the managed block and merges it into `/etc/environment`.
- `internal/audit`: the audit log's entry, writer and reader.
- `internal/ca`: makes and loads the CA, and mints a certificate per host.
- `internal/egress/swap`: swaps placeholders for real values in requests, and real values for
  placeholders in responses. No network code.
- `internal/egress`: the proxy server. SNI, passthrough, TLS termination, HTTP/1.1, HTTP/2 and
  WebSocket.
- `internal/egress/hook`: Linux only. The iptables rule, `SO_ORIGINAL_DST`, and the `veth-*` check.
- `internal/slots`: Linux only. Finds the sandbox a machine's connection comes from (S1).
- `internal/leakscan`: rebuilds a snapshot's memory and disk from E2B's headers and searches them.

**Delivery:** `hibd new` writes the managed block before it returns, so the first login has the
variables. `hibd secret add` and `hibd secret rm` write it into every running machine. The daemon
keeps the hash of the block it last wrote to each machine, and on each look writes it to any running
machine whose hash is out of date: one that just woke, one that was missed, or every machine after
a restart. Writes go through envd as root, with placeholders only.

**Proxy:** `hibd proxy` listens on port `HIBD_PROXY_PORT` (default 5443) on all addresses, and
closes any connection whose local address isn't on a `veth-*` interface. Its rule carries the
comment `hibernaut`. For each connection it reads the real destination with `SO_ORIGINAL_DST` and
refuses private, loopback and link-local destinations. It reads the SNI without decrypting. If no
secret is bound to that host, or there's no SNI, it dials the real destination and copies bytes
both ways. Otherwise it terminates TLS with a certificate for that host, refuses a request whose
`Host` isn't the SNI host, swaps, connects to the real destination, verifies the site's
certificate for the SNI host, and scrubs the response. It reloads the vault when `vault.json`
changes.
The hardened systemd unit needs no `ReadWritePaths` for iptables on the tested host.

**Leak scan:** the scan follows the snapshot's header to read memory and disk in their logical
order, so a value that spans blocks from different builds is still found. It refuses headers newer
than version 3, which E2B compresses. It reports the secret's name, the encoding, the file and the
offset, never the value.

## Tasks

**Spike first (S1)**

- [x] Map a machine's slot IP to its sandbox ID on the host (E2B's API doesn't expose it).
- [x] Prove our inserted iptables rule stays first while E2B adds and removes machines.
- [x] Write the findings into `docs/research/e2b-capabilities.md` §10.7 and the spec.

**Vault**

- [x] AES-GCM vault under `/var/lib/hibernaut/`, key in a root-only file.
- [ ] `hib secret add NAME --env VAR --bind site,site` (value from stdin, never from arguments),
      `hib secret ls` (never shows values), `hib secret rm`.
- [x] Placeholders: same prefix, length and characters as the real value; stable when the real
      value rotates.

**Delivery**

- [ ] Managed placeholder block in each machine's `/etc/environment`, written through envd at
      create. When secrets change, running machines are updated at once and paused ones when they
      next wake (the poll loop notices).

**CA**

- [ ] `hibd` creates the Hibernaut CA once; its key never leaves the host.
- [ ] The `dev` image trusts it and sets `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`,
      `REQUESTS_CA_BUNDLE`, `CODEX_CA_CERTIFICATE` (§4.2).

**Proxy**

- [ ] Interception port that only accepts connections arriving on machine interfaces (`veth-*`).
- [ ] iptables hook `-I PREROUTING 1 -i veth-+ -p tcp --dport 443 -j REDIRECT`: added on start,
      checked and re-added every few seconds, removed on clean stop.
- [ ] Real destination from `SO_ORIGINAL_DST`; hostname from SNI.
- [ ] Unbound sites: bytes pass straight through, nothing decrypted.
- [ ] Bound sites: terminate TLS with a per-host certificate from our CA; connect to the real site
      and verify its real certificate; HTTP/1.1 and HTTP/2.
- [ ] Swap in headers, decoded Basic auth, the URL query and text bodies up to 1 MB. Everything
      else streams through unbuffered. WebSocket upgrades work.
- [ ] Scrub responses: a real value is turned back into its placeholder.
- [ ] Audit log (time, machine, site, method, path, secret name) and `hib log`.

**Leak scan**

- [ ] `hibd leakscan <machine|image>`: pauses the machine, then searches its memory and disk
      snapshot files for every real secret (raw, base64 and URL-encoded), decompressing first if
      E2B compresses them. Reports hits without printing values.
- [ ] Canary mode: plant a known value in a machine and require the scan to find it.

## Tests

Unit and fuzz:

- [x] Placeholder shapes (`gho_`, `github_pat_`, `sk-`, JWTs, …).
- [ ] Swap in every location; a real value never goes to an unbound site (fuzzed); responses
      scrubbed.
- [x] Vault round-trip; the vault file never contains a plaintext value.
- [ ] `/etc/environment` block: idempotent, leaves the rest of the file alone.

Integration (laptop and CI; fake TLS servers signed by a test CA):

- [ ] Real `curl`, `git`, `gh` and Node clients through the proxy get the swapped value upstream.
- [ ] Unbound site: identical bytes, and the client sees the site's own certificate.
- [ ] Streaming: SSE and a large body start arriving before the upstream finishes.
- [ ] WebSocket through the proxy.
- [ ] An upstream with an invalid certificate is refused.
- [ ] The audit log never contains a real value.

Host integration (Linux, sudo):

- [ ] CI: a `test-host` job running `make test-host` on a Linux runner with sudo (moved from M0).
- [ ] A network namespace on `veth-99` stands in for a machine: its port 443 traffic reaches
      `hibd`, `SO_ORIGINAL_DST` returns the real destination, port 80 is untouched.
- [ ] The hook comes back after the rule is deleted or another rule is inserted above it.
- [ ] The interception port refuses connections that don't come from `veth-*`.

End-to-end:

- [ ] `TestSecretSwap`: the laptop's `gh auth token` is added as a secret bound to
      `api.github.com`. In a machine, `curl` to `api.github.com/user` with the placeholder
      returns your login; the same header sent to `postman-echo.com/headers` comes back as the
      placeholder.
- [ ] `TestNoHostAccess` (S8): from a machine, the E2B API, Postgres, Redis, `hibd` and
      `169.254.169.254` can't be reached; the host's public IP answers only on SSH; `192.0.2.1`
      exposes only E2B's expected endpoint.
- [ ] `TestAuditLog`: `hib log` shows the swap with machine, site and secret name.
- [ ] `TestLeakScan`: after the tests above, zero real values found; the canary is found.

## Done when

- [ ] `make lint test test-host`, CI and `make e2e` are green.
- [ ] Spec: S1 and S8 marked answered.
- [ ] README: how secrets work, `hib secret`, `hib log`.

## Changes

- S1 verified the design on the live host. The proxy can keep the default port 5443 and iptables
  hook. The hardened systemd unit does not need `ReadWritePaths` for iptables on this host.
