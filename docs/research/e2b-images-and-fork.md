# E2B snapshot templates, forks and storage dependencies (M6)

Source: `e2b-dev/infra` at `5cfcf3282ddb95e90a8214ce71329641bdfe6cb6`.
This extends [e2b-capabilities](e2b-capabilities.md), especially sections 1.5–1.9.
Sections 1 to 10 are static research in E2B's source. Section 11 is live spikes on our host.

## 1. Saving a sandbox as a snapshot template

- **Verified in code.** Save requires a live source and envd >=0.5.0. Paused returns 404.
  Transition conflict returns 409. Admission refusal returns 503. It calls node Checkpoint for
  full memory, then restores Running with the same ID, execution, TTL, and reservation.
  Defaults restart FC rather than resume the same process. Fatal errors can kill the source.
  Refusal/healthy in-place failures restore it. Cancellation has special handling.
  Neither save nor fork has an enabling feature gate. [save-handler] [versions] [states]
  [save-orch] [grpc] [checkpoint-node] [flags-wp]
- **Verified in code.** First save creates a private source `snapshot` env, team-owned,
  `created_by=NULL`. Its snapshot row records sandbox/base/env/team IDs, metadata, start,
  secure/internet settings, origin node, autoPause, and config containing network, autoResume,
  volumes, IAM, full-memory mode, and autoPauseFilesystemOnly. Every save creates a build and
  source `default` assignment. CPU/RAM/disk and kernel/FC/envd versions come from the source.
  Free disk is zero. CPU identity comes from its build. Status moves `snapshotting` to
  `uploaded`/ready with finished time. Assignment triggers fill team and legacy first env.
  A new private `snapshot_template` env and source/node/build record share that same build.
  Local cluster IDs are NULL. Both env types use `created_by=NULL`. [upsert-sql] [pause-fields]
  [image-env] [save-resolve] [status-group] [build-team] [legacy-env]
- **Verified in code.** Name syntax is `[namespace/]alias[:tag]`, trimmed and lowercased.
  Alias/namespace match `^[a-z0-9-_]+$`. Tag matches `^[a-z0-9-_.]+$`. Default is `default`.
  Explicit namespace must equal team slug. No parser/schema length cap or reserved alias list
  exists. Alias storage is text. Bare references try team namespace, global namespace, then
  raw ID. An owned alias, even a regular template, receives another assignment under the
  requested tag. No automatic new tag is created. Foreign ownership creates a local alias.
  [names] [fork-schema] [alias-schema] [alias-resolution] [save-handler] [save-resolve]
- **Inferred.** Reserve image prefixes and explicit namespace. Concurrent first saves can leave
  partial DB state. Cost is dirty memory/disk export, headers, snapfile, metadata, restart, and
  local storage copies, not ancestor flattening. Upload defaults synchronous after source
  restoration. `use-nfs-for-snapshots` is only cache selection. [save-resolve] [snapshot-export]
  [upload-v3] [local-storage] [checkpoint-upload] [cache-nfs]

## 2. Creating a sandbox from a snapshot template

- **Verified in code.** Select latest ready tag assignment or an assigned UUID after `:`.
  CPU/RAM/disk and kernel/FC/envd versions come from that build, with no resource overrides.
  Metadata, envVars, lifecycle, timeout, internet, and network come from the create request,
  not source snapshot policy. Image env becomes BaseTemplateID. Defaults: autoPause false,
  autoPauseMemory true, autoResume off, TTL 15s/v1 and 300s/v2. TTL must be positive and within
  team limits. V1 is unsecured unless requested. V2 always secures envd. Private ingress
  requires secure envd. Both envd and private-traffic tokens scope to the new sandbox ID.
  [select-build] [create-data] [create-handler] [create-network] [create-config] [envd-token]
  [states] [timeouts]
- **Verified in code.** Node restores memory through UFFD and resumes execution. MMDS receives
  new identity/token hash. `/init` receives token, lifecycle ID, clock, and nonempty envVars,
  replacing envd defaults and thawing frozen user cgroups. Empty/omitted envVars are omitted
  by JSON encoding and leave copied defaults intact. [node-create] [fc-resume] [resume-ready]
  [envd-init] [guest-init] [init-wire]
- **Inferred.** Restored processes keep their old environment and in-memory credentials.
  New setup affects files/future executions, not every running process. External connections
  need testing. There is no create hostname option or automatic SSH-key rotation. Base hostname
  is `e2b.local`. Custom source hostname/keys should persist. Rotate keys and reload sshd.
  [rootfs-hostname] [fc-resume] [guest-init]
