# How cloud coding agents run, for Hibernaut

**Date checked:** 2026-10-18.

**Summary**

This note sits under `cloud-agents.md` (Martin's survey dated 2026-10-10) and covers the mechanics
underneath it: how a task gets a machine, where the agent loop runs, what survives idle and what a
self-hosted worker says to the vendor. Points the survey covers get a one-line recap cited as
`cloud-agents.md §B1` (etc.). Cursor gets the most depth. Implications for Hibernaut, near the
end, maps the host list onto Hibernaut as of M12.

- **The loop mostly runs off the machine.** Cursor, the Codex Agents API, Claude Managed Agents and
  Devin run it in the vendor's cloud and send tool calls to the machine. Cursor does this even on
  its own VMs, where the loop is a set of Temporal workflows (§A3). Claude Code on the web,
  Anthropic's self-hosted runner, Kiro and Warp run the agent inside the sandbox. Either way a task
  survives a closed laptop.
- **Fast starts come from a prepared disk.** Cursor, Codex, Claude, Devin and Jules run an install
  step once, save the filesystem and boot each task from a copy. Cursor also keeps pre-warmed
  copies (§A1). Cursor, Codex and Claude document these snapshots as disk only, so services start
  again on every boot (§A4, §B3, §C3).
- **Idle differs most.** Cursor hibernates idle VMs and keeps disk snapshots for 30 days (§A1).
  Devin snapshots memory at the hypervisor, sleeps after 30 minutes and stays resumable for 14 days
  (§E1). Claude pauses a VM with its files, but a reclaimed VM returns fresh (§C1). Codex keeps VM
  state for 7 days (§B1). Copilot destroys the runner after each session (§D1).
- **Self-hosted modes share one shape.** An always-awake dispatcher connects outbound, watches the
  vendor's queue and starts one machine per task (`cloud-agents.md §E`). Cursor and Devin use
  list-then-watch with an atomic claim (§A10, §E6). Claude and GitHub long-poll (§C7, §D4). Codex
  starts its executor from a webhook (§B6). E2B's guides warn that queue traffic "cannot wake a
  paused dispatcher" (§A10).
- **Resume is where they part.** Cursor wants a stopped worker back under the same ID with its disk
  (`claimed_offline`, §A10). Devin queues `resume` entries and asks for a snapshot before
  terminating (§E6). Claude's runners, Copilot and the Codex Agents API resume on fresh compute and
  lose unpushed work (§C7, §D1, §B6).
- **Credentials stay out where they can.** Cursor proxies git. Claude and Codex proxies inject
  secrets by host (§A9, §C6, §B5). Cursor and Devin mint OIDC tokens inside the machine (§A7, §E5).
  Self-hosted modes hand egress to the operator (§E6).
- **Limits expose the machinery.** Copilot sessions are Actions jobs capped at 59 minutes. Cursor's
  Lambda workers live at most 8 hours and Claude's spawned runners 480 minutes. Idle timers run
  from "a few minutes" (Claude) to 1 hour (Cursor's self-hosted default).

## Sources

Inline citations drop `https://`. Code citations read `owner/repo@sha7 path:lines`. Roots and full
SHAs:

- **Cursor.** cursor.com/docs (mostly `cloud-agent/`), six cursor.com/blog posts (2026-02-24 to
  2026-09-02), www.cursor.com/schemas/environment.schema.json and docs.e2b.dev/agents/cursor. Code:
  anysphere/aws-lambda-workers `6c36161b3ea5d65a2fd9abed54343bcd2b16e0e3`, anysphere/k8s-workers
  `7e12775f0feb923a699a9e834a9c7f209b1f44a1`.
- **OpenAI.** learn.chatgpt.com/docs (moved from developers.openai.com/codex) and
  developers.openai.com/api/docs/guides/agents-api. Code: openai/codex
  `8c076942cb70fba8e5c4732c23f1faabc4aed484`, openai/codex-universal
  `47f4f0eb5337083e2f610db0d15558932cb4901d`.
- **Anthropic.** code.claude.com/docs/en, platform.claude.com/docs/en/managed-agents and
  docs.e2b.dev/agents/claude-managed-agents. Code: anthropics/claude-code-action
  `f27be6b65a4652f08a2ba3cff79114829b588967`.
- **GitHub.** docs.github.com/en (`copilot`, `actions`, `rest`) and github.blog/changelog. Code:
  actions/runner `9abfe11d2abd9de09d3a8d933d7ed701ea0518a3`, actions/scaleset
  `e6daac702355cdb5b880b4fbdcf6d85dcd9e48e5`, actions/actions-runner-controller
  `5aed393af8a412f6b0fe27507713687a6beeb000`, actions/runner-images
  `06f4a9146f061c2776b044dab74b5390d9c000b9`.
- **Cognition.** docs.devin.ai, cognition.com/blog/what-we-learned-building-cloud-agents and
  docs.e2b.dev/agents/devin-outposts.
- **Google.** jules.google/docs and developers.google.com/jules/api.
- **Others.** ampcode.com/docs, docs.factory.com, kiro.dev/docs, docs.warp.dev, ona.com/docs/ona,
  docs.replit.com and replit.com/blog.

**Tags.** **Docs**: read on the vendor's docs, changelog or blog on the date above. **Code**: read
in source at the named commit. **Unverified**: only the named secondary source says so. News and
forums served only as leads. Quotes are verbatim. "(paraphrase)" marks spots where a quote would
break this note's prose rules. "(inference)" marks my own deductions. An untagged claim takes the
tag of its sentence's citation.

## Comparison table

One row per runtime. Codex and Claude each have two, because their hosted product and their
self-hosted API work differently. Sections A to G carry the citations.

| Product | Substrate | Agent loop runs in | Idle and follow-up | Environment snapshot | Self-hosted worker | Limits that show |
|---|---|---|---|---|---|---|
| **Cursor Cloud Agents** | Firecracker microVM, separate AWS account | Cursor's cloud (Temporal), tool calls sent to the VM | Hibernate, disk snapshot kept 30 days after last use, follow-up reconnects | Build: disk snapshot plus pre-warmed copies | Outbound worker, list, watch and claim API, wake under the same worker ID | Pool idle release 1 h by default, Lambda template 8 h |
| **Codex cloud** | VM per task (legacy flow: container) | Not stated | Saved VM state recoverable 7 days, follow-up continues with its files | Published filesystem plus a Start skill | None for cloud tasks | Max duration not published |
| **Codex Agents API** | OpenAI-hosted sandbox or your executor | OpenAI | Hosted sandbox deleted after 1 h without activity, replacement compute starts empty | Pre-warmed sandboxes, unattached ones expire in 5 min | `codex exec-server`, outbound relayed WebSocket, one executor per session | 5 min for an executor to connect |
| **Claude Code on the web** | Anthropic VM, Ubuntu 24.04, 4 vCPU, 16 GB | Claude Code CLI inside the VM | Pauses after a few minutes with files saved, reclaimed VM returns fresh with history only | Setup script output cached as a filesystem snapshot, rebuilt about weekly | Self-hosted runner, outbound long-poll, lease of about 60 s | Setup must finish in about 5 min to be cached |
| **Claude Managed Agents** | Anthropic containers or your worker | Anthropic | Queued session waits for a worker | Not applicable | `ant beta:worker poll` or a webhook-started poller | Worker idles out 60 s after `end_turn` by default |
| **Copilot cloud agent** | Actions runner on an Azure VM, 2 vCPU and 8 GB on the standard private-repo runner | Not documented | Runner destroyed at session end, follow-up starts a new session | `copilot-setup-steps.yml` reruns every session | Actions self-hosted runner, long-poll, JIT config | 59 min per session |
| **Devin** | Cognition's own microVMs, a kernel per session | Cognition's cloud, WebSocket to the machine | Sleeps after 30 min, memory and processes kept, resumable 14 days | Blueprint snapshot rebuilt about daily | Outposts: list, watch, CAS claim, `resume` jobs | Sleep timer 5 to 120 min on Enterprise |
| **Jules** | "Short-lived" Ubuntu VM | Not stated | Not documented | Run and Snapshot | None | 3 to 60 concurrent tasks by plan |
| **Amp orbs** | E2B microVM | Not stated | Pauses with memory and files, webhooks wake it | Setup script plus project snapshot | Runners on the user's machine | Burst of 20 orbs, then one per 5 min |

## A. Cursor Cloud Agents

`cloud-agents.md §B1` covers the rename from Background Agents, `.cursor/environment.json`,
environment selection, per-environment secrets and allowlists, private connectivity, the surfaces
and the basics of Self-Hosted Machines. It lists the substrate as "Isolated VM (type not stated)"
and persistence as "Not documented". Both are documented now (§A1, §A2).

### A1. One task, start to finish

Cursor's security page names six stages (**Docs**, cursor.com/docs/cloud-agent/security). A user or
an integration starts a task (§A7). "Cursor provisions an isolated VM for that agent and clones the
authorized repository into it." The agent runs tools in the VM and streams progress back while
conversation state and artifacts go to Cursor-managed storage (paraphrase). "The agent pushes its
branch and opens a draft pull request for a human to review before anything merges." Last, "VM
runtime resources are hibernated and then deleted on lifecycle timers once the run is idle" (same
page).

**Machine.** With an active Build the VM boots from it. "Cursor keeps pre-warmed copies of active
Builds ready. This removes repository cloning and dependency installation from the agent startup
path" (**Docs**, cursor.com/docs/cloud-agent/builds). A task on another branch checks it out on top
of the Build (same page). After boot Cursor runs `start` and then `terminals` (**Docs**,
cursor.com/docs/cloud-agent/setup).

**Idle and retention.** Each follow-up restarts the idle timer (paraphrase,
cursor.com/docs/cloud-agent/security). VM snapshots are "Point-in-time copies of the VM disk
(including cloned code) used to start and resume without recloning". Each lasts a "Rolling 30 days
of inactivity" that every start or resume extends. Conversation state is "Kept indefinitely by
default" (same page). No idle timer is published for Cursor's own VMs. Cursor's Kubernetes template
contrasts its disk-only scheme with the managed one: "Cursor-managed hibernation snapshots memory"
(**Code**, anysphere/k8s-workers@7e12775 README.md:522-525).

**Follow-ups.** An `IDLE` agent accepts follow-ups and "The agent's machine may be hibernated or
snapshotted". During an `ACTIVE` turn the API says "Keep the agent's machine up". A second
concurrent run returns `409 agent_busy` (**Docs**, cursor.com/docs/cloud-agent/api/endpoints).
Since 3.24 "A cloud agent whose machine went away reconnects its workspace when you send a new
message" (**Docs**, cursor.com/docs/release-notes). So a follow-up resumes from the disk snapshot
while it lasts (inference). No page says what happens once the snapshot is deleted.

**Long and event-driven runs.** The Temporal loop survives inference outages and pod hibernation
and supports runs of days or weeks (paraphrase, cursor.com/blog/cloud-agent-lessons). A
subscription lets an agent end its turn and wake when an event such as a finished CI run or a Slack
reply arrives as a follow-up. A subscription lasts at most 180 days. Bursts coalesce into one wake
(paraphrase, cursor.com/docs/cloud-agent/capabilities).

### A2. Substrate

"Runtime workspaces run on Firecracker-based microVM infrastructure" in "a separate AWS account
from the rest of Cursor's production infrastructure" (**Docs**,
cursor.com/docs/cloud-agent/security). The environment is a container inside that VM. The schema's
`ports` are "Ports to expose from the container" (**Docs**,
www.cursor.com/schemas/environment.schema.json). "Docker has edge cases in Cloud Agents because it
runs inside another container layer" (**Docs**, cursor.com/docs/cloud-agent/setup). The sample
Dockerfile gives a non-root `ubuntu` user sudo (paraphrase, same page). Each agent gets "a default
VM profile with limited memory and CPU", with more for Enterprise on request and no published
figures (same page). Each VM has "a full desktop environment" that the agent drives (**Docs**,
cursor.com/docs/cloud-agent/capabilities). No page mentions GPUs.

