# How sandbox hosts take inbound traffic and triggers, for Hibernaut

**Date checked:** 2026-10-24.

**Summary**

This note supplies ingress and trigger evidence for Hibernaut's cloud-agent design. It extends
`cloud-agents.md`, Martin's survey dated 2026-10-10, with service URLs, authentication, sleeping
compute and durable delivery. `cloud-agent-internals.md` covers task execution and worker
protocols. `always-on-agents.md` covers persistent agents and channel transports. Their overlapping
findings get short recaps here, not a second survey.

- **A preview proxy and an inbox solve different failures.** E2B holds a request during restore.
  Queues, Hookdeck and Svix retain events. Tunnels alone do not provide durable delivery or
  host power-on (§A3, §I4, §J).
- **E2B authenticates private wake before allocating compute.** Its API checks the traffic token
  before startup. The orchestrator checks before forwarding. Auto-resume needs memory (§A2, §A3).
- **E2B's wait is not one HTTP deadline.** Credential-scoped flights can park and retry.
  The 15-second budget gates new retries. The source parking flag defaults to off (§A4).
- **Idle has incompatible meanings.** E2B's deadline ignores traffic. Daytona preview traffic
  extends auto-stop, not auto-pause. CodeSandbox separates HTTP and WebSocket wake. Coder excludes
  direct preview activity (§A1, §B2, §C2, §G2).
- **Sleep does not always preserve RAM.** Fly suspend does. Sprites restart from disk.
  Current Cloudflare Sandbox starts a new instance after inactivity (§D2, §E).
- **GitHub receipt differs from product acknowledgement.** HTTP success, eyes, working comments
  and completed checks differ. Three comment/review families need separate routing (§K, §L3).
- **App identity differs from human authorship.** Public App records do not reveal private token
  handling. Devin and Jules offer attribution modes. Open SWE publishes user PRs through requester
  OAuth and scheduled PRs through the App (§K3, §P to §S).

## Sources

Inline citations omit `https://`. Code citations read `owner/repo@sha7 path:lines`. Full SHAs:

- **E2B.** docs.e2b.dev, especially `network/` and `sandbox/`. Code: e2b-dev/infra
  `3996d14cf2ee9f938508b70d1f0f71a4fc6a5235` and e2b-dev/E2B
  `d1fbe13c2a86198ca087e0c63966592388f2dc58`.
- **Daytona.** www.daytona.io/docs/en, especially `preview.md`, `custom-preview-proxy.md` and
  `sandboxes.md`.
- **CodeSandbox.** codesandbox.io/docs/sdk returned 403. First-party documentation source:
  codesandbox/docs `1f85ffe79cf1134fcdfe74e6ebc6d844bc3045f8`. SDK:
  codesandbox/codesandbox-sdk `10f82664b5d0869e6d67e9610ae48b40cb560548`.
- **Cloudflare Sandbox and Containers.** developers.cloudflare.com/sandbox,
  developers.cloudflare.com/containers and their versioned SDK 0.x pages under `sandbox/sdk/`.
- **Fly and Sprites.** docs.fly.io/launch/autostop-autostart,
  docs.fly.io/reference/suspend-resume and docs.fly.io/sprites/working-with-sprites.
- **Modal.** modal.com/docs/guide, especially `tunnels`, `sandbox-networking`, `sandboxes`,
  `webhooks`, `webhook-timeouts` and `webhook-proxy-auth`.
- **Coder.** coder.com/docs, especially `admin/networking/wildcard-access-url`,
  `user-guides/workspace-access/port-forwarding` and `user-guides/workspace-scheduling`.
  Code: coder/coder `7eacfe95b28dd776bafbd7d5f04b9b7188ab1930`.
- **Codespaces.** docs.github.com/en/codespaces, especially port forwarding, timeout preferences
  and the codespace lifecycle.
- **Cloudflare Tunnel and Access.** developers.cloudflare.com/tunnel and
  developers.cloudflare.com/cloudflare-one/access-controls. Pricing:
  www.cloudflare.com/plans/zero-trust-services.
- **Tailscale.** tailscale.com/docs/features/tailscale-funnel,
  tailscale.com/docs/features/tailscale-serve and tailscale.com/docs/reference/tailscale-cli/serve.
- **ngrok.** ngrok.com/docs/gateway and ngrok.com/docs/pricing-limits.
- **Hookdeck.** hookdeck.com/docs and hookdeck.com/pricing.
- **smee.** probot/smee.io `922d925bc241c2e40a1ec86da631b3dbbdd329cb` and probot/smee-client
  `869ccb228859cd8e6310d2eef1b775d09e7c3509`.
- **Svix.** docs.svix.com, especially `ingest/receiving-with-ingest`, `retries` and
  `receiving/using-app-portal/polling-endpoints`.
- **Cloudflare inbox primitives.** developers.cloudflare.com/queues and
  developers.cloudflare.com/durable-objects. Existing agent coverage: `always-on-agents.md §C1`.
- **GitHub protocol.** docs.github.com/en/webhooks and docs.github.com/en/apps. The specific long
  reference URLs below cover event payloads, token minting and user attribution.
- **Claude.** code.claude.com/docs/en/github-actions and api.github.com/apps/claude. Code:
  anthropics/claude-code-action `925afb53776cf1a2b738c6bdbed37b4d4000ef13`.
- **Copilot.** docs.github.com/en/copilot and api.github.com/apps/copilot-swe-agent.
- **Cursor.** cursor.com/docs/cloud-agent, cursor.com/docs/bugbot,
  cursor.com/docs/integrations/github and api.github.com/apps/cursor.
- **Codex.** learn.chatgpt.com/docs/third-party/github and
  api.github.com/apps/chatgpt-codex-connector.
- **Devin.** docs.devin.ai/integrations/gh and api.github.com/apps/devin-ai-integration.
- **Jules.** jules.google/docs, its dated changelog and api.github.com/apps/google-labs-jules.
- **OpenHands resolver.** Archived All-Hands-AI/openhands-resolver
  `baa2f4515e3a68cb5b8d1fe1eba28c03e474c798`. Current OpenHands/OpenHands tree checked at
  `fee10e3d2909ebbf9950578575b1ee011492171e`.
- **Open SWE.** langchain-ai/open-swe `a7bb5302290af16a88ebf3219c35723b0fa82a5b`.
  Its installation guide is evidence about this revision, not an older hosted service.

[gh-events]: https://docs.github.com/en/webhooks/webhook-events-and-payloads
[gh-signatures]: https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries
[gh-install]:
https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app
[gh-user]:
https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app
[gh-refresh]:
https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens
[copilot-start]:
https://docs.github.com/en/copilot/how-tos/copilot-on-github/use-copilot-agents/kick-off-a-task
[copilot-progress]:
https://docs.github.com/en/copilot/how-tos/copilot-on-github/use-copilot-agents/manage-and-track-agents
[codespaces-ports]:
https://docs.github.com/en/codespaces/developing-in-a-codespace/forwarding-ports-in-your-codespace
[codespaces-life]:
https://docs.github.com/en/codespaces/about-codespaces/understanding-the-codespace-lifecycle
[codespaces-idle]:
https://docs.github.com/en/codespaces/setting-your-user-preferences/setting-your-timeout-period-for-github-codespaces
[access-token]:
https://developers.cloudflare.com/cloudflare-one/access-controls/service-credentials/service-tokens/
[access-jwt]:
https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/

**Tags.** **Docs**: read on the vendor's docs, changelog or blog on the date above. **Code**: read
in source at the named commit. **Unverified**: only the named secondary source says so. News and
forums served only as leads. Quotes are verbatim. "(paraphrase)" marks spots where a quote would
break this note's prose rules. "(inference)" marks my own deductions. An untagged claim takes the
tag of its sentence's citation.

## Comparison table

Sections A to J carry the evidence. A wake is restoration or startup, not an uptime guarantee.
Latency figures are documented descriptions, not measured here or treated as SLAs.
An unknown means the checked sources do not establish the behavior.

