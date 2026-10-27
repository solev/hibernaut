# How self-hosted platforms expose their control plane, for Hibernaut

**Date checked:** 2026-10-27.

**Summary**

- **An API does not require a public port.** Docker and Podman carry HTTP API requests over SSH
  to a Unix socket. Their remote CLIs are not remote shell command wrappers. **Docs**, §A and §B.
- **SSH is an established transport, not a dead end.** A resource API can replace command
  dispatch while keeping today's keys and firewall rules. **Code** (inference), §A1 and §B1.
- **Following work needs a recovery contract.** Incus requires refetching after disconnect.
  OpenHands replays durable events but not live deltas. **Docs** and **Code**, §C3 and §F3.
- **A phone does not require a public control plane.** OpenHands documents phone access through
  Tailscale. tsnet can identify the connecting tailnet user. **Docs**, §F2 and §G.
- **Product names hide API changes.** Coder Tasks is removed. Daytona's maintained core is
  private. OpenHands separates Canvas, Agent Server and Sandbox Server. **Docs**, §D3 to §F1.
- **Changing transport cannot wake a deallocated host.** Hibernaut still needs Azure startup
  and an always-on inbox for external triggers. **Docs** (inference), final section.

## Sources

**Tags.** **Docs** means first-party documentation. **Code** means implementation at the linked
commit or tag and line range. **Unverified** marks unresolved questions. Conclusions carry
"(inference)". Tables inherit their cited sections' tags. Live docs may differ from releases.