### A3. Where the loop runs

The loop runs in Cursor's cloud for all three runtimes: Cursor-hosted, My Machines and Team Pools
(**Docs**, cursor.com/docs/cloud-agent/self-hosted/choose-runtime). The first design was
work-stealing, "where worker nodes could pick up agents and loop them to completion", at "one 9 of
reliability". Moving the loop to Temporal "took us past two 9s of reliability". Workflows are now
short and exit "after completing a single task" (**Docs**, cursor.com/blog/cloud-agent-lessons).
"Because the agent loop lives in Temporal rather than on the VM itself, we can manage pod
lifecycles independently", which allows "readonly VMs or prewarmed VMs". The conversation lives in
"an efficient append-only storage mechanism that streams conversation updates out to web and
desktop clients" (same post).

How tool calls reach a VM is documented only for self-hosted workers: "Cursor's agent harness
handles inference and planning, then sends tool calls to a dedicated worker for execution"
(**Docs**, cursor.com/blog/self-hosted-machines). The worker "opens a long-lived outbound HTTPS
connection to Cursor's backend" (**Docs**, cursor.com/docs/cloud-agent/self-hosted). Managed VMs
also serve a Unix socket at `/run/cursor/api.sock` that mints OIDC tokens. The process behind it
isn't named (**Docs**, cursor.com/docs/cloud-agent/identity).

Closing the laptop doesn't affect a run because clients only read the stream. Since 3.24 "Cloud
agents reconnect on their own after your computer wakes from sleep instead of failing" (**Docs**,
cursor.com/docs/release-notes). API clients resume the SSE stream with `Last-Event-ID` until a
retention window passes, then get `410 stream_expired` (**Docs**,
cursor.com/docs/cloud-agent/api/endpoints).

### A4. Environment definition and Builds

The schema at www.cursor.com/schemas/environment.schema.json has grown since the survey (**Docs**).
The base is `build` (a Dockerfile), `image` or `snapshot`, which "Takes precedence over `build` and
`image` when set". The lifecycle keys are `install`, `start` and `terminals`.
`agentCanUpdateSnapshot` defaults to true for snapshot and default bases and is always false for
`build` and `image`. Policy keys include `egressAllowlist`, `egressMode` and `enable_testing`.
`repositoryDependencies` adds repositories to the generated GitHub token.

A Build is "a bootable snapshot of a prepared Cloud Agent environment" (**Docs**,
cursor.com/docs/cloud-agent/builds). Cursor boots the base, clones the repositories, runs `install`
and saves a fresh snapshot that new agents, automations and code reviews start from (paraphrase,
same page). A schedule, a configuration or secret change, a manual run or a test Build that the
agent asks for triggers one. A scheduled check is skipped when no repository has new default-branch
commits and nothing changed. With **Update stale builds** an agent on an old Build pulls the latest
code at start. `install` must be idempotent because it "may run on top of previously prepared disk
state" (same page).

Three rules matter for a host. "Builds preserve disk state only", so processes, exported variables
and in-memory caches never reach a run (paraphrase, cursor.com/docs/cloud-agent/setup). "A failed
Build doesn't replace the active Build" (same page). "Build secrets are scoped to the build step
and aren't passed to the running agent's environment" (**Docs**,
cursor.com/blog/cloud-agent-development-environments). An agent can also set the environment up
itself through Cursor Cloud MCP tools such as `take-environment-snapshot`, which will "Snapshot a
machine after the agent verifies its environment setup" (**Docs**,
cursor.com/docs/cloud-agent/capabilities).

Services start on every boot. The `terminals` "run in a `tmux` session shared by you and the agent"
(**Docs**, cursor.com/docs/cloud-agent/setup). For its own monorepo Cursor built `anydev`, whose
supervisor "monitors and restarts long-running build commands, removing that responsibility from
the model entirely" (**Docs**, cursor.com/blog/cloud-agent-environment). Self-hosted runs never
boot a Build and use only the environment's repository list (paraphrase,
cursor.com/docs/cloud-agent/builds, cursor.com/docs/cloud-agent/self-hosted/choose-runtime).

### A5. Watching and taking over

- **Terminal.** The `terminals` tmux session is shared with the user (§A4). Runtime Secrets are
  hidden from the model but "still visible to users interacting with the agent's environment via
  the Terminal" (**Docs**, cursor.com/docs/cloud-agent/security-network). No SSH is documented.
- **Desktop.** "You can take control of the agent's remote desktop to interact with the software
  the agent is building. Hand control back to the agent at any time" (**Docs**,
  cursor.com/docs/cloud-agent/capabilities).
- **Ports.** "Cursor forwards ports from the agent's environment to your machine". Detected ports
  can forward automatically (**Docs**, cursor.com/docs/cloud-agent/setup). No public preview URL is
  documented.
- **Local and cloud.** "Move to Cloud" "does not snapshot your local uncommitted changes" and
  carries only the conversation (**Docs**, cursor.com/help/ai-features/cloud-agents). No page moves
  a cloud machine's state back to a laptop.
- **Self-hosted desktop.** `--share-desktop` shares a TigerVNC desktop that the worker creates, to
  view or to control. "Pixels leave the machine only through the worker's existing outbound
  connection. No inbound ports open" (**Docs**,
  cursor.com/docs/cloud-agent/self-hosted/computer-use).

### A6. Computer use and evidence

The agent starts dev servers, opens the app in the VM's browser and clicks through flows before it
pushes. It attaches screenshots, videos and log references to the PR (paraphrase,
cursor.com/docs/cloud-agent/capabilities). A "recordScreen tool" records the demos (**Docs**,
cursor.com/blog/cloud-agent-environment). Artifacts upload to
`cloud-agent-artifacts.s3.us-east-1.amazonaws.com`, which a restricted egress mode must allow by
exact name (paraphrase, cursor.com/docs/cloud-agent/security-network).

Self-hosted workers get computer use with `--computer-use`. On a headless Linux machine the worker
starts a managed TigerVNC desktop running Xfce. `xdotool`, `ffmpeg` and the X11 utilities are
required, plus Chrome or Chromium for a browser (**Docs**,
cursor.com/docs/cloud-agent/self-hosted/computer-use).

### A7. Triggers and identity

- **Git access.** "Cloud Agents reach your code through the Cursor GitHub or GitLab App, not
  through any single person's credentials". Each user also connects a Git account. "A Cloud Agent
  can only reach repositories the triggering user could already reach" (**Docs**,
  cursor.com/docs/cloud-agent/security).
- **Commits** are signed "with a HSM-backed Ed25519 key" and show as "Verified" (**Docs**,
  cursor.com/docs/cloud-agent/security-network). Whose name they carry isn't stated.
- **Routing.** Slack options such as `repo=` and `env=` pick the repository and environment. Linear
  reads `[repo=owner/repository]`, then labels (paraphrase, cursor.com/docs/integrations/slack,
  cursor.com/docs/integrations/linear). On public repositories only `OWNER` or `COLLABORATOR`
  commenters can send a run to a self-hosted pool (**Docs**,
  cursor.com/docs/cloud-agent/self-hosted/pool).
- **Automations.** Schedules, GitHub and GitLab events, Slack, Linear, PagerDuty and webhooks start
  runs (**Docs**, cursor.com/docs/cloud-agent/automations). PR triggers refuse forks because
  "running external code with the repo's permissions isn't safe". An automation runs as its creator
  or as "its own dedicated service account" (same page).
