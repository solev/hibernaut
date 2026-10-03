# M4 GitHub

Status: **in progress** · Spec: §4.3, §8 (end-to-end details)

**Goal:** a fresh machine does the whole GitHub loop (clone, commit, push, open a PR) with no
login, and the token never enters it.

## Design

The M4 code is built against this section. Code that has to differ updates it in the same commit.

**Files on the host:**

| Path | Owner, mode | Holds |
| --- | --- | --- |
| `/var/lib/hibernaut/vault.json` | root, 0644 | Secret `github`: variable `GH_TOKEN`, bound to `github.com`, `api.github.com` and `uploads.github.com`. Same file and rules as M3. |
| `/var/lib/hibernaut/github.json` | root, 0644 | The connected account: login, ID, name, and how the token was given (`gh` or `token`). No token. Written atomically. |

**Who runs what:**

- `hib connect github --from-gh` runs `gh auth token --hostname github.com` on the laptop. Then it
  runs `sudo -n hibd connect github --source gh --json` on the host, with the token on stdin.
- `hib connect github --token` reads the token from a hidden prompt, or from stdin when stdin isn't
  a terminal. It never takes the token as an argument, which would land in shell history. An extra
  argument, or `--token=VALUE`, fails without echoing it. Then it runs
  `sudo -n hibd connect github --source token --json`.
- `hibd connect github` checks the token with `GET https://api.github.com/user`, from the host.
  That one call gives the login, ID and name from the body, and the scopes from the
  `X-OAuth-Scopes` header. If the check fails, nothing is stored. Otherwise it stores the secret,
  writes `github.json`, writes the machine setup into running machines, and replies with the
  account. A new token that fits the old placeholder keeps it, as `hib secret add` does.
- `hib status` runs `sudo -n hibd status --json`. It never wakes the host. If ssh can't reach the
  host and Azure says its VM isn't running, it says the host is asleep and exits 0.
- `hibd connect` and `hibd status` need root, because they read real values. Without root they
  say so before doing anything else.

**Packages:**

- `internal/github`: GitHub's API (`GetUser`), parsing `X-OAuth-Scopes`, the noreply address,
  checks for logins, tokens and names, and loading and saving `github.json`. Its constants are the
  secret's name `github`, its variable `GH_TOKEN` and its three sites.
- `internal/hibapi`: `GitHub`, `Status`, `Health`, `SecretHealth` and `SecretState`, with
  `GitHub.ScopeWarning`, `GitHub.Reconnect`, `Status.OK` and `PrintStatus`. `Response` gains
  `GitHub` and `Status`. Both are optional, so the protocol stays at version 1. This part is on
  main before the rest is built.
- `internal/machines`: `Setup` replaces `Config.EnvBlock`. It's what hibd writes into every machine
  after machine-init, and again whenever it changes: the managed environment block and git's
  identity. `Setup.Write` writes both through envd as root. `Setup.Hash` is what the daemon
  compares.

**GitHub calls:** each call to GitHub's API sends `Authorization: Bearer TOKEN`,
`Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28` and a `User-Agent`. It
refuses redirects, gives up after 10 seconds, and reads at most 1 MiB of body. A 401 is its own
error. Errors never quote the token or a request header. The API's base URL and HTTP client are
fields on `hibd.App` for tests only, never a setting, so no config file can send a token elsewhere.

**Checks on what GitHub says:** a login must match `^[A-Za-z0-9](-?[A-Za-z0-9]){0,38}$`, and an ID
must be positive. The name loses control characters and surrounding spaces, and is cut to 255
bytes. If it's then empty or starts with `-`, the login stands in. The noreply address is
`<id>+<login>@users.noreply.github.com`. A token must be 1 to 255 letters, digits and
underscores, as every GitHub token format is. Errors about a bad token never quote it.

**Scopes:** without an `X-OAuth-Scopes` header GitHub isn't saying (fine-grained and app tokens),
so nothing is checked. Otherwise a token without `repo` or `workflow` gets one warning that says
what machines can't do and how to fix it. From gh, that's `gh auth refresh -h github.com -s
workflow`, then `hib connect github --from-gh` again. From a token, it's a new classic token with
the missing scopes. The warning doesn't stop the connect.

**Machine setup:** git's identity goes into `/etc/gitconfig`, with `git config --system
--replace-all` for `user.name` and `user.email`. It runs through envd as root, as `sh -c` with a
fixed script and the values as positional arguments, so a name can't inject anything. A machine
without git gets no identity, and that's not an error. Machines made before M4 get it too.

- `hibd new` reads `github.json` (it runs as the login user, and the file is 0644) and writes the
  whole setup before it returns.
- `hibd connect github`, `hibd secret add` and `hibd secret rm` write it into every running named
  machine. Failures there are warnings.
- The daemon keeps the hash of the setup it last wrote to each machine, and rewrites any running
  machine whose hash is out of date: one that just woke, one that was missed, or every machine
  after a restart.
- Without `github.json`, machines get no identity, and one already written stays. A `github.json`
  that can't be read is an error, as a broken vault is.

**Image:** `images/dev/gitconfig` becomes `/etc/gitconfig` in the configure step. It makes gh git's
credential helper for `https://github.com` and `https://gist.github.com`, after an empty
`helper =` so no other helper answers first. It rewrites `git@github.com:` and
`ssh://git@github.com/` to `https://github.com/`. With `GH_TOKEN` set, gh answers as
`x-access-token` with the placeholder, and the proxy swaps it inside the decoded Basic auth (M3).
It's the system file rather than `gh auth setup-git` for `user`, so root and other users get it
too. Machines made before the image is rebuilt don't have it.