[docker-api]: https://docs.docker.com/reference/api/engine/
[docker-protect]: https://docs.docker.com/engine/security/protect-access/
[docker-security]: https://docs.docker.com/engine/security/#docker-daemon-attack-surface
[docker-ssh]:
https://github.com/docker/cli/blob/66786b775ad2f71a4f6de79965a6f3d2efb08852/cli/connhelper/connhelper.go#L25-L69
[docker-stdio]:
https://github.com/docker/cli/blob/66786b775ad2f71a4f6de79965a6f3d2efb08852/cli/command/system/dial_stdio.go#L16-L63
[docker-wire]: https://docs.docker.com/reference/api/engine/version/v1.56.yaml
[docker-events]: https://docs.docker.com/reference/cli/docker/system/events/
[podman-service]: https://docs.podman.io/en/latest/markdown/podman-system-service.1.html
[podman-connection]: https://docs.podman.io/en/latest/markdown/podman-system-connection-add.1.html
[podman-remote]: https://docs.podman.io/en/latest/markdown/podman-remote.1.html
[podman-ssh]:
https://github.com/containers/podman/blob/83d9108ed4ef65e66e3d19b7e355a7fc2e0e4017/pkg/bindings/connection.go#L178-L304
[podman-stdio]:
https://github.com/containers/podman/blob/83d9108ed4ef65e66e3d19b7e355a7fc2e0e4017/pkg/bindings/connection_stdio.go#L29-L75
[podman-logs]: https://docs.podman.io/en/latest/markdown/podman-logs.1.html
[podman-run]: https://docs.podman.io/en/latest/markdown/podman-run.1.html
[podman-history]: https://podman.io/blogs/2020/08/01/deprecate-and-remove-varlink-notice
[incus-rest]:
https://github.com/lxc/incus/blob/5d036d636d1e127e699e7cec7b349f034db73bdf/doc/rest-api.md#L1-L92
[incus-expose]:
https://github.com/lxc/incus/blob/5d036d636d1e127e699e7cec7b349f034db73bdf/doc/howto/server_expose.md#L1-L68
[incus-auth]:
https://github.com/lxc/incus/blob/5d036d636d1e127e699e7cec7b349f034db73bdf/doc/authentication.md#L4-L218
[incus-permissions]:
https://github.com/lxc/incus/blob/5d036d636d1e127e699e7cec7b349f034db73bdf/doc/authorization.md#L1-L50
[incus-events]:
https://github.com/lxc/incus/blob/5d036d636d1e127e699e7cec7b349f034db73bdf/doc/events.md#L1-L27
[incus-exec]:
https://github.com/lxc/incus/blob/5d036d636d1e127e699e7cec7b349f034db73bdf/doc/rest-api.yaml#L12613-L12640
[coder-api]: https://coder.com/docs/reference/api
[coder-tokens]: https://coder.com/docs/admin/users/sessions-tokens
[coder-roles]: https://coder.com/docs/admin/users/groups-roles
[coder-network]: https://coder.com/docs/admin/networking
[coder-login]:
https://github.com/coder/coder/blob/7eacfe95b28dd776bafbd7d5f04b9b7188ab1930/cli/login.go#L335-L391
[coder-ssh]:
https://github.com/coder/coder/blob/7eacfe95b28dd776bafbd7d5f04b9b7188ab1930/cli/ssh.go#L502-L595
[coder-migration]:
https://github.com/coder/coder/blob/7eacfe95b28dd776bafbd7d5f04b9b7188ab1930/docs/ai-coder/agents/tasks-to-chats-migration.md#L5-L101
[daytona-auth]: https://www.daytona.io/docs/en/authentication/
[daytona-keys]: https://www.daytona.io/docs/en/api-keys/
[daytona-architecture]: https://www.daytona.io/docs/en/architecture/
[daytona-api]: https://www.daytona.io/docs/openapi.json
[daytona-toolbox]: https://www.daytona.io/docs/toolbox-openapi.json
[daytona-process]: https://www.daytona.io/docs/en/process-code-execution/
[daytona-compose]:
https://github.com/daytonaio/daytona/blob/v0.190.0/docker/README.md#L1-L57
[daytona-status]:
https://github.com/daytonaio/daytona/blob/ec4c21b2d597091ac09ecc278f3bcc172575a987/README.md#L1-L7
[daytona-byoc]: https://www.daytona.io/docs/en/bring-your-own-compute/
[oh-architecture]: https://docs.openhands.dev/openhands/usage/agent-canvas/architecture
[oh-v1]: https://docs.openhands.dev/openhands/usage/api/v1
[oh-server]: https://docs.openhands.dev/sdk/arch/agent-server
[oh-cloud]: https://docs.openhands.dev/openhands/usage/cloud/cloud-api
[oh-mobile]: https://docs.openhands.dev/openhands/usage/agent-canvas/mobile-access
[oh-vm]: https://docs.openhands.dev/openhands/usage/agent-canvas/backend-setup/vm
[oh-create]:
https://docs.openhands.dev/sdk/guides/agent-server/api-reference/conversations/start-conversation
[oh-run]:
https://docs.openhands.dev/sdk/guides/agent-server/api-reference/conversations/run-conversation
[oh-replay]:
https://github.com/OpenHands/software-agent-sdk/blob/d0d9d81324210414462fb676b377e0563a08a0cb/openhands-agent-server/openhands/agent_server/session_protocol.py#L1-L21
[oh-local-auth]:
https://github.com/OpenHands/sandbox-server/blob/43e91604ccd0a9960c8d5c52faadd60b30f10e63/openhands/app_server/user_auth/default_user_auth.py#L23-L42
[tsnet]: https://tailscale.com/docs/features/tsnet
[tsnet-example]: https://tailscale.com/docs/features/tsnet/how-to/create-basic-tsnet-app
[tailscale-policy]: https://tailscale.com/docs/features/access-control
[tailscale-ios]: https://tailscale.com/docs/install/ios
[e2b-key]: https://docs.e2b.dev/api-key
[e2b-api]: https://docs.e2b.dev/api-reference/sandboxes/list-sandboxes
[github-hooks]:
https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks
[hib-spec]: ../spec.md

**Docs** and **Code**. Existing notes retain their pinned sources. `agent-task-apis.md` covers
contracts. `open-source-agent-platforms.md` §A and §B cover execution. `ingress-and-triggers.md`
§I2 and §K cover tunnels and GitHub. `e2b-capabilities.md` §1 and §5 cover lifecycle and envd.

## Comparison table