- **Workload identity.** On a managed VM any process can `POST /v1/tokens/oidc` on
  `/run/cursor/api.sock` for a short-lived JWT with an audience it picks. Self-hosted workers serve
  the socket only with `--identity-socket`. "Any process running as the worker's OS user can mint
  on that claim's socket" (**Docs**, cursor.com/docs/cloud-agent/identity).

### A8. Concurrency

"You can run as many agents as you want in parallel" (**Docs**, cursor.com/docs/cloud-agent). Each
agent has one active run (§A1). An API run can define up to 20 subagents. `/in-cloud` gives a
subagent its own VM and branch (**Docs**, cursor.com/docs/cloud-agent/api/endpoints,
cursor.com/docs/subagents). The UI runs Best-of-N candidates side by side (**Docs**,
cursor.com/docs/release-notes, week of Sep 28, 2026). Team Pools run "one agent per machine". A
request with no free worker "waits until more capacity becomes available" (**Docs**,
cursor.com/docs/cloud-agent/self-hosted, cursor.com/blog/self-hosted-machines).

### A9. Network and secrets

- **Egress.** The modes are **Allow all network access**, **Default + allowlist** and **Allowlist
  only**. "Internet access is on by default" (**Docs**, cursor.com/docs/cloud-agent/security).
  Allowlist only still reaches Cursor's services and the source control providers (paraphrase,
  cursor.com/docs/cloud-agent/security-network).
- **Secrets are environment variables.** A Runtime Secret's value is replaced with `[REDACTED]` in
  tool results, the transcript and commits. A Build Secret reaches only the Docker build through a
  secret mount (paraphrase, cursor.com/docs/cloud-agent/security-network). Per-agent `envVars`
  override environment, user and team secrets and are "deleted with the agent" (**Docs**,
  cursor.com/docs/cloud-agent/api/endpoints).
- **Git.** Cursor's controls include scoped and proxied git remote access and secret scanning in
  commits (paraphrase, cursor.com/blog/cloud-agent-environment). No page describes the git proxy.
- **Self-hosted.** Once an admin enables it, `--mint-github-token` gives a worker "short-lived"
  GitHub tokens for the request's repositories. `--sync-dashboard-secrets` delivers secrets as
  environment variables. Both assume "one worker per container or OS user" (**Docs**,
  cursor.com/docs/cloud-agent/self-hosted/pool).
- **Audit.** Enterprise audit logs stream to a SIEM, a webhook or S3 (paraphrase,
  cursor.com/docs/cloud-agent/security).

### A10. Self-hosted pools: the worker protocol

`cloud-agents.md §B1` covers the outbound endpoints, the two modes and the worker caps. The
protocol below comes from the pool guide, the API reference and two templates Cursor publishes.

**Two processes.** A worker (`agent worker start`) serves one agent: "Once a worker is matched to a
request, Cursor forwards all agent tool calls directly to the machine". A controller
(`agent worker controller`) watches the queue and runs a team-supplied `--spawn` hook to start
machines (**Docs**, cursor.com/docs/cloud-agent/self-hosted/pool). Since CLI v2026.09.28 a worker
whose connection "goes silent for 90 seconds" reconnects (**Docs**, cursor.com/docs/release-notes).

**The queue** (**Docs**, cursor.com/docs/cloud-agent/api/endpoints). The controller lists
`GET /v0/private-workers/pending-requests`, then follows `.../pending-requests/stream?cursor=...`
over SSE. Events are `created`, `claimed`, `claimed_offline` (with `claimedWorkerId` and
`wakeTimeoutMs`), `expired` and a `heartbeat` "about every 20 seconds". "Every cursor in a watch
chain expires **five minutes after the list that issued it**", so the controller lists again.
Events are hints because "the list is the source of truth". A dropped event "is never redelivered"
(same page).

**Claim.** `POST /v0/private-workers/claim` takes `{id, workerId}`. The worker starts with
`CURSOR_AGENT_WORKER_ID` set to that ID. "A second claim while a live claim exists is rejected". An
optional session token "serves only this claim" and expires "7 days after minting" (same page). A
controller that can't start a machine calls `/fail` (paraphrase, same page). Otherwise a run fails
only "when the wait for its worker times out". No page states that wait (**Docs**, pool page
above).

**The controller** (pool page). Claim-then-spawn, the default, runs `--spawn` once per claim. A
warm pool (`--warm-idle N`) keeps N unclaimed workers connected and "never calls claim". One warm
controller runs per pool because "there is no server-side spawn lease". The hook gets the request,
user, repository, pool and worker ID in `CURSOR_*` variables plus a credential. With
`--identity-audience` it also gets `CURSOR_IDENTITY_TOKEN` to verify who the run is for. If the
hook fails, "the controller keeps holding the claim". "A wake (`CURSOR_WAKE=1`) revives a claimed
worker without a claim call".

**Idle, release and hibernation** (pool page). `--idle-release-timeout` defaults to 3600 seconds
and resets on each follow-up. Then "the CLI exits with code 0 so a supervisor can recycle the
machine". Without hibernation a follow-up "reacquires from the pool" and the agent may spend
minutes rebuilding its workspace (paraphrase). With hibernation the pool sets
`workerReadyTimeoutSeconds` (default `0`, example 900) and the operator:

1. Snapshots and stops the machine when the worker exits 0 or the agent is `IDLE`.
2. On `claimed_offline`, restores the snapshot and starts a worker "with the same id before the
   window lapses".
3. Releases the claim if the snapshot is gone. A lapsed window returns the request as a fresh
   `created` event.

**AWS Lambda template** (**Code**, anysphere/aws-lambda-workers@6c36161). "Cursor hosts the agent
loop. Each worker runs in a Firecracker-isolated MicroVM in your AWS account, started from a
snapshot", for at most 8 hours (README.md:3, 26). The controller is a Lambda that serves one
five-minute SSE window. EventBridge starts the next. "Reserved concurrency 1 prevents overlapping
controllers" (controller/handler.py:1-6). `spawn.sh` returns at once. It sets an 8-hour idle policy
because "Outbound-only workers never hit the HTTPS ingress, so a short default idle policy would
suspend the VM before the agent connects" (spawn.sh:2-3, 25-26, 35).

**Kubernetes template** (**Code**, anysphere/k8s-workers@7e12775). Each spawn creates a Pod with
`restartPolicy: Never` (README.md:20-27). Hibernation keeps a PersistentVolumeClaim per worker ID,
so "Cursor asks the controller to bring the *same* worker id back" onto a new Pod. Processes and
tmux sessions are gone on wake (paraphrase, README.md:494-500, 522-524). "A wake has to beat two
timers": the reconnect window and the worker's idle release both run "from the follow-up's arrival"
(README.md:607-613). "Cursor never deletes Kubernetes objects", so a reaper CronJob deletes volumes
after 7 days by default (README.md:619-620, chart/values.yaml:127-136).

**E2B's partner guide** runs the controller in a dispatcher sandbox with one worker sandbox per
request. Idle workers pause and resume for a follow-up. Git credentials go "through the E2B egress
proxy instead of writing them to the worker". "Cursor queue traffic cannot wake a paused
dispatcher", so the dispatcher renews its own timeout (**Docs**, docs.e2b.dev/agents/cursor).

### A11. Pricing and limits

Cloud Agents need the Premium plan ($30 a month) or above and draw on one usage pool shared with
the editor, the CLI and Grok Bot (paraphrase, cursor.com/docs/models-and-pricing). No page prices
VM time. The limits that show the design are the 30-day snapshot window (§A1), the 1-hour default
idle release (§A10), the 8-hour Lambda cap (§A10) and 180-day subscriptions (§A1).

## B. OpenAI Codex

`cloud-agents.md §B2` covers the container flow (setup and maintenance scripts, a 12-hour cache,
setup-only secrets and agent internet off by default) and the Agents API executor. OpenAI now calls
that flow **Codex Cloud (Legacy)** and says "We plan to deprecate this experience" (**Docs**,
learn.chatgpt.com/docs/environments/cloud-environments). The docs moved from
developers.openai.com/codex to learn.chatgpt.com/docs. The current product gives each task its own
VM (B1 to B5). B6 covers the Agents API.

### B1. One task, start to finish

1. **Prepare.** Codex inspects the repositories, installs dependencies and tests the workflow,
   asking for missing access as it goes (paraphrase, learn.chatgpt.com/docs/cloud). It records an
   install script and a Start skill (§B3).
2. **Publish.** "Publishing captures the prepared filesystem for new tasks" (**Docs**,
   learn.chatgpt.com/docs/environments/cloud-environments).
3. **Start.** "New task: starts from the published environment's prepared filesystem." "Each cloud
   task runs in a VM" (same page).
4. **Run.** Each task "can keep working while your computer is asleep" (**Docs**,
   learn.chatgpt.com/docs/cloud).
5. **Deliver.** The user commits or opens a pull request. `codex cloud apply` fetches the diff and
   runs `git apply` locally (**Code**, openai/codex@8c07694
   codex-rs/cloud-tasks-client/src/http.rs:478-485).
6. **Follow up.** "Existing task: continues with its own saved files, including uncommitted changes
   and installed tools." Saved VM state "is recoverable for up to seven days" after the last turn
   or resume (**Docs**, cloud-environments page above).

A background "Repository refresh" keeps dependency caches "without rerunning installation or
startup commands". A changed setup reaches new tasks only after **Republish** because "existing
tasks retain their own state" (same page).

### B2. Substrate and loop