| System | URL shape | Inbound auth | TLS and DNS | Wakes on request | Wake latency | Idle policy | WebSockets |
|---|---|---|---|---|---|---|---|
| E2B (§A) | `<port>-<id>.<domain>` | Public or traffic-token header | Managed HTTPS, configurable guest HTTPS | Opt-in memory restore | No guarantee found | Fixed timeout, renewed after wake | Yes |
| Daytona (§B) | `<port>-<id>.<proxy>` or `<port>-<token>.<proxy>` | Public, preview header or expiring URL | Managed HTTPS | Custom proxy can start, native behavior unknown | Not established | Auto-stop 15 min, pause-capable classes default to 60 min auto-pause | Yes |
| CodeSandbox SDK (§C) | `<id>-<port>.csb.app` | Public or host token in URL, header or cookie | Managed HTTPS | HTTP on by default, WebSocket off by default | No request-wake guarantee found | 300 s free, 1,800 s Pro defaults | Yes |
| Cloudflare Sandbox (§D) | 0.x: `<port>-<id>-<token>.<domain>`, current examples use paths or own hosts | 0.x URL token, current application policy | Edge TLS, wildcard DNS for host routing | Application starts container | App readiness bound, not resume SLA | 0.x `sleepAfter` 10 min, current inactivity API | Yes, lifetime caveat |
| Fly Machines (§E) | `<app>.fly.dev` or own domain | Application-defined | Fly Proxy TLS, app DNS | Stop or memory suspend, if enabled | Suspend a few hundred ms, cold start often 2+ s | Proxy excess-capacity policy | HTTP proxy supports upgrade |
| Sprites (§E) | `<name>-<org-id>.sprites.app` | Private token or public | Managed HTTPS | Yes, services restart | Normal 100 to 500 ms, cold 1 to 2 s | Activity-based warm/cold | TCP activity documented, upgrade details not checked |
| Modal (§F) | Random `*.modal.host`, Web Functions `*.modal.run` | Tunnel app auth or Connect Token, optional Web Function proxy auth | Relay TLS or web edge TLS | Web Functions cold-start, finished sandbox tunnel does not promise wake | Workload-dependent | Sandbox lifetime 5 min default, optional idle timeout | Yes |
| Coder (§G) | `/@user/workspace.agent/apps/slug/` or `<app>--<agent>--<workspace>--<user>.<wildcard>` | Coder session and scoped app cookie, owner/authenticated/public shares | Operator TLS and wildcard DNS | Checked app path returns offline page, no start | Not applicable to that request | Template schedule and active sessions | Proxy preserves upgrade headers |
| Codespaces (§H) | `<codespace>-<port>.app.github.dev` | Private GitHub auth, organization or public | Managed HTTPS | Not established for stopped codespace | Not published | 30 min idle default, 12 h lifetime | No explicit guarantee located |
| Cloudflare Tunnel + Access (§I1) | Own hostname | Public, SSO cookie, service token or app JWT | Edge TLS, outbound tunnel | No host-start mechanism | Not applicable | Requires live connector | Yes |
| Tailscale Funnel / Serve (§I2) | `<node>.<tailnet>.ts.net` | Funnel public, Serve tailnet policy | TLS ends on device | No host-start mechanism | Not applicable | Requires live device | TCP proxy, explicit wake semantics not documented |
| ngrok (§I3) | Assigned domain, own domain or wildcard | Traffic Policy OAuth, OIDC, JWT or webhook checks | HTTPS edge TLS, raw TLS can pass through | No host-start mechanism | Not applicable | Agent endpoint follows agent lifetime | Yes |
| Hookdeck (§J1) | Source-specific HTTPS URL | Source verification and destination signature | Hosted HTTPS | Queues and retries, not VM power-on | Retry schedule | Plan retention | Not a preview transport |
| smee (§J2) | `smee.io/<channel>` | Unauthenticated channel | Hosted HTTPS and SSE | No durable offline wake | Live forwarding | No server-side payload storage | SSE, not preview WebSockets |
| Svix Ingest (§J3) | Source URL with rotatable token | Provider verification and Svix delivery signatures | Hosted HTTPS | Retry or pull, not VM power-on | Retry or poll interval | Retention must be checked per plan | Not a preview transport |
| Workers + Queues / DO (§J4) | Worker route or own hostname | Application verifies provider signature | Cloudflare edge TLS | Wakes Worker/DO, external host start is separate | No external-host SLA | Queue retention and DO durable state | DO supports WebSockets |

For Part 2, permissions below are abbreviated. Sections L to S give exact App records or source
configuration. Events are subscriptions, not proof every event starts a coding task.
C/I/PR means `contents`, `issues` and `pull_requests`.
Commit attribution is not the same as the API actor that opens a PR (§K3).

| Product's GitHub integration | Permissions | Events | Acknowledgement | Progress | Commit author | PR author |
|---|---|---|---|---|---|---|
| Claude App and Action (§L) | Broad shared App, Action token defaults C/I/PR write | Mentions, issues and reviews through workflow | Working comment | Updated tracking comment and Actions log | Configured bot, default `claude[bot]` | Token actor, issue flow may offer a creation link |
| Copilot SWE Agent (§M) | C/I/PR/workflows write plus native agent scopes | Public App record has `events: []` | Task/session UI, exact reaction unspecified | Session log and review request | Copilot, initiator co-author | Copilot-created PR or user publication from branch |
| Cursor Cloud Agents (§N) | C/I/PR/checks/actions/workflows write | App includes comment, review and PR events | Mention starts agent, exact reaction unknown | Agent run and handoff | Exact author mapping not established | Exact user/App token split not established |
| Cursor Bugbot (§N) | Same shared App | PR updates and manual command comments | `Cursor Bugbot` check | Review comments and check conclusion | Review itself has none | Autofix PR actor not established |
| Codex Connector (§O) | C/I/PR/actions/workflows write | Comment, review, PR and check events | Eyes for `@codex review` | GitHub review | Fix commit actor not established | Not established |
| Devin (§P) | Broad C/I/PR/checks/actions/statuses write | Comment, review, issue, PR and push events | Session-link reply | Existing PR session gets follow-ups | User-configurable author and committer | Devin, linked user or user-only mode |
| Jules (§Q) | C/I/PR/actions/workflows write | Issues, comments, reviews and CI events | Issue comment, eyes on review comments | Task feed and final PR link | Jules, co-authored or user-only | Jules documented historically, newer mode mapping unclear |
| OpenHands resolver (§R) | Workflow C/I/PR write plus supplied PAT | Labels and three comment/review families | Start comment | Actions artifacts and outcome comment | Existing git config or `openhands` fallback | PAT owner in checked workflow |
| Open SWE (§S) | Configured C/I/PR/checks/workflows write | Comments, reviews and CI, optional automation events | Eyes and reply | Review check and thread/run UI | Sandbox git identity not established here | Requester OAuth, system-owned tasks use App |

## A. E2B

### A1. Public contract and lifecycle

**Docs.** `getHost(3000)` returns a hostname such as
`3000-i62mff4ahtrdfdkyn2esc.e2b.app`, without a scheme. The leftmost label encodes the port
before the sandbox ID. The public URL is HTTPS. Guest forwarding uses HTTP unless
`network.httpsPorts` selects HTTPS. Guest certificates are not verified, so this is re-encryption,
not TLS passthrough. `maskRequestHost: 'localhost:${PORT}'` changes the guest-facing Host
(docs.e2b.dev/network/public-url).

**Docs.** Auto-resume is opt-in. The documented defaults are `onTimeout: "kill"` and
`autoResume: false`. The following configuration pauses with memory at the deadline and lets
incoming tunneled traffic, commands and file operations wake it
(docs.e2b.dev/sandbox/auto-resume).

```js
const sandbox = await Sandbox.create({
  timeoutMs: 600000,
  lifecycle: { onTimeout: "pause", autoResume: true },
  network: { allowPublicTraffic: false }
})
```

**Docs.** The timeout after wake is at least five minutes or the longer original creation timeout.
It starts at resume. Traffic, commands and file activity do not extend an already running
sandbox's deadline. `setTimeout()` extends it explicitly. This is not a sliding idle timer.
`kill()` deletes permanently. Filesystem-only pause cannot combine with transparent auto-resume
(docs.e2b.dev/sandbox/auto-resume).

**Code.** The SDK also has a separate `requestTimeoutMs`, default 60,000, for API requests.
Control-plane retries default to three and honor retryable statuses and `Should-Retry`.
Streaming setup can clear the initial timeout after handshake. These are not app POST retries
(`e2b-dev/E2B@d1fbe13 packages/js-sdk/src/connectionConfig.ts:97-114,191-201`).

**Code.** Lifecycle types require memory for transparent restore. Unset `autoResume` permits
the API default. Explicit `false` pins the opt-out
(`e2b-dev/E2B@d1fbe13 packages/js-sdk/src/sandbox/sandboxApi.ts:570-621`).

### A2. Route selection and inbound authorization

**Code.** The shared host parser reads the first DNS label, splits the port from the sandbox ID
and validates a nonzero 16-bit port. `E2b-Sandbox-Id` and `E2b-Sandbox-Port` provide alternate
routing only on allowed host classes such as localhost, IP addresses and `sandbox.<domain>`.
Normal preview hosts do not accept arbitrary header overrides. Suffix allowlisting is not done
by this parser itself. Domain validation is a separate ingress responsibility (inference)
(`e2b-dev/infra@3996d14 packages/shared/pkg/proxy/host.go:17-113,139-149`).

**Docs.** Public is the default, not an authenticated sharing mode. Any listener can be reached
by someone with its URL. That includes Code Interpreter on port 49999, which executes code.
Set `network.allowPublicTraffic: false` and send `e2b-traffic-access-token` with
`sandbox.trafficAccessToken`. Missing credentials receive 403
(docs.e2b.dev/network/restrict-public-access).

**Code.** `ResolveIngress` blocks internal envd paths before lookup and checks traffic tokens
in constant time before guest forwarding. Envd has separate authentication and is excluded
from this particular app-token check. Host masking and the guest HTTPS selection happen in the
same shared ingress layer. CORS preflights can be answered locally without reaching the guest
(`e2b-dev/infra@3996d14 packages/shared/pkg/proxy/ingress.go:14,47-89` and
`packages/shared/pkg/proxy/handler.go:56-59`).

### A3. The running and paused request paths

**Code.** The client proxy resolves a running sandbox through its catalog. On a miss it calls
the paused-sandbox resumer, then forwards to the selected orchestrator. The legacy client
proxy explicitly refuses full-router network mode. This trace is not a claim that every newer
network mode follows the same route
(`e2b-dev/infra@3996d14 packages/client-proxy/internal/proxy/proxy.go:50-105,194-283`).