- **Verified in code.** Disk/memory remain layered. Later pauses compose headers over the image,
  retaining referenced ancestors, not squashing. Keep the loaded build while actively used.
  After completed pauses, headers may prove some old layers unnecessary. REST nevertheless
  blocks image-env deletion while direct running/paused children exist. [header-diff]
  [mapped-reads] [delete-handler] [template-dependants]

## 3. Native fork

- **Verified in code.** Body only has count/timeout. Count defaults one, range 1–100, strictly
  below team concurrency. Source must be live. Paused returns 409, missing 404, transition
  conflict 409, admission refusal 503. One checkpoint creates a source snapshot-env build,
  finishes `success`/ready, and creates no image env or per-fork assignment before first pause.
  [fork-schema] [fork-handler] [checkpoint-orch] [upsert-sql]
- **Verified in code.** Metadata including `hib.name`/`hib-test`, autoPause/memory policy,
  autoResume, internet/network, mounts, IAM, and secure state are inherited. No envVars override
  is supplied. Guest defaults/processes are copied. New IDs get fresh envd/private-ingress
  tokens and `/init`. TemplateID is source snapshot env. BaseTemplateID remains source base.
  [fork-config] [create-config] [envd-init] [guest-init]
- **Verified in code.** Fresh-copy TTL defaults 15s, must be positive within team max.
  Zero is rejected despite schema minimum zero. Boot consumes TTL. Source TTL is unchanged.
  Inherited autoPause determines expiry behavior. Boots use new-start placement in parallel.
  HTTP 201 carries independent sandbox/error entries with no rollback of successful peers.
  Team/node reservation can partially reject them. Checkpoint failure aborts before booting.
  [timeouts] [start-times] [fork-handler] [team-concurrency] [node-create]
- **Inferred.** Source interruption ends before upload/fork boots. No latency guarantee exists.
  Each callback fetches the source's last snapshot rather than a captured checkpoint build ID.
  Concurrent later checkpoints can therefore change the selected build. Serialize source
  lifecycle operations or spike this race. [checkpoint-upload] [fork-config] [snapshot-cache]

## 4. Changing sandbox metadata

- **Verified in code.** No metadata PATCH/PUT exists. Sandbox PUT only updates network.
  Connect accepts timeout/memory. Deprecated resume additionally accepts autoPause.
  None changes metadata/envVars/autoResume. Node update only has end time/egress.
  Fork cannot override metadata globally or per copy. Guest `/init` cannot rename it.
  [sandbox-routes] [resume-schema] [grpc] [fork-schema] [fork-config]
- **Inferred.** Options: extend E2B fork, replace authoritative `hib.name` with a durable hib
  registry, or snapshot-template plus N ordinary v2 creates with distinct metadata/policy.
  The last fits current discovery. Retain hidden templates until dependants are gone.
  [create-data] [delete-handler]

## 5. Deletion and dependant checks

- **Verified in code.** Only `/templates/{templateID}` DELETE exists. No v2/v3/snapshot DELETE.
  A tag suffix is ignored, deleting the whole env. Guards reject running/pausing/snapshotting
  direct BaseTemplateID users and active snapshot envs with matching base_env_id, including
  checkpointed running sources. They do not scan transitive header references.
  [template-routes] [delete-handler] [template-dependants]
- **Verified in code.** Successful deletion sets deleted_at, releases aliases, clears active
  build tracking, and invalidates caches. Build/assignment/snapshot rows and files remain.
  No template-manager purge is scheduled. Sandbox DELETE kills execution and soft-deletes
  only its own snapshot env, not saved images/builds. It bypasses template dependant checks.
  [delete-handler] [delete-sql] [kill-handler]
- **Inferred.** Indirect image descendants and native forks do not protect a parent build via
  these logical checks. Cleanup must retain shared builds and transitive data dependencies.
  [image-env] [fork-config] [header-diff]

## 6. Listing, finding, and durable image discovery

- **Verified in code.** V1/v2 template lists exclude snapshot templates. Normal-template
  responses include aliases/names, buildID, timestamps, resources, envd, status, not tags/source
  sandbox/metadata. `/snapshots` lists active same-team images with cursor paging capped at 100,
  source/name filters, and one newest successful assignment per env. Bare name allows any tag.
  Explicit `:default` filters default. JSON exposes only snapshotID and names. SnapshotID is
  preferred namespace/alias:tag or raw ID:tag. SQL's build/resource/source/time fields are dropped.
  [templates-v1-sql] [templates-v2-sql] [templates-v1] [snapshot-list] [snapshot-list-sql]
