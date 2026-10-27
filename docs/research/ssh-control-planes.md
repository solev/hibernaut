# Products that use SSH as their control channel, for Hibernaut

**Date checked:** 2026-10-27.

**Summary**

- **SSH can be an application protocol.** Gitolite, Dokku and pico expose operations without an
  ordinary shell. Wish serves handlers directly. **Docs**, **Code** (§§A to E).
- **Multiple users do not require abandoning SSH.** Gitolite maps keys to application users behind
  one Unix account. Authorization remains a separate policy. **Docs** (§§A1, E1).
- **Machine-readable output is possible, not automatic.** Gitolite has `info -json`, Dokku has
  command-specific JSON and exe.dev has `--json`. Piku illustrates why subprocess exit codes still
  need explicit handling. **Docs**, **Code** (§§A2, B2, C2, F1).
- **exe.dev offers the closest HTTP bridge.** Its HTTPS endpoint dispatches the same commands as
  SSH. It adds bearer credentials, status codes and per-key limits without a second command model.
  **Docs** (§F2).
- **boxd is not an SSH-only counterexample.** Its current CLI, SDKs and console use a public gRPC
  API. SSH remains useful for machines and editors. **Docs** (§G).
- **Hibernaut can keep SSH behind its gateway.** A restricted submission key is a known pattern.
  Public HTTPS ingress and durable acceptance solve different problems from the host connection.
  **Docs**, **Code** (inference), §§A1, B1 and “What this means for Hibernaut”.

## Sources

**Tags.** **Docs** means first-party docs, specs or references. **Code** means pinned source.
**Unverified** means not established, not necessarily absent. Conclusions say “(inference)”.