"Each cloud task runs in a VM with these default resources" (**Docs**,
learn.chatgpt.com/docs/environments/cloud-environments). Plus gets 2 vCPUs, 8 GiB of memory and 8
GiB of disk. Pro, Business and Enterprise get 4 vCPUs, 16 GiB and 32 GiB. Enterprise can have
larger VMs (paraphrase, same page). No page names the hypervisor or covers root, Docker or GPUs.
"Computer and browser use" is listed as not yet supported (same page). The legacy `codex-universal`
image starts `FROM ubuntu:24.04` (**Code**, openai/codex-universal@47f4f0e Dockerfile:2).

OpenAI doesn't say where the loop runs. The open-source gRPC attach client notes that "`Attach` has
no history snapshot or replay guarantee". Dropping an attach leaves the thread running (**Code**,
paraphrase, openai/codex@8c07694 codex-rs/cloud-client/README.md).

### B3. Environment

Codex records the tested setup in two fields: "**Install script:** commands to install dependencies
and prepare development assets" and "**Start skill:** instructions to start services and check that
they're ready" (**Docs**, learn.chatgpt.com/docs/environments/cloud-environments). The Start skill
is instructions, not a command, so the agent starts services itself (inference). Environment
variables are "Passed directly to programs in the environment". Network secrets go through a proxy
(§B5).

### B4. Watching, triggers and identity

- **Watching.** Tasks are followed in ChatGPT on the web, on mobile and in the desktop app
  (**Docs**, learn.chatgpt.com/docs/cloud). No terminal, SSH, desktop or preview URL is documented.
- **GitHub.** `@codex review` starts a review. Any other `@codex` comment "starts a legacy cloud
  chat using your pull request as context" (**Docs**, learn.chatgpt.com/docs/third-party/github).
  Commit identity isn't documented.
- **Slack and Teams.** In Enterprise workspaces `@ChatGPT` runs a task in an environment "available
  to the account running the task". Another participant's request doesn't continue someone else's
  task (paraphrase, cloud-environments page above). Linear works only through the legacy flow (same
  page).
- **Best-of-N.** `codex cloud exec` takes 1 to 4 attempts and passes `best_of_n` to the backend
  (**Code**, openai/codex@8c07694 codex-rs/cloud-tasks/src/cli.rs:52-60).

### B5. Network and secrets

- **Egress.** Internet access is **Package managers**, **Custom domains only** or **All
  (unrestricted)**. The source IPs "are shared across customers" (**Docs**,
  learn.chatgpt.com/docs/environments/cloud-environments).
- **Network secrets.** Programs see a placeholder that the proxy swaps for the real value on
  allowed destinations (paraphrase). "Substitution works for HTTPS on port 443, during setup and
  tasks". It "doesn't put the raw credential in a local process or file" (same page). The legacy
  flow removed secrets before the agent ran (`cloud-agents.md §B2`).
- **Personal vault and VPN.** A shared environment can ask each user for their own values. "Only
  requested values reach a task". Each task VM joins Tailscale with a reusable ephemeral key
  (paraphrase, same page).
- **Policy and audit.** "OIDC is available by request for Enterprise workspaces" (same page). The
  Compliance API exports `codex_log` events (**Docs**,
  learn.chatgpt.com/docs/enterprise/compliance-api).

### B6. Agents API: the self-hosted executor

"OpenAI runs the agent harness." `codex exec-server` runs shell commands, file edits and local MCP
servers on a machine the customer controls (**Docs**,
developers.openai.com/api/docs/guides/agents-api/environments/self-hosted, `cloud-agents.md §B2`).

**Connecting.** "The executor registers with the API using an environment ID and a restricted API
key. It then connects over WebSocket to receive commands and return results. All connections are
outbound. The executor reconnects if the connection drops" (same page). The environment key "only
permits connecting environments". The application's `OPENAI_API_KEY` stays outside the sandbox.
"Each session has its own environment ID and needs its own executor" (same page).

**On the wire** (**Code**, openai/codex@8c07694 codex-rs/exec-server/README.md). The executor
registers, then "reconnects to the service-provided rendezvous websocket as the environment" (lines
56-62). The socket carries "binary protobuf relay frames carrying encrypted payloads" multiplexed
by `stream_id` (lines 125, 150-152). Inside them run JSON-RPC calls such as `process/start`,
`process/read` (a long poll with `afterSeq` and `waitMs`), `process/write`, `process/terminate` and
`fs/*` (lines 255-476). "If the websocket connection closes, the server terminates any remaining
managed processes for that client connection" (lines 195-203).

**Starting executors on demand** (**Docs**,
developers.openai.com/api/docs/guides/agents-api/environments/lifecycle):

1. When input needs an environment, the API emits `agent.session.action_required` with
   `environment_connection`. "Your webhook handler starts or reconnects the environment."
2. The handler verifies the signature and queues the request. "Return a successful HTTP response
   only after queuing succeeds."
3. A worker starts or reconnects the executor. "Repeated or concurrent requests must not create
   duplicate environments."
4. "The API waits up to five minutes for an input-time connection. If it expires, the submission
   fails."

"Turn creation and `agent.session.in_progress` events arrive too late to start an offline executor"
(same page).

**Stopping and resuming** (same page). "An idle event alone is not a safe shutdown signal." "If
your application cannot coordinate shutdown with incoming work, keep the environment running." "A
mid-turn disconnect can fail a tool even if the turn completes." "Reusing the environment ID does
not restore files in replacement compute. Use provider storage or snapshots to preserve them."

**Hosted and pre-warmed sandboxes.** OpenAI-hosted sandboxes come in `small` (1 vCPU, 1 GB),
`medium` (2 vCPU, 4 GB) and `large` (4 vCPU, 16 GB). One that gets neither activity nor keep-alives
for an hour can be deleted (paraphrase,
developers.openai.com/api/docs/guides/agents-api/environments/openai-hosted). "Unattached
pre-warmed sandboxes expire after 5 minutes" (**Docs**,
developers.openai.com/api/docs/guides/agents-api/environments/prewarmed). Multi-agent sessions run
up to 6 subagents that share the coordinator's filesystem (**Docs**,
developers.openai.com/api/docs/guides/agents-api/multi-agent).

**app-server.** The JSON-RPC server behind Codex's rich clients speaks over stdio, a Unix socket or
an "experimental and unsupported" WebSocket. "Use plain WebSockets only for localhost or an SSH
port-forwarded connection" (**Docs**, learn.chatgpt.com/docs/app-server).

### B7. Limits

No maximum task duration or per-plan concurrency is published for Codex Cloud. A "5-hour" figure
appears on hypereal.cloud, prismgist.com, justinmckelvey.com and a community.openai.com thread but
on no OpenAI page (**Unverified**). Per-plan concurrency figures appear only in such sources
(**Unverified**). Usage bills in credits per model and token type, with no price for VM time
(**Docs**, learn.chatgpt.com/docs/pricing).

## C. Claude Code

`cloud-agents.md §B3` covers the surfaces, the GitHub App, self-hosted environments, on-demand
runners and Remote Control. It called disk-only persistence "the expected default" without
re-checking it. The docs now describe a pause that keeps the VM's files (§C1). Claude Code on the
web runs the CLI in a VM. Claude Managed Agents is an API whose loop runs at Anthropic (§C8).

### C1. One task, start to finish

1. **Start.** From claude.ai/code, mobile, the Desktop app, `claude --cloud` or a Routine.
   `--remote` is now "a deprecated alias for `--cloud`" (**Docs**,
   code.claude.com/docs/en/claude-code-on-the-web).
2. **Machine.** "each session gets a fresh virtual machine (VM) running Ubuntu 24.04 on x86_64"
   with the repository cloned (**Docs**, code.claude.com/docs/en/cloud-environments).
3. **Setup** runs once per environment and is cached as a filesystem snapshot (§C3).
4. **Run.** Claude Code runs inside the VM. "Closing the tab or navigating away doesn't stop the
   session" (**Docs**, code.claude.com/docs/en/web-quickstart).
5. **Deliver.** Commits carry "a `Claude-Session: <url>` git trailer" (**Docs**,
   code.claude.com/docs/en/cloud-environments).
6. **Idle.** "after a few minutes without activity, a session's VM pauses with its files saved.
   Your next message restores the same VM and starts Claude Code again". The setup script doesn't
   run again (paraphrase, same page).
7. **Reclaim.** "Cloud sessions stop after a period of inactivity and the session's VM is
   reclaimed" (**Docs**, code.claude.com/docs/en/claude-code-on-the-web). A wait on an MCP approval
   counts as inactive. Reopening provisions a fresh VM with the conversation history but without
   background subagents, shell commands or a pending `/loop` wakeup (paraphrase, same page).

No page gives the pause timer or how long a paused VM lasts before it's reclaimed.

### C2. Substrate and loop

Hosted sessions get "approximate resource ceilings" of 4 vCPUs, 16 GB of RAM and 30 GB of disk
(**Docs**, code.claude.com/docs/en/cloud-environments). "Scripts run as root on Ubuntu 24.04".
"Docker is available for running containerized services." "PostgreSQL and Redis are pre-installed
but not running by default" (same page). No page names the hypervisor, offers a custom image or
mentions GPUs. The loop is the Claude Code CLI inside the VM. On self-hosted runners the runner
"spawns a child Claude Code process" that streams events to Anthropic (**Docs**,
code.claude.com/docs/en/self-hosted-environments). "You don't get a shell into the session VM"
(**Docs**, cloud-environments above).

### C3. Environment and setup cache