**Code.** Configuration includes `PROXY_PORT` 3002, `HEALTH_PORT` 3003 and
`ORCHESTRATOR_PROXY_PORT` 5007. `API_INTERNAL_GRPC_ADDRESS` selects the internal resume API.
Otherwise `API_EDGE_GRPC_ADDRESS` can use TLS and OAuth client credentials. No API address
means no constructed gRPC resumer. The client proxy itself calls HTTP `ListenAndServe`.
Public certificate termination therefore belongs to the ingress around this listener, not
this Go server (inference)
(`e2b-dev/infra@3996d14 packages/client-proxy/internal/cfg/model.go:14-30` and
`packages/client-proxy/main.go:149-181,223`).

**Code.** The resumer sends the requested port, traffic token and envd credential as gRPC
metadata. The API checks its caller and team, loads the snapshot and requires an auto-resume
policy. It can wait up to one minute for a pausing or snapshotting transition. A disk-only
snapshot is refused rather than silently cold-booted. Before starting a paused private sandbox,
it validates the app token. Secure envd traffic must instead present its envd token
(`e2b-dev/infra@3996d14`
`packages/client-proxy/internal/proxy/paused_sandbox_resumer_grpc.go:79-125` and
`packages/api/internal/handlers/proxy_grpc.go:107-139,142-283`).

**Code.** An already running route can return earlier in this API path. The orchestrator's
shared ingress check still gates guest traffic. Its connection pool uses the sandbox lifecycle
ID, not just sandbox ID, to avoid reusing connections after pause/resume or network-slot reuse.
It makes five connection attempts to accommodate envd forwarding readiness
(`e2b-dev/infra@3996d14 packages/orchestrator/pkg/proxy/proxy.go:47-86,106-110`).

### A4. Holding the request, retries and overload

**Code.** Concurrent resume work is coalesced by sandbox ID, requested port and a SHA-256 digest
of both supplied credentials. Callers with different authorization material do not share an
authorization answer. One attempt has a 90-second ceiling. At most 64 detached flights may
finish after their last caller leaves
(`e2b-dev/infra@3996d14 packages/client-proxy/internal/proxy/resume_flight.go:31-58,124-149`).

**Code.** Optional parking retries a shortage, an unavailable control plane, a still-pausing
sandbox or a placement error explicitly classified as retryable. Permanent placement refusal
does not retry. Backoff starts at 250 ms, multiplies by 1.5, adds 20 percent jitter and caps at
two seconds. A new retry needs at least two seconds of useful budget
(`e2b-dev/infra@3996d14`
`packages/client-proxy/internal/proxy/resume_flight.go:44-58,218-226,269-290,669-750`).

**Code.** Source flag defaults are `client-proxy-resume-parking: false`,
`client-proxy-resume-parking-budget-ms: 15000` and
`client-proxy-resume-parking-lot-size: 512`. The budget starts at the first retryable failure.
It gates starting another attempt. An attempt already running may finish beyond that budget
while a caller still waits. The 512 limit is waiting requests per proxy process, not sandboxes.
Overflow is shed with 503. These are source defaults, not evidence of hosted rollout settings
(`e2b-dev/infra@3996d14 packages/shared/pkg/featureflags/flags.go:197-200,761-775` and
`packages/client-proxy/internal/proxy/resume_flight.go:772-809,812-858`).

**Code.** Without parking, a caller receives the first retryable error, subject to joining an
attempt already finishing. With parking, its original HTTP request waits for routing. This is
not a durable queue. Cancellation removes its waiter. Shutdown cancels flights. Once a create
has started, the API can let it finish after the proxy caller disconnects
(`e2b-dev/infra@3996d14`
`packages/client-proxy/internal/proxy/resume_flight.go:401-424,830-870` and
`packages/api/internal/handlers/proxy_grpc.go:285-311`).

### A5. What the client receives

**Code.** The proxy distinguishes errors rather than returning a universal cold-start 502:

- Bad routing host, ID or port: 400
  (`e2b-dev/infra@3996d14 packages/shared/pkg/proxy/handler.go:62-85`).
- Missing sandbox or a running sandbox with a closed port: 502
  (`e2b-dev/infra@3996d14 packages/shared/pkg/proxy/template/sandbox_not_found.go:23-31`
  and `packages/shared/pkg/proxy/template/port_closed.go:24-33`).
- Resume permission denied or a missing/invalid traffic token: 403
  (`e2b-dev/infra@3996d14`
  `packages/shared/pkg/proxy/template/sandbox_resume_permission_denied.go:23-31` and
  `packages/shared/pkg/proxy/template/traffic_access_token_error.go:29-51`).
- Team sandbox limit: 429
  (`e2b-dev/infra@3996d14 packages/shared/pkg/proxy/template/team_sandbox_limit.go:23-35`).
- Still transitioning: 409 on app ports but 503 on envd, whose SDK treats 409 specially
  (`e2b-dev/infra@3996d14`
  `packages/shared/pkg/proxy/template/sandbox_still_transitioning.go:23-40`).
- Retryable capacity: 503. Parking overflow: 503 with `proxy_parking_lot_full`. Permanent
  placement refusal preserves the carried API status where available
  (`e2b-dev/infra@3996d14 packages/shared/pkg/proxy/template/sandbox_capacity.go:19-65`).

**Code.** Transition and ordinary retryable capacity responses carry `Retry-After: 2`.
Permanent refusal and shed requests deliberately do not. Browser navigation gets HTML.
Scripts, JSON clients and WebSocket requests get JSON errors. The two reverse-proxy layers use
upstream idle limits of 610 and 620 seconds. The shared HTTP server sets no read, write or
header timeout. Those settings do not promise unlimited lifetime through every external edge
(`e2b-dev/infra@3996d14 packages/shared/pkg/proxy/handler.go:18-27,126-175`,
`packages/shared/pkg/proxy/template/template.go:55-139`,
`packages/client-proxy/internal/proxy/proxy.go:28-33`,
`packages/orchestrator/pkg/proxy/proxy.go:29-35` and
`packages/shared/pkg/proxy/proxy.go:64-73`).

**Code.** `getHost()` explicitly supports HTTP and WebSocket access
(`e2b-dev/E2B@d1fbe13 packages/js-sdk/src/sandbox/index.ts:530-536`).
**Docs.** The checked auto-resume page gives no numeric wake SLA
(docs.e2b.dev/sandbox/auto-resume). A capacity wait also includes more than memory restoration
(inference).

## B. Daytona

### B1. Two preview credentials with different lifetimes

**Docs.** Standard previews use `https://{port}-{sandboxId}.{daytonaProxyDomain}`.
Private previews require `x-daytona-preview-token`. A public sandbox omits this check for
ordinary ports. The standard token is sandbox-wide and cannot be individually revoked.
Stop/start replaces it. Memory pause/resume preserves it. Signed URLs instead use
`https://{port}-{token}.{daytonaProxyDomain}` and bind a revocable credential to one port.
Their default lifetime is 60 seconds, configurable from one to 86,400 seconds
(www.daytona.io/docs/en/preview.md).

**Docs.** Browser previews show a warning unless accepted or bypassed with
`X-Daytona-Skip-Preview-Warning: true`. Acceptance sets a 24-hour
`daytona-preview-page-accepted` cookie. WebSocket upgrades skip the warning and are proxied.
Reserved service ports still need authentication even on public sandboxes
(www.daytona.io/docs/en/custom-preview-proxy.md).

### B2. Activity, stop and actual wake responsibility

**Docs.** The auto-stop setting defaults to 15 minutes. Zero disables it. Preview requests,
SSH connections, Toolbox API calls and lifecycle changes reset it. Background work alone does not.
`X-Daytona-Skip-Last-Activity-Update: true` lets a preview request avoid extending auto-stop.
VM auto-pause preserves memory and excludes preview traffic from activity. The policies are
mutually exclusive. With neither set, non-ephemeral classes that support pause default to
60-minute auto-pause with auto-stop disabled. This qualifies the general auto-stop default
(www.daytona.io/docs/en/sandboxes.md and www.daytona.io/docs/en/custom-preview-proxy.md).

**Docs.** Auto-archive applies to containers, not VMs. It defaults to seven continuously
stopped days. Zero selects the maximum 30 days rather than disabling archive. Stopped
containers keep files on the runner. Archived files move to object storage
(www.daytona.io/docs/en/sandboxes.md).

**Docs.** The custom proxy must check state and `start` before forwarding. The checked native
preview docs specify neither stopped/paused wake nor its latency or waiting response.
URLs are HTTPS. The internal TLS boundary remains unverified
(www.daytona.io/docs/en/custom-preview-proxy.md and preview.md).

## C. CodeSandbox SDK

### C1. Hosts and private access

**Code.** Hosts use `https://{sandboxId}-{port}.csb.app`, with WebSocket hosts also documented.
Private sandboxes require host tokens. Opening a signed URL sets a cookie for later requests.
The SDK provides `?preview_token=`, header `csb-preview-token` and cookie
`csb_preview_token`. Tokens can expire, be listed, have their expiry changed or be revoked
(`codesandbox/docs@1f85ffe packages/projects-docs/pages/sdk/sandbox-hosts.mdx:10-58` and
`codesandbox/codesandbox-sdk@10f8266 src/HostTokens.ts:34-153`).