- **Verified in code.** Raw-ID template detail accepts images and returns names/timestamps plus
  paginated build IDs/status/timestamps/resources/envd. Alias lookup resolves raw ID. Separate
  tags route exposes assignments. Re-saving an alias does not update snapshot_templates'
  initial source/node/build record. Source filter and template creation time are not last-save
  history. No custom image metadata/labels exist. PATCH only changes public visibility.
  Storage metadata is provenance. [template-detail] [template-by-id] [alias-resolution]
  [tag-list-sql] [save-resolve] [template-update] [snapshot-cache-write]
- **Inferred.** Reserved aliases permit image discovery from E2B alone. Rich labels/latest
  source history need hib records or deliberate DB reads. Do not use ordinary template lists.
  [snapshot-list] [template-update]
- **Verified in code.** V2 sandbox metadata filtering treats forks normally. Running records
  use memory filtering and paused records snapshot metadata. Every fork matches its inherited
  original hib.name/hib-test. No automatic name rewrite occurs. [sandbox-list] [fork-config]

## 7. Tags and the “new builds only” assumption

- **Verified in code.** POST/DELETE `/templates/tags` and GET `/templates/{id}/tags` exist.
  POST assigns an existing ready build UUID to normalized/deduplicated tags without copying.
  GET returns latest assignment per tag. DELETE removes assignment history, not builds/files,
  and cannot delete default. UUID-shaped tags are rejected by this separate validator.
  [tag-routes] [tag-handler] [assign-build] [tag-list-sql] [delete-tags-sql] [names]
- **Verified in code.** “New independent build per image/fork” is false. Save creates one
  build shared across source/image envs. Fork creates one checkpoint reused by N executions.
  Tags assign existing builds under additional names. [upsert-sql] [image-env] [fork-handler]
- **Inferred.** M6 needs no tag mutation. Default assignments plus immutable assigned-build
  references suffice. Tag removal loses history needed to retain running boot builds.
  [select-build] [delete-tags-sql]

## 8. Local files, diff chains, and cleanup

- **Verified in code.** Local `<storage>/<buildUUID>/` holds memfile.header, rootfs.ext4.header,
  snapfile, metadata.json, and nonempty memfile/rootfs.ext4 diff bodies. Local upload copies
  files, not full ancestry. Headers preserve BaseBuildId and overlay dirty/empty ranges on
  prior mappings. Unchanged bytes reference earlier UUIDs/offsets. Images inherit base/source
  pause layers. Image-created machines and forks pause against their loaded composed headers.
  Not every intermediate generation remains referenced. [paths] [upload-v3] [local-storage]
  [header-diff] [mapped-reads] [snapshot-export] [fork-config]
- **Inferred.** Needed B: an active image/tag, actual live backing build, retained paused
  snapshot, or in-flight build/cache/upload roots B, or a needed header names B as base/range
  source. Follow transitively. Root snapfile/metadata remain necessary even with empty diffs.
  [node-create] [header-diff] [mapped-reads] [image-env]
- **Verified in code, local comparison.** Research-time `cleanup/host.go:114-163` reads all
  assignments. `cleanup.go:95-194` roots current ready slots, conservative image-start history,
  cache indexes, recent writes, and transitive headers. Removed means all owning envs removed.
  It already handles many-to-many assignments, unlike the question's single-owner assumption.
  [image-env] [assign-build]
- **Inferred, local comparison.** Images survive source deletion via their shared live env and
  header closure. Native fork F before its first pause has no own assignment. Delete source S
  and F's boot build B belongs only to deleted S. Start-history retention skips machine envs.
  Removed builds lose recent-write grace. B can be deleted while F reads it. Superseding S's
  checkpoint can similarly expose B after grace if newer headers no longer name it.
  Runtime template pins are not hib's layer-cache index roots. No live GC reproduction was run.
  [fork-config] [kill-handler] [header-diff] [mapped-reads]
- **Inferred.** Root actual live backing builds or keep hidden image roots for copies. REST
  exposes BaseTemplateID, not backing build ID. API checkpoint also leaves source BuildID old,
  so Redis BuildID alone is not a universal fix. [sandbox-detail] [fork-config] [save-orch]
  [checkpoint-orch]