The setup script runs "the first time you start a session in an environment". If it finishes within
roughly five minutes, "Anthropic snapshots the filesystem and reuses that snapshot as the starting
point for later sessions". Otherwise "the environment isn't cached" (**Docs**,
code.claude.com/docs/en/cloud-environments). The cache "keeps what the setup script writes to disk
and loses anything that was only running". Pulled Docker images survive, but "Claude still starts
the containers each session" (same page). The script runs again when its text or the allowed hosts
change or "when the cache reaches its expiry after roughly seven days" (paraphrase, same page).
Services that must always run belong in a SessionStart hook, which runs "on every session including
resumed" (same page). Self-hosted runners get no Anthropic image. The sample Dockerfile starts
`FROM debian:bookworm-slim` (**Docs**, code.claude.com/docs/en/self-hosted-environments-deploy).

### C4. Watching, triggers and identity

- **No cloud desktop.** Computer use is a macOS research preview. Claude in Chrome drives the
  user's own browser (paraphrase, code.claude.com/docs/en/computer-use,
  code.claude.com/docs/en/chrome). No browser, screenshot or preview URL is documented for cloud
  sessions.
- **Cloud and local.** `--teleport` pulls a cloud session into the terminal through "the same
  Remote Control session infrastructure that cloud sessions use" (**Docs**,
  code.claude.com/docs/en/claude-code-on-the-web). The Desktop app moves a local session to the
  cloud "with your conversation carried over as a summary" (**Docs**,
  code.claude.com/docs/en/desktop).
- **Slack.** The per-user `@Claude` bot is "retired effective October 5, 2026" where Claude Tag is
  connected. Claude Tag uses a shared identity and only organization environments (paraphrase,
  code.claude.com/docs/en/slack).
- **Routines** start sessions on a schedule, from an API call or on GitHub events, on `claude/`
  branches (**Docs**, code.claude.com/docs/en/routines).
- **GitHub Actions.** `claude-code-action` runs Claude Code on the workflow's own runner (**Code**,
  anthropics/claude-code-action@f27be6b action.yml:1-5, 189-194).

### C5. Concurrency and limits

Organizations have an unpublished cap on concurrent cloud sessions. Release 2.1.302 (Oct 16, 2026)
added a banner for "your organization reaching its limit on concurrent cloud sessions" (**Docs**,
code.claude.com/docs/en/changelog). No maximum session length is published for hosted sessions.

### C6. Network and secrets

- **Levels.** **None**, **Trusted** (the default), **Full** and **Custom**. A change reaches
  running sessions "within about a minute". Hosted traffic passes an HTTP/HTTPS proxy. Self-hosted
  traffic leaves through the operator's network (paraphrase,
  code.claude.com/docs/en/cloud-environments).
- **Network secrets.** "The agent proxy attaches a secret to a request when the request's host
  matches one you listed on that secret" (same page).
- **GitHub proxy.** "all GitHub operations go through a dedicated proxy that keeps your real GitHub
  credentials outside the session's VM". It swaps a scoped credential for the real token, rejects
  branch deletions and tag pushes and blocks GraphQL, so `gh pr` fails. `GITHUB_TOKEN` reads
  `proxy-injected` (paraphrase, same page).
- **Audit.** "All operations in cloud sessions are logged for compliance and audit purposes"
  (**Docs**, code.claude.com/docs/en/security).
- **Managed Agents.** Self-hosted network access "is determined by your VPC and firewall rules".
  The environment key is "scoped to one environment's work queue" (**Docs**,
  platform.claude.com/docs/en/managed-agents/self-hosted-sandboxes-security).

### C7. Self-hosted environments: the runner protocol

**Lease and poll** (**Docs**, code.claude.com/docs/en/self-hosted-environments). "A runner with
free capacity claims the session and holds a lease on it." It clones the repository and "spawns a
child Claude Code process". "each poll refreshes the lease and doubles as the heartbeat." "If the
runner stops polling, its lease lapses after about 60 seconds." Requeueing can then take minutes.
All connections go out to `api.anthropic.com` (paraphrase, same page).

**Owner lock and drain.** "The first session a runner picks up locks the runner to that session's
owner". By default the runner exits when its sessions finish, so the orchestrator restarts it "with
a fresh disk, ready to serve any owner" (same page). `--retire-at` releases sessions before a
planned host kill, so each "resumes on a fresh runner when the user sends their next message" (same
page).

**Release and resume.** `--release-idle-session-min` releases an idle session "and then exits 0"
(**Docs**, code.claude.com/docs/en/self-hosted-environments-configuration). "A released session
resumes from a fresh clone" (**Docs**, code.claude.com/docs/en/self-hosted-environments-deploy).
The `post-session` hook is "your only chance to save uncommitted work" and gets 60 seconds by
default (configuration page above). `--push-outcome-on-release` pushes outcome branches first.
`GET /healthz` reports `last_poll_age_ms` (**Docs**,
code.claude.com/docs/en/self-hosted-environments-reference).

**On-demand runners** (configuration page above). The `spawn-runner` hook "must submit work
asynchronously, without waiting for the runner to boot" and return within 60 seconds. It gets an
order ID, a session ID (empty when pre-warming) and a work-order JWT that registers exactly one
runner. "Be idempotent on `CLAUDE_RUNNER_ORDER_ID`." "Don't retry the workload." "If the runner
never registers, Anthropic re-requests with a fresh order ID after `--expected-spawn-seconds`",
which should cover the p99 boot time (paraphrase). Spawned runners use `--capacity 1`,
`--release-idle-session-min 30` and `--kill-session-after-min 480`. `--min-idle` standbys exit
after 60 unused minutes (same page).

### C8. Managed Agents: the worker protocol

"A `self_hosted` environment acts as a work queue." "Claude runs on Anthropic's side and requests
tool calls. Your worker runs each call locally and posts the result back" (**Docs**,
platform.claude.com/docs/en/managed-agents/self-hosted-sandboxes). Anthropic's own sandboxes "run
as isolated Linux containers" (**Docs**,
platform.claude.com/docs/en/managed-agents/cloud-sandboxes-reference).

- **Waiting.** An always-on worker "polls the queue continuously and needs only outbound HTTPS". A
  webhook-triggered worker "wakes on `session.status_run_started` and starts polling", which
  "requires a webhook endpoint that Anthropic can reach" (**Docs**,
  platform.claude.com/docs/en/managed-agents/self-hosted-sandboxes-workers).
- **Running.** One worker can run every session in a shared directory. Otherwise "A poller launches
  a fresh sandbox for each claimed session" (same page). `ant beta:worker run` "Handles one claimed
  session and exits". `--max-idle` waits 60 seconds after `end_turn` (**Docs**,
  platform.claude.com/docs/en/managed-agents/self-hosted-sandboxes-reference).
- **Stopping.** "The worker notices on its next lease heartbeat" and cancels the running tool call
  (**Docs**, platform.claude.com/docs/en/managed-agents/self-hosted-sandboxes-operations).

E2B's guide routes through a public webhook sandbox that "keeps a session-to-sandbox assignment
store". "Follow-up turns for the same session reconnect to the same worker and reuse its
`/mnt/session` filesystem" (**Docs**, docs.e2b.dev/agents/claude-managed-agents).

## D. GitHub Copilot cloud agent

GitHub renamed "Copilot coding agent" to "Copilot cloud agent" (**Docs**,
github.blog/changelog/2026-04-01-research-plan-and-code-with-copilot-cloud-agent/).
`cloud-agents.md §B4` covers the Actions environment, `copilot-setup-steps.yml`, runners, the
firewall and third-party agents.

### D1. One task, start to finish

1. **Trigger.** An issue assignment, an `@copilot` comment, the Agents tab, an IDE, mobile, the
   CLI, the API, Slack, Jira, Teams, Linear or Azure Boards (**Docs**,
   docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/use-cloud-agent-on-github).
2. **Setup.** A fresh Actions runner runs `copilot-setup-steps`. If a step fails, Copilot skips the
   rest and starts working anyway (paraphrase,
   docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-cloud-agent/customize-the-agent-environment).
3. **Run.** A steering message lands "after it finishes its current tool call" (**Docs**,
   docs.github.com/en/copilot/how-tos/copilot-on-github/use-copilot-agents/manage-and-track-agents).
   "Each Copilot cloud agent session has a maximum execution time of 59 minutes. This limit cannot
   be extended or bypassed" (**Docs**,
   docs.github.com/en/copilot/concepts/copilot-surfaces/copilot-on-github).
4. **Teardown.** "The environment is destroyed when the session ends, but the session log remains
   available on GitHub.com" (**Docs**,
   docs.github.com/en/copilot/concepts/security-governance-and-network-settings/session-data).
5. **Follow-up.** A new `@copilot` comment starts a new session that "remembers context from
   previous sessions on the same pull request" (**Docs**, use-cloud-agent-on-github above). Merged
   or closed pull requests get no response (paraphrase,
   docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/troubleshoot-cloud-agent).

There is no idle state. Each follow-up runs setup again on a new runner from the pushed branch
(inference).

### D2. Substrate, environment and loop

- **Runner.** GitHub-hosted runners are Azure VMs running "a fork of the Azure Pipelines Agent"
  (**Docs**, docs.github.com/en/actions/concepts/runners/github-hosted-runners). The standard
  private-repository runner has 2 vCPUs, 8 GB of RAM and a 14 GB SSD (**Docs**,
  docs.github.com/en/actions/reference/runners/github-hosted-runners). Larger runners reach 96
  vCPUs and offer one GPU size. Linux VMs "run using passwordless sudo" (**Docs**,
  docs.github.com/en/actions/reference/runners/larger-runners). Ubuntu 24.04 ships Docker 28.0.4
  (**Code**, actions/runner-images@06f4a91 images/ubuntu/Ubuntu2404-Readme.md:75-78).
- **Environment.** Copilot honors only `steps`, `permissions`, `runs-on`, `services`, `snapshot`
  and `timeout-minutes`. `container` is ignored (paraphrase, customize-the-agent-environment
  above). `services` starts databases as service containers. No page explains `snapshot`.
