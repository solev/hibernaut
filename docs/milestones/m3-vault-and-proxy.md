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
changes. Removing a secret takes effect on the next request, including on open connections.
The proxy also reloads when `vault.key` changes or first appears. Reload fingerprints come from the
opened files and include size, modification time and file identity. Missing or invalid files disable
swaps until a later change. Errors are rate-limited and never include values or file contents.
Machine names come from the engine's `hib.name` metadata. The proxy caches names and refreshes
on a miss at most once every five seconds. If the name lookup fails, the audit keeps the sandbox ID.
On stop, the proxy stops hook repairs, removes the rule, closes open connections and closes the
audit log. Rule removal has a ten-second timeout. The unit allows thirty seconds to stop.
Only `Authorization` is swapped, including decoded Basic auth. Other headers, the URL, and bodies
stay untouched. Request bodies stream without buffering, regardless of type or length.
Every intercepted response is scrubbed against all vault values in headers, trailers, and bodies,
including binary bodies. Matches cover raw, JSON-escaped, URL query-escaped, and base64 forms.
Base64 uses the leak scanner's shared alignment-invariant core and also covers final partial groups
and padding. Embedded values can retain at most eight boundary bits. A value at the end, such as a
Basic password, can retain at most four leading bits. Patterns shorter than eight base64 characters
are skipped. Line-wrapped base64 is not matched. Response `Content-Length` is removed because
rotation can change replacement length.
The proxy strips client `Accept-Encoding` before forwarding, so the transport can request and decode
gzip. Any non-identity response encoding left after transport decoding fails closed with 502.
The proxy talks HTTP/2 to sites that offer it. It uses HTTP/1.1 for WebSocket upgrades. If audit
writing fails, the proxy returns 502 without forwarding.
Non-TLS connections pass through when they have no SNI. WebSocket upgrade requests swap
`Authorization`. Frames after an upgrade pass through without swapping or scrubbing.
The hardened systemd unit needs no `ReadWritePaths` for iptables on the tested host.

**Leak scan:** the scan follows the snapshot's header to read memory and disk in their logical
order, so a value that spans blocks from different builds is still found. It also scans every file
of the build, and of the builds its headers reference, as raw bytes, which covers `snapfile` and
blocks that a later pause replaced. It checks referenced builds recursively, so a missing or short
ancestor file cannot produce a clean verdict.

After a pause, it selects the newest build by creation time without filtering its status. Image
scans use the same rule. The selected ID stays fixed while the scan polls once a second for up to
15 minutes. The clock and sleep are injectable for fast tests. A failed build fails immediately.
A timeout reports the build ID and its last status or missing file. It never scans an older ready
build instead. A 32 GiB export at 50 MiB per second takes about 11 minutes. The 15-minute limit
leaves time for its disk diff and competing host I/O while keeping the wait bounded.

Readiness in the database is not enough. E2B uploads files in parallel after the pause RPC returns,
and each local file appears through an atomic rename. Completion requires both headers to parse,
identify the build, and cover their logical sizes. It also requires valid `metadata.json` and a
non-empty `snapfile`. Legacy metadata without a build ID is accepted. If metadata declares an ID,
it must match the build.
Each mapped data file must reach the furthest storage offset plus length. Empty diffs can omit
their own data files when all mappings use ancestors or zero blocks. A missing header is not an
empty diff. Filesystem-only snapshots fail because they cannot prove memory is clean. The scan
also fails on a short read if a file shrinks after the size check.

The scan refuses version 4 and later headers, which E2B compresses. It reports the secret's name,
the encoding, the file and the offset, never the value. Known limit: memory is scanned in
guest-physical order, so a value split across two guest pages that are not adjacent physically can
be missed. The canary checks the common case and requires hits in both memory and disk. It holds
`$HIBD_STATE_DIR/leakscan-canary.lock` for the whole run. Before creating a fresh canary, it removes
machines and vault secrets named `leakscan-canary-` followed by 12 lowercase hex digits. That
namespace is reserved for canaries. The lock prevents a second run from removing an active canary
and releases automatically on process exit.

## Tasks

**Spike first (S1)**