- **Verified in code.** Compression/V4/V5 writing default off. V3 has 64-byte metadata and
  40-byte mappings. Hib supports versions 1–3 and aborts on unreadable retained headers.
  Extend parsing before enabling compressed/new-format headers. [compression] [header-wire]
  [header-v3] [header-flags]

## 9. Single-node operational traps

- **Verified in code.** Limits use project overrides, otherwise tier/addons. Stock seed team is
  `0b8a3ded-4489-4722-afd1-1d82e64ec2d5`, slug local-dev-team, tier base_v1.
  Defaults: 20 concurrent sandboxes, 20 template builds. API key selects team. Seed supports
  explicit/generated persistent keys. Actual installed overrides were not inspected.
  [team-limits] [seed-team] [seed-identity] [seed-key] [base-tier] [build-limit] [auth-header]
- **Verified in code.** Save has no total image quota or ordinary build-concurrency reservation.
  Snapshot upsert cap zero means unlimited. Starts consume team slots. Node caps default 200
  sandboxes/three starts. Native fork TryAcquire can partially fail at high counts.
  Keyless LaunchDarkly uses offline defaults. Sync-WP, in-place, async upload, P2P, ancestor
  fallback, and superseded-cache release default false. [save-orch] [upsert-limit] [node-limits]
  [starting-limit] [node-create] [team-concurrency] [flag-client] [flags-wp] [flags-peer]
  [cache-flags]
- **Verified in code.** API snapshot cache TTL is five minutes, refresh one minute, DB fallback.
  Checkpoints invalidate it. Node template cache is execution-lifetime pinned. Stock compose
  persists Postgres but not Redis. Redis is live authority. Reconciliation kills node-only VMs,
  not adopting them. This strengthens prior “unreachable orphan” research. [snapshot-cache]
  [checkpoint-orch] [node-create] [local-compose] [reconcile]
- **Inferred.** Reboot recovery needs surviving DB/storage and completed durable snapshots.
  Never-snapshotted copies have no paused state. Source recovery uses last checkpoint, not
  unsaved execution. Cached/async artifacts are not guaranteed durable. [last-snapshot]
  [sandbox-detail] [checkpoint-upload] [local-compose]

## 10. Live checkpoint-restart failure and rollback

Scope: coordinator-supplied host evidence on 2026-10-04. The first fork at 13:03:43 succeeded
in 446 ms. At 13:03:52 replacement-FC metrics failed. Resume rolled back nine seconds.
A fresh-sandbox rerun succeeded. Not independently reproduced here. Research stayed read-only.

### 10.1. Root cause and file cleanup

- **Verified in code.** Every `ResumeSandbox` allocates `NewSandboxFiles(sandboxID)` with a
  freshly generated suffix. Paths are `<os.TempDir()>/fc-<sandboxID>-<suffix>.sock` and
  `fc-metrics-<sandboxID>-<suffix>.fifo`. The suffix is not the preserved execution ID.
  Checkpoint replacement also has a new LifecycleID. [restart-files] [restart-allocation]
  [checkpoint-upload]
- **Verified in code.** File cleanup removes exact socket/UFFD/rootfs-link paths, not a
  sandbox-ID wildcard. `Process.Stop` removes its own FIFO. Stale map removal checks
  LifecycleID. Cleanup uses sync.Once, but each Close calls/logs MarkStopped. Two traces can
  therefore report one lifecycle repeatedly, not two VMs destroying each other. [restart-cleanup]
  [restart-stop] [restart-close] [restart-map]
- **Verified in code.** Startup creates the FIFO before launching FC. Readiness only checks
  socket-file existence with `os.Stat`, not a working listener. Resume then starts the FIFO
  reader asynchronously and calls `PUT /metrics`. On failure it calls `Process.Stop`, which
  unlinks that FIFO. Snapshot loading comes *after* metrics setup. [restart-configure]
  [restart-socket-wait] [restart-metrics] [restart-stop] [restart-reader]
- **Inferred.** The provided trace establishes failure contacting the replacement's FC API,
  not its initiating cause. A delayed reader can log missing FIFO after this same replacement's
  failed-start Stop unlinks it. This fits the ordering without old/new filename collision.
  A readiness race or premature process exit remains possible, not proven. Obtain full FC
  stderr, process exit status/signal, and both suffixes/LifecycleIDs to distinguish them.
  Successful reruns do not establish a ten-second workaround. [restart-configure]
  [restart-socket-wait] [restart-metrics] [restart-reader] [restart-map]