[gitolite-ssh]: https://gitolite.com/gitolite/glssh.html
[gitolite-admin]: https://gitolite.com/gitolite/basic-admin.html
[gitolite-http]: https://gitolite.com/gitolite/http.html
[gitolite-shell]:
https://github.com/sitaramc/gitolite/blob/afbafec5efc609f36abd702cb972d1604c09d19a/src/gitolite-shell#L89-L186
[gitolite-info]:
https://github.com/sitaramc/gitolite/blob/afbafec5efc609f36abd702cb972d1604c09d19a/src/commands/info#L12-L69
[gitolite-exit]:
https://github.com/sitaramc/gitolite/blob/afbafec5efc609f36abd702cb972d1604c09d19a/src/lib/Gitolite/Common.pm#L109-L162
[git-pack]: https://git-scm.com/docs/pack-protocol
[dokku-users]: https://dokku.com/docs/deployment/user-management/
[dokku-remote]: https://dokku.com/docs/deployment/remote-commands/
[dokku-logs]: https://dokku.com/docs/deployment/logs/
[sshcommand]:
https://github.com/dokku/sshcommand/blob/2c76e35ea82f930f85f322e04ff7fc70fc16cf88/sshcommand#L166-L204
[dokku-acl]: https://github.com/dokku-community/dokku-acl
[dokku-pro]: https://pro.dokku.com/docs/getting-started/
[dokku-pro-features]: https://pro.dokku.com
[piku-docs]: https://github.com/piku/piku
[piku-manage]: https://piku.github.io/manage.html
[piku-keys]:
https://github.com/piku/piku/blob/40d3eb3810f7314e415668a9a5a17e345fb78ccd/piku.py#L267-L277
[piku-run]:
https://github.com/piku/piku/blob/40d3eb3810f7314e415668a9a5a17e345fb78ccd/piku.py#L1605-L1677
[wish-docs]: https://github.com/charmbracelet/wish
[wish-code]:
https://github.com/charmbracelet/wish/blob/2af05484795152f53d9ad10b5be5748a4bec3c95/wish.go#L15-L92
[soft-docs]:
https://github.com/charmbracelet/soft-serve/blob/e87b6c01725c8f5fc3632dfc7e01c1d3f4573840/README.md
[soft-cli]:
https://github.com/charmbracelet/soft-serve/blob/e87b6c01725c8f5fc3632dfc7e01c1d3f4573840/pkg/ssh/middleware.go#L99-L162
[pico-start]: https://pico.sh/getting-started
[pico-services]: https://pico.sh
[pico-pgs]: https://pico.sh/pgs
[pico-ui]: https://pico.sh/ui
[pico-tuns]: https://pico.sh/tuns
[pico-access]: https://pico.sh/access-control
[pico-token]: https://pico.sh/api-token
[pico-ssh]:
https://github.com/picosh/pico/blob/bdd5983bd8b4d087aa2acfd2d33727cf57549f58/pkg/apps/pgs/ssh.go#L20-L65
[pico-http]:
https://github.com/picosh/pico/blob/bdd5983bd8b4d087aa2acfd2d33727cf57549f58/pkg/apps/auth/api.go#L885-L906
[exe-ssh]: https://exe.dev/docs/api.md
[exe-http]: https://exe.dev/docs/https-api.md
[exe-run]: https://exe.dev/docs/https-api-run-on-vm.md
[exe-sharing]: https://exe.dev/docs/sharing.md
[exe-teams]: https://exe.dev/docs/teams/members.md
[exe-keys]: https://exe.dev/docs/vm-scoped-ssh-keys.md
[exe-undocumented]: https://exe.dev/docs/faq/undocumented.md
[boxd-architecture]: https://boxd.sh/blog/architecture/how-boxd-works
[boxd-cli]: https://docs.boxd.sh/cli/commands.md
[boxd-auth]: https://docs.boxd.sh/cli/authentication.md
[boxd-api]: https://docs.boxd.sh/reference/grpc-api.md
[boxd-jobs]: https://docs.boxd.sh/guides/automations/jobs.md
[boxd-triggers]: https://boxd.sh/blog/automations-that-sleep
[boxd-mobile]: https://docs.boxd.sh/guides/mobile.md
[sprites-cli]: https://docs.fly.io/sprites/cli/commands.md
[sprites-create]: https://docs.fly.io/sprites/api/sprites/create-a-sprite.md
[sprites-exec]: https://docs.fly.io/sprites/api/exec/execute-command.md
[github-ssh]:
https://docs.github.com/en/authentication/connecting-to-github-with-ssh/testing-your-ssh-connection
[github-api]: https://docs.github.com/en/rest/about-the-rest-api/about-the-rest-api
[github-versions]: https://docs.github.com/en/rest/about-the-rest-api/api-versions
[gitlab-shell]: https://docs.gitlab.com/development/gitlab_shell/
[gitlab-api]: https://docs.gitlab.com/api/rest/
[devpod-overview]: https://devpod.sh/docs/what-is-devpod
[devpod-architecture]: https://devpod.sh/docs/how-it-works/overview
[devpod-providers]: https://devpod.sh/docs/developing-providers/quickstart
[devpod-ssh]:
https://github.com/loft-sh/devpod/blob/5a0efcbff6610ab114b421f68a890739a452e66b/pkg/ssh/server/ssh.go#L34-L104
[devpod-desktop]:
https://github.com/loft-sh/devpod/blob/5a0efcbff6610ab114b421f68a890739a452e66b/desktop/src-tauri/src/commands/config.rs#L49-L70
[devpod-json]:
https://github.com/loft-sh/devpod/blob/5a0efcbff6610ab114b421f68a890739a452e66b/desktop/src-tauri/src/commands/list_workspaces.rs#L16-L44
[devpod-release]: https://api.github.com/repos/loft-sh/devpod/releases?per_page=1
[devpod-fork]:
https://github.com/skevetter/devpod/blob/8dec889699a91e48ff0db177c829f8d36ab58a92/README.md
[openssh-keys]: https://man.openbsd.org/sshd.8#AUTHORIZED_KEYS_FILE_FORMAT
[openssh-force]: https://man.openbsd.org/sshd_config.5#ForceCommand
[openssh-exit]: https://man.openbsd.org/ssh.1#EXIT_STATUS

## Comparison table