**Code.** Privacy defaults to `public`. `private` and `public-hosts` are distinct settings
(`codesandbox/codesandbox-sdk@10f8266 src/types.ts:37-43,174-178`). HTTPS hosts do not establish
every internal TLS hop
(`codesandbox/docs@1f85ffe packages/projects-docs/pages/sdk/sandbox-hosts.mdx:10-58`).

### C2. HTTP wake and WebSocket wake are separate

**Code.** `automaticWakeupConfig.http` defaults to true.
`automaticWakeupConfig.websocket` defaults to false. A platform can support WebSockets
without letting a WebSocket connection wake hibernated compute. Hibernation timeout defaults
are 300 seconds for free users and 1,800 seconds for Pro, with a maximum 86,400 seconds.
Interactions with exposed endpoints count toward inactivity
(`codesandbox/codesandbox-sdk@10f8266 src/types.ts:145-170` and
`src/Sandbox.ts:105-120`).

**Code.** Guidance recommends a 24-hour fallback, disabled wake and backend `resume()` at
session start. A custom proxy example wakes on HTML, not every asset. These are recommendations
(`codesandbox/docs@1f85ffe`
`packages/projects-docs/pages/sdk/manage-sandboxes.mdx:14-26` and
`packages/projects-docs/pages/sdk/sandbox-hosts.mdx:148-175`).

## D. Cloudflare Sandbox SDK and Containers

### D1. Check the SDK generation before copying the example

**Docs.** Pages updated October 22 and 23 mark `exposePort()` as SDK 0.x for existing apps.
New apps use SDK 1.0 and the Durable Object Container API, a different interface
(developers.cloudflare.com/sandbox/sdk/concepts/preview-urls and sandbox/previews).

**Docs.** In 0.x, `exposePort(8080, { hostname, token })` returns
`https://{port}-{sandbox-id}-{token}.yourdomain.com`. Random tokens have 16 characters and
change on re-exposure. Stable custom tokens allow one to 16 lowercase letters, digits or
underscores. The public URL requires its bearer token, not SSO. Call `proxyToSandbox()` first
in the Worker's fetch handler
(developers.cloudflare.com/sandbox/sdk/concepts/preview-urls).

**Docs.** Host previews need a custom domain, wildcard DNS and Worker route, not `workers.dev`.
TLS ends at Cloudflare. Universal SSL does not cover another wildcard level such as
`*.sandbox.example.com`. The guide lists Advanced Certificate Manager at $10 per month
(developers.cloudflare.com/sandbox/sdk/guides/preview-urls-custom-domain).

### D2. Startup, inactivity and WebSockets

**Docs.** The 0.x `sleepAfter` default is `"10m"`. `keepAlive: true` sends 30-second pings and
overrides it. Instance acquisition and port readiness have separate 30,000 and 90,000 ms
defaults, configurable with `SANDBOX_INSTANCE_TIMEOUT_MS` and `SANDBOX_PORT_TIMEOUT_MS`
(developers.cloudflare.com/sandbox/sdk/configuration/sandbox-options).

**Docs.** The current example starts a container and waits up to 30 seconds for a listener.
It forwards once, avoiding uncertain POST replay. Readiness failure returns 503. Forwarding
failure returns 502. Inactivity is five minutes in the sample. `/previews/<NAME>/` supports
path routing, but separate hosts avoid absolute-asset-path problems
(developers.cloudflare.com/sandbox/previews).

**Docs.** Current sandbox stop preserves DO storage, not processes or local disk. `start()`
creates a new instance. Filesystem snapshots carry files, not running servers.
`setInactivityTimeout()` allows six hours and must be reapplied after DO restart.
Internal builds do not count as activity
(developers.cloudflare.com/sandbox/concepts/lifetime).

**Docs.** WebSocket upgrades work, but pass-through alone does not keep the instance alive.
Bridge and `accept()` both ends to hold it. Inactivity resumes after the last bridge closes.
The 0.x preview docs support WebSockets and SSE, not raw TCP or UDP
(developers.cloudflare.com/sandbox/previews and sandbox/sdk/concepts/preview-urls).
Durable Object wake, durable state and alarms are already covered in
`always-on-agents.md §C1`.

## E. Fly Machines and Sprites

### E1. Fly Proxy can start or restore existing Machines

**Docs.** Fly Proxy routes to an internal service port. `auto_start_machines` starts existing
Machines, not new ones. Direct 6PN bypasses it. Use Flycast for private proxy-driven startup
(docs.fly.io/launch/autostop-autostart and docs.fly.io/networking/app-services).

```toml
[http_service]
  internal_port = 8080
  force_https = true
  auto_stop_machines = "suspend"
  auto_start_machines = true
  min_machines_running = 0
```

**Docs.** `auto_stop_machines` accepts `"off"`, `"stop"` or `"suspend"`. Minimum running count
applies in the primary region. Periodic autostop removes at most one Machine per region per
pass based on excess capacity, not a request-idle timer. Private apps need app authentication
(docs.fly.io/launch/autostop-autostart and docs.fly.io/networking/app-services).

**Docs.** Firecracker suspend snapshots CPU and memory. Resume takes a few hundred milliseconds
versus common cold starts of two seconds or more. Migration, maintenance or snapshot loss can
force cold boot. Restored sockets can be stale despite surviving process memory
(docs.fly.io/reference/suspend-resume).

**Docs.** Fly supplies `<app>.fly.dev` and edge TLS. An HTTP service accepts `ws://` and
`wss://` without extra setup. Idle WebSockets stay open, but do not prevent autostop.
Proxy restarts and autostop send clients close code 1012. Clients must reconnect.
No precise maximum held-request startup interval was established in the checked sources
(docs.fly.io/networking/services and docs.fly.io/networking/websockets).

### E2. Sprites wake on HTTP but do not retain RAM

**Docs.** A Sprite URL is `https://<name>-<org-id>.sprites.app`, where the organization ID is
generated, not its name. URL auth is private by default. `--url-auth public` exposes it and
`--url-auth sprite` restores organization-token access. Port 8080 is the default destination
or the first HTTP port opened. A request wakes the Sprite
(docs.fly.io/sprites/working-with-sprites).

**Docs.** Normal wake is 100 to 500 ms, cold start one to two seconds. Disk and network persist,
not RAM. Services restart processes: `sprite-env services create web --cmd node --args server.js`.
Exec, TTY sessions and open TCP connections count as activity. The checked guide does not
establish exact private headers or WebSocket upgrades (docs.fly.io/sprites/working-with-sprites).

## F. Modal

### F1. A live tunnel is not a scale-to-zero Web Function

**Docs.** `modal.forward(8000)` returns a random URL such as
`https://wtqcahqwhd4tu0.r5.modal.host`. The relay ends TLS and forwards TCP, without L7 auth.
Randomness is not user identity. `unencrypted=True` uses a shared host plus TCP port.
Tunnels cost no extra beyond container compute
(modal.com/docs/guide/tunnels).

**Docs.** Sandboxes expose `encrypted_ports`, `unencrypted_ports` and `h2_ports`.
`inbound_cidr_allowlist` restricts encrypted tunnels and Connect Token access, not unencrypted
ports. Custom sandbox tunnel domains require Team or Enterprise, manual setup and NS delegation,
not the CNAME setup used for other Modal custom domains
(modal.com/docs/guide/sandbox-networking).

**Docs.** Connect Tokens authenticate HTTP and WebSockets through `Authorization` or
`_modal_connect_token` query/cookie. Query access sets the cookie. `X-Verified-User-Data`
passes metadata, limited to 512 serialized characters without secrets. A finished sandbox
has ended its processes. The checked tunnel contract does not promise HTTP revival
(modal.com/docs/guide/sandbox-networking and modal.com/docs/guide/sandboxes).

**Docs.** Sandbox lifetime defaults to five minutes. `timeout` can extend it to 24 hours.
Optional `idle_timeout` terminates inactivity. An active exec command, stdin writes or an
open tunnel TCP connection counts as activity. These are termination policies, not memory
hibernation (modal.com/docs/guide/sandboxes).

### F2. Web Functions do cold-start on requests

**Docs.** Web Functions use `https://<source>--<label>.modal.run` or a custom domain.
They cold-start from zero and queue at container limits. `requires_proxy_auth=True` checks
`Modal-Key` and `Modal-Secret` or their period-joined value in `Authorization: Bearer`.
They default to public, unlike newer Modal Endpoints and Servers
(modal.com/docs/guide/webhooks, webhook-urls and webhook-proxy-auth).

**Docs.** The HTTP limit is 150 seconds. Longer execution can return a 303 result URL that
waits for the same call, not submit another one. This behavior has CORS limitations.
ASGI apps, WSGI apps and web servers support WebSockets. Each connection holds one function
call. Message size is two MiB. HTTP/2 WebSockets and per-message deflate are not supported
(modal.com/docs/guide/webhook-timeouts and webhooks).

## G. Coder

### G1. Apps, domains and the authenticated control plane