### 10.2. Why resuming rolls back

- **Verified in code.** The fresh-checkpoint path snapshots/caches first and only uploads after
  the replacement starts. Resume failure returns Internal before `runCheckpointUpload`.
  Deferred handoff settlement abandons that unstarted upload. API marks the new build failed,
  finishes the transition without restoring Running, and kills/removes live routing/store
  state. This internal removal does not run REST DELETE's snapshot-env soft-delete.
  Earlier successful source snapshot assignments remain. [checkpoint-upload] [upload-abandon]
  [checkpoint-orch] [restart-remove] [kill-handler]
- **Verified in code.** `GetLastSnapshot` selects the newest `default` assignment whose build
  has status group `ready`. The failed second build is excluded. GET details falls back to
  this snapshot and reports `paused`. Resume uses that same selector through the snapshot
  cache. “Paused” here does not mean the failed checkpoint was preserved. [last-snapshot]
  [restart-paused-detail] [fork-config] [snapshot-cache]
- **Inferred.** The available checkpoint from 13:03:43 explains the observed nine-second loss.
  `autoPause=true` is not the cause of fallback. The failed fork never reaches its copy-boot
  phase, and timeout 20 applies to prospective copies, not source recovery. [fork-handler]
  [checkpoint-orch] [last-snapshot]

### 10.3. First-checkpoint failure

- **Inferred.** If this is the first checkpoint and replacement startup fails this way, the
  original is stopped and there is no ready source snapshot to select. GET/connect/resume
  cannot recover machine state through supported APIs. Cached bytes may remain, but upload
  was abandoned and the build is failed. Status flipping is not durability repair. [upsert-sql]
  [checkpoint-upload] [upload-abandon] [checkpoint-orch] [last-snapshot]

### 10.4. Shared path and source state

- **Verified in code.** Fork and snapshot-template save both call node `Sandbox.Checkpoint`.
  Both require a live source. Fork explicitly reports paused as 409. Save only reads the
  live store and reports paused as 404. Snapshot-save plus ordinary creates solves metadata
  inheritance, but its initial save still has this restart/failure risk. [fork-handler]
  [save-handler] [checkpoint-orch] [save-orch] [checkpoint-node]

### 10.5. A safer sequence

- **Verified in code.** No public paused-source promotion or fork. Ordinary creates exclude
  source-kind `snapshot` envs. Connect/resume restores the same ID. Pause-resume-save/fork
  still invokes Checkpoint. Existing usable named images support v2 creates without another
  source checkpoint. [select-build] [resume-schema] [fork-handler] [save-handler] [create-data]
  [image-env]
- **Verified in code.** True in-place memory checkpoint avoids replacement FC startup, but
  requires sync-WP, the in-place flag, and an E2B-format FC release at least 0.2.0.
  A configuration version literally `v1.14.4` is bare format and fails that gate even with
  flags enabled. The binary's log banner alone does not establish its configured release
  label. Flags alone are therefore not a verified workaround. [checkpoint-node] [flags-wp]
  [restart-fc-contract] [restart-fc-version]
- **Inferred.** Pause, prove full-memory durability, promote its build through an internal/DB
  operation, then resume could avoid checkpoint-restart. `CreateSnapshotTemplateEnv` can
  assign an existing build, but needs new integration/ownership/durability handling.
  Normal pause can also fail destructively. Not spiked. [image-env] [upstream-loss-issue]

### 10.6. Upstream status, checked 2026-10-04

- **Verified in code and GitHub records.** `e2b-dev/infra` now resolves to `e2b-dev/runtime`.
  Commit/PR/issue searches covered checkpoint, FC metrics, and exact resume-error text.
  Main checked was `92197909dce5a1bef33e764ae4af76f0732fd7a8`, October 3. Replacement resume
  failure still returns Internal before upload.
  No merged fix for this loss path was found. [upstream-fresh-failure]
- **Verified in GitHub records.** Issue #3658 covers analogous first-checkpoint loss with
  replacement memory-allocation failure. PR #3666 proposed uploading the cached snapshot on
  resume failure. It was closed September 29 with `merged=false`. The maintainer closed the
  issue and PR as intended fail-closed behavior. It is not an upstream-shipped fix.
  [upstream-loss-issue] [upstream-upload-pr] [upstream-loss-resolution]