| System | SSH carries | Identity and dispatch | Beyond SSH | Evidence |
|---|---|---|---|---|
| Gitolite | Git and allowlisted commands | Key to virtual user, per-key forced command | Smart HTTP, external web tools | **Docs**, **Code** §A |
| Dokku | Git deployment, management and logs | Named keys under `dokku`, `sshcommand` | Pro web UI, REST API and HTTPS Git | **Docs**, **Code** §B |
| Piku | Git deployment, commands and logs | Shared account, per-key forced command | Public HTTP apps | **Docs**, **Code** §C |
| Wish / Soft Serve | TUI, commands and Git | Application SSH server, key-based users | Soft Serve HTTP Git, LFS and webhooks | **Docs**, **Code** §D |
| pico.sh | Signup, file publishing, commands and tunnels | Account keys or scoped SSH certificates, custom servers | Published websites, tunnel console and token APIs | **Docs**, **Code** §E |
| exe.dev | Control commands, REPL and VM access | Account keys, VM-scoped keys and SSH proxies | Same commands through HTTPS, web sharing and teams | **Docs** §F |
| boxd | Guest SSH and editor/file access | Linked keys through its proxy | gRPC control API, SDKs, console and grpc-web | **Docs** §G |
| Sprites | Optional SSH compatibility over its CLI proxy | Organization API token | REST control, WebSocket execution and HTTP execution | **Docs** §H |
| GitHub / GitLab | Git transport and related Git operations | Account keys, Git-specific dispatch | Web UI, HTTPS APIs and webhooks | **Docs** §I |
| DevPod | Workspace shell, forwarding and agent communication | Provider credentials and deployed agents | Local desktop UI, provider/cloud APIs | **Docs**, **Code** §J |

## A. Gitolite

### A1. A key selects a virtual user and a restricted dispatcher

**Docs**. Users share a Unix account such as `git`. Each key's `command=` invokes
`gitolite-shell USER`. It reads `SSH_ORIGINAL_COMMAND`, authorizes the operation and delegates to
Git. An update hook checks branch and tag writes. SSH authenticates, while Gitolite authorizes
([SSH mechanics][gitolite-ssh]).

**Docs**. Admins push keys and rules through `gitolite-admin`. `keydir/home/alice.pub` and
`keydir/work/alice.pub` can identify the same user. Rules distinguish reads from writes and finer
write restrictions ([administration][gitolite-admin]).

### A2. Git has its own wire contract inside SSH

**Code**. The dispatcher allowlists Git verbs and configured commands, validates names and logs
the application user, requested operation and source address
(`src/gitolite-shell:89-186`, [source][gitolite-shell]).
`info -json` produces structured inventory. Child command failures propagate through `_system`
(`src/commands/info:12-69`, [source][gitolite-info] and
`src/lib/Gitolite/Common.pm:148-162`, [source][gitolite-exit]).

**Docs**. Git negotiates capabilities and streams pack data over the connection
([protocol][git-pack]). Gitolite also supports smart HTTP and custom commands through URLs.
Its HTTP guide warns that failures do not appear to set exit codes as SSH failures do.
It does not describe HTTP as a replacement management platform ([HTTP guide][gitolite-http]).

## B. Dokku

### B1. Named keys are identity, not automatic tenant isolation

**Docs**. Management uses `ssh -t dokku@HOST COMMAND`. The official client is a Bash SSH wrapper.
`dokku ssh-keys:add NAME` records a named public key. The SSH username remains `dokku`.
A key name containing `admin` grants remote key-add privileges ([remote commands][dokku-remote],
[users][dokku-users]).

**Code**. `sshcommand` writes a per-key forced command containing server-provided `FINGERPRINT`
and `NAME`, the configured dispatcher and `SSH_ORIGINAL_COMMAND`. Default restrictions disable
agent, user-rc, X11 and port forwarding. This is `authorized_keys` dispatch, not a custom SSH
server (`sshcommand:166-204`, [source][sshcommand]).

