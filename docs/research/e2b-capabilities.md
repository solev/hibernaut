# E2B Infrastructure Capabilities — Research Notes for Hibernaut

- **Date:** 2026-09-30
- **Primary source:** `github.com/e2b-dev/infra` @ commit `5cfcf3282ddb95e90a8214ce71329641bdfe6cb6`
- **Repo identity note:** GitHub's API resolves `owner=e2b-dev, repo=infra` to the renamed repository `e2b-dev/runtime` at this commit (confirmed via `html_url`/`git_url` in every `get_file_contents` response and `search_code` results, which report `"repository":"e2b-dev/runtime"`). All permalinks below are written in the `e2b-dev/infra` form used by the user's request; they resolve correctly via GitHub's rename redirect to the same blobs at the same SHA. **Code at the pinned commit wins over any docs/prose below** — official docs (e2b.dev) were consulted only where noted, and are secondary to source citations.
- **Citation format:** `https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/path/to/file.ext#L<start>-L<end>` — every substantive claim below carries one.
- **Confidence markers:** claims are **Verified** (read directly from source) unless marked **Inferred** (reasoned from adjacent evidence) or **Docs-only** (from official docs, not cross-checked against this commit's code).

## Summary

E2B's infra is a control-plane/data-plane split system: a stateless **API** (Gin, REST + gRPC) fronts a fleet of **orchestrator** nodes (one Firecracker-microVM hypervisor process per bare-metal/VM host, root-run) and a stateless **client-proxy** edge router. Sandboxes are always "resumed snapshots" — a template is a pre-booted VM snapshot (memory + disk + VM state) in object storage, and a running sandbox can be paused (snapshotted), resumed (from that snapshot, same ID), or **forked** (snapshotted in place while the original keeps running, then N new sandboxes booted from that one snapshot with fresh IDs). Auto-pause is a server-side idle-timeout eviction sweep; auto-resume is lazy, triggered by the first inbound proxy request hitting a paused sandbox's routing miss, including envd-port SDK traffic when policy allows. Inbound traffic is routed by parsing `<port>-<sandboxID>.<domain>` at the client-proxy, looking up the owning node via a Redis-backed routing record, and forwarding via plain HTTP/HTTPS to the orchestrator's own reverse proxy (port 5007), which in turn dials the sandbox's per-slot host IP (NAT'd into a private network namespace + TAP device) — no vsock or unix sockets are used for sandbox traffic. Inside the guest, `envd` (in-VM agent, port 49983, Connect RPC + REST) exposes process/filesystem APIs and receives `envVars` (but not `metadata`) via a `POST /init` call driven from the orchestrator. Egress control has SNI/Host-aware raw TCP allow/deny plumbing, but the pinned OSS proxy does **not** implement TLS MITM or outbound secret substitution; `caBundle` installation plumbing exists but the OSS egress proxies return an empty CA. Persistence in local/self-hosted mode uses Postgres + ClickHouse (Docker volumes, durable) and Redis (no volume, fully ephemeral) for control-plane state, and a **pluggable storage abstraction** (`Local` filesystem / GCS / AWS S3 / Azure Blob) for template & snapshot artifacts, defaulting to GCS unless `STORAGE_PROVIDER=Local` is set.

---

## 1. REST API — sandbox lifecycle endpoints and fields

Source: `spec/openapi.yml` (~5581 lines) at the pinned commit, cross-checked against Go handlers in `packages/api/internal`.

### 1.1 Create

| Endpoint | Notes |
|---|---|
| `POST /sandboxes` (deprecated) | https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L2828-L2905. Request `NewSandbox` (L915-L967), response `201` `Sandbox` (L756-L789). |
| `POST /v2/sandboxes` (current) | https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L2906-L2933. Request `NewSandboxV2` (L968-L1017). |

`NewSandbox`/`NewSandboxV2` fields (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L915-L1017): `templateID` (required), `timeout` (default 15s v1, 300s v2), `autoPause` (bool, default false), `autoPauseMemory` (bool, default true — controls whether an auto-pause snapshot is full-memory or filesystem-only; `false` cannot combine with `autoResume`), `autoResume` (`SandboxAutoResumeConfig`, L601-L608), `secure`, `allow_internet_access`, `network` (`SandboxNetworkConfig`), `metadata` (`SandboxMetadata`, L420-L423, arbitrary string map), `envVars` (`EnvVars`, L458-L461, arbitrary string map), `mcp`, `iam` (`SandboxIam`), `volumeMounts`.

### 1.2 Read

- `GET /sandboxes/{sandboxID}` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3170-L3186. Response `SandboxDetail` (L790-L855): `startedAt`, `endAt`, `cpuCount`, `memoryMB`, `diskSizeMB`, `metadata`, `state` (`SandboxState` enum `running`/`paused`, L425-L431), `network`, `lifecycle` (`SandboxLifecycle`, L617-L629: `autoResume` bool + `onTimeout` enum `kill`/`pause`), `volumeMounts`.
- `GET /sandboxes` (deprecated, L2828-L2860) / `GET /v2/sandboxes` (current — filters `metadata`, `state`, `order`, `startedAfter`, `template`, pagination) — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L2906-L3017. Response: array of `ListedSandbox` (L856-L914).
- `GET /sandboxes/{sandboxID}/metrics` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3227-L3277 (query `start`/`end` unix seconds; `SandboxMetric` schema L692-L742). `GET /sandboxes/metrics` (bulk, ≤100 IDs) — L3018-L3059 → `SandboxesWithMetrics` (L913-L919).

### 1.3 Delete

- `DELETE /sandboxes/{sandboxID}` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3187-L3200. `204`, no body.

### 1.4 Pause

- `POST /sandboxes/{sandboxID}/pause` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3278-L3314. Optional request body `SandboxPauseRequest` (L1138-L1151): single field `memory: boolean` (default `true`; `false` = filesystem-only snapshot, and per spec text "auto-resume … refuses such a sandbox"). Response `204`, no body. A comment at L3169 flags a future `POST /sandboxes/{sandboxID}/snapshot` naming unification.
- Handler: `PostSandboxesSandboxIDPause` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox_pause.go#L44-L121. Parses optional `memory`, computes `filesystemOnly := body.Memory != nil && !*body.Memory`, calls `backend.RemoveSandbox(ctx, teamID, sandboxID, sandbox.RemoveOpts{Action: sandbox.StateActionPause, FilesystemOnly: filesystemOnly})`.
- Node-side snapshot RPC: `pauseSandbox`/`snapshotInstance` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/pause_instance.go#L31-L104 — calls orchestrator gRPC `client.Sandbox.Pause(ctx, &orchestrator.SandboxPauseRequest{SandboxId, TemplateId, BuildId, FilesystemOnly})` (L83-L90). `buildUpsertSnapshotParams` (L109-L152) persists a `queries.UpsertSnapshotParams` row with `Config: &types.PausedSandboxConfig{Network, AutoResume, VolumeMounts, FilesystemOnly, AutoPauseFilesystemOnly, Iam}` and `SourceBuildID: sbx.BuildID` — pinned to the *originating build* so a pause/resume across CPU generations stays compatible (L149-L151).

### 1.5 Resume / connect (explicit)

- `POST /sandboxes/{sandboxID}/resume` (deprecated) — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3315-L3360. Request `ResumedSandbox` (L1050-L1071): `timeout`, `autoPause` (deprecated), `memory`. Response `201` `Sandbox`.
- `POST /sandboxes/{sandboxID}/connect` (deprecated) — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3413-L3464. Request `ConnectSandbox` (L1072-L1091, required `timeout`). Doc: "Returns sandbox details. If the sandbox is paused, it will be resumed."
- `POST /v2/sandboxes/{sandboxID}/connect` (current) — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3465-L3518. Request `ConnectSandboxV2` (L1092-L1110, `timeout` default 300, optional). `200` (already running) or `201` (resumed): `Sandbox`.
- Handler: `PostSandboxesSandboxIDResume` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox_resume.go#L47-L231. If the sandbox record is mid-transition (`StatePausing`/`StateSnapshotting`) it waits (`backend.WaitForStateChange`, L104-L128); else it fetches the **last snapshot** (`a.snapshotCache.Get`, L152) and calls `a.startSandbox(..., a.buildResumeSandboxData(...), ..., true /*isResume*/, ...)` (L189-L200) — resume reuses the **same `sandboxID`**. `buildResumeSandboxDataFromSnapshot` (L245-L314) is the shared helper also used by fork, distinguishing `sandboxID` (execution target) from `snapshotSandboxID` (source) — comment at L237-L244.

### 1.6 Fork/clone

- `POST /sandboxes/{sandboxID}/fork` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3361-L3412. Doc: *"checkpoint the running sandbox in place (it is briefly paused, snapshotted with its full memory state, and resumed on its node, keeping its ID and expiration untouched) and create `count` new sandboxes from that snapshot."* Request `SandboxForkRequest` (L1152-L1172: `timeout` default 15s, `count` 1-100 default 1). Response `201`: array of `SandboxForkResult` (L1173-L1183, each entry `sandbox` or `error`).
- Handler: `PostSandboxesSandboxIDFork` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox_fork.go#L37-L206. Calls `a.orchestrator.CheckpointSandbox(ctx, teamID, sandboxID)` once (L123) to snapshot the original in place, then in parallel (`errgroup`) for each of `forkCount` forks generates `forkedSandboxID := InstanceIDPrefix + id.Generate()` and calls `a.startSandboxInternal(..., a.buildResumeSandboxDataFromSnapshot(sandboxID, forkedSandboxID, nil, nil), ..., false /*isResume*/, ...)` (L162-L178) — **each fork is created as a new sandbox** (not a resume), so placement spreads forks across the cluster. Result list at L216-L230, each fork independently succeeding/failing.
- Checkpoint primitive: `CheckpointSandbox` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/checkpoint_instance.go#L23-L131. Doc: *"snapshots a running sandbox in place: the sandbox is briefly paused on its node, snapshotted, and resumed under the same execution ID, so it keeps running with its ID, expiration, and reservation untouched. The snapshot is written to the sandbox's own snapshots row, so it can immediately be resumed or forked from."* Calls orchestrator gRPC `client.Sandbox.Checkpoint(ctx, &orchestrator.SandboxCheckpointRequest{SandboxId, BuildId, Metadata})` (L74-L79); on `FailedPrecondition`/`ResourceExhausted` refusal it restores the VM to Running rather than killing it (L88-L103).

### 1.7 Snapshot-to-template

- `POST /sandboxes/{sandboxID}/snapshots` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3616-L3654. Request `SandboxSnapshotRequest` (L1131-L1137, optional `name`). Response `201`: `SnapshotInfo` (L440-L457: `snapshotID`, `names[]`).
- `GET /snapshots` (list team snapshots, filterable `sandboxID`/`name`, paginated) — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3655-L3702.
- `POST /v3/templates` (build a template directly, not from a running sandbox) — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3703-L3729. Request `TemplateBuildRequestV3` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L1506-L1514). Response `202`: `TemplateRequestResponseV3` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L1349-L1357).
- Handler: `PostSandboxesSandboxIDSnapshots` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/snapshot_template_create.go#L24-L146. Resolves an optional `name` into a namespace/alias/tag (L57-L91), fetches the running sandbox, calls `a.orchestrator.CreateSnapshotTemplate(ctx, teamID, sandboxID, opts)` (L108), returns `api.SnapshotInfo{SnapshotID, Names}` (L143-L146). This reuses the pause/checkpoint machinery but registers the result as a **template** (with its own alias/tag) so it survives the source sandbox's deletion and can be used as `templateID` for future creates.

