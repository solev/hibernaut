# E2B snapshot compaction for M9

Source: `e2b-dev/infra` at `5cfcf3282ddb95e90a8214ce71329641bdfe6cb6`.

- **Verified in code.** This note uses owning source at the immutable commit above.
  Every pinned reference's file and line range was checked in `/tmp/e2b-infra`.
  Hibernaut citations identify checked worktree paths and lines separately.
  Current-upstream references name their own commit. No host commands or performance tests ran.
  [flag-client] [hib-installer]
- **Verified in code.** M9 asks for bounded disk use across pauses. Its historical result is
  approximately 280 MiB per pause and 13 required builds after 20 cycles, totaling 3.6 GiB.
  Those are recorded results, not measurements from this research. The test is not truly idle.
  It overwrites a file with 64 MiB of random bytes before every pause. [hib-m9] [hib-cycles]

## 1. What a pause actually writes

- **Verified in code.** Pause waits for prior deferred exports, stops health checks, and runs
  best-effort guest reclaim. It then pauses Firecracker and creates `snapfile`. The custom
  Firecracker snapshot operation also drains and flushes the virtio disk. Memory and rootfs
  postprocessing happen against the stopped guest. [pause-prepare] [pause-stop]
- **Verified in code.** Storage names are `memfile`, `memfile.header`, `rootfs.ext4`,
  `rootfs.ext4.header`, `snapfile`, and `metadata.json` under a build UUID. Bodies contain packed
  diff data, not necessarily a complete RAM image or an ordinary mountable ext4 image.
  An empty body can be omitted. Headers and metadata still describe the logical device.
  Local persistence copies the exported file. It does not fetch and merge its ancestors.
  [paths] [upload-v3] [storage-local]
- **Verified in code.** The standard resumed-memory backend drains UFFD work and obtains the
  dirty bitmap from Firecracker's `GetDirtyMemory`. It combines that bitmap with zero-installed
  and removed pages. Dirty wins over empty. With `use-sync-wp` and `sync-wp-tracker-dirty`,
  it can instead use the handler's page tracker. Requesting that tracker on an async-WP guest
  fails closed. A freshly booted `NoopMemory` backend exports resident, nonempty pages and
  describes the other pages as empty. [uffd-dirty] [uffd-empty] [fc-dirty] [noop-memory]
- **Verified in code.** `HugepageSize` is 2 MiB. `PageSize` and `RootfsBlockSize` are 4 KiB.
  Memory dirty tracking uses the handler's page size, which is 2 MiB for huge-page guests.
  Dedup subdivides each dirty block into 4 KiB pages. Its diff metadata uses 4 KiB, while
  the inherited header's `Metadata.BlockSize` can remain 2 MiB. This is intentional.
  Mapping validation uses 4 KiB. Rootfs dirty and zero tracking use 4 KiB blocks.
  [block-sizes] [memory-geometry] [uffd-dirty] [memfd-dedup-result] [header-compose] [disk-cache]
- **Verified in code.** Rootfs writes update a COW cache. Nonzero writes become dirty.
  Zero writes and accepted discard requests punch holes and mark blocks zero. Export copies
  only dirty ranges into a packed body and puts zero ranges in the header with `uuid.Nil`.
  It uses `copy_file_range`, with ordinary-copy fallback. The NBD export ejects the cache and
  stops the sandbox before exporting it. [disk-cache] [disk-export] [disk-zero] [nbd-export]
- **Verified in code.** A `BuildMap` holds logical `Offset`, `Length`, source `BuildId`, and
  source `BuildStorageOffset`. These are byte offsets, not page indices. `ToDiffHeader`
  overlays dirty and empty ranges on the parent's mappings, normalizes the result, advances
  `Generation`, and preserves `BaseBuildId`. Unchanged ranges keep their original source UUID
  and storage offset. Reads resolve those ranges directly. This is a flattened source map,
  not a recursive lookup through one header per historical pause. [mapping] [header-compose]
  [header-generation] [mapped-offset] [mapped-read]
- **Verified in code.** Uncompressed template metadata starts at V3. The ordinary uncompressed
  upload path preserves its incoming header version. `v4-header-for-uncompressed`, compression,
  or `header-v5-write` select the newer upload path. V5 wins over V4. Turning those flags off
  does not downgrade a V4/V5 header inherited from an existing parent. [header-generation]
  [upload-version] [compression-resolve] [upload-finalize] [header-wire]
- **Inferred.** A parent remains necessary while even one current nonzero range names its body.
  A small surviving range can retain a large old packed file. Memory dedup can reduce the new
  body while retaining more old source UUIDs. Rootfs can independently retain the same build
  directories. The correct metric is the union of memory and disk sources, not memory savings
  alone. Hib additionally retains base UUIDs and recursively reads retained ancestors' headers.
  That closure is conservative compared with the latest header's direct read requirements.
  [header-compose] [mapped-read] [hib-parser] [hib-closure]
- **Inferred.** The directly needed build count rises by one when a pause adds a new nonzero
  source and no previous source loses its last surviving range across both devices.
  Overwriting or zeroing every surviving range from one source removes that dependency.
  There is no special transition at pause 12. The observed late-cycle slope is consistent
  with one new source surviving each cycle. It is not caused by a recursive read chain
  becoming one level deeper. [header-compose] [mapping] [hib-cycle-size]
- **Unverified.** The historical headers and per-source byte counts were not supplied.
  Source alone cannot identify which memory pages or disk blocks kept each of those 13
  builds alive, or prove their share of the 3.6 GiB. The original test's direct-header count
  is also distinct from cleanup's conservative recursive retention. Record both in M9.
  [hib-m9] [hib-cycle-size] [hib-closure]
- **Verified in code.** No pause-chain retention limit was found in the production pause,
  memory export, header, and command implementations searched for full snapshots, flattening,
  squashing, and chain limits. There are representation bounds, not an automatic squash.
  `Mapping` permits at most 65,535 distinct nonnil source UUIDs and 32-bit 4 KiB indices.
  V4/V5 inner headers have a 256 MiB decompression/write cap. V5 limits mapping entries to
  `8 << 20`. The scheduler's `chainLimit = 128` only truncates advertised locality metadata.
  It does not truncate the storage header or make older data disposable.
  [compact-map] [header-cap] [header-store] [header-v5] [scheduler-limit]

## 2. `memfile-diff-dedup`

- **Verified in code.** The complete coded fallback is the following JSON. Every budget is
  disabled by default. `fetchRunWindowPages = 0` resolves to the 2 MiB default fetch frame,
  which is 512 pages. Despite the flag comment's shorthand, this is not read from a configured
  compression frame size. Known parent frame tables do supply their actual frame boundaries.
  [dedup-default] [dedup-compare] [dedup-fetch-key] [compression-default]

```json
{
  "enabled": false,
  "bestEffort": false,
  "directIO": false,
  "maxFetchWindowsPerBlock": 0,
  "maxPromotedParentPagesPerBlock": 0,
  "maxPagesPerPromotedFrame": 0,
  "blockFaultPct": 0,
  "fetchRunWindowPages": 0
}
```

- **Verified in code.** The pause callsite reads this JSON in `processMemorySnapshot`.
  It passes the loaded parent memory device as the comparison base. Each dirty 4 KiB page
  is classified as zero, different from the same logical parent page, or byte-identical.
  Zero pages become nil mappings. Different pages are stored in the new body. Equal pages
  retain the parent's source mapping unless a promotion budget selects them.
  This is positional comparison, not content-addressed dedup across arbitrary offsets or
  other machines. `bestEffort` stores an uncached parent page as current instead of fetching
  it. Parent read failures otherwise fail dedup. [dedup-pause] [dedup-compare]
- **Verified in code.** `maxFetchWindowsPerBlock` and `maxPromotedParentPagesPerBlock` work
  together. The planner groups parent pages by source UUID and fetch window, then copies
  whole cheapest groups until the window target is met or the page budget is exhausted.
  Both values must be positive. An insufficient budget can leave more windows than the target.
  The target is a best-effort fetch-fragmentation bound for processed dirty blocks.
  It is not a maximum number of ancestors or snapshot generations. [dedup-budget]
  [dedup-block] [dedup-window]