- [x] Map a machine's slot IP to its sandbox ID on the host (E2B's API doesn't expose it).
- [x] Prove our inserted iptables rule stays first while E2B adds and removes machines.
- [x] Write the findings into `docs/research/e2b-capabilities.md` §10.7 and the spec.

**Vault**

- [x] AES-GCM vault under `/var/lib/hibernaut/`, key in a root-only file.
- [x] `hib secret add NAME --env VAR --bind site,site` (value from stdin, never from arguments),
      `hib secret ls` (never shows values), `hib secret rm`.
- [x] Opaque placeholders retain the real token's prefix, characters and initial length.
      Renewing the same credential keeps its placeholder byte for byte, even if its length
      changes. Only a changed token type or prefix, or changed exposed JWT identity claims,
      makes a new placeholder. JWT identity excludes the top-level fields `exp`, `iat`, `nbf`,
      `jti`, `auth_time`, `nonce`, `at_hash`, `c_hash`, `s_hash` and `sid`. It includes every other
      claim (§4.1). JWT placeholders retain the claims with an expiry in 2100 and a fake
      signature. Their encoded length may differ.

**Delivery**

- [x] Managed placeholder block in each machine's `/etc/environment`, written through envd at
      create. When secrets change, running machines are updated at once and paused ones when they
      next wake (the poll loop notices).

**CA**

- [x] `hibd` creates the Hibernaut CA once; its key never leaves the host.
- [x] The `dev` image trusts it and sets `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`,
      `REQUESTS_CA_BUNDLE`, `CODEX_CA_CERTIFICATE` (§4.2).

**Proxy**

- [x] Interception port that only accepts connections arriving on machine interfaces (`veth-*`).
- [x] iptables hook `-I PREROUTING 1 -i veth-+ -p tcp --dport 443 -j REDIRECT`: added on start,
      checked and re-added every few seconds, removed on clean stop.
- [x] Real destination from `SO_ORIGINAL_DST`; hostname from SNI.
- [x] Unbound sites: bytes pass straight through, nothing decrypted.
- [x] Bound sites: terminate TLS with a per-host certificate from our CA; connect to the real site
      and verify its real certificate; HTTP/1.1 and HTTP/2.
- [x] Swap only in `Authorization`, including decoded Basic auth. Other request fields stay
      untouched. All request bodies stream unbuffered. WebSocket upgrades work.
- [x] Scrub all vault values in bound-site response headers, trailers, and streaming bodies,
      including raw, escaped, and base64 forms. Uninspectable body encodings fail closed.
- [x] Audit log (time, machine, site, method, path, secret name) and `hib log`.

**Leak scan**

- [x] `hibd leakscan <machine|image>`: pauses the machine, then searches its memory and disk
      snapshot files for every real secret (raw, base64 and URL-encoded). It refuses compressed
      headers. Reports hits without printing values.
- [x] Canary mode: plant a known value in a machine and require the scan to find it.

## Tests

Unit and fuzz:

- [x] Placeholder shapes (`gho_`, `github_pat_`, `sk-`, JWTs, …).
- [x] Swap only in `Authorization`. Site bindings and untouched request content are fuzzed.
      Streaming responses are scrubbed in raw, escaped, and base64 forms.
- [x] Vault round-trip; the vault file never contains a plaintext value.
- [x] `/etc/environment` block: idempotent, leaves the rest of the file alone.

Integration (laptop and CI; fake TLS servers signed by a test CA):

- [x] Real `curl`, `git`, `gh` and Node clients send swapped `Authorization` upstream.
- [x] Unbound site: identical bytes, and the client sees the site's own certificate.
- [x] Streaming: SSE and a large body start arriving before the upstream finishes.
- [x] WebSocket through the proxy.
- [x] An upstream with an invalid certificate is refused.
- [x] The audit log never contains a real value.
- [x] Large request bodies reach upstream before the client finishes, with and without a known
      length. HTTP/1.1 and HTTP/2 headers, trailers, binary bodies, gzip, and reflected Basic auth
      are scrubbed. Changed lengths preserve framing. Unsupported encodings return 502.

Host integration (Linux, sudo):

- [x] CI: a `test-host` job running `make test-host` on a Linux runner with sudo (moved from M0).
- [x] A network namespace on `veth-99` stands in for a machine: its port 443 traffic reaches
      `hibd`, `SO_ORIGINAL_DST` returns the real destination, port 80 is untouched.