**Docs** and **Code**, §§A to H. Suggested browser/phone adapters are deductions (inference).
HTTPS reachability does not make an arbitrary GitHub webhook payload a valid control API call.
An adapter validates and translates the delivery. **Docs** (inference), [GitHub][github-hooks].

| System | Public control contract | Reachability and enrollment | Following work | Browser or phone |
|---|---|---|---|---|
| Docker | Versioned REST and SDKs | Unix socket, SSH keys or TCP with mTLS | HTTP logs, attach and JSON event stream | Separate UI or adapter over an authenticated route |
| Podman remote | Versioned REST, Docker compatibility and Libpod | Unix socket, SSH account or TCP with mTLS | Log follow and attach | Separate UI or adapter |
| Incus | REST `/1.0` and feature extensions | Unix socket, HTTPS certificates, trust token or OIDC | Operations and WebSockets, refetch after gaps | HTTPS with browser-compatible authentication |
| Coder | REST `/api/v2` | Reachable control plane, browser session or API token | Historical task polling, current chat WebSocket | Dashboard over reachable HTTPS |
| Daytona | REST OpenAPI and SDKs | HTTPS API key or browser-login JWT | Async session command IDs and WebSocket logs | Dashboard, no phone-specific guarantee checked |
| OpenHands | App REST `/api/v1` and Agent Server REST/WebSocket | Local server, configured session keys or Cloud account | Startup polling and replayable session events | Canvas has a documented phone route |
| tsnet | Application chooses its API | Private tailnet device enrollment and policy | Application chooses streaming and replay | Tailscale phone app plus normal browser |
| E2B | REST lifecycle and guest Connect RPC/REST | API key, separate envd access token | Process streams and reconnect | Console or an application backend, not a phone API |

## A. Docker

### A1. The CLI uses REST even when its transport is SSH

**Docs.** Engine offers a documented REST API and Go/Python SDKs. URLs include an API version.
The CLI and daemon negotiate supported versions. The default listener is a Unix socket.
`DOCKER_HOST=ssh://user@host` or `docker context create --docker host=ssh://user@host remote`
selects SSH without publishing a Docker TCP port. [API][docker-api], [access][docker-protect].

**Code.** The client runs `ssh` with the remote command `docker system dial-stdio`.
An explicit socket path adds `--host=unix://PATH`. That hidden command copies stdin and stdout
to the daemon connection. It carries HTTP bytes, not a remote `docker run` invocation per action.
[SSH helper][docker-ssh], [stdio helper][docker-stdio].

### A2. A trusted Docker client is a powerful host administrator

**Docs.** The SSH account needs socket permission. Alternatively, TLS on TCP port 2376 can
require client certificates signed by a trusted CA. New TLS clients receive a certificate and
key. Docker warns that possessing those keys gives root-equivalent control. Authorization
plugins can add finer permissions. Prefer a trusted network or VPN even with HTTPS.
[Access][docker-protect], [security][docker-security].

### A3. HTTP streams and saved container identity support detached work

**Docs.** Create with `POST /containers/create` then `POST /containers/{id}/start`.
Follow `/containers/{id}/logs?follow=true` as an HTTP response-body stream, not SSE.
Attach hijacks HTTP for bidirectional IO and also has a WebSocket endpoint. Reattach by ID or
request logs with `since`. `/events` streams JSON, but only 256 recent events are retained.
[Wire reference][docker-wire], [event retention][docker-events].

**Docs.** Docker changed localhost TCP to a Unix socket in 0.5.2 because of cross-site request
forgery exposure. [Security history][docker-security].

## B. Podman

### B1. Remote mode adds a REST service without changing local Podman's model

**Docs.** `podman system service` offers versioned Docker-compatible and native Libpod REST
APIs. Unsupported version strings are not rejected. Systemd can socket-activate it.
Rootless uses `$XDG_RUNTIME_DIR/podman/podman.sock`. Rootful uses `/run/podman/podman.sock`.
`podman system connection add --socket-path PATH NAME user@host` records the endpoint.
Remote CLI commands use this REST API. [Service][podman-service], [connections][podman-connection],
[remote CLI][podman-remote].