- **Verified in code.** `maxPagesPerPromotedFrame` enables a separate global cheap-frame pass.
  It is a price per expected backing fetch removed, not a global page-count cap.
  The planner checks references outside the dirty set. `blockFaultPct` in `(0, 100)` models
  independent block-fault probability. Values outside that interval use strict attribution.
  With zero, a frame referenced outside the processed diff is not promoted by this pass.
  The maximum price is clamped to `fetchRunWindowPages - 1`. The global pass runs before the
  per-block pass. These are two separate opportunities to copy parent-identical pages.
  [dedup-budget] [dedup-global] [dedup-compare]
- **Inferred.** No JSON setting guarantees bounded total chain storage for arbitrary workloads.
  Neither promotion pass rewrites never-dirty blocks. Neither copies every surviving range
  from each old build. The same source UUID can still be needed by rootfs. A real bounded
  design needs periodic complete materialization or an explicit source-count/byte threshold
  that triggers it. The test-only `plannerFlatten` illustrates that distinct operation.
  [dedup-compare] [dedup-block] [dedup-global] [sim-flatten] [rootfs-header]
- **Verified in code.** Dedup has no envd-version gate and does not require V4, V5, compression,
  reporting, or hinting. It works with memfd and the fallback Firecracker-memory export.
  The block size must be a multiple of 4 KiB. The fallback path first writes the coarse diff,
  then writes a separate `.dedup` output. In-place checkpoints explicitly disable dedup because
  the running guest retains its memfd. Normal pause and resume-fresh checkpoint can use it.
  [dedup-pause] [memfd-dedup-start] [fc-export] [fc-export-fallback]
- **Verified in code.** The memfd path starts compare and drain on a goroutine. The final header
  waits on that result. Upload waits for the materialized body. By default, an immediate
  overlapping resume also waits rather than serving a provisional memfd view.
  `directIO` adds `O_DIRECT` to the packed output file and attempts preallocation.
  It does not change comparison or logical correctness. [memfd-dedup-start]
  [memfd-dedup-result] [memory-header-future] [upload-v3] [dedup-direct]
- **Inferred.** CPU work scales with scanned dirty bytes and parent comparisons. Parent cache
  misses add storage reads when `bestEffort` is false. The host retains the source memfd,
  parent cache pages, Roaring bitmaps, candidate-frame maps, and the final header during work.
  More promotion increases body writes. Background completion can move cost out of the pause
  response and into immediate resume or durability time. There is no dedicated wall-time
  budget in this JSON. Measure compare, drain, upload, and resume separately.
  [dedup-compare] [memfd-dedup-start] [memfd-dedup-result] [dedup-metrics]
- **Verified in code.** Source simulations sweep prices 16, 64, 256, and 1024 pages and fault
  probabilities 0, 20, 40, and 80. One combined scenario uses eight fetch windows and 64 copied
  parent pages per block. Its every-sixth-pause full materialization is test-only.
  These are synthetic planner tests, not Hibernaut host measurements or recommended defaults.
  [sim-budgets] [sim-scenarios] [sim-flatten]

## 3. `guest-pause-reclaim`

- **Verified in code.** The coded default is JSON `null`. The complete supported flat shape is
  `{"sync":0,"drop_caches":0,"compact_memory":0,"fstrim":0}`, with values in milliseconds.
  Missing values resolve to zero. The script skips caps below one millisecond, including
  negative values. There is no `enabled` key and no version-specific reclaim endpoint.
  [reclaim-default] [reclaim-config] [reclaim-script]
- **Verified in code.** Actual order is optional user-cgroup freeze, optional envd heap collapse,
  then `fstrim -av`, `sync`, `echo 3 > /proc/sys/vm/drop_caches`, and
  `echo 1 > /proc/sys/vm/compact_memory`. Each enabled step runs in its own root shell with
  a guest-side hard deadline. Failures do not stop later steps. The outer deadline is the
  sum of enabled caps plus 500 ms. Start, stream, and nonzero-exit errors are warnings.
  They do not veto a normal memory pause. [reclaim-script] [reclaim-call]
- **Verified in code.** Reclaim uses envd's process `Start` RPC with a system tag, root user,
  access token when needed, and `Connect-Timeout-Ms`. It is not `POST /reclaim`.
  The callsite has no minimum-version check. Pinned envd honors the request timeout by giving
  the process a separate deadline. A client disconnect alone does not kill the process.
  Guest utilities and writable procfs remain prerequisites. [system-shell] [process-start]
  [reclaim-call]
- **Unverified.** There is no named minimum envd version for reclaim in this callsite.
  The pinned 0.9.0 implementation supports the process timeout. That does not establish
  the oldest release supporting both the timeout and system tag. Snapshot eligibility
  starts at 0.5.0, but that gate is not a reclaim-capability guarantee.
  [envd-version] [envd-versions] [process-start] [system-shell]
- **Verified in code.** Native freeze requires envd at least 0.6.3 and collapse requires 0.6.5.
  Pinned envd reports 0.9.0. These are distinct prerequisites from the reclaim shell itself.
  Rebuilding host envd does not retroactively replace the binary or running process in an
  existing snapshot. New template builds use the promoted host binary by default.
  Resume upgrade has its own flag, defaulting to `off`. [envd-versions] [envd-version]
  [envd-selection] [collapse-call]
- **Verified in code.** Reclaim does not zero every free RAM page, inflate the balloon, or turn
  on free-page reporting. Balloon installation uses amount zero. Reporting and hinting are
  separate build-time choices, conditional on Firecracker capabilities. Reporting defaults
  false. Hinting config defaults null. Hinting has a separately budgeted pre-pause drain.
  UFFD's empty/removed tracking is what replaces discarded memory with nil header ranges.
  [reclaim-script] [balloon-install] [balloon-build] [reporting-default] [balloon-flags]
  [hint-drain] [uffd-remove] [uffd-dirty] [uffd-empty]
- **Inferred.** `drop_caches` can reduce live cache pages. `compact_memory` can consolidate
  physical allocation. Neither operation alone proves that freed guest pages become zero
  or disappear from a persisted memfile. Without a working balloon mechanism, old contents
  can remain in free guest RAM and still be exported. Dedup's content-zero detection is
  different from guest allocation-state detection. [reclaim-script] [dedup-compare]
  [balloon-build] [uffd-empty]
- **Verified in code.** The host NBD device advertises TRIM and WRITE_ZEROES. Both request
  types punch the overlay cache and mark ranges zero. Rootfs export omits zero ranges, and
  header overlay replaces their old source mappings with nil. Thus delivered discard can
  reduce the new disk body and remove dependencies for discarded ranges. It does not truncate
  already stored ancestor files. [nbd-flags] [nbd-trim] [disk-zero] [disk-export]
  [header-compose]
- **Unverified.** Whether the installed guest filesystem, virtual disk, and Firecracker
  version deliver `fstrim` to this NBD path must be measured. The infra source proves the host
  endpoint behavior, not that the installed guest actually sends discard. Missing `fstrim`,
  unsupported discard, short budgets, or incomplete trimming can make this step ineffective.
  [reclaim-script] [nbd-flags] [nbd-trim]
- **Inferred.** A sensible first reclaim experiment is the exact configuration already used by
  `resume-build -reclaim`: `{"sync":500,"drop_caches":200,"compact_memory":1000,"fstrim":500}`.
  Its outer budget is 2.7 seconds. These are best-effort tuning values, not a durability SLA.
  Normal memory pause preserves dirty guest cache in RAM. Filesystem-only pause instead has
  a separate mandatory filesystem-preparation path. [tool-reclaim] [reclaim-script]
  [pause-filesystem]

## 4. `collapse-envd-heap`

- **Verified in code.** Defaults are `collapse-envd-heap = false` and
  `collapse-envd-heap-timeout-ms = 10000`. The orchestrator evaluates them before guest reclaim.
  It calls authenticated `POST /collapse` only for envd at least 0.6.5. HTTP, decode, timeout,
  and version errors are logged. Pause proceeds. The timeout is a call budget, not a promise
  that the guest kernel finishes a running syscall by that instant. [collapse-default]
  [reclaim-call] [collapse-call] [collapse-http] [envd-versions]
