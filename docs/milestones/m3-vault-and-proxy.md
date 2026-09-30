# M3 Vault, egress proxy, leak scan

Status: **not started** · Spec: §4.1, §4.2, §4.5, §8 (leak scan) · Spikes: S1 (first), S8

**Goal:** machines use secrets without ever holding them, and we can prove it.

## Tasks

**Spike first (S1)**

- [ ] Map a machine's slot IP to its sandbox ID on the host (E2B's API doesn't expose it).
- [ ] Prove our inserted iptables rule stays first while E2B adds and removes machines.
- [ ] Write the findings into `docs/research/e2b-capabilities.md` §10.7 and the spec.

**Vault**

- [ ] AES-GCM vault under `/var/lib/hibernaut/`, key in a root-only file.
- [ ] `hib secret add NAME --env VAR --bind site,site` (value from stdin, never from arguments),
      `hib secret ls` (never shows values), `hib secret rm`.
- [ ] Placeholders: same prefix, length and characters as the real value; stable when the real
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

- [ ] Placeholder shapes (`gho_`, `github_pat_`, `sk-`, JWTs, …).
- [ ] Swap in every location; a real value never goes to an unbound site (fuzzed); responses
      scrubbed.
- [ ] Vault round-trip; the vault file never contains a plaintext value.
- [ ] `/etc/environment` block: idempotent, leaves the rest of the file alone.

Integration (laptop and CI; fake TLS servers signed by a test CA):

- [ ] Real `curl`, `git`, `gh` and Node clients through the proxy get the swapped value upstream.
- [ ] Unbound site: identical bytes, and the client sees the site's own certificate.
- [ ] Streaming: SSE and a large body start arriving before the upstream finishes.
- [ ] WebSocket through the proxy.
- [ ] An upstream with an invalid certificate is refused.
- [ ] The audit log never contains a real value.

Host integration (Linux, sudo):

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

(none yet)