**Code.** At the checked main commit, SSH normally opens a Unix-socket forwarding channel.
If the server refuses that channel, it falls back to running
`podman --url unix://PATH system dial-stdio`. Without a path, discovery can run
`podman info --format '{{.Host.RemoteSocket.Path}}'`. These are transport helpers, not separate
remote commands for each API operation. [Connection][podman-ssh], [fallback][podman-stdio].

### B2. Rootless separates OS users, not permissions within one API

**Docs.** SSH supports account credentials, identity files and an SSH agent. A connection entry
selects existing account access rather than issuing an API token. The API permits arbitrary
execution as its service user and cannot limit or audit individual operations. Podman recommends
SSH forwarding and warns against TCP without mutual TLS, including localhost exposure.
Separate rootless users have separate service sockets. [Service][podman-service],
[connections][podman-connection].

**Docs.** Start detached work with `podman run -d IMAGE COMMAND`. Follow with
`podman logs --follow --since TIME`. After disconnect, request the retained logs again.
Removal can delete final output before it is read. This is not a durable task-event cursor.
[Run][podman-run], [logs][podman-logs].

**Docs.** Podman replaced Varlink with REST in 2.0 because Varlink development and support were
being reduced. The team cited native Go libraries, flexibility and removing that dependency.
It did not cite SSH as the problem. [Removal notice][podman-history].

## C. Incus

### C1. Local and remote clients share a documented REST API

**Docs.** All clients use HTTP REST over a Unix socket locally or TLS remotely.
The API uses `/1.0` with additive `api_extensions` capability checks. `incus --debug` shows
the actual API calls and `incus query` exposes direct requests. Network listening is off by
default. `incus config set core.https_address=:8443` enables HTTPS.
[REST][incus-rest], [exposure][incus-expose].

### C2. Trust tokens enroll a certificate rather than becoming permanent bearer keys

**Docs.** Run `incus config trust add CLIENT` on the server then
`incus remote add NAME TOKEN` on the client. The client and server generate key pairs.
The token enrolls the client's certificate and expires on use or after its configured lifetime.
Server fingerprint confirmation establishes server trust. Certificate removal revokes access.
[Authentication][incus-auth], [enrollment][incus-expose].

**Docs.** Local `incus-admin` members have full access. Local `incus` members are restricted to
their own project. Remote certificates can be restricted to projects. OpenFGA adds finer
resource permissions. OIDC uses browser device authorization and stored access/refresh tokens.
OIDC alone grants full access without an authorization policy.
[Permissions][incus-permissions], [OIDC][incus-auth].

**Docs.** The current docs also describe certificate-signed bearer JWTs. `/tls/login` exchanges
one for a Secure, HttpOnly cookie because browsers cannot set WebSocket headers.
This is a documented browser accommodation, not evidence of abandoning SSH. [Auth][incus-auth].

### C3. An asynchronous operation is distinct from its live stream

**Docs.** Background requests return HTTP 202 and an operation URL that can be queried again.
Exec uses WebSockets for IO and control. `/1.0/events` streams operation updates, logs and
lifecycle events. Missed events are not replayed. On reconnect, refetch the resources being
tracked. An HTTPS browser client still needs suitable authentication and a reachable server.
[Operations][incus-rest], [exec][incus-exec], [events][incus-events], [auth][incus-auth].

## D. Coder

### D1. The public REST API and CLI share account identity

**Docs.** `/api/v2` is documented REST. Scripts send `Coder-Session-Token`.
`coder login URL` obtains a session through `/cli-auth`. Sessions default to 24 hours with
refresh. Longer-lived API tokens come from the dashboard or `coder tokens create`.
Tokens can have scopes and resource allow lists. Roles separately govern user actions.
[API][coder-api], [tokens][coder-tokens], [roles][coder-roles].

**Code.** Login opens the browser and can create a fresh session through the Go API client.
This is browser-assisted token enrollment, not SSH key registration or an assumed OAuth
device grant. [Login implementation][coder-login].