- **Verified in code.** Envd selects its own unnamed readable/writable anonymous mappings.
  It first uses `MADV_HUGEPAGE`, then tries `MADV_COLLAPSE` on fully contained aligned 2 MiB
  windows. Empty or ineligible windows are skipped. Cancellation stops further calls.
  Statistics separate migrated windows, already-huge no-ops, and skipped windows using
  `AnonHugePages`. The implementation is not Go object relocation or a garbage collector.
  It does not compact user processes' heaps. [collapse-linux] [collapse-api]
- **Inferred.** The primary expected benefit is fewer distinct guest-physical frames touched
  by envd during resume. It may change which huge frames are dirty and exported, but it
  supplies no ancestor-count or body-size bound. It can itself dirty migrated pages and add
  pause CPU/time. Keep the 10-second fallback for the first isolated test and record actual
  duration and migrated-window counts before reducing it. [collapse-linux] [collapse-call]

## 5. Other relevant controls and limits

- **Verified in code.** There is no production full-chain-compaction flag in the searched
  feature definitions and pause/export/header consumers. `autoPauseMemory`, default true,
  means retain execution memory rather than make a filesystem-only snapshot. It does not
  request a parent-independent full RAM or rootfs body. Setting it false changes resume into
  a cold boot and loses process state. It requires auto-pause and compatible Firecracker.
  It is not an M9 substitute. [snapshot-kind] [pause-filesystem] [memory-header-future]
  [rootfs-header] [fc-snapshot]
- **Verified in code.** `use-memfd` defaults true and `memfd-background-copy` defaults true.
  They select memory export backing and timing, not ancestor elimination.
  `memfd-dedup-inflight-serve`, `memfd-dedup-free-index`, and
  `snapshot-cache-drop-provisional-header` default false. The first enables provisional local
  serving during compare/drain. The others release its index/header holders. They change
  host-memory lifetime and latency, not the final source dependencies. [memory-flags]
  [dedup-default] [dedup-pause] [fc-export] [upload-provisional]
- **Verified in code.** `use-sync-wp`, `in-place-checkpoint`, `defer-memory-export`, and
  `sync-wp-tracker-dirty` default false. In-place requires synchronous WP and compatible FC.
  Deferred memory export uses a COW capture window. It is gated on in-place operation and
  safe balloon-reporting coordination. In-place exports are cumulative against the original
  loaded template and disable dedup. They do not implement periodic complete device
  materialization. Keep them out of the first M9 flag experiment. [memory-flags]
  [memory-preflight] [memory-union] [dedup-pause] [checkpoint-gate]
- **Verified in code.** `compress-config` defaults to this complete JSON.
  Compression stores framed compressed bodies and newer headers. It does not merge source
  UUIDs. `compressionType` must select a real codec for compression to take effect.
  [compression-flags] [compression-resolve] [upload-version]

```json
{
  "compressBuilds": false,
  "compressionType": "",
  "compressionLevel": 0,
  "frameSizeKB": 0,
  "minPartSizeMB": 0,
  "frameEncodeWorkers": 0,
  "encoderConcurrency": 0
}
```

- **Verified in code.** Unlike the three M9 flags, compression also has startup environment
  configuration: `COMPRESS_ENABLED`, `COMPRESS_TYPE`, `COMPRESS_LEVEL`,
  `COMPRESS_FRAME_SIZE_KB`, `COMPRESS_MIN_PART_SIZE_MB`, `COMPRESS_FRAME_ENCODE_WORKERS`,
  and `COMPRESS_ENCODER_CONCURRENCY`. Defaults are false, empty type, and zero numeric values.
  A zero frame size means 2 MiB. Flags can override the base config at upload time.
  Compression and forced V4/V5 writing need a cleanup-parser change first.
  [compression-default] [compression-resolve] [hib-parser]
- **Verified in code.** `v4-header-for-uncompressed` and `header-v5-write` default false.
  They alter header encoding even when bodies remain uncompressed. V4 uses an LZ4-compressed
  inner header. V5 additionally changes the mapping layout. Neither rewrites old sources
  into the new body. [header-flags] [upload-version] [header-v4] [header-v5]
- **Verified in code.** `snapshot-cache-release-superseded` and
  `snapshot-cache-ancestor-storage-fallback` both default false. Release only acts when both
  are enabled and a superseded local template has no pins, resolved devices, and a completed
  storage upload. Ancestor fallback repairs upload metadata after cache eviction.
  These affect local template-cache lifetime. They do not delete the durable ancestor bodies
  named by the current header. `use-nfs-for-snapshots` defaults to `env.IsDevelopment()` and
  chooses a cache path if configured. It is not a squash switch. [cache-flags]
  [cache-release] [cache-choice] [cache-choice-default]
- **Verified in code.** `peer-to-peer-chunk-transfer` and `peer-to-peer-async-checkpoint`
  default false. They permit peer reads and early checkpoint return while storage upload
  continues. They do not flatten either artifact. Async checkpoint must not be equated with
  durable storage completion. The installer experiment should leave both off.
  [peer-flags] [checkpoint-upload] [snapshot-cache-add]
- **Verified in code.** `defer-rootfs-export` defaults false. Ordinary pause can stop/eject the
  rootfs cache and seal it in the background. In-place checkpoint can swap caches and fold
  the sealed cache back later. Non-NBD providers and resume-fresh checkpoints keep synchronous
  export. Upload gates both rootfs body and header on a successful seal.
  This changes critical-path latency, not exported logical ranges or durable dependencies.
  [rootfs-defer-flag] [pause-rpc] [checkpoint-export] [upload-v3]
- **Verified in code.** `free-page-reporting` defaults false.
  `free-page-hinting-config` defaults null. Its keys include `enabled`, `pause`, `build`,
  `stop`, `stop_grace`, and optional `periodic`. Absent pause/build budgets are zero.
  Enabled drains default stop handling to 2000 ms and 100 ms grace. An absent periodic block
  disables the loop. Enabling a build-time balloon capability does not install it into a
  preexisting snapshot. Keep this separate from reclaim until guest/FC eligibility is known.
  [balloon-flags] [hint-config] [balloon-build] [hint-drain]
- **Verified in code.** `freeze-user-cgroup` defaults to `env.IsDevelopment()`.
  Its timeout defaults to 2000 ms. `freeze-guest-hierarchy` defaults false and
  `freeze-guest-hierarchy-max-cgroups` is 512. These control workload quiescence, not storage
  compaction. `nbd-async-write-zeroes`, default false, changes TRIM/WRITE_ZEROES dispatch
  scheduling rather than zero semantics. It can matter when reclaim exposes NBD contention.
  [freeze-flags] [freeze-call] [freeze-budget] [nbd-async] [ancillary-flags] [nbd-trim]
- **Verified in code.** `build-cache-max-usage-percentage`, default 85, triggers diff-cache
  eviction. Pinned or unsealed entries are protected from that pressure path.
  Cache TTL is 25 hours and initialization clears stale build-cache content.
  These are cache bounds, not caps on durable local-template storage.
  `max-concurrent-snapshot-upserts`, `max-concurrent-sandbox-list-queries`, and
  `max-concurrent-snapshot-build-queries` default zero, meaning no database-query throttling.
  `build-base-rootfs-size-limit-mb`, default 25000, limits initial OCI filesystem creation.
  `build-reserved-disk-space-mb`, default 256, configures guest reserved blocks.
  None bounds accumulated pause directories. [cache-default] [cache-pressure] [cache-pins]
  [cache-ttl] [storage-limits] [upsert-throttle] [rootfs-size] [reserved-disk]
- **Verified in code.** `build-envd-memory-protection`, default false, bakes envd cgroup
  protection into new template rootfs layers. It can change the base-layer cache key.
  `resume-prefetch-source`, default `init`, and `resume-last-cycle-prefetch-max-mib`,
  default -1, control fetched working sets. `pause-resume-prefetch-harvest` and
  `pause-resume-prefetch-consume` default false. Consume writes prefetch metadata, not merged
  memfile/rootfs bodies. These affect baseline footprint or resume behavior, not chain bounds.
  [ancillary-flags] [build-protection] [prefetch-call] [prefetch-harvest]