### 1.8 Timeout / keepalive / network update

- `POST /sandboxes/{sandboxID}/timeout` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3519-L3550. Request `SandboxTimeoutRequest` (L1111-L1121, required `timeout`). Doc: "Calling this method multiple times overwrites the TTL … using the current timestamp as the starting point." `204`, no body.
- `POST /sandboxes/{sandboxID}/refreshes` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3586-L3615. Request `SandboxRefreshRequest` (L1122-L1130, `duration` max 3600s).
- `PUT /sandboxes/{sandboxID}/network` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3551-L3585. Request `SandboxNetworkUpdateConfig` (L514-L534).

### 1.9 Fork vs. resume vs. snapshot-to-template — semantics summary

- **Resume** (`/resume`, `/v2/.../connect`): same `sandboxID`; restarts the *same logical sandbox* from its last stored snapshot (full-memory or filesystem-only, per snapshot kind / `memory` override).
- **Fork** (`/fork`): checkpoints (pause+snapshot+resume) the *original* in place so it stays untouched, then boots N *new* sandboxes with fresh IDs from that one snapshot, independently placed across the cluster (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox_fork.go#L150-L179).
- **Snapshot-to-template** (`/sandboxes/{id}/snapshots`, `POST /v3/templates`): persists a snapshot as a first-class **template** with an alias/tag, decoupling its lifetime from the source sandbox and making it usable as `templateID` for any future `NewSandbox`/`NewSandboxV2` create — not merely a resume target for that one sandbox.

---

## 2. Auto-pause / auto-resume behavior

### 2.1 Auto-pause (idle-timeout eviction — server-side, polling)

`Evictor.Start`/`evictSandbox` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/evictor/evict.go#L106-L149. A background ticker (`pollInterval = 50ms`, L24) periodically calls `e.store.ExpiredItems(ctx)` (L118) to find sandboxes whose `EndTime` has passed. For each:
```go
action := sandbox.StateActionKill
if sbx.AutoPause && canTake(sbx.State, sandbox.StateActionPause) {
    action = sandbox.StateActionPause
    ...
}
```
(https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/evictor/evict.go#L164-L168) — i.e. `autoPause=true` (set at create time) is what turns an idle-timeout eviction into a pause instead of a kill. `opts.FilesystemOnly = sbx.AutoPauseFilesystemOnly` (L177) honors the `autoPauseMemory` create-time setting. A "degrade to filesystem-only" fallback (L180-L192, `degradeCause` L237-L249) triggers if a node keeps refusing memory pauses past a feature-flag-configured overstay budget (`AutoPauseOverstayBudgetMs`, default **120,000 ms** — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L646-L655; routing-record behavior cross-referenced at https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/docs/ARCHITECTURE.md#L471-L489).

### 2.2 Auto-resume (lazy — triggered by first inbound request, not a poller)

- `SandboxService.ResumeSandbox` gRPC — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/proxy_grpc.go#L127-L243. Called by client-proxy on a routing-cache miss for inbound sandbox traffic:
  1. Resolves the last snapshot (`getAutoResumeSnapshot`, L100-L119) and requires `autoResume.Policy == SandboxAutoResumeAny` (L112-L114) — else returns `NotFound` ("sandbox auto-resume disabled").
  2. Refuses a **filesystem-only** snapshot outright (L117-L119: "filesystem-only snapshot must be resumed explicitly") — auto-resume applies only to full-memory snapshots.
  3. If the sandbox record still exists (mid-transition or already running), delegates to `HandleExistingSandboxAutoResume` (L179-L195).
  4. Otherwise calls `s.api.startSandboxInternal(..., s.api.buildResumeSandboxData(sandboxID, nil, nil), ..., true /*isResume*/, ...)` (L232-L242) to physically resume it, returning the fresh orchestrator IP.
- `HandleExistingSandboxAutoResume` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/autoresume.go#L23-L118. Waits on `Pausing`/`Snapshotting` up to `MaxAutoResumeTransitionRetries = 3` (L18); returns "not found" for `Killing`; returns node IP immediately if already `Running`.
- Timeout: `calculateAutoResumeTimeout` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/timeout_helper.go#L60-L68 — defaults to `defaultProxyAutoResumeTimeout = 5 * time.Minute` (L16) unless `autoResume.Timeout` was set at create time, clamped to the team's plan limit and a feature-flagged minimum.
- Auth still applies during auto-resume: private-ingress sandboxes require a matching `trafficAccessToken` (L245-L258); secure sandboxes validate the envd access token on envd traffic (L261-L267) — auto-resume does not bypass access control.
- On the routing side, client-proxy's cache-miss path is `handlePausedSandbox` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/client-proxy/internal/proxy/proxy.go#L95-L124 — mapping gRPC error codes to specific proxy errors (`PermissionDenied`→auto-resume-permission-denied, `NotFound`→sandbox-not-found, `FailedPrecondition`+`SandboxStillTransitioningMessage`→still-transitioning, `ResourceExhausted`→resource-exhausted) at L105-L119.

**Conclusion:** auto-pause is a server-side idle-timeout sweep (`Evictor`); auto-resume is lazy, edge-triggered by the very first request the client-proxy forwards for a paused sandbox, and both are gated by explicit per-sandbox policy fields (`autoPause`/`autoPauseMemory` at create; `autoResume.policy`/`autoResume.timeout` at create, checked against the *snapshot's* memory-vs-filesystem-only kind at resume time).

---

## 3. Networking internals

Source: `packages/orchestrator/pkg/sandbox/network/{slot.go,network.go,pool.go,firewall.go}`, cross-checked with `docs/ARCHITECTURE.md`.

### 3.1 Slot model / IP allocation

Each sandbox gets a numeric **slot** (`Idx`) from which three IP families derive deterministically — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/slot.go#L44-L96:

- **Host IP** — `/32` inside a `/16`, default `10.11.0.0/16` (`defaultHostNetworkCIDR`, L32-L33; configurable via `SANDBOXES_HOST_NETWORK_CIDR`); allocated by index via `netutils.GetIndexedIP` (L100-L104). This is the IP the orchestrator's own reverse proxy dials.
- **Vpeer/Veth IPs** — `/31` per slot inside `10.12.0.0/16` (`defaultVrtNetworkCIDR`, configurable via `SANDBOXES_VRT_NETWORK_CIDR`), `vrtAddressPerSlot = 2` (L33-L38, L74-L88).
- **Tap IP** — fixed link-local `169.254.0.22/30` with fixed MACs `02:FC:00:00:00:05`/`06` (L29-L42, L106-L111); guest-facing DNAT/PREROUTING gateway `NamespaceIP() = "169.254.0.21"` (L172-L174).
- `NewSlot` validates `idx ∈ [1, GetVrtSlotsSize()]` (L66-L69, L280-L292).

### 3.2 Namespace/device creation (`Slot.CreateNetwork`)

https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/network.go#L74-L260. Steps: (1) create a new named netns `ns-<idx>` (L121); (2) create a veth pair, peer stays in the namespace (Vpeer IP), other end moves to host namespace (Veth IP) via `netlink.LinkSetNsFd` (L130-L183); (3) inside the namespace, create a **TAP device** `tap0` (fixed MAC) that Firecracker attaches to as the guest NIC (L186-L215); (4) default route + **iptables NAT**: SNAT namespace-internal→host-IP on egress, DNAT host-IP-destined→namespace on ingress (L225-L238); (5) optional DSCP QoS marking (L240-L249); (6) `InitializeFirewall()` (nftables per-slot firewall); (7) host-side routes/FORWARD/MASQUERADE plus three **PREROUTING REDIRECT** rules sending guest traffic for a fixed "orchestrator-in-sandbox" IP (`192.0.2.1` default) on ports 80/111/2049 to the host's hyperloop proxy, portmapper, and NFS proxy respectively (L298-L320). `RemoveNetwork` (L327-L395) reverses all of this, preserving the netns anchor on partial teardown failure for a later reclaim sweep.

### 3.3 Isolation & firewall

Each sandbox has its own Linux network namespace + veth + TAP (never shares a bridge with other guests) plus a dedicated nftables table `slot-firewall` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/firewall.go#L39-L107) with `predefinedDenySet`/`predefinedAllowSet`/`userDenySet`/`userAllowSet` IP sets bound to a `PREROUTE_FILTER` chain at `ChainHookPrerouting` priority `-150`, scoped to the sandbox's own tap interface (`tapIfaceMatch()`, L170-L179) — enabling per-sandbox egress allow/deny CIDR lists plus SNI/Host-header-aware TCP inspection (`SandboxTCPFirewallHTTPPort/TLSPort/OtherPort`, https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/pool.go#L78-L83) for domain allow/deny lists.

### 3.4 Slot pooling

`Pool` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/pool.go#L150-L205): `NewSlotsPoolSize=32` fresh-slot channel, `ReusedSlotsPoolSize=100` recycled-slot channel, `ReturnDelay=3s` before reuse (lets in-flight requests drain). Slot allocation is node-local (`storage_local.go`); `reclaim.go` handles startup reclamation of leftover namespaces from a prior orchestrator crash. A `NETWORK_VERSION` config (default `1`) selects between this iptables implementation and a `v2/` nftables/host-sets implementation (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/pool.go#L96-L98; not deep-dived).

**Confidence:** Verified for slot/netns/TAP/NAT mechanics; `v2/` nftables implementation existence only (contents not read).

---

## 4. Inbound routing / client-proxy / orchestrator proxy

### 4.1 Host/subdomain parsing

`https://<port>-<sandboxID>.<domain>` requests are parsed into `(sandboxID, port)` by shared code `reverseproxy.GetTargetFromRequest()` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/proxy/host.go#L16-L42 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/proxy/host.go#L74-L99; also used at https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/client-proxy/internal/proxy/proxy.go#L139-L141 and summarized in https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/docs/ARCHITECTURE.md#L311-L317).

### 4.2 Sandbox → node lookup (service discovery)

Client-proxy is stateless; it looks up the owning node via a **Redis routing record**, not a persistent catalog:
- `catalog := e2bcatalog.NewRedisSandboxRoutingCatalog(redisClient)` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/client-proxy/main.go#L120-L121.
- Record key: `sandbox:routing:{sandboxID}` (JSON `sandbox_catalog.SandboxInfo`: `orchestrator_id`, `orchestrator_ip`, `execution_id`, `sandbox_started_at`, `sandbox_max_length_in_hours`) — written by the orchestrator on `MarkRunning` (envd ready), deleted on `MarkStopping` (kill/pause/checkpoint/crash), TTL = `sandbox_max_length_in_hours`, delete guarded by `execution_id` via a Lua script to avoid a stale lifecycle deleting a newer record (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/docs/ARCHITECTURE.md#L471-L489 and `SandboxInfo.MaxLengthInHours` at https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/sandbox-catalog/catalog.go#L11-L23). An older API-owned key `sandbox:catalog:{sandboxID}` still exists but is no longer read by client-proxy.
- Lookup: `catalogResolution` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/client-proxy/internal/proxy/proxy.go#L69-L88. On `catalog.ErrSandboxNotFound` miss it falls through to `handlePausedSandbox` (auto-resume path, §2.2).

### 4.3 Forwarding to orchestrator node

Once a node IP is resolved, client-proxy builds an HTTP destination at that node's **orchestrator proxy port** (default `5007`, `ORCHESTRATOR_PROXY_PORT`/`config.OrchestratorProxyPort`):
```go
url := &url.URL{Scheme: "http", Host: net.JoinHostPort(nodeIP, strconv.Itoa(int(orchestratorProxyPort)))}
```
https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/client-proxy/internal/proxy/proxy.go#L156-L159. Handled by shared `reverseproxy.Proxy`. `clientProxyMaskRequestHost` (L57-L67) can rewrite the `Host` header downstream, gated by feature flag `OrchAcceptsCombinedHostFlag`. Two access-control headers propagate downstream: `e2b-traffic-access-token` and an envd HTTP access-token header (L75-L77, L143-L144).

Note (**Inferred**): despite the directory being `packages/client-proxy`, `main.go` imports reference `github.com/e2b-dev/infra/packages/proxy/internal...` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/client-proxy/main.go#L19-L22) — the Go module path is `packages/proxy`, distinct from the directory name; likely a historical rename where the directory moved but the import path didn't (not independently verified from `go.mod`).

### 4.4 Orchestrator's own reverse proxy (port 5007)

`newDestinationResolver` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/proxy/proxy.go#L66-L146. Steps:
1. Parses `(sandboxID, port)` via the same host-parsing helper.
2. **Blocks envd's internal routes from external reach**: if target port == envd's default port (`consts.DefaultEnvdServerPort`, 49983) *and* the path is internal (`envd.IsInternalPath`, generated from `x-internal: true` markers in `spec/envd.yaml`), the proxy refuses (`ErrInternalRoute`) **before** looking up the sandbox at all, so a 404 leaks no information about sandbox existence (L82-L93).
3. Looks up the sandbox in the orchestrator's in-memory `sandbox.Map`; 404 (`ErrSandboxNotFound`) if absent.
4. Validates the per-sandbox `e2b-traffic-access-token` header via constant-time compare, unless the target port is envd's own (envd authenticates itself) (L98-L116).
5. Builds destination `scheme://<slot HostIP>:<port>` — `sbx.Slot.HostIPString()` is the same per-slot Host IP from §3.1; scheme is HTTP unless the port is listed in `SandboxNetworkIngressConfig.https_ports` and isn't envd's port (`schemeForPort`, L47-L56); HTTPS backends get `InsecureSkipTLSVerify` for self-signed guest certs (L135-L143).
6. `ConnectionKey: sbx.LifecycleID` scopes pooled connections per sandbox *lifecycle* (not sandbox ID) to avoid stale connection reuse across a pause/resume cycle that recycles the same IP:port for a different tenant (comment, L129-L132).

**Path summary:** client-proxy → HTTP → orchestrator `:5007` proxy → HTTP/HTTPS → slot Host IP : requested port (NAT'd through veth/tap into the Firecracker guest netns) → envd or user process. **No vsock, no unix socket** for sandbox traffic (the only unix-socket use found is Firecracker's own local HTTP API for VM control, orthogonal to guest traffic).

### 4.5 Edge / cluster service discovery (`spec/openapi-edge.yml`)

Defines the cluster edge/BYOC API (not the sandbox hot-path): `ClusterServiceDiscovery`/`ClusterOrchestratorNode` (`nodeID`, `serviceHost`, `serviceStatus` enum `healthy|draining|unhealthy|standby|shutting_down`, `roles: [orchestrator, template-builder]`) via `GET /v1/service-discovery` and deprecated `GET /v1/service-discovery/nodes/orchestrators` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi-edge.yml#L224-L258). Also defines `/v1/sandboxes/{sandboxID}/logs`, sandbox metrics, and admin "Rig" (autoscaling group) endpoints under `/clusters/{clusterID}/rigs`.

---

## 5. envd — in-guest agent

Source: `packages/envd/{main.go,spec/}`.

### 5.1 Purpose & startup

envd starts early in guest boot (systemd-started) and listens on **port 49983** (`defaultPort = 49983`, https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/main.go#L44). Built on **chi** (HTTP router) + **Connect RPC** (gRPC-compatible unary/streaming over HTTP). Responsibilities wired in `run()`: filesystem service (`filesystemRpc.Handle`, L206-L208), process service (`processRpc.Handle`, L222-L223, sharing a `WorkloadFreezer` with `/freeze`/`/unfreeze`/`/init`), and a REST API exposing `/health`, `/metrics`, `/init`, `/envs`, `/files`, `/files/compose`, `/freeze`, `/unfreeze`, `/collapse`, `/fsfreeze`, `/fsthaw`, `/upgrade` (the last deliberately absent from the OpenAPI spec).

- **MMDS**: `go host.PollForMMDSOpts(ctx, mmdsChan, defaults.EnvVars)` (L184-L185) — envd learns its access token/config from Firecracker's **Microvm Metadata Service**, not a pushed HTTP body at boot.
- **Port forwarding**: `publicport.NewScanner(portScannerInterval)` (1s) + `publicport.NewForwarder(...)` (L301-L317) binds open ports on `127.0.0.1`/localhost to `eth0`, which is how a user process's opened port becomes reachable via sandbox URLs.
- **Live self-upgrade**: `/upgrade` streams a new envd binary into the running guest; envd `syscall.Exec`s into it with the same PID, carrying stdio/PTY fds forward via a tmpfs "handover" blob (`ResumeFromHandover`, L70-L77, L226-L260).

### 5.2 API surface

`packages/envd/spec/` contains `envd.yaml` (REST OpenAPI), `process/process.proto`, `filesystem/filesystem.proto`, `upgrade/`, `buf.gen.yaml`.

**REST (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/spec/envd.yaml#L18-L291):**
- `GET /health` — 204 healthy (L18-L24).
- `GET /metrics` — CPU/mem/disk JSON, optional `AccessTokenAuth` (L26-L35).
- `POST /init` (**x-internal**) — pushes `envVars`, `accessToken` (`SecureToken`), `hyperloopIP`, `lifecycleID`, `defaultUser`, `defaultWorkdir`, `volumeMounts`, `caBundle`, `timestamp` (L37-L77).
- `POST /freeze` / `/unfreeze` (**x-internal**) — cgroup freeze/thaw for pause, `mode=legacy|hierarchy`, `maxCgroups`, `maxWaitMs`, structured `FreezeResult` (L79-L166).
- `POST /collapse` (**x-internal**) — collapses envd's own heap into THP pre-pause to reduce cold-fault count on resume (L168-L180).
- `POST /fsfreeze` / `/fsthaw` (**x-internal**) — `FIFREEZE`/`FITHAW` on guest rootfs for filesystem-only pauses (L182-L208).
- `GET /envs` — dump env vars (L210-L219).
- `GET/POST /files` — download/upload with `path`, `username`, `signature`, `signature_expiration`; uploads support `X-Metadata-<key>` headers persisted as xattrs (≤246B key / 4096B total per inode) (L221-L264).
- `POST /files/compose` — zero-copy concatenation of uploaded files, sources deleted after (L266-L283).
- Auth: `AccessTokenAuth` = `X-Access-Token` header, checked against the MMDS-delivered token; `/init` is exempt (it's what delivers the token) (L287-L291).

**Process Connect RPC (`https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/spec/process/process.proto#L1-L24`)** — service `Process`: `List`, `Connect` (server-stream), `Start` (server-stream — core "run code" primitive), `Update` (PTY resize), `StreamInput`/`SendInput` (stdin), `SendSignal` (SIGTERM/SIGKILL, L139-L149), `CloseStdin`. `ProcessEvent` carries `StartEvent{pid}`, `DataEvent{stdout|stderr|pty}`, `EndEvent{exit_code, exited, status, error}`, `KeepAlive` (L66-L97).

**Filesystem Connect RPC (`https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/spec/filesystem/filesystem.proto#L1-L20`)** — service `Filesystem`: `Stat`, `MakeDir`, `Move`, `ListDir`, `Remove`, `WatchDir` (server-stream), plus `CreateWatcher`/`GetWatcherEvents`/`RemoveWatcher`. `EntryInfo` carries mode/owner/group/mtime/symlink target and xattr-backed `metadata` map (L22-L40).

### 5.3 Host communication channel

**TCP over the veth/tap NAT'd network**, addressed as `<slot HostIP>:49983` from the host side (translated via the DNAT rule in `CreateNetwork`, §3.2) — **not vsock, not a unix socket** (per §4.4 and `docs/ARCHITECTURE.md`: "The orchestrator reaches them over the host network at the sandbox slot IP"). `pkg/sandbox/envd` generates the orchestrator's internal-path rejection list from `x-internal: true` markers in `spec/envd.yaml`, keeping §4.4 step 2's rejection list in sync with the spec.

---

## 6. Templates — build/registration flow and storage

### 6.1 Proto contract

`packages/orchestrator/template-manager.proto` (imported by `orchestrator.proto`) defines `TemplateService`, exposed by the orchestrator binary in `template-manager` role, same gRPC port `:5008` as `SandboxService`:
```
service TemplateService {
  rpc TemplateCreate (TemplateCreateRequest) returns (google.protobuf.Empty);
  rpc TemplateBuildStatus (TemplateStatusRequest) returns (TemplateBuildStatusResponse);
  rpc TemplateBuildDelete (TemplateBuildDeleteRequest) returns (google.protobuf.Empty);
  rpc InitLayerFileUpload (InitLayerFileUploadRequest) returns (InitLayerFileUploadResponse);
}
```
https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/template-manager.proto#L157-L169.

Key messages: `TemplateConfig` (L57-L83) — `templateID`, `buildID`, `memoryMB`, `vCpuCount`, `diskSizeMB`, `freeDiskSizeMB`, `startCommand`, `readyCommand`, `steps: repeated TemplateStep` (Dockerfile-like build script), `oneof source { fromImage | fromTemplate }`, `fromImageRegistry` (AWS ECR / GCP SA JSON / user-password) (L29-L55). Deprecated `kernelVersion`/`firecrackerVersion`/`hugePages` fields indicate template-manager now selects these itself rather than trusting the caller. `TemplateCreateRequest{template, cacheScope, version}` kicks off a build; `TemplateStatusRequest`/`TemplateBuildStatusResponse` stream `TemplateBuildLogEntry` and report `TemplateBuildState{Building, Failed, Completed}` plus `TemplateBuildMetadata` (`rootfsSizeKey`, `envdVersionKey`, resolved kernel/FC versions, shared `SchedulingMetadata`) (L110-L153). `InitLayerFileUploadRequest/Response` supports peer-to-peer/cached layer uploads scoped by `cacheScope`, with dedup via a `present` flag and a signed `url` + headers when not already cached (L14-L27).

### 6.2 Storage format

Per the repo architecture data-store table (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/docs/ARCHITECTURE.md#L390-L397) and cross-checked against `packages/shared/pkg/storage/paths.go`, template & snapshot artifacts are keyed by build ID:
```
{buildID}/memfile
{buildID}/rootfs.ext4
{buildID}/snapfile
{buildID}/metadata.json  (+ .header index files)
```
Key architectural claim: **"A template and a paused-sandbox snapshot have the same artifact shape — a snapshot is just a new build whose memfile/rootfs are stored as diffs against the template it came from"** (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/docs/ARCHITECTURE.md#L346-L348) — the `SchedulingMetadata` fields (`memfile_base_build_id`, `rootfs_base_build_id`, `memfile_build_ids`/`rootfs_build_ids` lists) referenced in both protos describe this ancestor-chain deduplication.

**Host check (2026-10-03):** `/var/lib/e2b/local-template-storage/<build-id>/` has `memfile`,
`memfile.header`, `metadata.json`, `rootfs.ext4`, `rootfs.ext4.header` and `snapfile`. A sampled
`memfile.header` was uncompressed version 3, with 64 bytes of metadata before 40-byte mappings.

### 6.3 End-to-end flow (**Verified** proto/paths.go; sequence narrative **Docs-only** from ARCHITECTURE.md, not independently re-derived from `pkg/sandbox/{fc,uffd,rootfs,nbd,block,template}` source in this pass)

1. API build endpoints send gRPC `TemplateCreate` to a build node running the orchestrator binary in `template-manager` role.
2. Template-manager builds from a Docker image (`fromImage` + optional registry auth) or from another template's build (`fromTemplate`), applying `TemplateStep`s, producing a Firecracker pre-booted VM snapshot: `rootfs.ext4` (COW-capable disk image), `memfile` (memory snapshot for lazy/userfaultfd restore), `snapfile` (Firecracker's VM-state snapshot), plus `metadata.json`/`.header` index files.
3. Artifacts upload to object storage keyed by `buildID`; `InitLayerFileUpload` supports cache/dedup of shared layer files, with peer-to-peer serving of not-yet-uploaded chunks between nodes.
4. On sandbox creation, a regular orchestrator node resolves the template's ready build (Postgres/Redis-cached alias→buildID), pulls cached/peer/object-storage artifacts, and **resumes** the Firecracker snapshot rather than cold-booting.

---

## 7. Persistence in local/self-hosted mode

### 7.1 Docker Compose volumes (`packages/local-dev/docker-compose.yaml`)

| Service | Volume | Persists across restart? |
|---|---|---|
| postgres (`postgres:18-alpine`) | named volume `postgres:/var/lib/postgresql` | **Yes** — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/local-dev/docker-compose.yaml#L59-L68 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/local-dev/docker-compose.yaml#L127-L130 |
| clickhouse | named volume `clickhouse:/var/lib/clickhouse` | **Yes** — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/local-dev/docker-compose.yaml#L1-L13 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/local-dev/docker-compose.yaml#L127 |
| redis (`redis:7.4.6`) | **none** | **No** — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/local-dev/docker-compose.yaml#L70-L72. Running-sandbox catalog/routing state is fully ephemeral; a restart wipes it. |
| tempo | named volume `tempo:/var/tempo` | Yes (tracing only) — L92-L113 |
| grafana/loki/mimir/otel-collector/vector/memcached | none | No (observability-only) |

### 7.2 Local storage backend for artifacts

`packages/orchestrator/.env.local` sets (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/.env.local#L1-L19):
```
STORAGE_PROVIDER=Local
LOCAL_TEMPLATE_STORAGE_BASE_PATH=./tmp/local-template-storage
LOCAL_BUILD_CACHE_STORAGE_BASE_PATH=./tmp/local-build-cache
ORCHESTRATOR_BASE_PATH=./tmp/
ORCHESTRATOR_LOCK_PATH=./tmp/.lock
SANDBOX_CACHE_DIR=./tmp/sandbox-cache-dir
NBD_POOL_SIZE=16
```
This filesystem backend (`storage_fs.go`) implements the same `StorageProvider` interface as GCS/S3/Azure (Put/WriteTo/OpenSeekable/OpenBlob), backed by atomic rename-based writes (`replaceFile`, https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/storage_fs.go#L296-L325). **Persists across orchestrator restarts** as long as `./tmp/...` on host disk isn't deleted: template rootfs/memfile/snapfile/metadata under `local-template-storage/{buildID}/...` and build-cache layers; Postgres `snapshots`/`env_builds` rows referencing these build IDs also persist (named volume). **Does NOT persist / transient**: `SANDBOX_CACHE_DIR` and `ORCHESTRATOR_BASE_PATH` scratch are node-local working caches repopulated lazily from the storage backend; the Redis-backed running-sandbox map/routing catalog is fully in-memory — losing Redis loses "what's currently running" bookkeeping (though the durable Postgres `snapshots` row for any paused sandbox is unaffected; running/non-paused sandboxes whose Redis entry is lost become unreachable by client-proxy until reconciled).

If neither `TEMPLATE_STORAGE_URL` nor `STORAGE_PROVIDER` overrides are set, defaults fall back to real-OS `/tmp/templates` / `/tmp/build-cache` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/cfg/storage.go#L44-L58) — local dev's `.env.local` overrides these to repo-relative paths.

### 7.3 Storage abstraction

Interface — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/storage.go#L80-L86:
```go
type StorageProvider interface {
    DeleteObjectsWithPrefix(ctx context.Context, prefix string) error
    UploadSignedURL(ctx context.Context, path string, ttl time.Duration) (UploadURL, error)
    OpenBlob(ctx context.Context, path string) (Blob, error)
    OpenSeekable(ctx context.Context, path string) (Seekable, error)
    GetDetails() string
}
```
Backends: `storage_fs.go` (Local), `storage_google.go` (GCS), `storage_aws.go` (AWS S3), `storage_azure.go` (Azure Blob). Selection — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/storage_factory.go#L33-L46:
```go
switch spec.Provider {
case LocalStorageProvider:
    return newFileSystemStorage(spec.BasePath, o.uploadBaseURL, o.hmacKey), nil
case AWSStorageProvider:
    return newAWSStorage(ctx, spec, o.limiter)
case GCPStorageProvider:
    return NewGCP(ctx, spec.Bucket, o.limiter)
case AzureStorageProvider:
    return newAzureStorage(ctx, spec.Bucket, o.limiter)
}
```
Provider enum & URL dialect — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/storage_url.go#L18-L25 (`GCPBucket`/`AWSBucket`/`AzureBucket`/`Local`, default `GCPBucket`), schemes `gs://bucket`, `s3://bucket?endpoint=...`, `azblob://container`, `file:///abs/path`/`file:relative/path` (L52-L61).

Env vars — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/cfg/storage.go#L1-L99 (`storageEnv` struct): `TEMPLATE_STORAGE_URL`/`BUILD_CACHE_STORAGE_URL` (authoritative full URL) take priority; else legacy `STORAGE_PROVIDER` + `TEMPLATE_BUCKET_NAME`/`LOCAL_TEMPLATE_STORAGE_BASE_PATH` + `BUILD_CACHE_BUCKET_NAME`/`LOCAL_BUILD_CACHE_STORAGE_BASE_PATH` + `S3_USE_PATH_STYLE` are converted via `legacyStorageURL` (L77-L99). Default provider if unset: `GCPBucket` (L78). A dedicated local-only upload handler validates HMAC-signed tokens for filesystem-backed PUTs, registered only when `STORAGE_PROVIDER=Local` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/localupload/handler.go#L13-L24). Canonical artifact layout constants (`MemfileName`, `RootfsName`, `SnapfileName`, `MetadataName`, `HeaderSuffix`) shared by every backend — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/paths.go#L9-L16 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/paths.go#L29-L49.

---

## 8. Metadata / env vars propagation

### 8.1 OpenAPI schemas

- `metadata`: `SandboxMetadata` — `additionalProperties: {type: string}` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L420-L423), used in `NewSandbox`/`NewSandboxV2` (L936-L937/L1013-L1014), returned in `SandboxDetail`/`ListedSandbox`, queryable via `GET /v2/sandboxes?metadata=...` (L2917-L2921, e.g. `user=abc&app=prod`).
- `envVars`: `EnvVars` — `additionalProperties: {type: string}` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L458-L461), used in request bodies only — **not** returned in any response schema (write-only).

### 8.2 Orchestrator protobuf

`SandboxConfig` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/orchestrator.proto#L7-L68: field 7 `map<string,string> env_vars`, field 8 `map<string,string> metadata` ("Metadata about the sandbox"). Both carried inside `SandboxCreateRequest.sandbox` (orchestrator.proto:L130-L141).

### 8.3 Divergent handling — `envVars` reaches the guest, `metadata` does not

- API→orchestrator: `packages/api/internal/handlers/sandbox_create.go` builds `SandboxConfig` from `NewSandbox` body; `SandboxMetadata struct{ Metadata, EnvVars map[string]string; ... }` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/create_instance.go#L48-L50 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/create_instance.go#L338-L339.
- Orchestrator node-side (`Server.Create`) — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/sandboxes.go#L126-L306:
```go
Envd: sandbox.EnvdMetadata{
    Version:     req.GetSandbox().GetEnvdVersion(),
    AccessToken: req.GetSandbox().EnvdAccessToken,
    Vars:        req.GetSandbox().GetEnvVars(),   // env_vars forwarded to envd
},
```
(L291-L295). The full incoming proto (including `metadata`) is stored verbatim as `sbx.APIStoredConfig`, used only for reconciliation/list/analytics — `eventData["sandbox_metadata"] = utils.ShallowCopyMap(sbx.APIStoredConfig.GetMetadata())` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/sandboxes.go#L1514-L1519) — **`metadata` is never forwarded to envd**, only `env_vars` is.
- envd request build — `doRequestWithInfiniteRetries`, https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/envd.go#L86-L96 — constructs `POST /init` body `PostInitJSONBody{LifecycleID, EnvVars: s.Config.Envd.Vars, HyperloopIP, AccessToken, DefaultUser, DefaultWorkdir, VolumeMounts, CaBundle}`.
- Guest side: `API.SetData` — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/internal/api/init.go#L356-L372 — receives `PostInitJSONBody.EnvVars` and calls `a.defaults.EnvVars.ReplaceUserVars(*data.EnvVars)` (L372); these become process-default env vars for every command envd spawns (consumed in `packages/envd/internal/services/process/handler/handler.go` via `defaults.EnvVars.All()`).

**Conclusion:** `envVars` is end-to-end: OpenAPI field → orchestrator gRPC `SandboxConfig.env_vars` → node's `EnvdMetadata.Vars` → envd `POST /init` body → injected as guest process environment. `metadata` is control-plane/analytics-only — stored for filtering (`X-Total-Running`, list queries) and event tagging, **never exposed to envd or the guest**.

---

## 9. Other relevant details

### 9.1 Feature flags (LaunchDarkly-backed, `packages/shared/pkg/featureflags`)

- `AutoPauseOverstayBudgetMs` default **120,000 ms** (§2.1).
- `deferred-rootfs-export` — defers rootfs diffing off the pause critical path via background reflink (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/docs/ARCHITECTURE.md#L449-L458).
- `snapshot-cache-release-superseded` / `snapshot-cache-ancestor-storage-fallback` — govern releasing superseded template-cache layers.
- `BEST_OF_K_HUGEPAGE_MEMORY` — toggles hugepage-pool scoring in placement.
- `rate-limit-v2-mode` (`disabled`/`shadow`/`enabled`).
- `fs-only-resume-api`, `preboot-fs-recovery`, `envd-upgrade-target`, `envd-offline-upgrade-target`, `envd-binary-cache`, `pause-refusal-restore` — gate resume/upgrade behaviors.

(All from the repo architecture notes, especially https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/docs/ARCHITECTURE.md#L110-L220 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/docs/ARCHITECTURE.md#L430-L506 — **Docs-only**, flag *names* not individually re-verified against `flags.go` source except `AutoPauseOverstayBudgetMs`.)

### 9.2 TTLs

- Redis routing record TTL = `sandbox_max_length_in_hours` (`sandbox_catalog.SandboxInfo.MaxLengthInHours`, https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/sandbox-catalog/catalog.go#L11-L23).
- Pause transition: ~80s detached budget for API/node coordination; Redis transition key TTL 95s; terminal build-status writes use a separate 10s detached budget (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/docs/ARCHITECTURE.md#L440-L448; **Docs-only**).

### 9.3 Resource limits & observability (ClickHouse)

Per-sandbox vCPU/RAM quota is set at template-build time (Postgres `env_builds.vcpu`/`ram_mb` — **Docs-only**, column names from ARCHITECTURE.md prose, not the SQL migration itself). Actual usage tracked in ClickHouse:
```sql
CREATE TABLE sandbox_host_stats_local (
    timestamp DateTime64(9) ..., sandbox_id String, sandbox_execution_id String,
    sandbox_template_id String, sandbox_build_id String, sandbox_team_id UUID,
    sandbox_vcpu_count Int64, sandbox_memory_mb Int64,
    firecracker_cpu_user_time Float64, firecracker_cpu_system_time Float64,
    firecracker_memory_rss UInt64, firecracker_memory_vms UInt64
) ENGINE = MergeTree() PARTITION BY toDate(timestamp) ORDER BY (sandbox_id, timestamp)
TTL toDateTime(timestamp) + INTERVAL 7 DAY;
```
https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/clickhouse/migrations/20260209152327_add_sandbox_host_stats.sql#L1-L19 (sharded `Distributed` table over this local table, keyed by `xxHash64(sandbox_id)`). Similar 7-day-TTL gauge metrics table: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/clickhouse/migrations/20250717135224_sandbox_metrics.sql#L1-L15.

### 9.4 Build tags / roles

`ORCHESTRATOR_SERVICES` env var selects binary role(s): `orchestrator` and/or `template-manager`, comma-separated (local dev sets both) — https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/docs/ARCHITECTURE.md#L184-L185 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/.env.local#L10. Most of `packages/orchestrator` is `//go:build linux`-tagged (per a comment in `packages/orchestrator/pkg/cfg/storage.go` noting `storage_url.go` is deliberately *not* so tagged, unlike the rest of the package).

### 9.5 Local dev system requirements

From `DEV-LOCAL.md` (full file read): Linux bare-metal or nested-virtualization VM, `/dev/kvm`, NBD kernel module (`modprobe nbd nbds_max=64`), huge pages (`vm.nr_hugepages=2048`), Docker Compose v2, Node.js (for base template build), `gsutil` (anonymous access to public kernel/firecracker builds). Orchestrator **requires `sudo`** for KVM, TAP device creation, cgroup management, and NBD mounting.

---

## Gaps and uncertainties

- **Not read in depth:** `packages/orchestrator/pkg/sandbox/{fc,uffd,rootfs,nbd,block,template}` — low-level Firecracker snapshot file format, UFFD lazy-restore mechanics, and rootfs COW/diff implementation were only described via `docs/ARCHITECTURE.md` prose, not independently verified against that Go source. If Hibernaut needs exact snapshot binary format or restore timing guarantees, a follow-up deep-read of these packages is warranted.
- **Not read in depth:** `packages/client-proxy/internal/proxy/paused_sandbox_resumer_grpc.go` and `grpc_resume_auth.go` — discovered but not opened; the exact gRPC client call to `ResumeSandbox` is inferred from the server-side handler (§2.2) and ARCHITECTURE.md, not directly cited from the client-proxy call site.
- **Not read in depth:** `packages/shared/pkg/storage/{storage_google,storage_aws,storage_azure}.go` — backend implementations confirmed to exist and be selected via `storage_factory.go`, but auth/config specifics (ADC, workload identity, `AWS_ENDPOINT_URL`, Azure connection strings) were not examined.
- **Not read in depth:** `packages/db/migrations` (Postgres schema) — `env_builds`/`snapshots` column names come from `docs/ARCHITECTURE.md`'s summary table, not the SQL migrations themselves; treat exact column types/constraints as **Docs-only**, unverified.
- **Not read in depth:** `packages/shared/pkg/featureflags/flags.go` in full — only `AutoPauseOverstayBudgetMs` was matched via code search; the full flag catalog (§9.1) is largely **Docs-only** from ARCHITECTURE.md prose.
- `docs/ARCHITECTURE.md` (49KB) was read via two agents across overlapping ~20-40KB windows; content past roughly the 40,000-character mark of the file (if any beyond what's cited here) was not confirmed read in full by either sub-investigation.
- GitHub's `search_code` API intermittently failed for `repo:e2b-dev/infra` (renamed-repo search-index mismatch); all cited file *content* was fetched via direct `get_file_contents` pinned to the commit SHA, so citation accuracy is unaffected — only ad hoc discovery search was occasionally impacted.
- Official docs checked (secondary to code): docs index lists lifecycle/persistence/snapshots/fork/auto-resume/SSH/network/secrets pages (https://docs.e2b.dev/llms.txt). Current docs agree with pinned source on pause/resume persistence, fork, snapshot-to-template, auto-resume-on-activity, SSH via WebSocket, and internet allow/deny/domain rules (https://docs.e2b.dev/sandbox/persistence.md; https://docs.e2b.dev/sandbox/snapshots.md; https://docs.e2b.dev/sandbox/fork.md; https://docs.e2b.dev/sandbox/auto-resume.md; https://docs.e2b.dev/sandbox/ssh-access.md; https://docs.e2b.dev/network/internet-access.md). Discrepancies/rename notes: docs expose modern SDK `lifecycle.onTimeout/autoResume` names while the pinned OpenAPI exposes lower-level `autoPause`, `autoPauseMemory`, and `autoResume` fields; docs also describe outbound HTTPS secret injection (https://docs.e2b.dev/secrets/inject.md), but §10 finds that the pinned OSS code only carries dormant rule/CA plumbing and no MITM/substitution implementation.

---

## Implications for Hibernaut

1. **Pause/resume/fork map onto exactly three primitives Hibernaut needs to distinguish, not conflate:** *resume* keeps the sandbox ID and is idempotent-ish (safe for "wake up my paused workspace"); *fork* is the right primitive for "spin up N throwaway copies of a live workspace without disturbing the original" (new IDs, original untouched, parallel placement — good for preview/ephemeral-branch workflows); *snapshot-to-template* is the right primitive for "promote a configured workspace into a reusable base image" (survives sandbox deletion, gets an alias). Building Hibernaut's abstraction directly on these three (rather than re-deriving pause+resume-as-fork or similar) avoids re-solving orchestration E2B already solved.
2. **Auto-pause is opt-in and policy-driven per sandbox at creation time** (`autoPause`, `autoPauseMemory`), and **auto-resume is opt-in per sandbox too** (`autoResume.policy`/`timeout`) and is refused for filesystem-only snapshots. If Hibernaut wants "seamless wake on first request" UX, sandboxes must be created with `autoPause=true` **and** a full-memory pause path (`autoPauseMemory=true`/default) — a filesystem-only auto-pause will strand the sandbox requiring an explicit `/resume` call, which breaks any assumption of transparent lazy wake-up via routing alone.
3. **The identity that matters for routing is the Redis-backed `sandbox:routing:{id}` record, which is ephemeral and short-TTL'd (`sandbox_max_length_in_hours`).** Hibernaut should not treat "sandbox exists in Redis" as a durable source of truth — the durable source of truth for a paused sandbox is the Postgres `snapshots` row (and the object-storage artifacts it references). Any Hibernaut-side cache of "where is sandbox X running" should be built with the same assumption: ephemeral, rebuildable from the snapshot/template store, never authoritative.
4. **In self-hosted/local mode, only Postgres + ClickHouse persist by default (Docker named volumes); Redis and the orchestrator's local scratch/cache directories do not.** If Hibernaut self-hosts E2B's infra, running-sandbox bookkeeping is lost on any Redis restart even though paused-sandbox snapshots (Postgres row + object/local-fs artifacts) survive — Hibernaut must plan for "orphaned running sandboxes become unreachable after a Redis blip" as an operational reality, distinct from data loss.
5. **The storage backend is pluggable and defaults to GCP unless `STORAGE_PROVIDER=Local` (or a `TEMPLATE_STORAGE_URL`/`BUILD_CACHE_STORAGE_URL` scheme) is explicitly set** — a real self-host/on-prem deployment for Hibernaut needs to set this explicitly, and should budget for the fact that "local" storage is literally an on-node directory (`./tmp/local-template-storage` or `/tmp/templates` by default) with no built-in replication — losing that node's disk loses every template/snapshot artifact stored only there, unless a shared network path is mounted at that base path.
6. **`metadata` is a control-plane-only string map (searchable, but never reaches the guest); `envVars` is the only mechanism that reaches guest process environment.** Any Hibernaut feature that needs to pass configuration *into* the running workspace (not just tag/filter sandboxes externally) must use `envVars`, not `metadata` — and note `envVars` is write-only in the API (never echoed back in `GET` responses), so Hibernaut must track what it sent independently if it needs to display/audit it later.
7. **All sandbox traffic (including to envd) is plain HTTP/HTTPS over a NAT'd per-sandbox network namespace — no vsock, no unix socket, no built-in end-to-end TLS to the guest by default** (self-signed guest certs are explicitly tolerated with `InsecureSkipTLSVerify`). If Hibernaut has stricter transport-security requirements, it needs its own encryption layer on top (e.g., mTLS terminated inside envd-adjacent user code, or tunneling), since E2B's own hop-by-hop model assumes the orchestrator's proxy as the trust boundary rather than true end-to-end TLS.
8. **The per-sandbox nftables/iptables firewall (§3.3) with SNI/Host-aware TCP inspection is a first-class, existing egress-control primitive, but not a MITM/secret-injection primitive.** Hibernaut can reuse `SandboxNetworkConfig`/`SandboxNetworkUpdateConfig` for domain/IP allow/deny, but placeholder→real-secret substitution over HTTPS needs either an E2B code change/new egress-proxy implementation or a host-side transparent proxy inserted into the per-slot redirect path (§10).
9. **SSH is best treated as WebSocket-over-public-port glue, not native TCP ingress.** E2B's proxy stack supports upgraded/long-lived HTTP connections, and the official docs recommend baking `openssh-server` plus `websocat` into the template; Hibernaut can automate that template pattern and use private-ingress `trafficAccessToken` when needed (§10).


## 10. Egress interception, secret injection and SSH (follow-up)

### 10.1 Secret injection / MITM

- **Verified:** the pinned OpenAPI has a `network.rules` surface for per-domain outbound HTTPS request transforms: `SandboxNetworkConfig.rules` keys are exact or wildcard DNS names, rules do not grant access by themselves, and the nested transform currently only defines `headers` to inject/override (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L498-L512 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L551-L569). The same schema says header values are plain strings and that “secret resolution happens client-side before sending to the API,” so the API field is not itself a server-side secret reference mechanism (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L562-L568).
- **Verified:** the API copies those rule/header strings into the orchestrator protobuf config (`SandboxNetworkEgressConfig.Rules`) without resolving secrets or adding CA material (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/create_instance.go#L100-L134), and the proto only models `map<string,string> headers`, not bodies, Basic-auth helpers, or placeholder expressions (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/orchestrator.proto#L100-L125).
- **Verified:** the OSS egress proxy is `packages/orchestrator/pkg/tcpfirewall`: it redirects guest dport 80/443/other TCP to three local listener ports (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/tcpfirewall/proxy.go#L48-L64), inspects HTTP Host for port 80 and TLS SNI for port 443 (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/tcpfirewall/proxy.go#L97-L125), then forwards raw TCP to the original IP or resolved hostname through `tcpproxy.DialProxy` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/tcpfirewall/handlers.go#L118-L138 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/tcpfirewall/handlers.go#L140-L190). There is no TLS termination in that path; the allow/deny decision uses `AllowedDomains`, `AllowedCidrs`, and `DeniedCidrs`, not `Rules` transforms (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/tcpfirewall/handlers.go#L193-L240).
- **Verified:** the two OSS `EgressProxy` implementations return an empty CA bundle: `tcpfirewall.Proxy.CABundle()` returns `""` and `SupportsBYOP()` returns `false` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/tcpfirewall/proxy.go#L195-L201), and `NoopEgressProxy.CABundle()` also returns `""` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/egressproxy.go#L9-L18 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/egressproxy.go#L20-L42). Therefore the pinned OSS code does **not** implement outbound HTTPS MITM or placeholder→secret substitution.
- **Verified:** `network.egressProxy` is a BYOP/SOCKS5 config surface (`address`, optional username/password) in OpenAPI (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L571-L595), and shared validation treats it as a SOCKS5 endpoint with RFC1929 credential limits and internal-CIDR rejection (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/sandbox-network/byop.go#L76-L128). But the OSS tcpfirewall `SupportsBYOP()` is false, so this is not a self-hosted-local MITM hook in this build (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/tcpfirewall/proxy.go#L199-L201).
- **Docs-only / discrepancy:** current docs describe stored secrets being injected into outbound HTTPS request headers by an “egress proxy” using per-host transforms (https://docs.e2b.dev/secrets/inject.md). **Code wins:** at this pinned OSS commit, the public schema carries header transform data, but the open-source egress proxy does not consume transforms, does not MITM TLS, and supplies no CA.

### 10.2 `caBundle`

- **Verified:** `caBundle` is set by the orchestrator from `f.egressProxy.CABundle()` when constructing a sandbox, including resume paths (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L1004-L1017 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L1582-L1591). In OSS, that value is empty (§10.1).
- **Verified:** orchestrator sends `CaBundle` to envd in `POST /init` together with `EnvVars`, `HyperloopIP`, access token, default user/workdir, and volume mounts (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/envd.go#L86-L95). The envd OpenAPI describes `caBundle` as “PEM-encoded CA certificates to install into the system trust store” (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/spec/envd.yaml#L60-L77).
- **Verified:** envd installs a non-empty `caBundle` by appending it to `/etc/ssl/certs/ca-certificates.crt` and writing the current cert to `/usr/local/share/ca-certificates/e2b-ca.crt` so later `update-ca-certificates` rebuilds include it (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/internal/api/init.go#L398-L402; https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/internal/host/cacerts.go#L19-L35; https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/internal/host/cacerts.go#L58-L160). The install path short-circuits on an empty bundle, which is what the OSS proxies provide (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/internal/host/cacerts.go#L77-L80).
- **Not found / Verified absence:** no public sandbox create/connect/resume API field accepts a CA bundle; `NewSandbox`/`NewSandboxV2` expose `network`, `metadata`, `envVars`, IAM, volumes, etc., but no `caBundle` field (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L936-L1017). `NODE_EXTRA_CA_CERTS` and `SSL_CERT_FILE` handling were not found in source search; envd modifies the system trust bundle instead.

### 10.3 Egress path and host-side hook points

- **Verified:** `hyperloop` is not general internet egress. It is the orchestrator's in-sandbox service IP, default `192.0.2.1`, with guest TCP `192.0.2.1:80` redirected to the host hyperloop proxy port, default `5010` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/pool.go#L61-L68 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/network.go#L324-L332). The same in-sandbox IP also fronts portmapper/NFS redirects on 111/2049 (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/network.go#L334-L352), and firewall comments call out that it fronts NFS, portmapper and hyperloop (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/firewall.go#L393-L402).
- **Verified:** all guest TCP egress is redirected per slot into the tcpfirewall proxy: dport 80 → HTTP Host-inspection port, dport 443 → TLS SNI-inspection port, other TCP → CIDR-only port (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/tcpfirewall/proxy.go#L60-L64 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/tcpfirewall/proxy.go#L164-L184). The listener ports are configurable as `SANDBOX_TCP_FIREWALL_HTTP_PORT`, `SANDBOX_TCP_FIREWALL_TLS_PORT`, and `SANDBOX_TCP_FIREWALL_OTHER_PORT`, defaulting to `5016`, `5017`, and `5018` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/pool.go#L78-L85).
- **Verified:** the proxy maps a connection back to its sandbox by looking up the TCP source host:port in the live sandbox map (`sandboxes.GetByHostPort(sourceAddr)`), then uses `sbx.Runtime.SandboxID` and `sbx.LifecycleID` for decisions/limits (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/tcpfirewall/proxy.go#L262-L289). It recovers the original destination through Linux `SO_ORIGINAL_DST` after iptables redirect (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/tcpfirewall/proxy.go#L301-L327 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/tcpfirewall/utils.go#L12-L56).
- **Inferred:** Hibernaut could patch/replace the `EgressProxy` implementation or change the per-slot redirect target to a local transparent MITM proxy and still preserve per-sandbox identity by source host:port lookup; however, there is no config/env var in this commit to point E2B at an arbitrary transparent proxy instead of its built-in tcpfirewall (§10.1/§10.3 citations above).

### 10.4 SSH over WebSocket and ingress tokens

- **Docs-only:** official SSH docs recommend building a template with `openssh-server` and `websocat`, running `sudo websocat -b --exit-on-eof ws-l:0.0.0.0:8081 tcp:127.0.0.1:22` as the start command, and connecting with `ssh -o 'ProxyCommand=websocat --binary -B 65536 - wss://8081-%h.e2b.app' user@<sandbox-id>` (https://docs.e2b.dev/sandbox/ssh-access.md).
- **Verified:** client-proxy uses an idle timeout of 610s and the node orchestrator proxy uses 620s; the shared proxy server has `ReadTimeout: 0` and `WriteTimeout: 0`, so it does not impose finite read/write deadlines on established streams (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/client-proxy/internal/proxy/proxy.go#L28-L33; https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/proxy/proxy.go#L32-L40; https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/proxy/proxy.go#L64-L74). Upgraded/hijacked connections are explicitly tracked during shutdown because `net/http` does not wait for them automatically (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/proxy/proxy.go#L119-L135), and a test exercises an `HTTP/1.1 101 Switching Protocols` upgraded tunnel (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/proxy/shutdown_test.go#L26-L65).
- **Verified:** `e2b-traffic-access-token` is only required for non-envd traffic when ingress is private (`allowPublicTraffic=false` → `AllowPublicAccess` false). The orchestrator proxy skips envd-port traffic and checks the header only if an ingress token is configured (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/proxy/proxy.go#L98-L114), while auto-resume validates private-ingress tokens only for non-envd traffic (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/proxy_grpc.go#L231-L245).
- **Verified:** private-ingress traffic tokens are generated at sandbox create time when `AllowPublicAccess` is false and are stored in the sandbox's ingress config (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/create_instance.go#L301-L316 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/create_instance.go#L147-L174). The REST `Sandbox` response schema includes nullable `trafficAccessToken` and `envdAccessToken` fields (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L756-L789), and the API-side sandbox type carries that token back to callers (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/sandbox/sandboxtypes/sandbox.go#L13-L78).

### 10.5 Auto-resume triggers and env var resend

- **Verified:** auto-resume is triggered by a client-proxy catalog miss for any sandbox request port: `catalogResolution` calls `handlePausedSandbox(... sandboxPort, trafficAccessToken, envdAccessToken ...)` on `ErrSandboxNotFound` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/client-proxy/internal/proxy/proxy.go#L74-L135), and the gRPC resumer forwards the requested port plus traffic/envd tokens to the API (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/client-proxy/internal/proxy/paused_sandbox_resumer_grpc.go#L73-L96). The API classifies `49983` as envd traffic rather than excluding it (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/proxy_grpc.go#L63-L83). **Conclusion:** SDK commands/files traffic to envd can trigger auto-resume, subject to the same `autoResume` snapshot policy and envd token validation.
- **Verified:** auto-resume requires the paused snapshot's `AutoResume.Policy == any`, refuses filesystem-only snapshots, and uses `buildResumeSandboxData(sandboxID, nil, nil)` with no env-var override (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/proxy_grpc.go#L99-L124 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/proxy_grpc.go#L258-L267).
- **Verified:** every orchestrator `POST /init` sends `EnvVars: s.Config.Envd.Vars` to envd (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/envd.go#L86-L95), and the re-init safety comment names the `/init` body as including “access token, env vars, CA bundle” (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L3808-L3814). `NewSandbox`/`NewSandboxV2` accept `envVars` at creation (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L953-L958 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L1006-L1011), but resume/fork request bodies do not include env vars (`ResumedSandbox` has timeout/autoPause/memory; `SandboxForkRequest` has timeout/count) (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L1050-L1071 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L1152-L1172). **Conclusion:** env vars are resent unchanged on resume and fork-derived starts, but cannot be changed via resume/connect/fork APIs.

### 10.6 Local-mode artifact paths and OS `/tmp`

- **Verified:** in the newer embedded compose local setup, template and paused-snapshot artifacts use file-backed storage under `/var/lib/e2b/storage/templates`, and build cache uses `/var/lib/e2b/storage/build-cache` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/embed/compose/compose.yaml#L269-L279). The local teardown instructions explicitly say `docker compose down` keeps data and `/var/lib/e2b` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/embed/compose/compose.yaml#L546-L555).
- **Verified:** `packages/orchestrator/.env.local` (used with `packages/local-dev/docker-compose.yaml` dependencies) sets `STORAGE_PROVIDER=Local`, `LOCAL_TEMPLATE_STORAGE_BASE_PATH=./tmp/local-template-storage`, `LOCAL_BUILD_CACHE_STORAGE_BASE_PATH=./tmp/local-build-cache`, `ORCHESTRATOR_BASE_PATH=./tmp/`, and `SANDBOX_CACHE_DIR=./tmp/sandbox-cache-dir` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/.env.local#L13-L24). Those are repository-relative `./tmp` paths, not necessarily OS `/tmp`; if the repo is checked out at `/tmp/e2b-infra`, the effective absolute paths become `/tmp/e2b-infra/packages/orchestrator/tmp/...` and may inherit the host's `/tmp` cleanup policy (**Inferred** from the relative paths).
- **Verified:** the `packages/local-dev` Docker dependencies persist Postgres and ClickHouse in Docker named volumes, not OS `/tmp` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/local-dev/docker-compose.yaml#L1-L14 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/local-dev/docker-compose.yaml#L128-L131). The storage resolver's legacy local defaults are `/tmp/templates` and `/tmp/build-cache` only when the local base-path env vars are unset; `.env.local` overrides them with `./tmp/...` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/cfg/storage.go#L42-L62 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/cfg/storage.go#L80-L91).

### 10.7 Default egress deny set, redirect ordering and slot identity (added while finalizing the spec)

- **Verified (live, 2026-10-03):** this host uses iptables through the nft backend: `iptables -V`
  prints `iptables v1.8.10 (nf_tables)`, and `update-alternatives --display iptables` points
  `/usr/sbin/iptables` at `/usr/sbin/iptables-nft`. E2B's host redirects are in the nft-managed
  iptables `ip nat` table, chain `PREROUTING`. For each slot, it has rules like `-i veth-2
  --dport 443 -j REDIRECT --to-ports 5017`, plus the `192.0.2.1` redirects for hyperloop,
  portmapper and NFS. `nft list ruleset` shows the same rules as `redirect to :5017`.
- **Verified (live, 2026-10-03):** port `5443` was free. The host filter table had `-P INPUT
  ACCEPT`, so no host INPUT rule stopped a redirected `veth-*` connection from reaching a listener
  on `0.0.0.0:5443`. The E2B slot interfaces were named `veth-1`, `veth-2`, and so on. No Docker
  `vethXXXX` interface matched the `veth-+` pattern.
- **Verified:** every sandbox's firewall resets its predefined deny set to `DeniedSandboxCIDRs`: `10.0.0.0/8`, `100.64.0.0/10`, `127.0.0.0/8`, `169.254.0.0/16` (including cloud metadata `169.254.169.254`), `172.16.0.0/12`, `192.168.0.0/16`, `::1/128`, `fc00::/7`, `fe80::/10` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/sandbox-network/firewall.go#L22-L36 and https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/firewall.go#L333-L335). Set rules match the packet's IPv4 **destination** on the sandbox's tap interface (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/firewall.go#L202-L219). Without BYOP, the predefined deny set drops all protocols, after only ESTABLISHED/RELATED and the predefined allow set are accepted (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/firewall.go#L290-L327). This filter runs in the slot network namespace (PREROUTING, priority -150), before traffic reaches the host namespace's NAT redirects.
- **Verified:** tcpfirewall installs its per-slot redirects with `iptables -t nat -A PREROUTING -i veth-<idx> -p tcp [--dport N] -j REDIRECT --to-port <proxy port>`, i.e. **appended** (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/tcpfirewall/proxy.go#L152-L173). The hyperloop, portmapper and NFS redirects are appended the same way (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/network.go#L325-L348). Slot veth names are `veth-<idx>` (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/network/slot.go#L171-L173).
- **Verified (live, 2026-10-03):** a host rule inserted at the top of `nat PREROUTING` stays ahead
  of E2B's appended per-slot rules while machines churn. The counting-only rule `iptables -t nat
  -I PREROUTING 1 -i veth-+ -p tcp --dport 443 -m comment --comment hibernaut-s1` stayed at line
  1 after creating two machines, removing one, pausing the other and resuming it. Its counter grew
  from `0` to `1`, then `2`, when the machine ran `curl -sI https://example.com`.
- **Verified (live, 2026-10-03):** a scoped REDIRECT on `veth-2` to a root probe on `0.0.0.0:5443`
  saw `remote=10.11.0.2:45834 local=10.12.0.4:5443 original=104.20.23.154:443 iface=veth-2`.
  The source is the slot's host IP. The local address is the host side of that slot's veth. Linux
  `SO_ORIGINAL_DST` returns the pre-redirect destination.
- **Verified absence:** the orchestrator gRPC `List` response (`RunningSandbox`) has no slot or IP field (https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/orchestrator.proto#L231-L256). Mapping a connection's source slot IP back to a sandbox ID from outside the orchestrator therefore needs host-side introspection.
- **Verified (live, 2026-10-03):** host-side mapping works by deriving the slot from the source IP
  (`10.11.0.N`) or local veth IP (`10.12.0.2N`), reading `/run/netns/ns-N`'s inode, finding the
  Firecracker process whose `/proc/<pid>/ns/net` points at that inode, and taking the sandbox ID
  from `/sys/fs/cgroup/e2b/sbx-<sandboxID>-<runid>/cgroup.procs`. This matched `hib ls` for
  `s1-b` (`i3mdr17vao23k84u6zud6`, slot 1) and `s1-a` (`iv2i1k4srhy0mf2mkc3at`, slot 2). While
  `s1-a` was paused its cgroup was gone. Immediately after resume it mapped again, but to slot 3
  and IPs `10.11.0.3` and `10.12.0.6/31`.
- **Verified (live, 2026-10-03):** template builds use the same network pool and slot machinery.
  The template-manager service is the orchestrator binary, wired to `sandbox.NewFactory` with the
  same `network.NewPool`, and build sandboxes carry `SandboxTypeBuild` for DSCP tagging.
- **Verified (live, 2026-10-03):** `iptables -t nat -S PREROUTING` and inserting then deleting a
  counting rule both worked in a transient unit with `ProtectSystem=strict`, `NoNewPrivileges=yes`,
  `PrivateTmp=yes` and `ProtectHome=yes`. No `ReadWritePaths` was needed for `/run/xtables.lock`
  on this host.