**Docs**. Core documents the `user-auth` extension point for command scoping.
The community `dokku-acl` plugin adds app and command rules. Its defaults still allow broad
access until configured, including repository reads. It explicitly warns that it has not been
extensively security audited ([users][dokku-users], [plugin author's README][dokku-acl]).

### B2. Pro adds the browser and automation layer

**Docs**. Output is command-specific, including `ssh-keys:list --format json` and `--quiet`.
The client guide suggests matching its version to the server. `logs --tail` streams, but old
deployment logs need external retention ([users][dokku-users], [client][dokku-remote],
[logs][dokku-logs]).

**Docs**. Dokku Pro says core has no built-in web interface or API. Pro adds a web UI, REST API
and Git-over-HTTPS deployment without giving out SSH access. Its feature page describes the API
as curl-friendly and HTTP push as useful behind firewalls. This is an official self-hosted add-on,
not evidence that core SSH was removed ([Pro introduction][dokku-pro],
[features][dokku-pro-features]).

## C. Piku

### C1. A small forced-command PaaS keeps the trusted-owner model

**Docs**. Deployment is `git push` to `piku@HOST:APP`. Commands change configuration and scale
workers. The helper infers host and app from Git and supports a PTY for `run bash`
([README][piku-docs], [management][piku-manage]).

**Code**. Key setup writes `command="FINGERPRINT=... NAME=default ... $SSH_ORIGINAL_COMMAND"`
with forwarding restrictions. The shown mapping uses one default name, not a separate app role
per key (`piku.py:267-277`, [source][piku-keys]).

### C2. SSH transports logs but cannot repair application exit semantics

**Code**. `logs` tails server files continuously. `run` attaches the subprocess to stdin, stdout
and stderr, then calls `communicate()`. It does not return or exit with `p.returncode`.
Do not assume its remote command status is the child process status (inference)
(`piku.py:1605-1618,1663-1677`, [source][piku-run]).

**Docs**. uWSGI manages workers after deployment ([README][piku-docs]).
**Unverified**. These sources establish no supported REST control API, per-user ACL model or
version-negotiation contract. Community extensions were not exhaustively searched.

## D. Charm Wish and Soft Serve

### D1. Wish runs application handlers instead of forcing an OpenSSH command

**Docs**. Wish is a Go SSH application library. Its benefits include key identity without HTTPS
certificate setup. Bubble Tea middleware connects a TUI to a session's PTY and resize events.
Git and command-access middleware are separate options ([README][wish-docs]).

**Code**. The checked Wish implementation constructs a `charm.land/ssh` server, not an OpenSSH
`ForceCommand` setup. `Fatal` writes stderr and exits 1. Its constructor accepts connections by
default, so an application must configure its authentication policy (inference)
(`wish.go:15-92`, [source][wish-code]).

### D2. Soft Serve combines SSH management with HTTP Git and event delivery

**Docs**. Soft Serve exposes a repository TUI plus SSH commands such as `repo tree`, `repo blob`,
user management and collaborator management. One public key belongs to one user. Repository
permissions and administrators are application concepts. Users mint expiring HTTP access tokens
through SSH ([README, Server Access and Authorization][soft-docs]).

**Code**. Soft Serve dispatches a TUI or Cobra commands, binds stdin, stdout and stderr to the
session and exits 1 on errors (`pkg/ssh/middleware.go:99-162`, [source][soft-cli]).

**Docs**. HTTP supports Git and LFS, with user tokens for authenticated repository access.
SSH commands configure outgoing webhooks. Hooks can launch CI work on pushes.
Soft Serve is now sunset and receives no further security updates
([README, HTTP, Webhooks, Hooks and opening notice][soft-docs]).

## E. pico.sh services

### E1. SSH signup grows into certificate-scoped team access

**Docs**. `ssh pico.sh` opens the signup TUI. A username and public key establish the account.
The TUI also manages keys and API tokens ([getting started][pico-start], [UI][pico-ui]).

**Docs**. pgs publishes files through rsync, scp or SFTP. prose accepts Markdown through scp.
tuns uses reverse SSH forwarding. `pipe.pico.sh` exposes publish and subscribe commands over SSH
([services][pico-services], [pgs][pico-pgs], [tuns][pico-tuns]).
**Code**. Current pgs uses pico's custom `pssh` server with explicit transfer, command and
forwarding middleware, not `authorized_keys command=`
(`pkg/apps/pgs/ssh.go:20-65`, [source][pico-ssh]).

**Docs**. Ordinary keys grant account-wide admin access. SSH certificates add service principals
such as `pgs`, `tuns` or `admin`, expiry and readable audit identity for teams and CI.
The owner manages the CA ([access control][pico-access]).

### E2. Web surfaces coexist with the SSH management plane

**Docs**. `ssh pico.sh logs` streams logs for Unix pipelines. Commands have their own output
formats, including JSON form data in pgs. Published sites use HTTPS. tuns offers a browser request
console and recommends autossh to restart dropped tunnels ([UI][pico-ui], [pgs][pico-pgs],
[tuns][pico-tuns]).

**Docs**. Tokens cover cases where pico cannot use SSH public-key authentication
([guide][pico-token]). **Code**. Auth routes include OAuth, token introspection and payment webhooks
(`pkg/apps/auth/api.go:885-906`, [source][pico-http]).

## F. exe.dev

### F1. The SSH API already has scriptable commands and scoped identities

**Docs**. The API guide calls SSH its API and shows `ssh exe.dev ls --json` and `new --json`.
An unknown key receives a registration instruction with exit status 1. Registration uses the
interactive SSH flow. A no-plan error can already be JSON ([SSH API][exe-ssh]).

**Docs**. VM-scoped keys access only the named VM. Shares distinguish Web from Root access, which
also grants SSH and browser Terminal. Team commands add members and roles alongside a web roster
([keys][exe-keys], [sharing][exe-sharing], [teams][exe-teams]).

### F2. HTTPS wraps the same command model and exposes its limitations

**Docs**. `POST https://exe.dev/exec` accepts the command as its body and enables JSON for
control-command responses. exe.dev says this leaves “only one API to learn”.
Bearer tokens can expire and allowlist command names. Failures distinguish malformed input,
permission denial, unknown commands, command failure and timeout. HTTP 429 limits are per SSH
key ([HTTPS API][exe-http]).

**Docs**. The HTTP wrapper has no stdin or PTY, a 64 KB request limit and a 30-second timeout.
VM execution streams combined raw output with an `X-Exe-Exit` trailer. Because browser `fetch`
cannot read trailers, NDJSON mode instead separates base64 stdout, stderr and the final exit code.
A missing final exit/error record means truncation ([HTTPS API][exe-http], [VM execution][exe-run]).

**Docs**. Undocumented commands may break. **Unverified**. No broader compatibility guarantee or
private dispatch implementation was established ([FAQ][exe-undocumented]).

## G. boxd.sh

### G1. Current control uses gRPC even though the architecture story is SSH-first

**Docs**. The architecture post describes SSH, key identities and no web console, but mentions
JWT/gRPC access too. Current docs say the CLI, SDKs and console use `boxd.api.v1.BoxdApi` at
`boxd.sh:9443`. The older slogan is not an SSH-only guarantee (inference)
([architecture][boxd-architecture], [API][boxd-api]).

**Docs**. Current login uses a browser device flow. Organization-scoped API keys exchange for
short-lived JWTs. Member keys and organization service keys have different authority.
Plain SSH, scp, rsync and editor access still work through managed host aliases and per-machine
proxy ports. `boxd connect` itself uses the boxd API ([authentication][boxd-auth],
[CLI, Interactive access and Editor integration][boxd-cli]).

### G2. Browser access and sleeping automations need more than SSH

**Docs**. CLI commands support `--json` and exec forwards remote exit codes. gRPC has standard
error statuses and bidirectional exec streams carrying stderr flags and a final exit code.
The API also supports grpc-web for browsers and Workers, but not an interactive PTY.
It retains h2c for older clients alongside TLS
([CLI][boxd-cli], [API, Streaming RPCs and Calling from a browser][boxd-api]).

**Docs**. boxd says its first automation poller kept machines awake. It moved trigger receipt and
wake responsibility onto the platform. Current job docs describe held events, persisted jobs and
log following. This is an unusually close precedent for hibgate, not proof of unlimited event
retention (inference) ([engineering account][boxd-triggers], [jobs][boxd-jobs]).

**Docs**. Phones can still use SSH. boxd's mobile web page creates a machine-specific SSH token
and opens an SSH app. It does not offer machine creation from that phone page
([mobile guide][boxd-mobile]).

## H. Sprites

### H1. The terminal client is an API client, not an SSH control plane

**Docs**. Creation uses bearer-authenticated `POST /v1/sprites` and JSON.
The CLI uses organization tokens and exposes exec, console, checkpoints and sessions. Execution
normally uses WebSockets. HTTP POST is a non-TTY alternative for clients that cannot handle them.
`sprite proxy --ssh` provides SSH compatibility over the CLI proxy. Console sessions can detach
and later reattach ([create API][sprites-create], [CLI][sprites-cli], [exec API][sprites-exec]).

**Docs**. The CLI documents exit codes and version-specific upgrades ([CLI][sprites-cli]).
For HTTP wake, see `ingress-and-triggers.md` §E2 rather than repeating it here.

## I. GitHub and GitLab

### I1. Git transport and resource management have different front doors

**Docs**. GitHub maps keys to accounts and denies shell access. Its successful SSH test exits 1.
GitLab Shell identifies the user, allows Git commands, asks Rails to authorize and streams data to
Gitaly. `AuthorizedKeysCommand` returns a forced command
([GitHub SSH][github-ssh], [GitLab Shell][gitlab-shell]).

**Docs**. GitHub's HTTPS API serves scripts, apps and resource automation, with separate webhooks.
GitLab's REST API uses HTTP methods, JSON and `/api/v4`. GitHub has an explicit API version header
and support policy. GitLab Shell also implements application-level rate limits and correlation-ID
logging. Neither structured policies nor auditing are impossible over SSH
(inference) ([GitHub API][github-api], [versioning][github-versions],
[GitLab API][gitlab-api], [Shell operations][gitlab-shell]).

## J. DevPod

### J1. The client owns provisioning and deploys a matching remote agent

**Docs**. DevPod is client-only. Providers use `provider.yaml` commands for resource lifecycle.
The CLI and desktop tunnel through cloud APIs, Kubernetes or SSH. The deployed agent starts SSH
on stdio for IDEs and forwarding ([overview][devpod-overview], [providers][devpod-providers],
[architecture][devpod-architecture]).

**Docs**. Deploying the client's agent avoids version conflicts, according to the guide.
Port forwarding requires an active IDE or SSH session. Provider status has stdout values such as
`Running` and `Busy` ([architecture][devpod-architecture], [providers][devpod-providers]).
**Code**. The agent's custom SSH server checks supplied public keys, not a central account
database (`pkg/ssh/server/ssh.go:34-104`, [source][devpod-ssh]).

**Code**. The desktop app launches the DevPod CLI sidecar and parses JSON stdout for workspace
listing. A GUI does not inherently require a remote HTTP management service (inference)
(`desktop/src-tauri/src/commands/config.rs:49-70`, [source][devpod-desktop] and
`desktop/src-tauri/src/commands/list_workspaces.rs:16-44`, [source][devpod-json]).

### J2. The release and fork status warrant caution

**Docs**. The latest upstream release found was `v0.7.0-alpha.34`, dated 2025-06-23.
The community fork directs development toward Devsy ([release list][devpod-release],
[fork announcement][devpod-fork]).
**Unverified**. No upstream maintainer declaration establishing a formal shutdown or current
support commitment was found. The fork's direction is not an upstream product announcement.

## What this means for Hibernaut

### SSH remains a good private transport, not a complete task contract

**Docs**, **Code** (inference). D1 remains reasonable for one laptop and host. Gitolite and Dokku
establish restricted dispatch. exe.dev establishes JSON commands. Keep secrets and prompts on stdin.
SSH need not mean an interactive shell or require REST to start tasks
(§§A, B and F, `docs/spec.md` §6 D1).

**Docs** (inference). The boundary changes when GitHub, a browser or another service starts work.
Those callers need a reachable HTTPS front door. Phones can use SSH apps, but a phone web UI needs
a web-facing adapter. SDKs and scripts can use an SSH library or process, while an HTTP API
avoids distributing SSH configuration and private keys to every integration (§§B2, F2, G2 and I).

**Docs** (inference). An API on a deallocated host cannot receive events or power that host on.
Keep durable receipt and Azure startup outside it, as boxd does for triggers. See
`ingress-and-triggers.md` §§I4, J and K for ingress, `cloud-agent-internals.md` §A10 for workers and
`always-on-agents.md` §§B10 and E1 for sleeping compute and delivery.

### The gateway can use a forced command, with a deliberately small authority

**Docs**. OpenSSH's `command=` replaces the requested command but does not itself disable
forwarding. `restrict` disables forwarding, PTYs and user rc execution. `from=` can restrict source
addresses or CIDRs. `ForceCommand` is a server-configuration alternative, not a replacement for
those other restrictions. Key comments are not trusted identity fields
([key options][openssh-keys], [ForceCommand][openssh-force]).

**Docs**, **Code** (inference). Give hibgate a separate key and service account with a fixed
submission wrapper, not owner sudo authority. Illustrative `authorized_keys` entry:

```text
restrict,from="10.0.0.4",command="/usr/local/bin/hibgate-submit" ssh-ed25519 AAAA...
```

**Docs**, **Code** (inference). Bind `from=` to the stable source address and pin the host key.
Use noninteractive SSH without a PTY. Keep the wrapper and key policy outside the account's write
access. Validate bounded, versioned JSON on stdin. Treat `SSH_ORIGINAL_COMMAND` as untrusted, never
shell code. Restrict any sudo delegation to this operation (§§A1, B1, [OpenSSH][openssh-keys]).

**Docs**, **Code** (inference). Give each integration a revocable key and server-controlled
identity. The key authenticates hibgate, not the originating GitHub user. Verify that actor and
authorize the repo and operation. Audit fingerprint, actor, request ID, task ID and operation,
plus outcomes and timestamps without secrets. A shared Unix username is not enough. Rate-limit
running concurrency per principal. SSH can enforce this in the application just as HTTP can
(§§A1, B1, E1, F2 and I1).

### Durable acceptance matters more than whether the envelope is HTTP or SSH

**Docs** (inference). Persist a task and idempotency key before acknowledging it. Return its ID
quickly. Let the daemon own execution independently of the connection. Offer status, cancellation
and replayable logs separately. If a submission loses its reply, reconcile by request ID rather
than blindly rerunning it. A live SSH stream or HTTP 200 alone is not that contract
(`agent-task-apis.md` §§A3, C3 and “What an agent task API must offer”, §§F2, G2 and H1 here).

**Docs** (inference). Define JSON results, typed errors and version checks even if SSH stays.
OpenSSH returns remote status or 255 on SSH error, not a Hibernaut error taxonomy.
A gateway HTTP facade can map that same task model to HTTP without duplicating orchestration
([SSH exit status][openssh-exit], §§F2 and G1).

## Gaps

- **Unverified**. These are published contracts and selected code, not production tests.
- **Unverified**. General compatibility guarantees were not found for Gitolite management commands,
  Piku, pico, Soft Serve or boxd's older SSH interface. Git negotiation, Dokku's matching client
  guidance and DevPod's agent deployment are narrower mechanisms (§§A2, B2 and J1).
- **Unverified**. No Piku tenant RBAC or supported HTTP API was established (§C).
- **Unverified**. exe.dev's private dispatcher and boxd's server implementation were not inspected.
  Their specific use of OpenSSH forced commands versus custom SSH handlers is not established.
  boxd's gRPC docs do not establish a general REST machine API (§§F and G).
- **Unverified**. Full HTTP mirrors of pico or Soft Serve's SSH management were not established.
  Neither Sprites' `/v1` naming nor Wish's SSH library establishes a compatibility support window.