## 6. Keyless overrides, context, and rebuilding

- **Verified in code.** With no `LAUNCH_DARKLY_API_KEY`, `NewClient` uses a package-global
  `ldtestdata` source. Constructors register every coded fallback there. The no-key client
  is static. This confirms the keyless-default statement in the earlier research.
  [flag-client] [flag-constructors] [hib-prior]
- **Verified in code.** The three M9 definitions do not read environment variables or files.
  The shared featureflag client and definitions have no generic production JSON-file or
  environment override loader. That negative finding covers the production files in
  `packages/shared/pkg/featureflags`, not arbitrary future callers.
  `OverrideBoolFlag`, `OverrideJSONFlag`, and `NewClientWithDatasource` are available Go
  mechanisms. They need a caller or test fixture and are not daemon command-line options.
  Recreating a same-key `NewJSONFlag` also updates the offline source.
  [dedup-default] [reclaim-default] [collapse-default] [flag-client] [flag-constructors]
- **Verified in code.** `create-build` and `resume-build` expose `-memfile-diff-dedup`.
  The resume tool overrides the entire JSON with `{"enabled":true}`, so omitted budgets remain
  zero. It also exposes `-reclaim` and `-collapse-envd-heap` and sets the offline source before
  creating its clients. Those options affect that standalone process only.
  They do not reconfigure the running E2B API or orchestrator. [tool-create]
  [tool-resume] [tool-overrides]
- **Verified in code.** Dedup is evaluated at memory export. Reclaim/collapse are evaluated
  before FC pause. Reclaim adds sandbox, team, and template contexts. Sandbox attributes
  include template ID, kernel version, FC version, envd version, and sandbox type.
  The server's pause/checkpoint contexts include sandbox and team. Upload adds
  `compress-use-case` and `compress-file-type` contexts.
  The offline source serves the same registered value to all of them.
  [dedup-pause] [reclaim-call] [sandbox-context] [server-context] [compression-resolve]
- **Verified in code.** These three controls are read by the orchestrator.
  Envd has no LaunchDarkly evaluation and receives HTTP/RPC operations instead.
  API reads lifecycle, snapshot database-throttle, and build-version flags, not the
  memfile comparison loop. Patching shared defaults requires rebuilding/restarting the
  consuming orchestrator. Hibernaut's installer already builds envd, API, orchestrator,
  and client-proxy after checkout. Applying the proposed default patch before those builds
  covers shared consumers on the next install. Existing guest binaries need a new image or
  a separately verified upgrade if too old for the selected endpoint.
  [dedup-pause] [reclaim-call] [freeze-flags] [upsert-throttle] [envd-selection]
  [hib-installer]
- **Inferred.** The smallest durable integration is a checked default-value patch in
  `vm/install-e2b.sh`, after its pinned checkout and before its builds.
  The exact candidate appears in section 10. No no-key runtime configuration service is
  needed. Rebuild only orchestrator for an isolated manual experiment if guest envd already
  supports the operations. Retain the normal installer rebuild set for reproducibility.
  [hib-installer] [flag-constructors] [envd-versions]
- **Verified in code.** The exact default-edit locations are all in
  `packages/shared/pkg/featureflags/flags.go`. Dedup's definition is line 253.
  Change `enabled` at line 254 from `false` to `true` for the first experiment.
  For the promoted candidate, lines 257, 258, 259, and 261 become 8, 64, 16, and 512.
  Keep `bestEffort`, `directIO`, and `blockFaultPct` unchanged.
  At line 677 replace reclaim's `ldvalue.Null()` with
  `ldvalue.FromJSONMarshal(map[string]int{"sync":500,"drop_caches":200,`
  `"compact_memory":1000,"fstrim":500})`. That is one expression, split here for wrapping.
  At line 333 change collapse's `false` to `true`. Leave its 10000 ms timeout at line 341.
  Section 10 gives the installer insertion that makes these edits reproducible.
  [dedup-default] [reclaim-default] [collapse-default]

## 7. Native full snapshots, tools, and a feasible offline merge

### 7.1. What already exists

- **Verified in code.** `header/compact.go` is an in-memory representation compactor.
  It packs mapping columns and deduplicates UUIDs into a lookup table. `NewHeader` calls
  `NewMapping`, so the lead that it has no callers is incorrect at this pin.
  It does not copy any body bytes, replace source UUIDs, or produce a self-contained build.
  `MergeMappings` overlays mapping intervals. It is called in header composition and
  `show-build-diff`, but likewise never materializes ancestor body data.
  [compact-map] [header-new] [mapping] [header-compose] [tool-show]
- **Verified in code.** `copy-build` enumerates every source UUID named by each header, then
  copies their bodies plus the selected headers, metadata, and snapfile.
  It preserves dependencies and source UUIDs. It can make a copied collection available
  elsewhere, but not make one independent build.
  `create-build -from-build` constructs an incremental template build.
  Its layer executor calls the same `Sandbox.Pause`.
  `resume-build` also calls that pause path and uploads its result.
  None is an offline memory-and-rootfs materializer. [tool-copy] [tool-create]
  [build-layer-pause] [tool-resume-pause]
- **Verified in code.** An initial OCI base layer is a different case. The base builder creates
  a full local rootfs and empty RAM under its new base UUID. It boots a fresh sandbox.
  Its direct rootfs provider scans the entire disk and classifies every block as nonzero
  or empty, rather than exporting only an NBD dirty bitmap. The base layer pauses with that
  same UUID. Resident RAM becomes new data and the rest becomes nil ranges.
  This can produce a self-contained initial base layer. Later build/finalize layers can
  still depend on it. This path constructs new execution state. It does not consolidate
  a paused machine while preserving its processes. `create-build` without `-from-build`
  takes the OCI branch, not an import of the paused machine.
  [base-files] [base-build] [base-direct-select] [direct-export] [layer-build-id]
  [noop-memory] [tool-create-oci]
- **Verified in code.** Save-as-template invokes node `Checkpoint`, which uses the same
  snapshot and memory-diff pipeline. Its “full memory snapshot” comments mean memory is
  included rather than filesystem-only. They do not mean all RAM pages are copied into the
  new body. Even `SnapshotTypeFull` in the FC request supplies only `SnapshotPath` here.
  The RAM body is separately created from the dirty/resident bitmap and composed over
  the loaded memory header. The M6 wording must not be read as an ancestor-flattening promise.
  [save-checkpoint] [checkpoint-export] [checkpoint-fresh] [snapshot-cache-add]
  [fc-snapshot] [memory-header-future] [hib-prior]
- **Verified in code.** A production search of all non-test Go files under
  `packages/orchestrator` found no full-snapshot/flatten/squash/chain-compaction implementation
  beyond unrelated OCI squashing and flattened mapping descriptions. The only
  `plannerFlatten` found is in the synthetic dedup test. Searches of command mains found
  mapping merge inspection, not a body merger. This is a scoped source finding, not a claim
  about private services or future upstream releases.
  [sim-flatten] [tool-show] [tool-copy] [memory-header-future] [rootfs-header]
- **Inferred.** The available API sequence is resume the paused machine, save its snapshot
  template, and pause it again. It creates new layered builds and assignments, can restart FC,
  and spends resume/export/upload work. It does not solve M9 compaction. There is no verified
  API-only sequence at this pin that makes both artifact headers parent-independent while
  preserving the machine's execution state. [save-checkpoint] [checkpoint-export]
  [header-compose] [rootfs-header]

### 7.2. Materializing a paused build

- **Inferred.** A pinned helper can materialize the latest paused build without booting it.
  Hold a machine lifecycle/cleanup lock, wait for the latest build to become ready, and retain
  all its source files during the operation. Read the two headers with E2B's deserializer.
  For each nonnil mapping, read exactly `Length` bytes at its source `BuildStorageOffset`
  from the named body. Write them at the same logical `Offset` in a fresh sparse device file.
  Nil mappings stay zero. Do not copy the packed body as though it were a complete filesystem.
  Validate coverage, alignment, body bounds, and a full logical-byte checksum.
  [mapping] [mapped-offset] [mapped-read] [header-wire] [hib-closure]