**Docs.** A `coder_app` uses a path or subdomain. Port example:
`8080--main--myworkspace--john.coder.example.com`. Named apps replace port with slug.
`CODER_WILDCARD_ACCESS_URL` accepts one leading wildcard. Supply matching DNS and certificates.
TLS ends at a reverse proxy or Coder with `CODER_TLS_ENABLE`, `CODER_TLS_CERT_FILE`
and `CODER_TLS_KEY_FILE`
(coder.com/docs/admin/networking/wildcard-access-url).

**Docs.** Named apps share as `owner`, `authenticated` or `public`. Port sharing additionally
has organization scope. These are different mechanisms. IDE examples disable guest IDE auth
because Coder already checks access. Shared path apps have security restrictions.
Subdomain apps are recommended for compatibility
(coder.com/docs/user-guides/workspace-access/port-forwarding and
coder.com/docs/admin/templates/extending-templates/web-ides).

**Code.** Path apps use `/@user/workspace[.agent]/apps/slug/`. Numeric port forwarding is not
supported on this path. Session authorization mints a signed app token in an HttpOnly cookie.
The cookie is scoped to the path prefix for path apps or `/` on the app subdomain. Existing
tokens must match the requested app, not merely have a valid signature
(`coder/coder@7eacfe9 coderd/workspaceapps/proxy.go:170-179,349-375`,
`coderd/workspaceapps/provider.go:41-86` and `coderd/workspaceapps/token.go:32-38,76-126`).

### G2. Connections and stop behavior

**Docs.** `coder port-forward` supports TCP and UDP through workspace connectivity
(coder.com/docs/user-guides/workspace-access/port-forwarding).
**Code.** The app proxy preserves WebSocket upgrade headers. If token issuance finds a
stopped workspace, it returns an HTML 400 page titled "Workspace Offline", not a start call.
The page links to the workspace. This describes the checked request path, not every
possible deployment extension
(`coder/coder@7eacfe9 coderd/workspaceapps/proxy.go:55-69`,
`coderd/workspaceapps/request.go:273-275`, `coderd/workspaceapps/db.go:155-164` and
`coderd/workspaceapps/errors.go:112-149`).

**Docs.** Autostop's default activity bump is one hour. IDE, terminal, SSH and working-agent
status extend it. Direct port URLs without an active session do not. Maintenance can ignore
active connections. Scheduled autostart is separate
(coder.com/docs/user-guides/workspace-scheduling).

## H. GitHub Codespaces

### H1. Visibility and private-port credentials

**Docs.** Forwarded URLs use `https://CODESPACENAME-PORT.app.github.dev`.
Visibility is private, organization or public, subject to organization policy. Organization
visibility needs GitHub Team or Enterprise Cloud. Public ports require no login. Private
browser access uses GitHub authentication. For command-line clients, the documented header is
`X-Github-Token` with the codespace's `GITHUB_TOKEN`. That token changes after stop/start.
Changing the forwarded guest protocol to HTTPS makes a public port private
([port forwarding][codespaces-ports]).

### H2. Traffic is not an idle heartbeat

**Docs.** Idle defaults to 30 minutes and can be set from five to 240 minutes within policy.
Keyboard, mouse and terminal input/output count. HTTP preview traffic does not directly
reset idle, but server logs appearing in a terminal can. The lifecycle docs now also impose
a 12-hour maximum lifetime even while active. Stop preserves saved files, not processes
([timeout preferences][codespaces-idle] and [lifecycle][codespaces-life]).

**Docs.** The checked pages do not promise stopped-preview wake, its waiting response,
latency or WebSocket lifetime. Automatic port discovery is not compute startup (inference)
([port forwarding][codespaces-ports]).

## I. Tunnels

### I1. Cloudflare Tunnel with Access

**Docs.** `cloudflared` connects outbound over encrypted tunnels. DNS uses
`<UUID>.cfargotunnel.com`. Public TLS ends at Cloudflare, with optional origin HTTPS.
One tunnel routes many hosts. Remote configuration is capped at one MB, roughly 5,000 to
10,000 published apps. Tunnel is on all plans
(developers.cloudflare.com/tunnel and tunnel/concepts/routing).

**Docs.** Access can require browser SSO with `CF_Authorization` or machine service tokens.
Service clients send `CF-Access-Client-Id` and `CF-Access-Client-Secret`. A Service Auth policy
is necessary for the machine flow. `read_service_tokens_from_header` supports a configured
single-header alternative. New organizations created since October 5 use strict service-token
authentication by default, with 401/403 failures rather than an IdP redirect
([service tokens][access-token]).

**Docs.** The origin can validate `Cf-Access-Jwt-Assertion` against the team's JWKS at
`https://<team>.cloudflareaccess.com/cdn-cgi/access/certs`. Check signature, issuer, audience
and expiry. Match the key ID. Do not trust the header merely because a request contains it
([JWT validation][access-jwt]).

**Docs.** All plans support WebSockets. WAF checks the upgrade, not subsequent messages.
Edge deploys can close connections. Access pricing offers a free proof of concept but did
not expose a stable numeric seat price in this read
(developers.cloudflare.com/network/websockets and
www.cloudflare.com/plans/zero-trust-services).

### I2. Tailscale Funnel and Serve

**Docs.** Funnel publishes `https://node.tailnet.ts.net` publicly. TLS ends in the device's
`tailscaled`, not the relay. Beta is available on all plans. MagicDNS, HTTPS certificates
and the `funnel` attribute are required. Ports are 443, 8443 and 10000. Bandwidth caps are
non-configurable. Arbitrary custom domains are not documented
(tailscale.com/docs/features/tailscale-funnel).

**Docs.** `tailscale serve 3000` is private to the tailnet and honors tailnet access controls.
It supplies `Tailscale-User-Login`, `Tailscale-User-Name` and
`Tailscale-User-Profile-Pic` after stripping spoofed incoming copies. Tagged devices do not
get user identity headers. Funnel traffic gets none. The service should listen only where
untrusted callers cannot bypass this identity proxy
(tailscale.com/docs/features/tailscale-serve).

### I3. ngrok policy and TLS choices

**Docs.** An Agent Endpoint follows the lifetime of its agent process. HTTPS endpoints
terminate TLS in ngrok's cloud and re-encrypt the secure tunnel. Raw TLS endpoints can
instead terminate at the agent or upstream. Own domains use DNS configuration. Wildcard
endpoints require a reserved wildcard domain. HTTP endpoints support WebSocket servers
(ngrok.com/docs/gateway/endpoints/http and gateway/domains/tls-termination).

**Docs.** Traffic Policy provides `oauth`, `openid-connect`, `jwt-validation` and
`verify-webhook`. GitHub verification takes `provider: github` and the webhook secret.
Invalid requests receive 403 when enforcement is enabled. OAuth/OIDC provide a browser
session, not a GitHub webhook acknowledgement or retry queue. An OAuth redirect in front of
a webhook is not provider authentication (inference)
(ngrok.com/docs/gateway/traffic-policy/actions/oauth, actions/oidc and actions/verify-webhook).

**Docs.** Free limits include one assigned development domain, one GB outbound per month,
20,000 HTTP requests, 500 webhook verifications and three OAuth/OIDC monthly active users.
There can be three online endpoints and three agents. Browser HTML has an interstitial,
bypassable with `ngrok-skip-browser-warning`. Custom domains and raw TLS need paid features.
Traffic Policy metering is $0.10 per 100,000 units, with action-specific unit costs
(ngrok.com/docs/pricing-limits/free-plan-limits and traffic-policy-unit-pricing).

### I4. The always-live connector boundary

**Docs.** All three need a live connector/device. None promises VM power-on or durable inboxes.
A local service can implement guest wake behind the connector. Whole-host shutdown needs a
separate live receiver (inference) (§I1 to §I3).

## J. Durable inboxes

### J1. Hookdeck

**Docs.** A source has an HTTPS ingestion URL. Source verification can check a provider's
signature or configured credentials. Hookdeck queues accepted events per connection and
retries delivery separately from the provider's original request. Destinations can verify
the base64 HMAC-SHA256 `x-hookdeck-signature`. During rotation there can be a second signature.
`x-hookdeck-verified: true` reports successful configured source verification
(hookdeck.com/docs/authentication and events).

**Docs.** Delivery failure includes non-2xx, a network error or the default 60-second
destination timeout. Timeout can be raised to 15 minutes. Automatic retries are capped at
50, with configurable linear or exponential intervals. Manual retries are unlimited.
`Retry-After` takes precedence and can schedule up to seven days ahead.
`Retry-After: -1` stops retries. Replay re-ingests as a new request. Retry retains the original
request identity and reprocesses eligible failures
(hookdeck.com/docs/retries, limits and requests).

**Docs.** Destination rate limits queue excess events. Concurrency one serializes delivery,
but the checked docs do not promise source-wide event ordering across retries. Retention is
three days on Developer, seven on Team and 30 on Growth. Developer includes 10,000 events
per month. Team starts at $39 per month. Retention bounds recovery from a long host outage
(hookdeck.com/docs/guides/control-throughput-queue-events and hookdeck.com/pricing).

**Docs.** Disconnected CLI listeners can produce ignored `CLI_DISCONNECTED` events, not
future queued delivery. Do not equate this development tunnel with an offline HTTP
destination's retry contract (inference) (hookdeck.com/docs/requests).

### J2. smee is explicitly not that inbox

**Code.** The README says it is for development, not production. Channels are unauthenticated.
Someone knowing the channel ID can see payloads. Delivery uses SSE to actively connected
clients. Payloads are never stored server-side. Browser `localStorage` is only UI history
(`probot/smee.io@922d925 README.md:19-25,53-59`).