- [x] The hook comes back after the rule is deleted or another rule is inserted above it.
- [x] The interception port refuses connections that don't come from `veth-*`.

End-to-end:

- [ ] `TestSecretSwap`: the laptop's `gh auth token` is added as a secret bound to
      `api.github.com`. In a machine, `curl` to `api.github.com/user` with the placeholder
      returns your login; the same header sent to `postman-echo.com/headers` comes back as the
      placeholder. A separate synthetic secret bound to postman-echo.com tests Bearer and Basic
      response scrubbing.
- [ ] `TestNoHostAccess` (S8): from a machine, the E2B API, Postgres, Redis, `hibd` and
      `169.254.169.254` can't be reached; the host's public IP answers only on SSH; `192.0.2.1`
      exposes only E2B's expected endpoint.
- [ ] `TestAuditLog`: `hib log` shows Bearer and Basic swaps with machine, site, and secret name.
      Placeholders in other headers and bodies create no audit entry.
- [ ] `TestLeakScan`: after the tests above, zero real values found; the canary is found.

## Done when

- [ ] `make lint test test-host`, CI and `make e2e` are green.
- [ ] Spec: S1 and S8 marked answered.
- [ ] README: how secrets work, `hib secret`, `hib log`.

## Changes

- S1 verified the design on the live host. The proxy can keep the default port 5443 and iptables
  hook. The hardened systemd unit does not need `ReadWritePaths` for iptables on this host.
- 2026-10-03: Documented the leak scan's guest-physical memory ordering limit.
- 2026-10-03: gh's proxy integration case runs on Linux only. Go on macOS ignores
  `SSL_CERT_FILE`, so e2e covers gh in a machine there.
- 2026-10-03: The leak scan refuses compressed (version 4 and later) headers instead of
  decompressing them. E2B on this host writes version 3, which is uncompressed.
- 2026-10-03: Wired CA initialization and the root proxy service. Added descriptor-safe vault and
  key reloads, cached audit names and bounded shutdown. Added opened-file vault readers for the
  reload fingerprint. Fixed proxy cancellation leaving open connections alive and a concurrent
  listener-close panic. Live checks covered CA persistence, TLS refusal, swapping, audit names,
  clean stop, crash restart and hook repair under systemd hardening.
- 2026-10-03: M3 end-to-end checks passed audit and leak scan. Secret swap's clients passed, but its
  final scan intermittently found a snapshot file before E2B had written it. Preserved failed
  snapshots later had those files, with cleanup stopped throughout the run. The host-access test
  stopped at its GitHub control probe. GitHub closed its Postgres-style probe without a reply,
  but valid SSH identification got a reply from the host and a machine. These failures remain
  outside the proxy wiring. A manual check with a valid control probed 1,462 host and Azure targets.
  Only the expected host SSH and E2B service answered. End-to-end and completion boxes stay open.
- 2026-10-03: Narrowed request swaps to Authorization to prevent gist and git blob credential
  extraction. Stored content now keeps the placeholder. Responses scrub all vault values in
  headers, trailers, binary bodies, escaped forms, and aligned base64. Unsupported encodings fail
  closed. The leak scanner and response scrubber share the base64 core without changing scan
  behaviour.
- 2026-10-03: Recorded the WebSocket limit. Authorization on upgrade requests is swapped.
  Frames after the upgrade are neither swapped nor scrubbed.
- 2026-10-03: CA initialization resumes from a published key when its certificate is missing.
  It never replaces an existing certificate. Vault key creation publishes a complete file
  atomically. An empty vault can repair an incomplete key, but a vault with secrets requires
  its original key. The image's trust script replaces `/etc/environment` atomically.
- 2026-10-03: Credential renewals keep placeholders despite length, expiry, nonce, token hash,
  session ID or signing-key changes. Changed token types, prefixes or exposed JWT identity
  claims make new placeholders. JWT placeholders now use an expiry of 2100-01-01 (§4.4).
- 2026-10-03: Leak scans fix the newest build ID before a bounded readiness and local-upload
  wait. They validate referenced builds too, reject incomplete files, and never fall back to an
  older ready snapshot. Canary reruns reconcile their reserved namespace under a host lock.