- **Inferred.** Write a fresh UUID and complete files before publishing any pointer.
  Use V3 initially. Preserve each device's logical size and block size.
  Reset header generation for the new base and set both `BuildId` and `BaseBuildId` to
  the fresh UUID. Every nonnil range must name that UUID with its new storage offset.
  Keeping the old `BaseBuildId` would cause Hib to retain it even with no old mapped bytes.
  A sparse identity-addressed body is simplest. A packed nonzero body is also valid if the
  header records its packing exactly. Reject unsupported format/codec combinations.
  [header-generation] [header-new] [mapping] [hib-parser]
- **Verified in code.** V3 needs no per-build frame table. V4/V5 also carry per-build sizes,
  SHA-256 checksums, and optional frame tables. Compressed source offsets are uncompressed
  offsets and must be resolved through those tables. `Paths.DataFile` selects the compressed
  body suffix. V5 changes the mapping encoding, not its logical source semantics.
  Reusing the pinned reader avoids a second handwritten compressed-artifact implementation.
  [header-new] [header-v4] [header-v5] [paths] [mapped-read]
- **Inferred.** Copy the latest `snapfile` unchanged because the VM state and logical device
  contents are unchanged. Copy all `metadata.json` fields, changing only
  `template.build_id` to the new UUID. Preserve metadata version, context, kernel/FC versions,
  startup/provenance, kernel arguments, balloon mode, filesystem-only/quiesced markers, and
  prefetch mapping. Prefetch stores logical block indices, so identity-preserving
  materialization should not need an offset rewrite. Do not recreate metadata with a helper
  that silently drops fields. The copied snapfile's compatibility needs an actual cold-cache
  resume test before this design is considered implemented. [artifact-metadata]
  [metadata-copy] [prefetch-metadata] [fc-snapshot]
- **Inferred.** Stream rather than mmap all memory and disk simultaneously.
  Full materialization costs reads of the live mapped nonzero data and writes of the new
  image, plus headers/metadata/snapfile. Its upper logical size is guest RAM plus guest disk,
  before sparse-zero savings. Reserve room for old and new builds at once and allow rollback.
  For the existing 2 GiB dev template, memory alone can require roughly 2 GiB. Measure the
  actual rootfs header size rather than treating `minFreeDiskMB = 10240` as proof of an exact
  10 GiB logical filesystem. Other live images and forks can legitimately keep old sources.
  [hib-image] [header-generation] [mapping] [hib-roots]

### 7.3. Database publication and cache safety

- **Verified in code.** `snapshots` has an env ID and sandbox ID, not a direct current-build
  column. `GetLastSnapshot` selects the newest `default` assignment in the snapshot's env
  whose build has `status_group = ready`. The assignment schema has UUID ID, env ID, build ID,
  tag, source defaulting to `app`, and creation time. It supports multiple assignments.
  `env_builds.env_id` is a nullable legacy first-env field, not the pointer to update.
  [snapshot-schema] [last-snapshot] [assignment-schema] [assignment-legacy]
- **Verified in code.** Current build rows hold vCPU, RAM, disk/free-disk, kernel/FC/envd versions,
  origin node, status/reason/timestamps, CPU architecture/family/model/name/flags, and team.
  Snapshot rows also preserve sandbox metadata, start time, secure/internet/autopause
  settings, team, origin node, and paused config. Upsert clones CPU identity from the source.
  Status `uploaded`, `ready`, or `success` computes `status_group = ready`.
  Assignment insertion backfills legacy env and missing team. [db-models] [snapshot-upsert]
  [status-group] [assignment-legacy] [assignment-team]
- **Inferred.** A publication transaction should lock the snapshot row, verify that its
  previously selected ready tip is still the expected UUID, insert the new `env_builds` row
  by copying the old resource/version/CPU/team/node fields, and append a later `default`
  assignment in the same env. Publish terminal-ready status only after files are durable.
  Preserve the existing `snapshots` row and its policies. Do not change unrelated image
  assignments, old shared builds, or the sandbox ID. Old assignments can remain for rollback.
  Newly published creation time must sort strictly after the previous tip.
  [last-snapshot] [snapshot-upsert] [db-models] [assignment-schema] [status-group]
- **Verified in code.** API snapshot caching uses Redis prefix `snapshot:last`, five-minute TTL,
  and one-minute refresh. `RedisKey` makes the full key `snapshot:last:<sandboxID>`.
  Normal checkpoint invalidates it. `TryDelete` attempts locking and reports delete failure,
  but its lock-error fallback can race a stale writer. Node template caches use build UUIDs.
  `Invalidate` retires a pinned instance rather than destroying a running user's files.
  [snapshot-cache] [redis-key] [redis-delete] [save-cache] [template-invalidate]
- **Inferred.** A SQL commit and raw Redis `DEL` are not by themselves a safe publication
  protocol. Serialize resume/checkpoint/cleanup with publication and use an API-side publish
  seam that invalidates under the same cache/lifecycle discipline. Confirm the next normal
  resume selected the fresh UUID before releasing rollback retention.
  Fresh UUIDs avoid serving an old node-cache instance under overwritten bytes.
  Do not mutate the old UUID's files in place. Existing live forks, golden images, node pins,
  recent-write grace, and Hib cache roots can still delay reclamation.
  [last-snapshot] [snapshot-cache] [redis-delete] [template-invalidate] [hib-roots]
- **Unverified.** The pinned source does not prove that an external Hib publisher can acquire
  every API lifecycle/cache lock without a small E2B integration. This is the largest design
  seam, alongside persistent operation journaling and crash recovery. A merge must fail
  without replacing the old ready tip if reads, checksums, file durability, or publication
  fail. Do not infer transactional safety across PostgreSQL, Redis, and files.
  [save-checkpoint] [snapshot-cache] [redis-delete] [last-snapshot]

### 7.4. Reuse and licensing

- **Verified in code.** The pinned root license is Apache-2.0. It grants source/object reuse
  and derivative work rights subject to its terms. Redistribution requires the license,
  preserved applicable notices, prominent modification notices, and applicable NOTICE
  attribution. Patent and trademark conditions still apply. This is not legal advice.
  Check the dependencies' licenses too. [license-grant] [license-conditions]
- **Inferred.** Importing the exported `packages/shared/pkg/storage/header` reader into a small
  pinned helper is preferable to vendoring another header parser. Its module is
  `github.com/e2b-dev/infra/packages/shared`, using Go 1.26.8. The logical read path can be
  reused or implemented as a narrowly scoped streaming copier over decoded mappings.
  Treat this as a pinned compatibility tool, not a stable upstream compaction API.
  [shared-module] [header-wire] [mapped-read] [license-grant]

## 8. Compatibility with `cleanup.ParseHeader`

- **Verified in code.** Hib accepts formats 1, 2, and 3 after masking the version word's low
  16 bits. It expects 64 bytes of metadata followed by zero or more 40-byte mappings.
  It reads own UUID at bytes 32–47, base UUID at 48–63, and each mapping UUID at mapping
  bytes 16–31. It drops the nil UUID. Host cleanup reads both header sidecars and fails closed
  if it cannot determine a retained build's dependencies. [hib-parser] [hib-host]
  [hib-closure]
- **Verified in code.** E2B V1/V2/V3 serialization shares the same encoding. The metadata is
  four little-endian uint64 fields followed by two 16-byte UUIDs. Each mapping is offset
  uint64, length uint64, UUID, then source offset uint64. There is no native struct-padding
  field on the wire and no mapping-count prefix. Hib's byte offsets and modulo check match.
  A 4 KiB dedup range still uses a 40-byte mapping. Header `BlockSize` remaining 2 MiB does
  not change that record layout. [header-generation] [header-v3] [header-wire]
  [memfd-dedup-result] [header-compose]