**Code.** Optional Redis is pub/sub, not a durable log. The server emits headers and parsed
payload. POST success does not prove offline replay (inference)
(`probot/smee.io@922d925 lib/event-bus.js:25-63` and `lib/server.js:244-267,316-326`).
It is not a production answer to a host that stays off (inference).

### J3. Svix Ingest and receiver polling

**Docs.** Svix Ingest verifies providers including GitHub. `genericWebhook` skips verification.
The source's `ingestUrl` is shaped like
`https://api.svix.com/ingest/api/v1/source/<source>/in/<token>`. Rotation leaves the old token
valid for 24 hours. Destinations get logs, replay, recovery and Svix signatures to verify
(docs.svix.com/ingest/receiving-with-ingest.md).

**Docs.** Svix documents 2xx acknowledgement within 30 seconds. Its retry intervals after a
failure are five seconds, five minutes, 30 minutes, two hours, five hours, ten hours and
another ten hours. Redirects fail delivery. `webhook-delivery: abort-message` stops retries.
Repeatedly failing endpoints can be disabled after five days. Manual resend and recovery
are distinct operations (docs.svix.com/retries.md).

**Docs.** Polling Endpoints avoid inbound network access. Each consumer has an independent
offset. GET returns messages in received order. POST to the same consumer URL plus `/commit`
advances the offset after processing. A stable consumer ID preserves progress across
restarts. Uncommitted messages may return after their lease expires. API keys are specific
to the polling endpoint and can expire or rotate
(docs.svix.com/receiving/using-app-portal/polling-endpoints.md).

**Docs.** The checked pages do not establish universal Ingest retention or every plan's polling
availability (docs.svix.com/ingest/receiving-with-ingest.md). Ordered polling does not prove
ordered processing at parallel push destinations (inference).

### J4. Workers with Queues or Durable Objects

Durable Object HTTP wake, alarms and SQLite state already appear in
`always-on-agents.md §C1`. Channel deadlines and signature credentials appear in §E1 to §E3.

**Docs.** Queues provides at-least-once delivery, not ordering. Resolved `queue.send()` confirms
a disk write. Await it before success and make processing duplicate-safe (inference).
Consumer retries default to three. After the limit, a message is deleted or sent to a
configured dead-letter queue. Individual `ack()` avoids replaying successful batch members
(developers.cloudflare.com/queues/reference/delivery-guarantees,
queues/reference/how-queues-works, queues/configuration/javascript-apis and
queues/configuration/batching-retries).

**Docs.** An external host can use HTTP pull consumers with an account API token.
`POST /client/v4/accounts/{account}/queues/{queue}/messages/pull` returns message bodies and
lease IDs. `visibility_timeout_ms` controls reappearance of unacknowledged messages.
Polling is short, not long polling. The matching messages acknowledgement endpoint accepts
lease IDs for success or retry
(developers.cloudflare.com/queues/configuration/pull-consumers).

**Docs.** Queue messages are limited to 128 KB. Retention is fixed at 24 hours on free and
configurable up to 14 days on paid. Visibility can be up to 12 hours. Max retries is 100.
GitHub's 25 MB webhook cap is much larger than this queue-message limit (§K1).
Large payloads need another durable body store and a queued reference (inference)
(developers.cloudflare.com/queues/platform/limits).

**Docs.** A Worker can verify a signature, persist an event and return before
starting external compute. A DO can serialize per-machine or per-conversation decisions.
Neither primitive automatically starts an arbitrary outside VM. A queue consumer's success
must mean durable handoff, not merely that a long job was started in volatile memory (inference)
(developers.cloudflare.com/queues/configuration/batching-retries and §J4 above).

## K. GitHub App protocol and event identity

### K1. Delivery receipt is not permission to run

GitHub's ten-second acknowledgement deadline, lack of automatic failed redelivery and
installation-token basics are already in `always-on-agents.md §E1 to §E3`.

**Docs.** `X-GitHub-Event` names the event family. `X-GitHub-Delivery` identifies its delivery.
`X-Hub-Signature-256` is `sha256=` plus HMAC-SHA256 of the original body using the webhook
secret. Verify with constant-time comparison before processing. Reformatting JSON before
verification changes the signed bytes. Payloads are capped at 25 MB, beyond which GitHub
may not deliver them. `sender` can be the `ghost` placeholder, not a known human
([payload reference][gh-events] and [signature validation][gh-signatures]).

**Docs.** Transport acceptance, execution permission and human-visible acceptance are separate.
Bind installation, repository, current actor permission and delivery ID before running work.
`author_association` alone is not a live permission lookup (inference) (§L2, §R1).

### K2. Three comments that look similar in the UI

**Docs.** `issue_comment` includes issue comments and PR conversation comments.
`issue.pull_request` distinguishes the latter. `pull_request_review_comment` is an inline
diff comment, with `comment.path`, line context and `comment.commit_id`.
`pull_request_review` is a submitted review, with `review.body`, `review.state` and its
commit ID. Comment text is not always at `comment.body`. Events also have action subtypes
([payload reference][gh-events]).

```json
{
  "action": "created",
  "installation": { "id": 123 },
  "repository": { "id": 456, "full_name": "owner/repo" },
  "sender": { "id": 789, "login": "alice", "type": "User" },
  "issue": { "number": 42, "pull_request": { "url": "https://api.github.com/..." } },
  "comment": { "id": 1001, "body": "@agent fix this", "user": { "login": "alice" } }
}
```

**Docs.** This is a reduced illustrative `issue_comment` payload (paraphrase), not a captured
delivery. For `pull_request_review_comment`, use top-level `pull_request.number` and the
comment's diff location. For `pull_request_review`, read `review.body` and expect a
`submitted` action. When selecting code, bind the PR head SHA and distinguish head repository
from base repository, especially for forks ([payload reference][gh-events]).

### K3. Installation tokens, user tokens and authorship

**Docs.** An App JWT authenticates `POST /app/installations/{installation_id}/access_tokens`.
Installation tokens last one hour. Re-mint instead of extending them. Requested scope can
only narrow installation access. This standard does not prove any vendor's implementation
([installation tokens][gh-install]).

**Docs.** User tokens attribute API activity to the user and intersect user and App access.
Expiring access tokens last eight hours. Refresh tokens last six months. Refresh rotates both
and invalidates the old pair
([user tokens][gh-user] and [refreshing tokens][gh-refresh]).

**Docs.** Four identities can differ: Git commit author, git committer, authenticated
push actor and PR creator. A co-author trailer changes attribution, not API authorization.
An installation token naturally acts as the App. A user token can make the PR belong to the
human. These identities must not be conflated (inference) ([user tokens][gh-user]).

## L. Claude GitHub App and claude-code-action

### L1. The shared App requests more than one Action run needs

The existing Claude trigger and identity recap is `cloud-agent-internals.md §C4`.

**Docs.** The current `claude` App record grants write access to `actions`, `checks`,
`contents`, `discussions`, `issues`, `pull_requests`, `repository_hooks` and `workflows`.
Read access covers `administration`, `members`, `merge_queues`, `metadata` and `statuses`
(api.github.com/apps/claude).

**Docs.** Its event list is `branch_protection_rule`, `check_run`, `check_suite`,
`commit_comment`, `discussion`, `discussion_comment`, `issues`, `issue_comment`,
`merge_group`, `merge_queue_entry`, `pull_request`, `pull_request_review`,
`pull_request_review_comment`, `pull_request_review_thread`, `push`, `release`,
`repository_ruleset`, `status` and `sub_issues`. This shared App serves Actions, hosted Code
Review and web auto-fix. It is not a minimal webhook manifest for one mention bot
(api.github.com/apps/claude and code.claude.com/docs/en/github-actions).

### L2. Workflow events, permission checks and token exchange

**Code.** The sample Action workflow selects `issue_comment.created`,
`pull_request_review_comment.created`, `issues.opened/assigned` and
`pull_request_review.submitted`. Trigger validation reads `review.body` for reviews and
`comment.body` for comment events. It checks mention boundaries. Current collaborator
permission must be admin or write. A non-write allowlist works only with an explicit custom
token (`anthropics/claude-code-action@925afb5 examples/claude.yml:3-25`,
`src/github/validation/trigger.ts:104-138` and
`src/github/validation/permissions.ts:124-138`).

**Code.** Default authentication requests a GitHub OIDC token for audience
`claude-code-github-action` and exchanges it at
`https://api.anthropic.com/api/github/github-app-token-exchange`.
Default requested permissions are contents, issues and pull requests write.
A supplied `github_token` bypasses this exchange. The runner does not need an App private
key. The checked exchange routine does not establish refresh during a long run
(`anthropics/claude-code-action@925afb5 src/github/token.ts:56-184`).

**Docs.** `id-token: write` permits OIDC. Interactive mode posts progress. With `prompt`,
automation runs without a mention and logs results. Actor checks and default bot rejection
still apply. GitHub Actions receives the event, not an Action-owned webhook endpoint
(code.claude.com/docs/en/github-actions).

### L3. Working comments and publication identity