- **Inferred.** PR #3666's node-only patch would still return Internal. The pinned API would
  still mark that build failed, so persisting bytes alone does not make it selectable by
  normal resume. A real recovery fix also needs an API/build-status contract for a durable
  checkpoint whose replacement failed. [upstream-upload-pr] [checkpoint-orch] [last-snapshot]
- **Verified in GitHub records.** #3275 is periodic flush noise, not startup PUT /metrics.
  #3441 proposes named-image fork and is still open, absent from pin. Neither proves crash cause.
  [upstream-metrics] [upstream-image-fork]

## 11. Live spikes on our host (2026-10-04, E2B 5cfcf3282ddb)

### Native fork (POST /sandboxes/{id}/fork)

- About 0.3 to 2.3 s. The origin pauses for about 0.5 s.
- Forks inherit ALL metadata (`hib.name`, `hib.image`, `hib.idle`, labels). Listing by
  `hib.name=x` then returns several machines, so a native fork breaks `machines.lookup`. There's no
  metadata-update endpoint, and SandboxForkRequest has only Count and Timeout.
- Forks inherit env vars, running processes, files, `lifecycle.onTimeout=pause`,
  `autoResume=false`. endAt is now plus the request timeout.
- A paused origin returns 409 "paused and cannot be forked; resume it first".
- Forks are isolated from the origin, and pause and resume fine.

### Named snapshot (POST /sandboxes/{id}/snapshots, Name)

- Running origin only. A paused origin returns 404 "doesn't exist".
- 0.24 s for a 512 MB machine, 0.4 to 2.0 s for a 2 GB hib-dev machine. Guest loses about 0.3 s.
- Response `{"names":["local-dev-team/<name>"],"snapshotID":"local-dev-team/<name>:default"}`.
- `Create` with Template `<name>` (plain alias) works. The `local-dev-team/<name>` form returned
  "template not found" in the spike. It was only tried while the alias was missing, so recheck
  it if needed.
- The new template takes the origin's cpu, memoryMB and diskSizeMB (512 MB stays 512 MB,
  hib-dev stays 2048 MB and 11346 MB disk).
- A machine created from it gets ONLY the metadata passed to Create (fresh `hib.name` works).
  It carries files, env vars and running processes, and the same `/proc/sys/kernel/random/boot_id`.
- `GET /templates` does NOT list snapshot templates. `GET /templates/aliases/{alias}` and
  `GET /templates/{id}` do. In Postgres, `envs.source` is `template` (built images), `snapshot`
  (pause snapshots) or `snapshot_template` (named saves).
- Re-saving to an existing name adds a build to the same template. New creates use the latest
  build. Machines from the older build keep their own state through pause and resume.

### Deleting a template (DELETE /templates/{id})

- 400 "cannot delete template ... because there are paused sandboxes using it", and the same
  for running sandboxes. So E2B itself refuses to delete an image anything still uses.
- 204 once nothing uses it. The alias then 404s and `envs.deleted_at` is set (soft delete).
- Build files stay on disk. `hibd cleanup` keeps the build for MinAge (one hour) as "a machine may
  have started from it", then frees it. Before the delete it was kept as "the current build of
  image <id>", and machines' snapshots built on it are kept through header references.
- hibd's automatic cleanup already removed killed spike machines' snapshots (journal 13:13:19).

### RNG and keys

- Clones do NOT share RNG state. The guest kernel has vmgenid, and dmesg shows "crng reseeded due
  to virtual machine fork". UUIDs and urandom differ across forks and across creates from one save.
- Re-key on a hib-dev copy: `rm -f /etc/hibernaut/ssh/ssh_host_ed25519_key{,.pub}` then
  `/usr/lib/hibernaut/machine-init <base64 keys>` prints a new key, and sshd on
  `/run/hibernaut/sshd.sock` serves it at once. The origin keeps its key.

### Checkpoint-restart race (critical, 1 in about 21 checkpoints)

- A second fork 9 s after the first, on an origin that had just paused and resumed, ttl 20.
- Orchestrator: "error resuming sandbox after checkpoint: failed to start FC: error setting fc
  metrics ... dial unix /tmp/fc-<id>-<suffix>.sock: connection refused", "fc process already
  exited", "failed to open fc metrics FIFO", two "sandbox lifecycle stopped" at the same ms.
- API returned 500, the origin was left paused, and resume rolled it back to its previous
  checkpoint, silently losing about 9 s of work.
- 18 later trials passed (fork, resumefork, snapshot, three each, twice, 1 s gap).
- Named snapshot uses the same checkpoint path, so `hib image save` shares the risk.