### D2. Workspace SSH is a data path, not the control API transport

**Docs.** Users and workspace agents reach the control plane over HTTP/HTTPS.
Workspace agents connect outward. CLI workspace connections use encrypted direct connections
when possible or relay through DERP when not. Reverse proxies must support WebSockets.
The control plane needs a reachable address, not necessarily a public internet address.
[Networking][coder-network].

**Code.** `coder ssh` calls `DialAgent` then creates an SSH client over that connection.
It does not run control-plane commands through a public workspace SSH port.
[SSH implementation][coder-ssh]. **Docs.** The dashboard provides browser access over the same
reachable deployment. [Networking][coder-network].

### D3. Task creation was REST, but current Coder uses a different resource

**Docs.** Historical creation was `POST /api/v2/tasks/{user}` with `template_version_id`
and `input`. Logs were polled. Tasks was deprecated in 2.34 and removed.
Current creation is `POST /api/v2/chats`, with message submission and a WebSocket stream.
Coder explains that the loop and credentials moved into the control plane and history into
its database. See `open-source-agent-platforms.md` §B for semantics, replay and recovery.
Do not implement against the removed endpoint. [Migration][coder-migration].

## E. Daytona

### E1. The CLI and SDKs are clients of the same HTTP service

**Docs.** The control plane is a REST service. Its OpenAPI identifies version `1.0`, but the
documented base URL is `https://app.daytona.io/api`, not a versioned URL contract.
`POST /sandbox` creates compute. SDKs and CLI accept `DAYTONA_API_URL` and an API key.
`daytona login` saves either a key or a browser-login JWT in its active profile.
JWT requests also name the organization. [Architecture][daytona-architecture],
[OpenAPI][daytona-api], [authentication][daytona-auth].

**Docs.** Keys have organization permissions and cannot exceed their creator's permissions.
Crucially, any valid organization API key can use its running sandboxes' toolbox even when
resource scopes are empty. These scopes do not isolate users' process or filesystem access
inside one organization. [Key trust boundary][daytona-keys].

### E2. Process control and logs have a separate sandbox-facing API

**Docs.** The sandbox's Toolbox API uses an HTTP/WebSocket proxy. Async execution returns a
command ID. Query status, retrieve logs or follow them over WebSocket. Later log retrieval is
available, but a replay cursor and exactly-once reconnect contract were not established.
The dashboard is a browser client. Preview and wake are in `ingress-and-triggers.md` §B.
[Architecture][daytona-architecture], [execution][daytona-process], [Toolbox][daytona-toolbox].

### E3. An archived self-hosted stack is not today's maintained control plane

**Docs.** Tag `v0.190.0` documents a Compose stack with API, dashboard, runners, database,
Redis and Dex. This is a local development setup, not an internet-hardening recipe (inference).
The repository says that core development moved private in June 2026 and the public code
receives no further fixes or releases. Current BYOC runs customer compute with Daytona's
control plane. Do not equate BYOC with self-hosting that control plane.
[Compose][daytona-compose], [status][daytona-status], [BYOC][daytona-byoc].

## F. OpenHands

### F1. Name the server before choosing its API

**Docs.** Current Canvas is a browser client. Agent Server owns execution over HTTP/WebSocket.
Sandbox Server is the extracted app server and sandbox control plane. Its documented REST
surface is `/api/v1`, including `POST /api/v1/app-conversations`.
Agent Server instead exposes `/api/conversations` and generated OpenAPI docs at `/docs`.
The reference reports version `0.1.0`, not a matching `/api/v1` route. See
`open-source-agent-platforms.md` §A for the earlier repository split and task semantics.
[Architecture][oh-architecture], [app API][oh-v1], [Agent Server][oh-server], [schema][oh-create].

### F2. Local session keys and Cloud account keys are different credentials