- **Secrets and hooks.** Agents secrets arrive as environment variables. Those prefixed
  `COPILOT_MCP_` reach only MCP servers (paraphrase,
  docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-cloud-agent/configure-secrets-and-variables).
  `.github/hooks/*.json` runs scripts on events such as `sessionStart` and `preToolUse` (**Docs**,
  docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-cloud-agent/use-hooks).
- **Loop.** No page says where the agent runs relative to the setup job or how it reaches GitHub.

### D3. Watching, evidence, identity and network

- **Watching.** The Agents tab shows the session log with tool calls and commands (**Docs**,
  github.blog/changelog/2026-01-26-introducing-the-agents-tab-in-your-repository/). "Open in VS
  Code" and the CLI's `/resume` move a session to the user's machine (**Docs**,
  docs.github.com/en/copilot/how-tos/copilot-in-your-ide/use-copilot-agents/use-cloud-agent-in-your-ide).
  No terminal, SSH or port forwarding reaches the runner.
- **Evidence.** With the default Playwright MCP, "Copilot can even share screenshots of what it has
  done in its pull request" (**Docs**,
  github.blog/changelog/2025-07-02-copilot-coding-agent-now-has-its-own-web-browser/).
- **Identity.** "Commits from Copilot cloud agent are authored by Copilot, with the person who
  started the task listed as co-author." They are signed (**Docs**, manage-and-track-agents above).
  Copilot pushes only to `copilot/` branches or the pull request's branch. Workflows on its pushes
  wait for approval (paraphrase,
  docs.github.com/en/copilot/concepts/security-governance-and-network-settings/risks-and-mitigations).
  The REST API accepts only user tokens (**Docs**,
  docs.github.com/en/rest/agent-tasks/agent-tasks).
- **Concurrency.** "Copilot can work on multiple tasks at once" with no number given (**Docs**,
  github.blog/changelog/2025-07-02-agents-page-for-copilot-coding-agent-in-public-preview/).
  **Unverified:** five to seven concurrent sessions per user, from
  github.com/orgs/community/discussions/199770.
- **Firewall.** It "only applies to processes started by the agent via its Bash tool", not to MCP
  servers or setup steps (paraphrase,
  docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-the-firewall).
- **Injection and audit.** Only users with write access can trigger it. Hidden characters are
  filtered from its input (paraphrase, risks-and-mitigations above). Audit events carry
  `actor_is_agent` and `agent_session_id` (**Docs**,
  docs.github.com/en/copilot/reference/enterprise-administrators/agentic-audit-log-events).
- **Billing.** Since 2026-06-01 sessions use AI Credits plus Actions minutes (**Docs**,
  github.blog/changelog/2026-06-01-updates-to-github-copilot-billing-and-plans/).

### D4. Self-hosted runners: the protocol

**The runner** (**Code**, actions/runner@9abfe11). A just-in-time config is a base64 blob of files
the runner writes, skipping `config.sh`. An ephemeral runner deletes its `.runner` and
`.credentials` after one job (Runner.cs:234-253, 895). It polls `GET {url}/message?sessionId=...`
at a URL that registration supplies (BrokerHttpClient.cs:60-69, ConfigurationManager.cs:144-155).
It renews the job lease every 60 seconds (JobDispatcher.cs:748-791). A cancel gets at least 60
seconds before a hard kill (JobDispatcher.cs:1269-1289). Processes tagged with `RUNNER_TRACKING_ID`
are swept at job end (JobExtension.cs:891-931). A reused runner keeps `_work` and the tool cache
between jobs (PipelineDirectoryManager.cs:104-117).

**Scale sets** (**Code**, actions/scaleset@e6daac7 and actions/actions-runner-controller@5aed393).
The listener opens a message session at `POST /_apis/runtime/runnerscalesets/{id}/sessions` (ARC
cmd/ghalistener/main.go:68-83). `GetMessage` is a `GET` with a `lastMessageId` cursor that waits on
the server and returns 202 for nothing or 200 for a message (scaleset session_client.go:131-176).
Messages are `JobAvailable`, `JobAssigned`, `JobStarted` and `JobCompleted` (types.go:11-62). The
listener must call `AcquireJobs` for each `JobAvailable` "or the job stays unassigned"
(session_client.go:244-297, listener/listener.go:111-112). It scales to
`min(MinRunners + TotalAssignedJobs, MaxRunners)` runners, each started with
`ACTIONS_RUNNER_INPUT_JITCONFIG` (examples/dockerscaleset/scaler.go:68-70, 124-151). "After 24
hours the Actions Service unassigns the job if no runner accepts it" (ARC
docs/gha-runner-scale-set-controller/README.md:16).

So runners can start on demand, but the listener must stay up and poll (inference).

## E. Devin

`cloud-agents.md §B9` gives Devin one line. Its Gaps say Outposts weren't re-checked. Everything
below comes from docs.devin.ai and Cognition's blog.

### E1. One task, start to finish

1. **Trigger.** Each session records an `origin` such as `webapp`, `slack`, `api`, `linear`,
   `automation` or `cli` (**Docs**,
   docs.devin.ai/api-reference/v3/sessions/post-organizations-sessions-messages).
2. **Machine.** Cognition's orchestration includes "predicting demand to keep warm VM pools ready"
   (**Docs**, cognition.com/blog/what-we-learned-building-cloud-agents). "Each session boots a
   **fresh copy** of the snapshot", then pulls the latest code (**Docs**,
   docs.devin.ai/onboard-devin/environment/blueprints).
3. **Sleep.** "Devin sleeps automatically after 30 minutes of inactivity by default." Enterprise
   can set 5 to 120 minutes. "While sleeping, Devin does not consume usage" (**Docs**,
   docs.devin.ai/admin/billing/usage).
4. **Wake.** Cognition snapshots "full machine state at the hypervisor level" (memory, process
   trees and files) and resumes when a CI result or review comment arrives (paraphrase, blog
   above). A message also resumes a session. Since Oct 16, 2026 "Opening a browser preview of a
   sleeping session restarts the servers behind the preview automatically, without a chat message"
   (**Docs**, docs.devin.ai/release-notes/2026).
5. **Retention.** Since Oct 9, 2026 "Sessions can be resumed for 14 days after they go to sleep
   (previously 30 days)" (same page). Terminating is final. Archived sessions still resume when
   messaged (paraphrase,
   docs.devin.ai/api-reference/v3/sessions/post-organizations-sessions-archive).

No wall-clock maximum is documented.

### E2. Substrate and loop

"Our own implementation of microVMs took over a year of hypervisor engineering, ensuring every
agent session runs on its own dedicated kernel" (**Docs**,
cognition.com/blog/what-we-learned-building-cloud-agents). Windows and macOS snapshots exist beside
Linux (**Docs**, docs.devin.ai/onboard-devin/environment). Sizes, root and Docker are undocumented.
GPUs come only through Outposts (§E6). The loop is "The Brain", "A stateless, cloud-based service".
The machine is "The Devbox". "On startup, Devin establishes a secure WebSocket connection to an
isolated container in Cognition's tenant" (**Docs**, docs.devin.ai/enterprise/deployment/overview).

### E3. Blueprints and snapshots

A blueprint is YAML in the app or `.devin/blueprint.yaml`. `initialize` runs "During builds only".
`maintenance` installs dependencies and is "Surfaced to the agent at session start (not
auto-executed)". `post-build` validates the machine before the snapshot is saved (**Docs**,
docs.devin.ai/onboard-devin/environment/blueprints). "Builds run automatically when you save a
blueprint and periodically (~every 24 hours) to keep dependencies fresh". Steps time out after 3
hours (same page). "Each organization has one active snapshot per platform". "Session changes don't
persist back to the snapshot" (**Docs**, docs.devin.ai/onboard-devin/environment). Secrets are
injected per step and scrubbed from the image. A `maintenance` step can start a background service
with `&` (paraphrase, docs.devin.ai/onboard-devin/environment/blueprint-reference).

### E4. Watching, computer use and evidence

- **Desktop.** Devin sees "a full desktop environment" as a 1024 by 768 display. Its Chrome takes
  CDP on port 29229, so Playwright can drive the same browser (paraphrase,
  docs.devin.ai/work-with-devin/computer-use).
- **Evidence.** In testing mode "The recording is sent directly to you as an attachment" (**Docs**,
  docs.devin.ai/work-with-devin/testing-and-recordings).
- **Watching.** The session shows an IDE, a shell and the browser or desktop (**Docs**,
  docs.devin.ai/work-with-devin/devin-session-tools). Preview chips open the session's preview in a
  new tab (paraphrase, docs.devin.ai/release-notes/2026, Oct 7). No SSH is offered.
- **Parallel.** A coordinator session starts managed Devins, "each running in its own isolated VM"
  (**Docs**, docs.devin.ai/work-with-devin/advanced-capabilities). No concurrency number is
  published.

### E5. Triggers, identity, network and secrets

- **Identity.** **Open PRs as** picks **Devin**, **User** (the linked account, else Devin) or
  **User only** (paraphrase, docs.devin.ai/integrations/gh).
- **Automations** start sessions from GitHub, Slack or a schedule, at most 50 runs per hour by
  default (**Docs**, docs.devin.ai/product-guides/automations).
- **Network.** On Devin's VMs "Devin enforces a session's network allowlist at the machine level".
  A session resolves its profile at creation and again on each wake or reboot (paraphrase,
  docs.devin.ai/product-guides/security-profiles).
- **Secrets.** Each session gets "a short-lived **identity token**, signed by Devin and refreshed
  for the lifetime of the session" to exchange for cloud credentials (**Docs**,
  docs.devin.ai/product-guides/oidc).