### Capacity

- A 2 GB create failed with 500 "Failed to place sandbox" when about 6 GB of huge pages were
  in use. hibd's checkRoom must count the resumed origin and every copy.

## What this means for M6

- **Inferred decision.** Use snapshot save plus v2 create, also for named forks unless changing
  E2B. Send fresh metadata, full-memory autoPause, autoResume off, private ingress, setup,
  and explicit TTL. There is no metadata rename. [save-handler] [create-data] [fork-config]
- **Inferred decision.** Paginate `/snapshots` and use reserved user-image/internal-fork prefixes.
  Retain internal templates while copies exist. Image removal needs an “in use” or deferred
  outcome. Cleanup must preserve transitive data dependencies. [snapshot-list] [delete-handler]
  [header-diff]
- **Inferred risk.** Direct native forks need actual live-build roots. Preserve existing
  many-to-many assignment support. Avoid tag deletion until its retention hazards are handled.
  [image-env] [fork-config] [delete-tags-sql]
- **Inferred risk.** Save/fork 500 is potentially destructive. Do not silently resume and retry
  as though state were current. Surface checkpoint age/rollback and require durability plus
  recoverable build state before treating source restart failure as safe. [checkpoint-orch]
  [last-snapshot] [upstream-upload-pr]
- **Inferred spike requirements.** Verify limits/flags/versions, timing, dev servers/sockets,
  tokens/credentials/environment/SSH isolation, and failure/cancellation recovery. Test cold
  reboot/source-deleted restore, repeated pauses, overwrites, GC, and concurrent checkpoints.
  Host outcomes and external name-size limits remain unverified. [checkpoint-node] [fork-handler]
  [guest-init] [mapped-reads]

## Pinned source permalinks