**Docs.** Agent Server defaults to no API authentication. Local-only guidance binds loopback.
Remote use requires configured `OH_SESSION_API_KEYS_*`, sent as `X-Session-API-Key`, plus
TLS, a private network or a trusted proxy. Canvas public mode requires `LOCAL_BACKEND_API_KEY`.
Its VM guide warns against unauthenticated internet exposure. Its phone guide recommends
Tailscale on the serving machine and phone, then opening Canvas in the phone browser.
[Server security][oh-server], [VM guidance][oh-vm], [phone access][oh-mobile].

**Code.** The extracted app server's default auth implementation returns no user identity
and explicitly does not support multi-tenancy. A shared key is not per-user authorization.
[Default auth][oh-local-auth] (inference).
**Docs.** Cloud separately issues account API keys from Settings and accepts authenticated
REST creation at `/api/v1/app-conversations`. Do not confuse these with an LLM provider key.
[Cloud API][oh-cloud].

### F3. Scripts can submit work without keeping a terminal attached

**Docs.** Scripts can create an app conversation then poll its start-task and execution status.
App startup can also stream an incrementally encoded JSON array, not SSE.
At Agent Server level, create `/api/conversations` then call `/{id}/run` beneath that prefix
to run in the background. The Python SDK wraps remote HTTP/WebSocket calls.
[App flow][oh-cloud], [create][oh-create], [background run][oh-run], [SDK][oh-server].

**Code.** `/sockets/session/{conversation_id}` supports durable replay through `after_seq`.
Progress and deltas are not replayed. The protocol explains that an envelope separates wire
fields from persisted events to avoid making every wire change a storage migration.
[Session protocol][oh-replay]. Full persistence semantics are in the existing note §A5.

## G. Tailscale tsnet

### G1. The HTTP server can exist only inside the private network

**Docs.** tsnet embeds Tailscale in Go. `tsnet.Server.Listen("tcp", ":80")` can serve an ordinary
`net/http` handler to tailnet peers.
The service enrolls through a login URL or an auth key. Device approval can add an admin gate.
Persistent state lets it reconnect after restart without enrolling again.
[tsnet][tsnet], [complete example][tsnet-example].

**Docs.** `LocalClient().WhoIs(ctx, r.RemoteAddr)` returns the peer's node and user profile.
Tailnet grants or ACLs govern connectivity. **Docs** (inference). The application must still
map that identity to machine and task permissions. A gateway proxy presents the gateway's peer
identity, so original user attribution needs a separate trusted mechanism.
[Identity example][tsnet-example], [access policy][tailscale-policy].

### G2. Private phone access is practical, but GitHub is not a tailnet peer

**Docs.** iOS enrollment installs a VPN configuration and uses an SSO login.
**Docs** (inference). A connected phone can browse the service allowed by tailnet policy.
HTTP inside this network needs no public listener. REST schemas, streaming and replay remain
application contracts. tsnet supports services with separate addresses and access rules,
not a task API. [iOS][tailscale-ios], [tsnet][tsnet], [example][tsnet-example].

**Docs** (inference). GitHub cannot directly deliver into that private listener.
Keep a public HTTPS receiver outside it. Serve and Funnel are already covered in
`ingress-and-triggers.md` §I2 and are not durable inboxes. [GitHub guidance][github-hooks].

## H. E2B

### H1. Platform credentials and guest access are separate boundaries

**Docs.** E2B publishes a REST API and API keys from its console. The same key authenticates
SDKs and CLI. The reference separates the platform API from sandbox endpoints.
[API keys][e2b-key], [reference][e2b-api].

**Code.** `e2b-capabilities.md` §1 and §5 cite the pinned lifecycle OpenAPI and guest protocols.
Lifecycle REST includes versioned create/connect routes. envd in the guest uses Connect RPC
and REST over TCP port 49983. Its `X-Access-Token` is separate from the platform API key.
Process `Start` and `Connect` provide server streams. This is a useful control/data boundary,
not a reason to expose Hibernaut's internal E2B services to users (inference).

## What this means for Hibernaut

**Docs.** [The spec][hib-spec] §3 and D1 in §6 define SSH-only control and laptop-driven Azure
startup. §5 documents the nightly 20:00 UTC deallocation.
**Unverified.** hibgate and cloud-task admission are plans supplied in the research brief.
This note did not inspect a deployed gateway or run anything against the Azure host.