**Code.** Tag mode creates a tracking comment with a workflow-run link. Inline review
comments receive an inline reply. Other triggers get issue/PR conversation comments.
`use_sticky_comment` can reuse a prior PR comment. Updates select the matching API and can
fall back if a review comment disappears
(`anthropics/claude-code-action@925afb5`
`src/github/operations/comments/create-initial.ts:19-83`,
`src/github/operations/comments/update-claude-comment.ts:25-69` and
`src/modes/tag/index.ts:30-52`).

**Code.** Default git inputs are `bot_name: claude[bot]` and `bot_id: 41898282`.
Git configuration builds the corresponding noreply identity. These inputs can be changed.
Git credentials use the Action's token. The resulting PR API actor follows that token, not
the configured git author. A workflow may offer a PR creation link rather than automatically
open one. Do not infer user-to-server publication just from a human's trigger
(`anthropics/claude-code-action@925afb5 action.yml:128-135`,
`src/github/operations/git-config.ts:22-135` and `cloud-agent-internals.md §C4`).

## M. Copilot cloud agent

### M1. Native integration does not publish ordinary event subscriptions

Copilot's write-user trigger limits and signed commits are recapped in
`cloud-agent-internals.md §D3`.

**Docs.** The public `copilot-swe-agent` record has `events: []`. This does not prove that
GitHub's native backend ignores repository events. It means the public App description does
not expose a normal subscription list (inference) (api.github.com/apps/copilot-swe-agent).

**Docs.** Write permissions are `actions`, `agent_tasks`, `contents`, `copilot_requests`,
`discussions`, `environments`, `gists`, `issues`, `plugin_gateway_connections`,
`pull_requests` and `workflows`. Read permissions are `actions_variables`, `administration`,
`agent_secrets`, `agent_variables`, `checks`, `code_quality`, `metadata`,
`organization_agent_secrets`, `organization_agent_variables`, `packages`, `secrets`,
`security_events` and `statuses` (api.github.com/apps/copilot-swe-agent).

### M2. Current task acknowledgement and progress

**Docs.** Assigning an issue always produces a PR. A task started from a prompt now works on
a branch by default, unless asked to open a PR. Repository seeding makes a draft PR.
Issue title, description and existing comments are captured at assignment. Later context
belongs on the PR, not subsequent issue comments ([task startup][copilot-start]).

**Docs.** The agents panel exposes logs, progress, token use and duration. Issue tasks request
review on completion. Commits name Copilot as author and the initiator as co-author, with
signatures and session links. A fixed reaction/working-comment sequence and private
signature/token-refresh implementation are not specified ([session tracking][copilot-progress]).

## N. Cursor Cloud Agents and Bugbot

### N1. One App supports two trigger products

GitHub access and HSM-backed signing were covered in `cloud-agent-internals.md §A7`.

**Docs.** `cursor` has write permissions for `actions`, `checks`, `contents`, `discussions`,
`issues`, `merge_queues`, `pull_requests` and `workflows`. Read permissions cover
`administration`, `deployments`, `emails`, `members`, `metadata`,
`organization_custom_org_roles`, `organization_custom_properties`,
`organization_custom_roles`, `packages`, `pages` and `statuses`
(api.github.com/apps/cursor).

**Docs.** Events are `branch_protection_configuration`, `branch_protection_rule`, `check_run`,
`check_suite`, `commit_comment`, `deployment`, `deployment_status`, `issues`, `issue_comment`,
`label`, `member`, `membership`, `merge_queue_entry`, `organization`, `pull_request`,
`pull_request_review`, `pull_request_review_comment`, `pull_request_review_thread`, `push`,
`repository`, `repository_ruleset`, `status`, `team`, `team_add`, `workflow_job` and
`workflow_run` (api.github.com/apps/cursor).

**Docs.** `@cursor` on an issue or PR starts a cloud agent that pushes a branch for handoff.
An installation belongs to one Cursor account/team unless explicitly shared in a Cursor
Organization. This is another routing boundary beyond installation ID (inference)
(cursor.com/docs/cloud-agent and cursor.com/docs/integrations/github).

### N2. Bugbot acknowledgement is a check, not a coding-task lease

**Docs.** Bugbot reviews PR updates and accepts `cursor review` or `bugbot run`.
Manual commands from bots or Apps are ignored. The check is `Cursor Bugbot`.
Success means no findings. Findings, cancellation and internal error normally conclude
neutral. Configured unresolved findings can fail it. Autofix has a separate
`Cursor Bugbot Autofix` check. Configuration comes from base-branch
`.cursor/config/bugbot.yaml`, not untrusted PR-head configuration
(cursor.com/docs/bugbot).

**Docs.** The checked docs do not identify an exact eyes/working-comment contract for cloud
mentions or the user-token versus App-token split for commits and PR creation. Bugbot review
output is documented. Its publication identity cannot be generalized to every cloud-agent
handoff (cursor.com/docs/bugbot and integrations/github).

## O. Codex

### O1. Connector permissions and supported mention behavior

Existing task triggers are recapped in `cloud-agent-internals.md §B4`.

**Docs.** `chatgpt-codex-connector` requests write `actions`, `contents`, `issues`,
`pull_requests` and `workflows`. It requests read `checks`, `emails`, `metadata` and
`statuses`. Events are `check_run`, `check_suite`, `commit_comment`, `issues`,
`issue_comment`, `pull_request`, `pull_request_review`, `pull_request_review_comment`,
`pull_request_review_thread`, `repository`, `status` and `sub_issues`
(api.github.com/apps/chatgpt-codex-connector).

**Docs.** `@codex review` receives an eyes reaction, then a GitHub review. GitHub reviews
focus on P0/P1 issues. Other `@codex` comments can start a legacy cloud task with PR context
and can request a branch fix where permitted. The docs do not establish commit author, PR
creator, backend signature verification or token-refresh mechanics
(learn.chatgpt.com/docs/third-party/github).

## P. Devin

### P1. Trigger gate and App scope

Existing GitHub identity coverage is `cloud-agent-internals.md §E5`.

**Docs.** The current write set is `actions`, `checks`, `contents`, `discussions`, `issues`,
`pull_requests`, `repository_creation`, `repository_projects`, `statuses` and `workflows`.
The read set is `code_quality`, `deployments`, `emails`, `members`, `metadata`,
`organization_administration`, `organization_custom_org_roles`, `packages`, `pages`,
`repository_advisories`, `repository_hooks`, `secret_scanning_alerts`, `security_events`
and `vulnerability_alerts` (api.github.com/apps/devin-ai-integration).

**Docs.** Events are `check_run`, `dependabot_alert`, `issues`, `issue_comment`, `member`,
`membership`, `organization`, `pull_request`, `pull_request_review`,
`pull_request_review_comment`, `push`, `repository`, `status`, `team` and `team_add`
(api.github.com/apps/devin-ai-integration).

**Docs.** `/devin <prompt>` must begin a comment, case-insensitively. PR conversation and
inline comments work on open PRs. Issues, closed PRs and merged PRs do not. The commenter
needs write/admin access, linked GitHub and Devin identity and permission to create an
organization session. Devin replies with a session link. An existing active session for the
PR receives follow-up instead of another independent task (docs.devin.ai/integrations/gh).

### P2. Authorship is an explicit policy

**Docs.** PR author modes are Devin, linked user with Devin fallback and user-only.
User-only rejects an unlinked human, with documented fallback exceptions for automation and
service users. Personal GitHub linkage uses OAuth and is revoked on unlink.
Commit author and committer have a separate matrix, including Devin-only, co-authored,
user-as-author with Devin committer and user-only. Enterprise policy can override a user's
selection. A human-looking commit is not evidence of a human API token in the sandbox
(inference) (docs.devin.ai/integrations/gh).

## Q. Jules

### Q1. Labels, review comments and progress

**Docs.** `google-labs-jules` requests write `actions`, `contents`, `issues`, `pull_requests`
and `workflows`. Read access is `administration`, `artifact_metadata`, `checks`,
`deployments`, `emails`, `members`, `metadata` and `security_events`
(api.github.com/apps/google-labs-jules).

**Docs.** Events are `check_run`, `check_suite`, `commit_comment`, `create`, `delete`,
`deployment`, `deployment_review`, `deployment_status`, `fork`, `gollum`, `issues`,
`issue_comment`, `label`, `member`, `membership`, `merge_queue_entry`, `milestone`,
`organization`, `public`, `pull_request`, `pull_request_review`,
`pull_request_review_comment`, `pull_request_review_thread`, `push`, `release`, `repository`,
`repository_dispatch`, `star`, `sub_issues`, `team`, `watch`, `workflow_dispatch`,
`workflow_job` and `workflow_run` (api.github.com/apps/google-labs-jules).

**Docs.** Applying a case-insensitive `jules` label to an issue starts a task. Jules posts
an acknowledgement comment and eventually a PR link. Review feedback receives eyes and can
produce another commit. Reactive Mode limits action to comments explicitly mentioning Jules.
The task UI has an activity feed, plan and diffs
(jules.google/docs/running-tasks.md and docs/changelog/2025-09-23.md).

### Q2. Do not keep the old author statement as the only truth

**Docs.** The February 19 changelog adds Jules-only, co-authored and user-only commit modes,
applied across task types. The older running-tasks guide says Jules is the commit author.
The newer authorship setting qualifies that statement. The changelog says Jules previously
authored every PR but does not precisely restate the current PR API actor for every mode.
It also adds automatic repair of failing CI on Jules PRs
(jules.google/docs/changelog/2026-02-19.md and running-tasks.md).