**Status:** `hibd status` reports:

- hibd's version.
- E2B: each URL in `E2B_HEALTH_URLS` once, with a 5-second timeout. Down if any fails.
- GitHub: the account in `github.json` while the vault has secret `github`, else nothing. Its
  scopes come from that secret's check.
- Each secret: one bound to `api.github.com` is checked with `GET /user`. 200 is `ok`, 401 is
  `revoked`, and anything else is `failed` with a short reason. Others are `unchecked`, since
  there's no general way to test a key. Checks run in parallel, at most 8 at once.
- A revoked `github` says "revoked: run hib connect github --from-gh again" (or `--token`, after
  how it was connected). Another revoked secret says "revoked: run hib secret add NAME again".

`hib status` prints the host, then `hibapi.PrintStatus`. It exits 1 if E2B is down or a secret is
revoked or failed, and 0 otherwise, also when the host is asleep.

**Tests:**

- Unit: `internal/github` covers scope parsing, the noreply address, login, token and name checks,
  `GetUser` against an `httptest` server (200, 401, redirect, big body, slow server) and the
  profile round trip. `internal/hibapi` covers the warning, `Reconnect`, `Status.OK` and
  `PrintStatus`.
- `internal/hibd`: `connect github` and `status` against a fake GitHub on
  `httptest.NewTLSServer`, through the App fields. This includes a revoked token (401), the token
  never in output or errors, and setup delivery through the fake engine.
- `cmd/hib`: `connect github` and `status` against fake ssh, gh and az, including an asleep host
  that status doesn't wake.
- `internal/egress`: real `git` clones and pushes through the proxy to `git http-backend` behind
  `net/http/cgi`, posing as `github.com` with a certificate from the test CA. The backend requires
  Basic `x-access-token:REAL`, and the client only has the placeholder. A credential helper script
  plays gh's part. `gh api user` through the proxy is M3's `TestRealGHClient`.
- End-to-end: `e2e/github_test.go`, with `TestGitHubFullFlow` and `TestWorkflowPush` against
  `solev/hibernaut-e2e`. Branches are `e2e-` and random hex. Cleanup is registered before the
  push. Setup removes `e2e-*` branches and PRs that killed runs left. `TestWorkflowPush` skips,
  saying how to fix it, while the connected token has no `workflow` scope.

## Tasks

- [ ] `hib connect github --from-gh`: reads `gh auth token` on the laptop. `hibd` checks it with
      `GET /user` on the host, then stores secret `github` (`GH_TOKEN`, bound to `github.com`,
      `api.github.com`, `uploads.github.com`) and your name, ID and login.
- [ ] Scope check from the `X-OAuth-Scopes` response header. Without `workflow`, warn and suggest
      `gh auth refresh -s workflow`.
- [ ] `hib connect github --token` for fine-grained tokens: same path, checked with `GET /user`.
      The token comes from a hidden prompt or stdin, never an argument.
- [ ] `dev` image: gh is git's credential helper for GitHub in `/etc/gitconfig`, and
      `git@github.com:` and `ssh://git@github.com/` are rewritten to `https://github.com/`.
- [ ] `hibd` writes git `user.name` and the noreply `user.email` into each machine at create, and
      again after reconnecting.
- [ ] `hib status`: host, E2B and each secret's health. GitHub is checked with `GET /user`; a 401
      says "revoked: run `hib connect github --from-gh` again".
- [x] Fixture repo `solev/hibernaut-e2e`: private, with a README and a workflow file.

## Tests

Unit:

- [ ] Scope parsing and warnings; the noreply address format.

Integration (fake GitHub signed by the test CA):

- [ ] Real `git` clones and pushes through the proxy to a local `git http-backend` posing as
      `github.com`. The backend sees the real token, the client only the placeholder.
- [ ] `gh api user` through the proxy against a fake API.
- [ ] `hib status` reports a revoked token (the fake answers 401).

End-to-end:

- [ ] `TestGitHubFullFlow`, on a fresh machine:
  - clone `solev/hibernaut-e2e` over HTTPS and again as `git@github.com:…`;
  - commit (author is your noreply address) and push a branch;
  - `gh pr create`, then `gh pr close --delete-branch`;
  - leak scan clean;
  - cleanup runs even when the test fails.
- [ ] `TestWorkflowPush`: push a change under `.github/workflows/` to a branch (needs `workflow`),
      then delete the branch.

## Done when

- [ ] `make lint test`, CI and `make e2e` are green.
- [ ] README: `hib connect github --from-gh`, the `workflow` scope, `hib status`.

## Changes

- 2026-10-03: `hibd` checks the token with `GET /user` on the host, instead of `hib` running
  `gh api user` on the laptop. One call gives the profile and the scopes, and no name crosses SSH
  as an argument.
- 2026-10-03: `hib connect github --token` takes the token from a hidden prompt or stdin, not as
  an argument, which would land in shell history.
- 2026-10-03: The image sets gh as git's credential helper in `/etc/gitconfig`, rather than
  `gh auth setup-git` for `user`, so every user in a machine gets it.
- 2026-10-03: The fixture repo `solev/hibernaut-e2e` exists, with Actions disabled. Its workflow
  only runs on `workflow_dispatch`, so it never runs.