- **Verified in code.** V4 keeps the 64-byte metadata, then adds a one-byte flags field,
  four-byte uncompressed-size prefix, and an LZ4-compressed block. Its inner block holds
  build information/frame tables and counted fixed-size mappings. V5 keeps that framing
  and build section but uses a UUID table and columnar varint mapping offsets, lengths,
  source-offset deltas, and UUID-table indices in 4 KiB units. Neither is a sequence of
  raw 40-byte records immediately after byte 64. [header-v4] [header-v5]
- **Verified in code.** Hib rejects V4/V5 before attempting its mapping scan. It does not
  silently reinterpret their compressed bytes. The proposed dedup/reclaim/collapse defaults
  do not select either format. With V1–V3 parents, compression disabled in both environment
  and flags, and V4/V5 writers off, no cleanup change is required for those three controls.
  With inherited V4/V5 or enabled compression/new-header flags, cleanup must gain version-aware
  decoding first. Merely accepting version numbers 4 and 5 is unsafe.
  [hib-parser] [dedup-pause] [upload-version] [compression-resolve] [upload-finalize]
- **Inferred.** Extend cleanup through the pinned deserializer or an equally validated
  dependency extractor. Test V1/V2/V3 page-granular records, nil ranges, preserved base UUID,
  V4 uncompressed bodies, V4 compressed ancestors, V5 varints and sparse frame tables,
  truncation, oversized LZ4 input, unsupported versions, and incomplete headers.
  Keep fail-closed behavior. Collect UUIDs from logical mappings plus own/base IDs.
  Do not treat a codec table or a stale build-info entry as proof of a live mapped byte.
  [header-wire] [header-v4] [header-v5] [header-store] [hib-parser]

## 9. Current upstream comparison

- **Verified in code.** The requested `main` tarball was downloaded and inspected on
  2026-10-05 within the 15-minute comparison budget. Its PAX `comment` identifies
  `25b19611d85e710d6d7aabb495610b701772a0fa`. GitHub's `commits/main` response matched.
  The commit timestamp is 2026-10-05 09:22:39 UTC. The old `infra` repository URLs redirect
  to `e2b-dev/runtime`, and the archive root is `runtime-main`.
  The downloaded archive and its extraction were deleted after this inspection.
  [up-commit] [up-api]
- **Verified in code.** The three M9 defaults did not change. Dedup remains disabled with
  the same eight keys and zero budgets. Reclaim remains `null`. Collapse remains false
  with a 10000 ms timeout. Memfd and background copy remain true. V4/V5 writing,
  superseded-cache release, ancestor fallback, and deferred rootfs export remain false.
  Compression remains disabled. These are coded keyless defaults, not live LaunchDarkly
  rollout values. [up-memory-flags] [up-dedup] [up-collapse] [up-reclaim] [up-header-flags]
  [up-cache-flags] [up-rootfs-defer] [up-compression]
- **Verified in code.** `compact.go`, `mapping.go`, `metadata.go`, all three V3/V4/V5
  serializers, and `block/dedup.go` are byte-identical to the pin.
  Production memory and rootfs exports still call `ToDiffHeader` over the parent.
  The whole-memfile `plannerFlatten` remains confined to the dedup simulation test.
  The inspected production orchestrator, header, flag, and command sources contain no
  newly introduced body-materializing chain compactor or parent-independent full-pause mode.
  `copy-build` is also byte-identical. [up-compact] [up-compose] [up-memory-header]
  [up-rootfs-header] [up-sim-flatten] [up-copy] [tool-copy] [dedup-compare]
- **Verified in code.** Upstream adds `filesystem-only-checkpoint`, default false.
  It checkpoints disk while the source process resumes in place. A sandbox created from
  that artifact cold-boots. It is not execution-state-preserving full compaction.
  `in-place-checkpoint-reporting`, also false, changes eligibility for deferred in-place
  memory checkpoints. Resume-fresh Checkpoint still uses the layered memory pipeline.
  Reclaim now skips envd heap collapse when no memory is exported.
  [up-checkpoint-flags] [up-checkpoint-route] [up-checkpoint-fresh] [up-reclaim-call]
- **Verified in code.** Upstream adds the `PAUSE_REFUSAL_RESTORE` environment fallback for
  `pause-refusal-restore`. Its absence still defaults false. This is lifecycle recovery,
  not a generic keyless override for the three M9 flags and not chain compaction.
  [up-refusal]
- **Inferred.** Updating the pin alone has no demonstrated bounded-storage benefit.
  The defaults and core mapping/dedup algorithms are unchanged. New checkpoint choices
  concern execution semantics and latency, not consolidation of both stored devices.
  Host performance and private LaunchDarkly rollout state were not inspected.
  [up-dedup] [up-compose] [up-checkpoint-flags] [up-memory-header] [up-rootfs-header]

## 10. What this means for M9

- **Inferred.** Test stages separately on the same workload and each available RAM size.
  First enable dedup with all promotion budgets zero. Then enable its conservative budgets.
  Then add reclaim. Finally add heap collapse with its unchanged 10000 ms timeout.
  Keep compression, V4/V5 writing, in-place checkpoint, deferred memory/rootfs export,
  inflight dedup serving, and balloon changes off for that comparison. This separates byte
  savings from page promotion and guest-side work. [dedup-default] [dedup-budget]
  [reclaim-script] [collapse-default] [upload-version]
- **Inferred.** The conservative promoted-dedup candidate is the following JSON.
  Eight windows and 64 copied parent pages per dirty block match an existing simulation
  scenario. Sixteen pages permits at most a 64 KiB price per strictly attributable parent
  frame in the global pass. `blockFaultPct = 0` avoids assuming a remote-fetch probability
  on a single local host. `fetchRunWindowPages = 512` makes the 2 MiB window assumption
  explicit. Use full comparison first and buffered output to avoid new I/O requirements.
  This is a measurement candidate, not a bound on chain length. [sim-scenarios]
  [sim-budgets] [dedup-global] [dedup-window] [compression-default]

```json
{
  "enabled": true,
  "bestEffort": false,
  "directIO": false,
  "maxFetchWindowsPerBlock": 8,
  "maxPromotedParentPagesPerBlock": 64,
  "maxPagesPerPromotedFrame": 16,
  "blockFaultPct": 0,
  "fetchRunWindowPages": 512
}
```

- **Inferred.** Proposed installer patch, **DO NOT APPLY during this research**.
  Insert after `cd "$E2B_INFRA_DIR"` and before the prebuilt-artifact/build steps.
  It changes only the checked defaults. It accepts the same patch on a repeated install and
  fails on unexpected source rather than making an unbounded replacement.
  Run the isolated stages above before adopting this combined candidate.
  [hib-installer] [dedup-default] [reclaim-default] [collapse-default] [tool-reclaim]

```diff
@@ -76,3 +76,44 @@
 cd "$E2B_INFRA_DIR"
-
+
+log "M9 snapshot defaults"
+python3 - <<'PY'
+from pathlib import Path
+import re
+
+p = Path("packages/shared/pkg/featureflags/flags.go")
+s = p.read_text()
+a = s.index('\tMemfileDiffDedupFlag = NewJSONFlag(')
+b = s.index("\n\t}))", a) + len("\n\t}))")
+block = s[a:b]
+values = {
+    "enabled": ("false", "true"),
+    "maxFetchWindowsPerBlock": ("0", "8"),
+    "maxPromotedParentPagesPerBlock": ("0", "64"),
+    "maxPagesPerPromotedFrame": ("0", "16"),
+    "fetchRunWindowPages": ("0", "512"),
+}
+for key, (old, new) in values.items():
+    pattern = rf'("{key}":\s+)({old}|{new})(?=,)'
+    block, count = re.subn(pattern, lambda m: m[1] + new, block)
+    assert count == 1, f"unexpected default for {key}"
+s = s[:a] + block + s[b:]
+
+old = 'NewJSONFlag("guest-pause-reclaim", ldvalue.Null())'
+new = (
+    'NewJSONFlag("guest-pause-reclaim", '
+    'ldvalue.FromJSONMarshal(map[string]int{'
+    '"sync": 500, "drop_caches": 200, '
+    '"compact_memory": 1000, "fstrim": 500}))'
+)
+for old, new in [
+    (old, new),
+    ('NewBoolFlag("collapse-envd-heap", false)',
+     'NewBoolFlag("collapse-envd-heap", true)'),
+]:
+    assert s.count(old) == 1 or s.count(new) == 1, old
+    if old in s:
+        s = s.replace(old, new, 1)
+p.write_text(s)
+PY
+
 log "Prebuilt Firecracker binaries, guest kernels and busybox"
```