## R. OpenHands resolver

### R1. An archived Actions implementation, not an inferred current App

**Code.** The resolver repository is archived. Its README points to the former
`OpenHands/openhands/resolver` location. That path is absent from the checked current
OpenHands tree. The implementation below is a historical reference, not evidence for today's
hosted OpenHands App permissions or endpoint
(`All-Hands-AI/openhands-resolver@baa2f45 README.md:1-4` and
`OpenHands/OpenHands@fee10e3`, recursive tree checked).

**Code.** The reusable workflow subscribes to issue/PR labels, issue comments, inline review
comments and submitted reviews. It grants contents, issues and PR write. Label `fix-me` or
a comment beginning `@openhands-agent` triggers it. Comment/review authorization uses
`OWNER`, `COLLABORATOR` or `MEMBER` author association, unlike Claude's current permission
lookup. It branches on review body, PR issue marker and top-level PR number
(`All-Hands-AI/openhands-resolver@baa2f45`
`.github/workflows/openhands-resolver.yml:25-57,108-125`).

### R2. Progress and two publishing identities

**Code.** Start and final comments use the workflow's `GITHUB_TOKEN`. Output becomes an
Actions artifact. Successful resolution creates a draft PR. Failed resolution can still push
an attempted branch. The PR publication step instead maps `PAT_TOKEN` into `GITHUB_TOKEN`.
The PR API actor is therefore the supplied PAT's owner, not necessarily the workflow bot
(`All-Hands-AI/openhands-resolver@baa2f45`
`.github/workflows/openhands-resolver.yml:131-155,178-211`).

**Code.** Git config is only filled if missing, with fallback name `openhands` and email
`openhands@all-hands.dev`. Push and PR calls use the supplied credential. Git author and PR
actor can differ. This Actions workflow does not implement an inbound HMAC server or mint
installation tokens with an App private key
(`All-Hands-AI/openhands-resolver@baa2f45`
`openhands_resolver/send_pull_request.py:139-155,195-211,249-251`).

## S. Open SWE

### S1. Self-hosted App scope and the real HTTP handler

**Code.** The current installation guide requests contents, issues, PRs, checks and workflows
write, with metadata and commit statuses read. Actions read is optional for logs. Actions
write is needed for CI reruns. Code-scanning read is optional. Members read supports
organization membership checks. It lists issue comments, PR reviews, inline review comments,
check runs, check suites and workflow runs, with status optional. The handler also supports
configured PR automation events, so the setup list is not every possible runtime event
(`langchain-ai/open-swe@a7bb530 docs/INSTALLATION.md:88-108` and
`openswe/github/routes.py:105-152`).

**Code.** `POST /webhooks/github` reads raw bytes, verifies `X-Hub-Signature-256` and rejects
with 401 on failure. The verifier rejects an unset secret and uses `hmac.compare_digest`.
It reads `X-GitHub-Event` and `X-GitHub-Delivery`, distinguishes PR conversation comments from
issues and dispatches reviews separately
(`langchain-ai/open-swe@a7bb530 openswe/github/routes.py:30-107,278-291` and
`openswe/github/comments.py:92-111`).

**Code.** The route calls `EventLog.record()` before normal dispatch, but does not check its
boolean result. Recording returns false on storage failure. Main mention processing is
scheduled through FastAPI `BackgroundTasks`, then the route returns accepted JSON. This
code path is not proof of fail-closed, durable enqueue-before-ack semantics (inference)
(`langchain-ai/open-swe@a7bb530 openswe/github/routes.py:42-52,278-288` and
`openswe/webhooks/event_log.py:236-298`).

### S2. Eyes, checks and token refresh

**Code.** Comment reactions use REST `eyes`. Review-body reactions use GraphQL `EYES` with
the review node ID, not the inline-comment REST endpoint. The install guide promises eyes,
a run and a reply for a mapped commenter. Auto-review reports `Open SWE Review` check
progress. This separates human-visible acceptance from the original webhook response
(`langchain-ai/open-swe@a7bb530 openswe/github/comments.py:82-86,163-237` and
`docs/INSTALLATION.md:101,290`).

**Code.** Installation tokens use an App private key and GitHub's token-creation API.
The cache key includes installation, repository selection and permission scope.
The in-process cache retires a token ten minutes before expiry. That margin stays above the
git proxy's five-minute refresh window. Minting reads returned expiry, not a hard-coded
assumption about token length or lifetime
(`langchain-ai/open-swe@a7bb530 openswe/github/app.py:26-54,73-89,157-208`).

### S3. The requester publishing the PR may change between runs

**Code.** Public coding threads normally use installation-wide App access for agent
operations. This is not intersection with each initiating user's repository permissions.
For user-owned threads, PR creation uses the current publishing run requester's stored
OAuth token. A later participant starting a publishing run can become PR author.
Missing requester identity or token fails instead of silently falling back to the bot
(`langchain-ai/open-swe@a7bb530 docs/INSTALLATION.md:7-15,461-469`).

**Code.** System-owned threads, including scheduled automations, open PRs as the App.
Legacy unowned threads retain bot authorship. A schedule records its creator for auditing
but does not require that person's OAuth credential. These policies are explicit, not
deduced from a commit's displayed author. Exact sandbox git author defaults were not traced
in this read (`langchain-ai/open-swe@a7bb530 docs/INSTALLATION.md:470-489`).

## What a self-hosted front door must provide

These are generic deductions from the systems above, not a design for a particular host.

1. **Separate previews from durable receipt.** Hold requests through guest startup. Persist
   asynchronous tasks before acknowledging durable receipt (inference) (§A4, §I4, §J, §S1).
2. **Keep a live control boundary.** Whole-host startup needs a receiver elsewhere or an
   independent control path. A tunnel alone is insufficient (inference) (§I4, §J4).
3. **Validate the whole route.** Bind host/domain, sandbox and allowed port. Prevent routing
   headers from overriding unrelated hosts (inference) (§A2, §D1, §G1).
4. **Authorize before wake.** Check provider signatures or preview credentials before allocation.
   Protect control ports separately from app traffic (inference) (§A2, §A3, §B1, §I1, §K1).
5. **Specify credential scope and lifetime.** Define expiry, rotation and revocation.
   A bearer URL is not human identity (inference) (§A2, §B1, §C1, §D1).
6. **Bound startup and overload.** Limit transition waits, readiness, flights and parked requests.
   Distinguish failure classes. Do not blindly replay POSTs (inference) (§A3 to §A5, §D2).
7. **Define liveness.** Deadlines, requests, sockets and working leases differ. Long builds need
   an explicit policy, not accidental terminal output (inference) (§A1, §B2, §C2, §D2, §G2, §H2).
8. **Specify streaming and reconnects.** Preserve upgrades, SSE and cancellation. State whether
   sockets prevent sleep. Memory restore does not preserve peers (inference) (§A5, §D2, §E1, §F2).
9. **Make retries duplicate-safe.** Keep delivery IDs, task IDs, retention and replay controls.
   Handle reordering and oversized payloads (inference) (§J1 to §J4, §K1).
10. **Separate parsing from authorization.** Normalize comment/review families. Check current
    access, prevent bot loops and bind PR/head context (inference) (§K2, §L2, §N2, §P1).
11. **Track product acknowledgement separately.** Link eyes, working comments and progress to
    one recoverable task and its final PR (inference) (§L3, §M2, §Q1, §R2, §S2).
12. **Define publication identity.** Separate author, committer, push actor and PR actor. Scope
    and refresh tokens. Choose fallback or failure (inference) (§K3, §L2, §P2, §Q2, §S2, §S3).

## Gaps

- **E2B deployment.** Source parking defaults can be overridden. Hosted flags and wake latency
  were not probed. The checked docs give no wake SLA (§A4, §A5).
- **Stopped previews.** Native Daytona wake and Codespaces wake remain unspecified. Coder's
  checked app path returns an offline error (§B2, §G2, §H2).
- **TLS internals.** Not every internal TLS hop is published for Daytona, CodeSandbox,
  Codespaces or Sprites (§B2, §C1, §E2, §H1).
- **WebSockets and wake.** Codespaces and Sprites lack an explicit checked upgrade contract.
  Tailscale wake semantics and Fly's first-request startup timeout remain unverified (§E, §H, §I2).
- **Modal wake.** Finished sandbox HTTP wake is not promised. Function scale-to-zero is a
  different contract (§F).
- **Inbox bounds.** Hookdeck's exact persistence-before-ack barrier and global ordering are
  unspecified. Svix Ingest retention and polling-plan availability remain open (§J1, §J3).
- **Closed App internals.** The checked sources do not expose complete vendor HMAC/token-cache
  implementations. Claude's exchange code does not prove long-run token refresh (§L to §Q).
- **Attribution.** Cursor/Codex actor mappings, Jules's newer PR actor mapping and Open SWE's
  git author default remain unspecified or untraced (§N, §O, §Q2, §S3).
- **Resolver lineage.** Its archived path is absent from current OpenHands. The old workflow
  does not establish today's hosted integration (§R1).
- **Pricing and live tests.** Access's stable numeric seat schedule was not exposed. No accounts,
  tunnels or webhooks were created. No vendor wake or API behavior was tested (§I1).