### 1. Keep SSH and hibd subcommands

**Docs** (inference), §A and §B. Lowest owner setup cost. Keep existing keys without new
listeners, certificates or tokens. Attack surface includes command parsing, sudo permissions
and each key's authority. Scripts work today. SDKs need a command-JSON wrapper. Phones need
an SSH app or gateway. Browser UI needs an adapter. Multiple users need attribution and
resource authorization. GitHub needs a public HTTPS receiver that invokes restricted SSH.

### 2. Serve local HTTP and JSON, carried over SSH

**Code** and **Docs** (inference), §A1 and §B1. Best incremental fit. Add a versioned resource
contract on a permission-restricted Unix socket and a small stdio bridge for `hib` and hibgate.
Keep SSH enrollment without a public API listener. Add HTTP parsing behind socket permissions.
Setup grows mainly in software. Scripts and SDKs share schemas, but need an SSH dialer or local
forwarder. Phones still need SSH or a gateway. A future UI can use that gateway or a private
listener. The socket alone does not preserve the remote SSH user's identity. Shared-host
access needs trusted identity and per-resource checks. Keep gateway authority restricted.

### 3. Serve public HTTPS with API tokens

**Docs** (inference), §C to §F. Most convenient for ordinary HTTP SDKs, browser UI and phones.
Highest internet exposure and setup burden: stable addressing, TLS renewal, token issuance,
revocation and authorization. Browser sessions need cookie and request-forgery protections.
Multiple users are easy to identify, but roles and resource ownership remain work. GitHub's
signed payload needs a dedicated receiver, not an ordinary task-token endpoint. Public hibd
does not replace hibgate's durable inbox. [GitHub guidance][github-hooks].

### 4. Serve HTTP only on a private tailnet

**Docs** (inference), §G and §F2. Moderate setup: enroll host, laptop and phone and maintain policy.
Add a tailnet identity/control-plane dependency. Keep hibd off the public internet.
Normal HTTP scripts, SDKs and a browser UI work from enrolled devices. Network identity can
help multi-user enrollment, but application permissions and browser protections remain.
Tagged service identities and proxy attribution need deliberate handling. GitHub still needs
a public HTTPS receiver because its delivery service is outside the tailnet.

### The power boundary is the same in all four options

**Docs** (inference), [spec][hib-spec] §3 and §5, [GitHub guidance][github-hooks].
When Azure deallocates the host, none of its SSH, Unix-socket, HTTPS or tailnet services answers.
Podman's socket activation needs a running OS. It cannot start a deallocated Azure VM.
Keep Azure startup in `hib` and hibgate. GitHub expects acknowledgement within ten seconds.
hibgate should authenticate, deduplicate and durably accept before waiting for startup.
Delivery into hibd needs retry-safe admission and a stable task ID, whatever the transport.
See `ingress-and-triggers.md` §J and §K for inbox and GitHub details.

**Docs** and **Code** (inference). Favor option 2 next, with optional option 4 for phones and UI.
This keeps D1's simplicity and separates API from reachability. Use public HTTPS on the always-on
gateway for external triggers, not public hibd merely to gain REST. Keep task lifecycle and
replay design separate. `agent-task-apis.md` covers that contract.

## Gaps

- **Unverified.** No host, phone, relay or reconnect behavior was exercised.
- **Unverified.** Podman's checked-main stdio fallback was not verified in a stable release.
- **Unverified.** No exactly-once event replay guarantee was established for Docker, Podman or
  Daytona. HTTP streaming alone must not be treated as that promise.
- **Unverified.** No maintained public Daytona self-hosting support promise or first-party reason
  for its private-codebase move was established beyond the cited repository notice.
- **Unverified.** No SSH-to-HTTP migration rationale was found for Incus, Coder or E2B.
  Their observed API designs should not be presented as evidence that they rejected SSH.
- **Unverified.** OpenHands Cloud permission details and E2B team RBAC were not traced here.
  A local shared server key must not be assumed to provide either platform's user isolation.