- **Inferred.** Memory output should become 4 KiB times the number of unique/promoted pages,
  instead of exporting each nonempty dirty 2 MiB frame wholesale. Rootfs remains 4 KiB
  times nonzero dirty blocks. Where the reclaim sync completes, the intentional 64 MiB
  random overwrite remains real disk churn plus filesystem metadata. It cannot be deduped
  against its previous random contents. There is no verified prediction of a new fixed
  MiB-per-pause number. Latest dependency count can still grow despite smaller bodies.
  [dedup-compare] [memfd-dedup-result] [disk-export] [hib-cycles]
- **Verified in code.** Current `TestPauseCycles` checks that the tmux session survives and logs
  resume time, all snapshot directory usage, and directly named latest-build dependencies.
  It does not currently assert a plateau, report pause latency, separate artifact sizes,
  count per-artifact UUIDs, or report compare/upload completion. `latestNeeds` intersects
  header UUIDs with the machine's snapshot builds. It excludes a base image outside that list.
  `du` measures allocated bytes, not body logical length. [hib-cycles] [hib-cycle-size]
- **Inferred.** Extend the eventual experiment metrics with pause duration, time to ready/durable
  upload, separate memory/rootfs allocated and logical bytes, header bytes/mapping counts,
  memory-only/disk-only/union source UUID counts, and the cleanup-retained transitive closure.
  Record dedup zero/equal/unique/promoted pages, compare/drain time, reclaim failures,
  collapse migration/no-op counts, host memory pressure, and warm versus cold-cache resume.
  Repeat beyond 20 cycles if the final cycles still slope upward. Test overwritten random
  data separately from a genuinely idle workload. Choose the plateau assertion from measured
  late-cycle behavior, not from a guessed 280 MiB reduction. [dedup-metrics] [collapse-call]
  [reclaim-call] [hib-cycles] [hib-cycle-size] [hib-closure]
- **Inferred.** If either artifact keeps acquiring indispensable old sources, merge is still
  needed for the M9 goal. A new-UUID streaming merge of both devices is feasible from the
  mapped format, but it is not a ready-made E2B command. Publication/cache locking,
  durability/rollback, shared-image/fork roots, disk headroom, and cold-resume verification
  are required implementation work. Adopt a measured byte/source threshold and periodic
  full materialization rather than presenting dedup promotion as guaranteed compaction.
  [mapping] [last-snapshot] [snapshot-cache] [template-invalidate] [hib-roots]

## 11. Open questions for the host

- **Unverified.** Are the installed parents actually V3, with compression environment values
  disabled and no older V4/V5 lineage? Which envd and FC versions are baked into each size's
  current image and paused machines? [header-wire] [envd-versions] [compression-default]
- **Unverified.** How much of the historical 280 MiB is memory, random-file data, filesystem
  metadata, header size, or retained cache allocation? Does a truly idle workload plateau
  under each staged setting for 1, 2, and 4 GiB guests? [hib-m9] [hib-cycles] [dedup-metrics]
- **Unverified.** Does guest `fstrim` complete and produce actual NBD TRIM/zero ranges?
  Are the 500/500/200/1000 ms caps sufficient? Does collapse migrate pages rather than mostly
  report already-huge windows, and does it improve cold resume? [reclaim-script] [nbd-trim]
  [collapse-call]
- **Unverified.** Does background dedup finish before the next resume, and what are its
  compare/drain/upload tail latency and peak memory costs under simultaneous pauses?
  Do memory savings trade off against higher source-count or cold-read fragmentation?
  [memfd-dedup-start] [dedup-metrics] [dedup-window]
- **Unverified.** Can a materialized V3 build resume with copied snapfile and preserved metadata,
  maintain process/file contents, survive restart/crash injection, and release old sources
  after cache pins and cleanup grace without harming golden images or forks?
  How much simultaneous old/new disk headroom is available? [artifact-metadata]
  [last-snapshot] [template-invalidate] [hib-roots]

**References**

E2B references use the pin unless prefixed `up-`. Hibernaut uses checked worktree lines.