### E6. Outposts: the worker protocol

"An **outpost** is a named queue of Devin sessions to be served on your own machines". A worker
connects out, claims a session and "executes its tool calls locally" (paraphrase,
docs.devin.ai/cloud/outposts/overview). "N workers serve N concurrent" sessions. Desktops need an X
session such as Xvfb. Passwordless `sudo` is meant only for machines "recycled after each session"
(same page).

**Orchestrator protocol** (**Docs**, docs.devin.ai/cloud/outposts/orchestration,
docs.devin.ai/cloud/outposts/reference):

1. **List, then watch** pending sessions over SSE, "the standard Kubernetes-style list-then-watch
   pattern". Delivery is at least once. Streams end within five minutes. The watcher must "Persist
   each event's top-level `cursor`".
2. **Claim** with an `acceptor_id`. A loser gets `409`. A worker must start before
   `status.claim_deadline` or the claim returns to the queue (paraphrase). Only the claim response
   carries `status.connect_token` and `status.gateway_url`.
3. **Start** a machine that runs `devin worker start` with that acceptor ID. It connects out and
   starts executing tool calls (paraphrase).
4. **End.** When the session sleeps or terminates, the worker "exits with status 0 on its own". "If
   your outpost is resumable, snapshot the machine before terminating so you can restore it if the
   session resumes."
5. **Resume.** Queue entries carry `spec.kind`, `new` or `resume`, so a woken session returns to
   the queue (inference from the field).

"Claims are the only coordination primitive". Cognition's devin-outpost-k8s operator implements
this loop (orchestration page). The worker reads `DEVIN_OUTPOST_GATEWAY_URL`,
`DEVIN_OUTPOST_CONNECT_TOKEN` and `DEVIN_OUTPOST_SESSION_ID`. `DEVIN_OUTPOST_DESKTOP=true` adds a
VNC stream that is "lazy on the remote side". `--once` serves one session and exits (reference
page). Each entry's `spec.network_policy` lists hostname globs and CIDRs, but Devin "does not
install firewall rules on your machines" (**Docs**, reference page,
docs.devin.ai/product-guides/security-profiles).

E2B's guide warns: "Do not pause the dispatcher while it needs to accept sessions. Devin queue
traffic cannot wake it" (**Docs**, docs.e2b.dev/agents/devin-outposts).

## F. Google Jules

`cloud-agents.md §B5` covers the VM, setup scripts, Run and Snapshot, the API, Jules Tools and the
lack of a self-hosted mode. The rest:

- **Machine.** "Jules runs each task inside a secure, short-lived virtual machine (VM)" with a 20
  GB disk since Aug 15, 2025 and Docker 28.2.2 (**Docs**, jules.google/docs/environment,
  jules.google/docs/changelog). CPU, memory, root, GPUs and the hypervisor are undocumented.
- **Lifecycle.** Setup scripts can't run dev servers or watchers (paraphrase,
  jules.google/docs/faq). The only idle state is a manual pause (**Docs**,
  jules.google/docs/running-tasks). No auto-sleep, expiry or maximum duration is published.
- **Triggers and identity.** A `jules` label on an issue starts a task. Commit Authoring offers
  **Jules**, **Co-authored** or **User only** (**Docs**, changelog above).
- **Evidence and handoff.** A sample plan step writes a Playwright script and captures a screenshot
  (paraphrase, developers.google.com/jules/api). "Apply Patches Locally" pulls work in progress
  onto the laptop (**Docs**, changelog above).
- **Limits and network.** Free allows 15 tasks a day with 3 at once, Pro 100 with 15 and Ultra 300
  with 60 (**Docs**, jules.google/docs/usage-limits). Tasks run "with internet access" with no
  documented allowlist (**Docs**, FAQ above).

## G. Other products

Only mechanisms the six above don't show. `cloud-agents.md §A` covers Amp in depth. `§B6` and `§B9`
cover the rest briefly.

- **Amp: wake on webhook.** An orb pauses after 5 minutes without agent work and 20 without user
  activity (paraphrase, ampcode.com/docs/orbs/sizes-and-costs). A paused orb keeps its files and
  memory (`cloud-agents.md §A1`). After "a burst of 20 metered orbs" Amp starts one new orb every
  five minutes. Later starts wait instead of failing (**Docs**, paraphrase,
  ampcode.com/docs/markdown/orbs). A webhook request "is stored before Amp returns HTTP 200, then
  Amp wakes the owning orb and calls the handler" (**Docs**, ampcode.com/docs/orbs/event-driven).
  The GitHub integration (page dated 2026-10-12) can "spawn a new orb when someone opens an issue
  or pull request". Amp checks the signatures itself, so no orb waits awake. "`git push` and `gh`
  in the orb run as you" (**Docs**, ampcode.com/docs/github). Desktop and Terminal tabs use WebRTC
  with a relay fallback (paraphrase, ampcode.com/docs/cli/runners).
- **Factory: pause, wake and tunnel.** "Platform-managed Droid Computers auto-pause when idle and
  auto-resume when a new session targets them". `droid computer ssh` "establishes a secure
  WebSocket tunnel through the daemon" and port forwards share it. "Managed Droid Computers are
  woken automatically before the connection is established" (**Docs**,
  docs.factory.com/droid-computers/overview). A user's own machine joins with
  `droid daemon --remote-control` and opens no inbound ports (paraphrase,
  docs.factory.com/remote-control).
- **Kiro: one harness, any client.** The harness is "a standalone process" that clients reach over
  the Agent Client Protocol (paraphrase, kiro.dev/docs/how-kiro-works). "Disconnecting a client
  doesn't stop the agent." A permission request waits for the next client to attach. A user runs at
  most 10 cloud sessions (**Docs**, kiro.dev/docs/cloud-sessions). The strictest network level
  still allows clones and pull requests through Kiro's "gateway service" (**Docs**,
  kiro.dev/docs/web/sandbox/internet-access).
- **Warp: addressed workers.** `oz-agent-worker` "connects to the Automation Platform" and "waits
  for tasks addressed to its worker ID" with no inbound ports. Its Command backend hands each task
  to an external job API. One-shot Direct lets an external scheduler start one worker per task
  (paraphrase, docs.warp.dev/factories/self-hosting). Hosted Linux runners "can also run Docker
  containers and KVM-based virtual machines, such as the Android emulator" (**Docs**,
  docs.warp.dev/factories/runners). "Up to 10 screenshots are attached per pull request" (**Docs**,
  docs.warp.dev/agents/capabilities/computer-use/artifacts-in-prs).
- **Ona: self-checking runner.** The AWS runner is a Fargate service in the customer's VPC that
  checks its connectivity "on startup and every 5 minutes" (**Docs**,
  ona.com/docs/ona/runners/aws/networking). "Prebuilds expire after 5 days". A published prebuild
  loses Ona's own credentials but keeps secrets a build step copied into files (paraphrase,
  ona.com/docs/ona/projects/prebuilds). Veto enforces policy as a Linux Security Module in the
  environment's kernel (paraphrase, ona.com/docs/ona/guardrails/veto).
- **Replit: copy-on-write disks.** Disks are network block devices split into "16 MiB chunks, which
  are then stored immutably in GCS", so "copying a disk is a matter of copying the manifest"
  (**Docs**, replit.com/blog/inside-replits-snapshot-engine). A checkpoint copies the manifest and
  a restore swaps it in. The dev database is PostgreSQL on the same storage, so it forks with the
  code (paraphrase, same post). Background tasks run "in isolated copies of your project", up to 10
  at once on Pro and 64 on Enterprise (paraphrase,
  docs.replit.com/core-concepts/agent/task-system). Agent 3 "runs on its own for up to 200 minutes"
  (**Docs**, replit.com/blog/introducing-agent-3-our-most-autonomous-agent-yet).

## What a host must provide

Each item names the products that need it and the sections with the detail. The URLs are the
primary sources for the key claim.

1. **Boot a task machine from a prepared disk in seconds, with warm copies ready.** Cursor keeps
   "pre-warmed copies of active Builds ready". Devin keeps warm VM pools. Codex, Claude, Jules and
   Ona boot from a saved filesystem too (§A1, §E1, §B1, §C3, §F, §G,
   cursor.com/docs/cloud-agent/builds, cognition.com/blog/what-we-learned-building-cloud-agents).
2. **Rebuild those disks safely and expire them.** Cursor rebuilds on a schedule or a configuration
   change and keeps the active Build when a new one fails. Cursor and Devin keep build secrets out
   of the image. Claude's cache lasts about seven days and Ona's prebuilds five (§A4, §E3, §C3, §G,
   cursor.com/docs/cloud-agent/setup).
3. **Start services on every boot.** No prepared snapshot is documented to keep processes. Cursor
   runs `start` and `terminals` in tmux. Claude uses a SessionStart hook, Codex a Start skill and
   Copilot `services` containers (§A4, §C3, §B3, §D2, cursor.com/docs/cloud-agent/setup).
4. **Pause idle machines with their disk or ideally their memory, then reap them.** Claude pauses
   "with its files saved". Cursor's hibernation and Devin's hypervisor snapshots keep memory.
   Retention is 30 days for Cursor, 7 for Codex and 14 for Devin. Cursor's Kubernetes template
   needs its own reaper (§C1, §A1, §E1, §B1, §A10, code.claude.com/docs/en/cloud-environments).
5. **Wake a paused machine on events, not only on chat.** Devin wakes on CI results, review
   comments and preview opens. Cursor subscriptions wake on CI runs or Slack replies. Amp wakes an
   orb from a stored webhook. Factory wakes a computer before a port forward (§E1, §A1, §G,
   docs.devin.ai/release-notes/2026, ampcode.com/docs/orbs/event-driven).