[save-handler]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/snapshot_template_create.go#L24-L158
[save-orch]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/snapshot_template.go#L40-L173
[save-resolve]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/snapshot_template.go#L200-L266
[versions]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/utils/version.go#L9-L58
[states]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/sandbox/sandboxtypes/states.go#L32-L118
[grpc]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/orchestrator.proto#L169-L230
[checkpoint-node]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/sandboxes.go#L1077-L1174
[checkpoint-upload]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/sandboxes.go#L1182-L1490
[checkpoint-orch]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/checkpoint_instance.go#L21-L141
[upsert-sql]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/snapshots/create_new_snapshot.sql#L1-L106
[pause-fields]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/pause_instance.go#L128-L183
[status-group]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/migrations/20260210120002_add_status_group_column.sql#L3-L21
[build-team]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/migrations/20260218120000_add_team_id_to_env_builds.sql#L4-L22
[legacy-env]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/migrations/20260204172712_remove_build_assignment_triggers.sql#L22-L39
[image-env]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/snapshots/create_snapshot_template_env.sql#L1-L36
[names]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/id/id.go#L15-L164
[alias-schema]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/migrations/20231124185944_create_schemas_and_tables.sql#L63-L70
[alias-resolution]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/cache/templates/alias_cache.go#L68-L182
[cache-nfs]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/template/cache.go#L1201-L1225
[snapshot-export]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L2183-L2410
[upload-v3]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/build_upload_v3.go#L17-L180
[local-storage]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/storage_fs.go#L90-L215
[select-build]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/templates/get_template_with_build_by_tag.sql#L5-L27
[create-handler]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox_create.go#L79-L238
[create-network]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox_create.go#L246-L336
[create-data]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox_create.go#L363-L403
[create-config]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/create_instance.go#L300-L362
[envd-token]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox_create.go#L641-L676
[node-create]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/sandboxes.go#L222-L354
[fc-resume]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/fc/process.go#L605-L660
[resume-ready]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L1705-L1743
[envd-init]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/envd.go#L85-L122
[init-wire]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/envd/envd.gen.go#L315-L346
[guest-init]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/internal/api/init.go#L246-L419
[rootfs-hostname]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/template/build/core/rootfs/templates.go#L38-L62
[header-diff]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/metadata.go#L32-L189
[mapped-reads]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/build/build.go#L285-L333
[fork-handler]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox_fork.go#L28-L239
[fork-schema]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L1131-L1183
[fork-config]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox_resume.go#L312-L397
[timeouts]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/timeout_helper.go#L18-L42
[start-times]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox.go#L53-L86
[team-concurrency]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/create_instance.go#L177-L219
[resume-schema]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L1050-L1110
[sandbox-routes]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3170-L3654
[template-routes]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L3830-L4028
[delete-handler]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/template_delete.go#L21-L141
[delete-sql]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/templates/delete_template.sql#L1-L22
[template-dependants]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/templates/exists_template_snapshots.sql#L1-L7
[kill-handler]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox_kill.go#L21-L112
[templates-v1-sql]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/get_team_templates.sql#L1-L40
[templates-v2-sql]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/get_team_templates_with_cursor.sql#L8-L54
[templates-v1]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/templates_list.go#L51-L91
[snapshot-list]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/snapshot_template_list.go#L21-L141
[snapshot-list-sql]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/snapshots/list_team_snapshot_templates.sql#L1-L51
[template-detail]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/template_get.go#L26-L123
[template-by-id]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/templates/get_template_by_id.sql#L1-L12
[template-update]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/template_update.go#L79-L144
[snapshot-cache-write]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/sandboxes.go#L1597-L1667
[sandbox-list]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandboxes_list.go#L288-L404
[tag-routes]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/spec/openapi.yml#L4142-L4246
[tag-handler]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/template_tags.go#L21-L292
[assign-build]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/templates/create_template_build_assignment.sql#L1-L12
[tag-list-sql]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/templates/list_template_tags.sql#L1-L8
[delete-tags-sql]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/templates/delete_template_build_assignment.sql#L1-L4
[paths]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/paths.go#L9-L110
[sandbox-detail]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox_get.go#L129-L192
[compression]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/compress_config.go#L24-L38
[header-wire]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/serialization.go#L20-L66
[header-v3]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/serialization_v3.go#L11-L40
[header-flags]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L396-L403
[team-limits]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/migrations/20260923090000_add_project_limits_api_list_rate.sql#L8-L25
[seed-team]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/local-dev/seed-local-database.go#L348-L366
[seed-identity]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/local-dev/seed-local-database.go#L31-L37
[seed-key]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/local-dev/seed-local-database.go#L145-L201
[base-tier]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/migrations/20231220094836_create_triggers_and_policies.sql#L1-L5
[build-limit]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/migrations/20250901161352_add_concurrent_template_builds_to_tier.sql#L1-L5
[auth-header]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/auth/pkg/auth/consts.go#L1-L9
[upsert-limit]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L657-L665
[node-limits]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L487-L487
[starting-limit]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L636-L638
[flags-wp]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L196-L227
[flags-peer]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L290-L294
[cache-flags]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L405-L426
[flag-client]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/client.go#L60-L77
[snapshot-cache]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/cache/snapshots/snapshot_cache.go#L17-L99
[local-compose]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/local-dev/docker-compose.yaml#L69-L85
[reconcile]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/sandbox/store.go#L144-L159
[last-snapshot]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/snapshots/get_last_snapshot.sql#L1-L21
[restart-files]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/sandbox.go#L27-L74
[restart-allocation]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L1228-L1236
[restart-cleanup]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/cleanup.go#L71-L124
[restart-stop]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/fc/process.go#L724-L757
[restart-close]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L1806-L1855
[restart-map]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/map.go#L334-L390
[restart-configure]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/fc/process.go#L301-L340
[restart-socket-wait]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/socket/socket.go#L11-L29
[restart-metrics]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/fc/process.go#L594-L615
[restart-reader]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/fc/fc_metrics.go#L331-L380
[upload-abandon]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/sandboxes.go#L1536-L1563
[restart-remove]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/delete_instance.go#L141-L150
[restart-paused-detail]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox_get.go#L175-L271
[restart-fc-contract]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/fcversion/sandbox_features.go#L12-L32
[restart-fc-version]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/fcversion/version.go#L62-L84
[upstream-fresh-failure]: https://github.com/e2b-dev/runtime/blob/92197909dce5a1bef33e764ae4af76f0732fd7a8/packages/orchestrator/pkg/server/sandboxes.go#L1618-L1627
[upstream-loss-issue]: https://github.com/e2b-dev/runtime/issues/3658
[upstream-upload-pr]: https://github.com/e2b-dev/runtime/pull/3666
[upstream-loss-resolution]: https://github.com/e2b-dev/runtime/issues/3658#issuecomment-5896980148
[upstream-metrics]: https://github.com/e2b-dev/runtime/issues/3275
[upstream-image-fork]: https://github.com/e2b-dev/runtime/pull/3441