[flag-client]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/client.go#L21-L112
[flag-constructors]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L54-L154
[pause-prepare]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L1972-L2075
[pause-stop]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L2188-L2252
[paths]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/paths.go#L9-L78
[upload-v3]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/build_upload_v3.go#L17-L113
[storage-local]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/storage_fs.go#L153-L172
[uffd-dirty]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/uffd/uffd.go#L303-L373
[uffd-empty]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/uffd/uffd.go#L377-L386
[uffd-remove]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/uffd/userfaultfd/userfaultfd.go#L464-L483
[memory-geometry]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L974-L981
[fc-dirty]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/fc/client.go#L638-L669
[noop-memory]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/uffd/noop.go#L38-L57
[block-sizes]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/diff.go#L9-L36
[memfd-dedup-result]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/memfd.go#L480-L541
[disk-cache]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/cache.go#L571-L616
[disk-export]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/cache.go#L180-L255
[disk-zero]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/cache.go#L619-L645
[nbd-export]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/rootfs/nbd.go#L123-L161
[mapping]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/mapping.go#L12-L177
[header-compose]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/metadata.go#L134-L214
[header-generation]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/metadata.go#L20-L65
[mapped-offset]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/header.go#L150-L171
[mapped-read]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/build/build.go#L285-L333
[upload-version]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/build_upload.go#L112-L138
[compression-resolve]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/build_upload.go#L274-L324
[upload-finalize]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/build_upload_v3.go#L170-L179
[header-wire]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/serialization.go#L14-L64
[compact-map]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/compact.go#L12-L133
[header-cap]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/serialization_v4.go#L17-L36
[header-store]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/serialization.go#L151-L190
[header-v5]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/serialization_v5.go#L13-L115
[scheduler-limit]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/scheduling/metadata.go#L13-L99
[dedup-default]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L247-L288
[dedup-compare]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/dedup.go#L79-L185
[dedup-fetch-key]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/dedup.go#L188-L217
[compression-default]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/compress_config.go#L3-L63
[dedup-pause]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L2609-L2658
[dedup-budget]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/dedup.go#L40-L57
[dedup-block]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/dedup.go#L225-L291
[dedup-window]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/dedup.go#L370-L426
[dedup-global]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/dedup.go#L337-L354
[sim-flatten]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/dedup_sim_test.go#L209-L224
[rootfs-header]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L3360-L3394
[memfd-dedup-start]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/memfd.go#L373-L407
[fc-export]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/fc/memory.go#L68-L119
[fc-export-fallback]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/fc/memory.go#L133-L173
[memory-header-future]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L3105-L3203
[dedup-direct]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/cache.go#L260-L290
[dedup-metrics]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/dedup.go#L466-L487
[sim-budgets]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/dedup_sim_test.go#L43-L79
[sim-scenarios]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/block/dedup_sim_test.go#L375-L387
[reclaim-default]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L674-L677
[reclaim-config]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L850-L868
[reclaim-script]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/reclaim.go#L59-L90
[reclaim-call]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/reclaim.go#L92-L144
[system-shell]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/envd_process.go#L33-L69
[process-start]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/internal/services/process/start.go#L28-L69
[envd-versions]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/utils/version.go#L9-L36
[envd-version]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/pkg/version.go#L1-L3
[envd-selection]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L935-L996
[balloon-install]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/fc/client.go#L455-L473
[balloon-build]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/template/server/create_template.go#L109-L127
[reporting-default]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L306-L309
[balloon-flags]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L674-L746
[hint-drain]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/fph.go#L48-L80
[nbd-flags]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/nbd/path_direct.go#L248-L257
[nbd-trim]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/nbd/dispatch.go#L358-L374
[tool-reclaim]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/cmd/resume-build/main.go#L105-L119
[pause-filesystem]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L2075-L2110
[collapse-default]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L330-L341
[collapse-call]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/reclaim.go#L414-L485
[collapse-http]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/envd.go#L212-L233
[collapse-linux]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/internal/services/memory/collapse_linux.go#L26-L171
[collapse-api]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/envd/internal/api/collapse.go#L12-L58
[snapshot-kind]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/handlers/sandbox_create.go#L195-L241
[fc-snapshot]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/fc/client.go#L142-L159
[memory-flags]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L194-L245
[upload-provisional]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/build_upload.go#L64-L112
[memory-preflight]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L2456-L2544
[memory-union]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L2545-L2607
[checkpoint-gate]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/sandboxes.go#L1077-L1164
[compression-flags]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L1625-L1637
[header-flags]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L396-L403
[header-v4]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/serialization_v4.go#L38-L109
[cache-flags]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L405-L426
[cache-release]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/template/cache_layers.go#L233-L269
[cache-choice]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/template/cache.go#L1201-L1224
[cache-choice-default]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L157-L162
[peer-flags]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L290-L294
[checkpoint-upload]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/sandboxes.go#L1182-L1207
[snapshot-cache-add]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/sandboxes.go#L1597-L1630
[rootfs-defer-flag]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L296-L304
[pause-rpc]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/sandboxes.go#L996-L1016
[checkpoint-export]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/sandboxes.go#L1273-L1313
[checkpoint-fresh]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/sandboxes.go#L1356-L1386
[hint-config]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L717-L829
[freeze-flags]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L306-L360
[freeze-call]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/reclaim.go#L386-L398
[freeze-budget]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/reclaim.go#L489-L545
[nbd-async]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/nbd/path_direct.go#L195-L203
[storage-limits]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L625-L671
[cache-default]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L518-L524
[cache-pressure]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/build/cache.go#L248-L286
[cache-pins]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/build/cache.go#L318-L336
[cache-ttl]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/template/cache.go#L35-L45
[upsert-throttle]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/pause_instance.go#L176-L183
[rootfs-size]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/template/build/core/rootfs/rootfs.go#L156-L161
[reserved-disk]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/template/build/layer/create_sandbox.go#L77-L80
[ancillary-flags]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/featureflags/flags.go#L527-L610
[build-protection]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/template/build/builder.go#L283-L295
[prefetch-call]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L1309-L1322
[prefetch-harvest]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/prefetch_harvest.go#L201-L212
[tool-create]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/cmd/create-build/main.go#L71-L89
[tool-resume]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/cmd/resume-build/main.go#L54-L103
[tool-overrides]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/cmd/resume-build/main.go#L105-L169
[sandbox-context]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L233-L241
[server-context]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/server/sandboxes.go#L1064-L1074
[tool-show]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/cmd/show-build-diff/main.go#L147-L174
[header-new]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/header.go#L16-L123
[tool-copy]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/cmd/copy-build/main.go#L355-L413
[build-layer-pause]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/template/build/layer/layer_executor.go#L274-L288
[tool-resume-pause]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/cmd/resume-build/main.go#L807-L880
[save-checkpoint]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/snapshot_template.go#L75-L98
[artifact-metadata]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/template/metadata/template_metadata.go#L114-L209
[metadata-copy]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/template/metadata/template_metadata.go#L279-L321
[prefetch-metadata]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/template/metadata/prefetch.go#L13-L43
[snapshot-schema]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/migrations/20241213142106_create_snapshots.sql#L4-L13
[last-snapshot]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/snapshots/get_last_snapshot.sql#L1-L21
[assignment-schema]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/migrations/20251218160000_allow_m_n_builds_with_tags.sql#L16-L46
[assignment-legacy]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/migrations/20260204172712_remove_build_assignment_triggers.sql#L4-L39
[db-models]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/models.go#L47-L90
[snapshot-upsert]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/queries/snapshots/create_new_snapshot.sql#L1-L106
[status-group]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/migrations/20260210120002_add_status_group_column.sql#L3-L21
[assignment-team]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/db/migrations/20260218120000_add_team_id_to_env_builds.sql#L4-L22
[snapshot-cache]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/cache/snapshots/snapshot_cache.go#L18-L100
[redis-key]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/cache/redis.go#L293-L295
[redis-delete]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/cache/redis.go#L184-L216
[save-cache]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/api/internal/orchestrator/snapshot_template.go#L153-L164
[template-invalidate]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/template/cache.go#L777-L794
[license-grant]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/LICENSE#L66-L87
[license-conditions]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/LICENSE#L89-L128
[shared-module]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/go.mod#L1-L3
[header-v3]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/shared/pkg/storage/header/serialization_v3.go#L11-L64
[hib-installer]: ../../vm/install-e2b.sh#L72-L96
[hib-m9]: ../milestones/m9-snapshot-compaction.md#L5-L42
[hib-cycles]: ../../e2e/cleanup_test.go#L64-L87
[hib-cycle-size]: ../../e2e/cleanup_test.go#L118-L163
[hib-parser]: ../../internal/cleanup/cleanup.go#L325-L365
[hib-host]: ../../internal/cleanup/host.go#L196-L214
[hib-closure]: ../../internal/cleanup/cleanup.go#L280-L323
[hib-roots]: ../../internal/cleanup/cleanup.go#L88-L117
[hib-image]: ../../images/images.go#L66-L71
[hib-prior]: e2b-images-and-fork.md#L183-L205
[base-files]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/template/build/phases/base/files.go#L24-L82
[base-build]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/template/build/phases/base/builder.go#L254-L296
[base-direct-select]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/sandbox.go#L888-L907
[direct-export]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/sandbox/rootfs/direct.go#L145-L180
[layer-build-id]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/pkg/template/build/layer/layer_executor.go#L139-L161
[tool-create-oci]: https://github.com/e2b-dev/infra/blob/5cfcf3282ddb95e90a8214ce71329641bdfe6cb6/packages/orchestrator/cmd/create-build/main.go#L416-L430
[up-commit]: https://github.com/e2b-dev/infra/commit/25b19611d85e710d6d7aabb495610b701772a0fa
[up-api]: https://api.github.com/repos/e2b-dev/infra/commits/25b19611d85e710d6d7aabb495610b701772a0fa
[up-memory-flags]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/shared/pkg/featureflags/flags.go#L194-L276
[up-dedup]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/shared/pkg/featureflags/flags.go#L278-L294
[up-collapse]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/shared/pkg/featureflags/flags.go#L361-L372
[up-reclaim]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/shared/pkg/featureflags/flags.go#L720-L723
[up-header-flags]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/shared/pkg/featureflags/flags.go#L427-L434
[up-cache-flags]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/shared/pkg/featureflags/flags.go#L436-L457
[up-rootfs-defer]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/shared/pkg/featureflags/flags.go#L327-L335
[up-compression]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/shared/pkg/featureflags/flags.go#L1732-L1744
[up-compact]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/shared/pkg/storage/header/compact.go#L12-L133
[up-compose]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/shared/pkg/storage/header/metadata.go#L134-L214
[up-memory-header]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/orchestrator/pkg/sandbox/sandbox.go#L3262-L3278
[up-rootfs-header]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/orchestrator/pkg/sandbox/sandbox.go#L3434-L3469
[up-sim-flatten]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/orchestrator/pkg/sandbox/block/dedup_sim_test.go#L209-L225
[up-copy]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/orchestrator/cmd/copy-build/main.go#L355-L413
[up-checkpoint-flags]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/shared/pkg/featureflags/flags.go#L219-L240
[up-checkpoint-route]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/orchestrator/pkg/server/sandboxes.go#L1258-L1288
[up-checkpoint-fresh]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/orchestrator/pkg/server/sandboxes.go#L1565-L1570
[up-reclaim-call]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/orchestrator/pkg/sandbox/reclaim.go#L92-L128
[up-refusal]: https://github.com/e2b-dev/infra/blob/25b19611d85e710d6d7aabb495610b701772a0fa/packages/shared/pkg/featureflags/flags.go#L548-L558