6. **Keep an outbound-only dispatcher awake.** Cursor, Devin, Claude, GitHub and Warp dispatchers
   watch or poll a vendor queue. Queue traffic can't wake a paused one. Cursor's Lambda template
   gives workers an 8-hour idle policy for the same reason (§A10, §E6, §C7, §C8, §D4, §G,
   docs.e2b.dev/agents/cursor, anysphere/aws-lambda-workers@6c36161 spawn.sh:25-26).
7. **Or accept a webhook that starts workers.** The Codex Agents API waits five minutes for the
   executor its webhook starts. Managed Agents needs a webhook "that Anthropic can reach". Amp
   stores each request before it returns 200 (§B6, §C8, §G,
   developers.openai.com/api/docs/guides/agents-api/environments/lifecycle).
8. **Spawn idempotently within a deadline or keep standbys.** Claude's hook returns within 60
   seconds and is idempotent. Devin has `status.claim_deadline`. Standbys are Cursor's
   `--warm-idle`, Claude's `--min-idle` and Codex's pre-warmed sandboxes (§C7, §E6, §A10, §B6,
   code.claude.com/docs/en/self-hosted-environments-configuration).
9. **Bring a stopped worker back under the same ID with its disk.** Cursor's `claimed_offline` wake
   must beat two timers. Devin queues `resume` entries. E2B's Managed Agents router reuses the
   sandbox. Claude self-hosted, Copilot and the Codex Agents API resume on fresh compute instead
   (§A10, §E6, §C8, §C7, §D1, §B6, cursor.com/docs/cloud-agent/self-hosted/pool).
10. **Run one machine per task and recycle it on a clean exit.** Cursor, Claude and Devin workers
    exit 0 when done. Claude adds a `post-session` hook, push-on-release and retirement. Actions
    and `exec-server` kill leftover processes. Codex needs one executor per session (§A10, §C7,
    §E6, §D4, §B6, code.claude.com/docs/en/self-hosted-environments-configuration).
11. **Keep credentials out of the machine.** Claude's proxy injects the GitHub token and network
    secrets by host. Codex's substitutes network secrets. Cursor proxies git and mints short-lived
    tokens. Cursor and Devin issue OIDC tokens. E2B routes Cursor's git credentials through its
    egress proxy (§C6, §B5, §A9, §A7, §E5, §A10, code.claude.com/docs/en/cloud-environments).
12. **Enforce egress per environment and apply it again on wake.** Cursor, Codex, Claude, Copilot
    and Kiro offer allowlists. Devin re-resolves its profile on each wake. Self-hosted, the
    operator enforces Devin's `spec.network_policy` and Managed Agents' VPC rules (§A9, §B5, §C6,
    §D3, §G, §E5, §E6, docs.devin.ai/product-guides/security-profiles).
13. **Give root, Docker and sometimes KVM.** Claude, Cursor, Copilot and Jules run Docker. Warp's
    runners run KVM guests. Devin wants passwordless sudo only on recycled machines (§C2, §A2, §D2,
    §F, §G, §E6, docs.github.com/en/actions/reference/runners/larger-runners).
14. **Provide a desktop and browser the agent drives, with recording and upload.** Cursor records
    demos to S3. Devin drives a 1024 by 768 desktop and Chrome over CDP. Copilot and Warp attach
    screenshots and Jules captures them. Self-hosted, Cursor needs TigerVNC, `xdotool` and `ffmpeg`
    and Devin needs Xvfb (§A6, §E4, §D3, §F, §G, §E6, docs.devin.ai/work-with-devin/computer-use).
15. **Let humans in over the outbound link.** Cursor shares tmux, the desktop and forwarded ports.
    Factory tunnels SSH and ports through its daemon. Amp uses WebRTC with a relay. Codex
    app-server expects SSH port forwarding (§A5, §G, §B6,
    cursor.com/docs/cloud-agent/self-hosted/computer-use).
16. **Report health and enforce hard caps.** Claude runners serve `/healthz`. Cursor workers
    reconnect after 90 silent seconds. Ona's runner checks its network every 5 minutes. Caps are 59
    minutes for Copilot, 480 for Claude's spawned runners, 8 hours for Cursor's Lambda MicroVMs and
    200 minutes for Replit (§C7, §A10, §G, §D1,
    docs.github.com/en/copilot/concepts/copilot-surfaces/copilot-on-github).

## Implications for Hibernaut

This section is analysis, not research. It maps the list above onto Hibernaut as of main `ccbc18d`
(M12). `cloud-agents.md §E` already maps the product layer: a task API, a web UI, triggers, images
per repo, pull requests, syncing back, preview URLs, more users, capacity, more hosts and the
awake dispatcher. The rows below add what this note's mechanics show. `always-on-agents.md` maps
the webhook ingress and the event inbox.

**Where Hibernaut is ahead**

- Images keep memory. A machine made from `hib image save` starts with its processes running
  (`docs/overview.md:116-118`). Cursor, Codex and Claude document their prepared disks as disk
  only, so services start again on every boot (items 1 and 3).
- A pause keeps memory as well, like Cursor's managed hibernation, Devin and Amp (§A1, §E1, §G).
  Self-hosted, Cursor's Kubernetes template keeps only the disk (§A10). On Hibernaut a worker
  woken under the same ID (item 9) comes back with its process and only has to reconnect
  (inference).
- `hib fork` copies a running machine with its memory (`docs/overview.md:118-121`), and a wake
  takes 0.6 to 0.7 s (`docs/overview.md:210`). Paused forks of a prepared machine could replace
  the warm workers Cursor, Claude and Codex keep running (item 8), since a dispatcher can wake one
  per claim. They cost disk, not memory (inference).
- Credentials stay on the host behind placeholders (`docs/spec.md:113-119`), as with Claude's and
  Codex's proxies (item 11). E2B's firewall blocks private ranges and the metadata endpoint
  (`docs/spec.md:367`).
- SSH, tmux and `ssh -L` already let a person watch and take over (item 15).

**What's missing**

| Need (item above) | Hibernaut today | What it would take | Already planned |
|---|---|---|---|
| A dispatcher that never sleeps (6, 8, 9) | None. An idle worker's machine pauses, its connection drops and queue traffic can't wake it (`cloud-agents.md §E`) | A dispatcher in `hibd`, which stays up. It lists, watches and claims, then forks or wakes one machine per task before the deadline (Claude's 60 s, Devin's `claim_deadline`). On `claimed_offline` or a `resume` entry it wakes the same machine before the window lapses | No |
| A webhook that starts workers, and wakes on CI results or review comments (5, 7) | No public endpoint (`docs/spec.md:35-36`) | The ingress and inbox in `always-on-agents.md` | No |
| One machine per task, recycled at the end (10) | Machines are made and removed by hand | Pausing or removing the machine when its worker exits 0, with a hook after each task | No |
| A short-lived token for each task (11) | Placeholders cover long-lived secrets shared by every machine (`docs/spec.md:113`) | The vendor's token for one claim (Cursor's session token, Devin's `connect_token`) goes only into the machine it serves, which needs secrets bound per machine | No |
| Docker in the machine (13) | The `dev` image has no Docker (`images/dev/packages:15`) | Docker in the image, once it's shown to run on E2B's kernel. Not tried | No |
| A desktop and browser the agent drives, with recordings (14) | No display, browser or recorder in the image | An image with Xvfb or TigerVNC, Chrome, `xdotool` and `ffmpeg`, viewed over `ssh -L`, and a way to get screenshots and videos out | No |
| Rebuilding prepared disks and expiring them (2) | A saved image stays until `hib image rm` (`docs/overview.md:283`) | Builds from a Dockerfile or a setup script, rebuilt on a schedule. A failed build keeps the old image, and build secrets stay out of it | Dockerfile images (`docs/ideas.md:175`) |
| Hard caps and reaping (4, 16) | No limit on run time. A paused machine stays until `hib rm` | A maximum run time per task and an expiry for paused machines. Cursor keeps them 30 days, Devin 14 and Codex 7 | Expiry for the try server (`docs/ideas.md:119`) |
| Egress per environment, applied on every wake (12) | Machines reach the whole internet (`docs/ideas.md:65`) | The allowlist, set per machine and checked by the proxy on each request | G2 (`docs/ideas.md:63`) |

Item 16's health checks would come with the dispatcher, which runs inside `hibd`.

## Gaps

- **Codex Cloud.** The 5-hour limit and per-plan concurrency rest on secondary sources (§B7,
  **Unverified**). Hypervisor, root, Docker, GPUs, loop location and commit identity are
  undocumented.
- **Copilot.** Five to seven concurrent sessions per user comes from a community thread (§D3,
  **Unverified**). The loop's location, the `snapshot` key and log retention are undocumented.
- **Claude Code.** Hosted sessions have no published hypervisor, concurrency number, maximum
  length, pause timer or reclaim timer.
- **Cursor.** VM sizes, the hosted idle timer, a concurrency cap, the claim-wait timeout, the
  commit author and the git proxy design are undocumented. So is what a follow-up gets after the
  30-day snapshot is gone.
- **Devin.** Sizes, root and Docker are undocumented. `spec.kind: resume` is read from the field.
  devin.ai/blog/introducing-devin-outposts, cited by secondary sources, returns 404.
- **Jules.** The FAQ says "Jules is currently in Public Beta" while the changelog said "Jules is
  out of beta!" on Aug 6, 2025.
- **Others.** Amp and Factory don't say where the loop runs. Ona's newest changelog entry is dated
  Aug 31, 2026. Google Antigravity, Imbue Sculptor, Tembo and Cosine weren't researched.
