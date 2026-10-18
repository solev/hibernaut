# Always-on agents, for Hibernaut

**Date checked:** 2026-10-18.

**Summary**

"Grok Bot" most likely means @grok on X, the account through which xAI's Grok answers posts that
tag it. xAI publishes the bot's system prompt on GitHub but not its pipeline. Its own incident
posts describe an unauthorized prompt edit in May 2025 and, in July 2025, "a code path upstream of
the @grok bot" that fed the model existing X posts for 16 hours (§A). A third party builds the
same shape from the X API: poll the mentions timeline, hold a streaming connection or receive
webhooks that must pass a CRC challenge every hour and answer within 10 s. Since February 2026 a
self-serve app may reply through the API only when the post's author summoned it (§A4).

OpenClaw (formerly Clawdbot, then Moltbot) is the other reading of the name and the reference
self-hosted always-on agent. One Gateway process owns every chat connection, the sessions, cron,
heartbeat and memory (§B). Most of its channels connect outward and stay connected (WhatsApp Web,
Discord, Slack Socket Mode, Telegram long polling, Signal), so the Gateway must stay up. Its own
Cloudflare guide says socket channels keep the container awake and only webhook-only installs can
sleep. Its cloud workers show the other half of the pattern: compute suspends after an idle period
and the next message provisions a replacement, while the Gateway keeps transcripts and credentials.

Platforms built for long-lived agents treat an agent as durable state behind an address, not as a
running process (§C). Cloudflare wakes a Durable Object on an HTTP request, a WebSocket message,
an alarm or an email and bills no duration while it hibernates. Temporal, Inngest and Restate keep
timers and waits in the engine, so workers use nothing while they wait. LangSmith Serverless scales
to zero and wakes on the next request. The mention bots of coding-agent vendors (@claude, @codex,
Cursor, Devin, Copilot) never keep an agent awake: the vendor's app receives the event and starts
fresh compute (§D).

Channels split in two (§E). Webhook channels need a public HTTPS endpoint that checks a signature
or shared secret and answers within 3 s (Slack, Discord), 5 s (Linear) or 10 s (GitHub, X). Their
retries last minutes (Slack, unless its Delayed Events option adds 24 hours), hours (Linear) or
days (WhatsApp, OpenAI). GitHub does not retry at all. Socket channels (Discord guild messages,
Slack Socket Mode, WhatsApp Web, Signal, X streams) need a live process that holds the connection.
A few queue while nobody listens: Telegram keeps updates for 24 hours, Discord replays missed
events on Resume and X replays 24 hours. Most bots send their credential in an `Authorization`
header. The exceptions are Telegram's token in the URL path, Discord's gateway token inside a
WebSocket message, the ticket in Slack Socket Mode's WebSocket URL, Postmark's own header and X's
OAuth 1.0a, which signs every request with two secrets (§E2).

This note supplies facts for one question: what a host that pauses idle machines must provide so
always-on agents can live on it. It describes the agents and their channels. Implications for
Hibernaut, near the end, maps those needs onto Hibernaut as of M12.

## Sources

Primary sources read for this note, grouped. Specific pages are cited inline.

- xAI and X: three posts by @xai and @grok on x.com (§A3), the repository `xai-org/grok-prompts`
  at commit `a7c186f5ccac95875c0041aed60398f6ecb6d6c7` with the history of its
  `ask_grok_system_prompt.j2` and the X developer docs at https://docs.x.com (pricing, changelog,
  rate limits, webhooks, filtered stream, authentication, the X Activity API and its migration
  guide), read through the site's `llms-full.txt` and spot-checked live.
- OpenClaw: `openclaw/openclaw` at commit `66485231b24d33c8c2472cad361cd81db37ef0ae` (2026-10-18),
  its `docs/` tree and its published GitHub security advisories, listed with `gh api`.
- Derivatives: `NousResearch/hermes-agent` at `5f274827ffcdf5f0686e7c86d5667b8f8e26a6e6`,
  `nanocoai/nanoclaw` at `93cc70105d582e8a26552bba6dcf95e747febb80` and `cloudflare/moltworker`
  at `7b00c1d7292190f2327e93bdcc7a0eabb0a26123`.
- Platforms: https://developers.cloudflare.com (Agents and Durable Objects),
  https://docs.letta.com, https://docs.langchain.com (LangSmith Deployment and LangGraph),
  https://code.claude.com/docs/en/agent-sdk/hosting, https://developers.openai.com/api/docs
  (background mode, webhooks and the Agents API), https://help.openai.com (ChatGPT scheduled
  tasks), https://docs.temporal.io, https://www.inngest.com/docs and https://docs.restate.dev.
- Coding-agent vendors: `anthropics/claude-code-action` at
  `f27be6b65a4652f08a2ba3cff79114829b588967`, https://claude.com/docs/claude-tag,
  https://code.claude.com/docs/en/slack, https://learn.chatgpt.com/docs/third-party (Codex),
  https://cursor.com/docs/integrations, https://docs.devin.ai/integrations and the Copilot cloud
  agent pages on https://docs.github.com.
- Channels: https://docs.slack.dev, `discord/discord-api-docs` at
  `f7416a2cf165455f72a31f5ea4e671a4f047f2f7`, https://core.telegram.org/bots/api (Bot API 10.3),
  https://developers.facebook.com (Graph API webhooks and the WhatsApp Cloud API),
  `WhiskeySockets/Baileys` at `0af2386292907f7d9742d8d41f830d8c48208fa1`, https://docs.github.com
  (webhooks and GitHub Apps), https://linear.app/developers, `AsamK/signal-cli` at
  `f107b8ff07881fbfe9700287e30e098ca9b17011`, Apple's Messages for Business REST API, Postmark's
  webhook and API docs, RFC 5321 and RFC 7617.
- Companion note: `cloud-agents.md` (Martin's draft of 2026-10-10), cited by section.

News articles served only to find primary pages. The one claim that rests on news alone is marked
**Unverified** and names its source.

**Markers.** **Docs** means read on the vendor's docs page or in a repository's docs at the pinned
commit. **Code** means read in source or in a versioned file at the pinned commit. **Unverified**
means only a secondary source supports the claim. Repository citations use the short form
`owner/repo@sha8 path`. The full commits are listed above.

## A. @grok on X

### A1. Which "Grok Bot"

@grok is the X account through which xAI's Grok replies to posts that tag it. xAI's own post calls
it "the Grok response bot" (**Docs**, https://x.com/xai/status/1923183620606619649). Until
mid-July 2025 the published prompt said "Your X handle is @grok and your task is to respond to
user's posts that tag you on X" (**Code**, `xai-org/grok-prompts@82a9ce73`
`ask_grok_system_prompt.j2`). The current file opens "You are @grok, a version of Grok 4 built by
xAI" (**Code**, `xai-org/grok-prompts@a7c186f5` `ask_grok_system_prompt.j2`).

That fits "Grok Bot" better than OpenClaw, a model-agnostic personal agent that was first called
Clawdbot (§B1). The two are different kinds of always-on agent. @grok is one vendor-run bot on one
public network that answers strangers. OpenClaw is software a person runs for themselves across
many chat networks. Both are covered.

### A2. How a tag becomes a reply

xAI publishes the prompt, not the pipeline. The prompt, at the pinned commit, says this about how
the bot works (**Code**, `xai-org/grok-prompts@a7c186f5` `ask_grok_system_prompt.j2`):

- It has "real-time search tools" and is told to "Use your X tools to get context on the current
  thread" and to view relevant images and video.
- It "must use the browse page to verify all points of information" from search.
- It answers in the language of the post, keeps the reply "under 550 characters", must not tag the
  person it replies to and must not use markdown.

xAI's July 2025 explanation names "a code path upstream of the @grok bot", "independent of the
underlying language model", in which deprecated code "made @grok susceptible to existing X user
posts" for 16 hours (**Docs**, https://x.com/grok/status/1943916977481036128). So a non-model stage
sits in front of the model and pulls posts from X into its context.

Read together, the two sources imply this order. A user tags @grok. A stage upstream of the model
gathers the post and its thread. The model runs with the published prompt and its X search, web
search and browse tools. The answer is posted as a reply from @grok. xAI documents no more than
that. It publishes nothing on how mentions are detected, how they queue, how replies are
rate-limited or whether the bot uses the public API. The "About Grok" page on help.x.com could not
be read for this note (see Gaps).

### A3. Incidents xAI explained

| When | What xAI said happened | What xAI said it changed | Source |
| --- | --- | --- | --- |
| 2025-05-14, about 3:15 AM PST | "an unauthorized modification was made to the Grok response bot's prompt on X" that directed a specific answer on a political topic. "Our existing code review process for prompt changes was circumvented" | Publish the system prompts on GitHub. Add checks so employees can't change the prompt without review. Staff a 24/7 team for bad answers that automated systems miss | **Docs**, https://x.com/xai/status/1923183620606619649 (posted 2025-05-16) |
| July 2025, active 16 hours | The update to the code path upstream of the bot (§A2) exposed it to existing X posts, "including when such posts contained extremist views" | Removed the deprecated code, "refactored the entire system", published the new prompt | **Docs**, https://x.com/grok/status/1943916977481036128 (posted 2025-07-12) |
| July 2025 | Asked its surname, Grok 4 searched the web and took a viral meme name. Asked "What do you think?", it searched for what xAI or Elon Musk had said | "we have tweaked the prompts and have shared the details on GitHub" | **Docs**, https://x.com/xai/status/1945039609840185489 (posted 2025-07-15) |

The prompt history matches the posts (**Code**, `gh api` commit list for
`ask_grok_system_prompt.j2` in `xai-org/grok-prompts`):

- `535aa67a` (2025-07-06) added the line "The response should not shy away from making claims which
  are politically incorrect, as long as they are well substantiated", and `c5de4a14` (2025-07-08)
  removed it.
- `82a9ce73` (2025-07-12) moved the prompt from Grok 3 to Grok 4.
- `e517db8b` (2025-07-15) added a rule that answers must come from independent analysis and not
  from beliefs stated by past Grok, Musk or xAI, the fix the 2025-07-15 post describes.

TechCrunch reports that the faulty July update went live on July 7 at about 11 PM PT
(**Unverified**, TechCrunch, 2025-07-12). xAI's own posts give only the 16-hour duration.

Two facts from these posts carry over to any public bot. The prompt is configuration that changes
behaviour without a model release, and in May 2025 someone changed it outside review. And the
context-gathering stage is an injection path: posts the bot reads shape its answer, which is how
extremist posts reached replies in July 2025.

### A4. Building a mention bot on X today

X API v2 offers four ways to learn about a mention. Prices are pay-per-use, which launched on
2026-02-06 (**Docs**, https://docs.x.com/changelog).

| Path | Endpoint | Auth | Limits | Cost per mention | What must run |
| --- | --- | --- | --- | --- | --- |
| Poll the mentions timeline | `GET /2/users/:id/mentions` | OAuth 2.0 user token or OAuth 1.0a user context | 450 requests per 15 min per app, 300 per user. Returns at most the 800 newest mentions | $0.001 per post returned when the bot account owns the app (Owned Read, from 2026-04-20), else $0.005 | A timer. No inbound endpoint |
| Filtered stream | `GET /2/tweets/search/stream` | App-only Bearer | 1 connection on pay-per-use, 50 connects per 15 min, 1,000 rules | Posts: Read, $0.005 per post | A process holding the HTTP stream |
| X Activity API stream | `GET /2/activity/stream` | App-only Bearer, subscriptions made with the user's token | 2 connections, `backfill_minutes` up to 5, `start_time` and `end_time` for recovery | $0.005 per `post.mention.create` event | A process holding the HTTP stream |
| Webhook (X Activity API, or Account Activity API until 2027-01-01) | Your HTTPS URL | CRC and signatures keyed with the app's secret | 200 within 10 s, CRC hourly, replay of the past 24 hours | $0.005 per `post.mention.create` event | A public HTTPS endpoint |

Sources for the table: rate limits (**Docs**, https://docs.x.com/x-api/fundamentals/rate-limits),
the 800 cap (**Docs**, https://docs.x.com/x-api/posts/timelines/introduction), prices (**Docs**,
https://docs.x.com/x-api/getting-started/pricing and
https://docs.x.com/x-api/activity/event-pricing), stream tiers (**Docs**,
https://docs.x.com/x-api/posts/filtered-stream/introduction), the Activity API stream and replay
(**Docs**, https://docs.x.com/x-api/account-activity/migrate/aaa-to-xaa-migration and
https://docs.x.com/x-api/activity/activity-stream) and webhooks (**Docs**,
https://docs.x.com/x-api/webhooks/introduction).

Details that decide the hosting:

- **Webhooks.** The URL must be HTTPS, publicly reachable and carry no port. It must answer with a
  200 within 10 s and support CRC (**Docs**, https://docs.x.com/x-api/webhooks/introduction). X
  sends a CRC `GET` with a `crc_token` at registration, hourly and on a manual `PUT`. The answer is
  an HMAC-SHA256 of the token keyed with the app's OAuth 2.0 client secret, or the legacy OAuth 1.0
  consumer secret. A webhook that fails CRC is marked invalid and gets no events until it passes
  again. Each event `POST` carries `X-Twitter-Webhooks-Signature-OAuth2` (or the legacy
  `X-Twitter-Webhooks-Signature`), a `sha256=` HMAC of the raw body (**Docs**,
  https://docs.x.com/x-api/webhooks/quickstart). X publishes no retry schedule for failed
  deliveries. `POST /2/webhooks/replay` recovers events from the past 24 hours (**Docs**,
  https://docs.x.com/x-api/webhooks/create-replay-job-for-webhook).
- **Streams.** The filtered stream sends a blank line every 20 seconds, and a client that hears
  nothing for 20 seconds should reconnect (**Docs**,
  https://docs.x.com/x-api/posts/filtered-stream/introduction). Backfill after a disconnect of up
  to 5 minutes and Recovery of the past 24 hours are both "available for Enterprise access" on the
  filtered stream. Without them the fallback is the Search endpoint (**Docs**,
  https://docs.x.com/x-api/fundamentals/recovery-and-redundancy). The Activity API stream offers up
  to 5 minutes of backfill and `start_time` recovery without a stated tier (**Docs**,
  https://docs.x.com/x-api/account-activity/migrate/aaa-to-xaa-migration).
- **The Account Activity API ends on January 1, 2027.** The X Activity API (XAA) replaces it with
  per-event-type subscriptions and an optional stream. It keeps the same webhooks, CRC and
  signatures. XAA takes OAuth 1.0a user tokens today, but "OAuth 1.0a support in XAA will be
  deprecated soon"
  (**Docs**, https://docs.x.com/x-api/account-activity/migrate/aaa-to-xaa-migration).
- **Replies need a summons.** Since 2026-02-23, "Programmatic replies via `POST /2/tweets` are now
  only permitted when the original Post's author has 'summoned' the replier", by @mentioning it or
  quoting one of its posts. This applies to self-serve tiers, not Enterprise (**Docs**,
  https://docs.x.com/changelog). A summoned reply costs $0.010. A post costs $0.015 and a post with
  a URL $0.200 (**Docs**, https://docs.x.com/x-api/getting-started/pricing).
- **Caps.** Pay-per-use is capped at 3 million Post reads per monthly billing cycle. Reads of the
  same resource are deduplicated within a UTC day, "a soft guarantee" (**Docs**,
  https://docs.x.com/x-api/getting-started/pricing).
- **Auth.** OAuth 1.0a user context puts an `Authorization: OAuth ...` header on every request,
  with an HMAC-SHA1 signature over the request built from the consumer secret and the user's token
  secret (**Docs**,
  https://docs.x.com/fundamentals/authentication/oauth-1-0a/creating-a-signature). OAuth 2.0 user
  tokens and app-only tokens travel as `Authorization: Bearer` (**Docs**,
  https://docs.x.com/fundamentals/authentication/overview). Since 2026-09-21 OAuth 1.0a user tokens
  can be exchanged for OAuth 2.0 tokens (**Docs**, https://docs.x.com/changelog).

OpenClaw's X channel is a worked example of these rules (§B3).

## B. OpenClaw and its relatives

### B1. Names

OpenClaw's own history page dates the names: Clawd and Clawdbot from 2025-11-25, Moltbot from
2026-01-27 (after a trademark email from Anthropic) and OpenClaw from 2026-01-30 (**Docs**,
`openclaw/openclaw@66485231` `docs/start/lore.md`). The project is MIT-licensed and had about 392k
GitHub stars on 2026-10-18 (`gh api repos/openclaw/openclaw`).

### B2. The Gateway

One long-lived Gateway process owns every messaging surface and is the only process that opens the
WhatsApp session. It serves a WebSocket control plane on `127.0.0.1:18789`, runs one per host and
is supervised by launchd or systemd (**Docs**, `openclaw/openclaw@66485231`
`docs/concepts/architecture.md`). The VPS guide installs it as a systemd user unit with
`Restart=always` and `RestartSec=5`, keeps the port on loopback and reaches it over an SSH tunnel
or Tailscale Serve (**Docs**, `openclaw/openclaw@66485231` `docs/vps.md`).

Everything that makes the agent always-on runs inside this one process: channel connections,
session queues, cron, heartbeat, memory indexing and tool dispatch. The Gateway's scheduler keeps
one host timer for the next wake. After the host sleeps, a late wake dispatches each due job once
and coalesces missed ticks. Events are not replayed (**Docs**, `openclaw/openclaw@66485231`
`docs/concepts/architecture.md`).

### B3. Channels and how each connects

| Channel | Transport in OpenClaw | Needs inbound public HTTPS | Source (`openclaw/openclaw@66485231`) |
| --- | --- | --- | --- |
| WhatsApp | WhatsApp Web through Baileys, linked by QR only. "The gateway owns the WhatsApp socket and reconnect loop", and a watchdog restarts a transport that goes quiet | No | `docs/channels/whatsapp.md` |
| Telegram | Long polling by default. Webhook mode needs `webhookUrl` and `webhookSecret` and commits each update to a durable ingress queue before answering 200 | Only in webhook mode | `docs/channels/telegram/transports.md` |
| Slack | Socket Mode by default (outbound WSS, bot token plus app-level token). HTTP Request URLs need a public URL and the signing secret. Relay mode: a trusted router holds the single Socket Mode connection and forwards events over an authenticated WebSocket | Only for HTTP Request URLs | `docs/channels/slack/transports.md` |
| Discord | Bot over the official Discord gateway | No | `docs/channels/discord.md` |
| Signal | `signal-cli` over HTTP, as a native daemon (JSON-RPC plus SSE) or the bbernhard REST container, linked by QR or registered by SMS | No | `docs/channels/signal.md` |
| iMessage | `imsg rpc` JSON-RPC over stdio on a signed-in macOS Messages host, or over an SSH wrapper to a remote Mac | No, but needs a Mac | `docs/channels/imessage.md` |
| X | `auto` uses the X Activity API stream when a bearer token is set, else polls mentions. Explicit `stream` or `poll` modes | No | `docs/channels/x.md` |
| SMS | Twilio webhooks to `/webhooks/sms`, signatures validated by default | Yes | `docs/channels/sms.md` |
| Gmail | Google Pub/Sub push through `gog gmail watch serve`, Tailscale Funnel suggested for the endpoint | Yes | `docs/automation/cron-jobs/gmail.md` |
| IMAP | Watches a mailbox without a public webhook | No | `docs/automation/imap.md` |

The X channel shows what a careful mention bot needs (**Docs**, `openclaw/openclaw@66485231`
`docs/channels/x.md`). It uses a confidential OAuth 2.0 app with the scopes `tweet.read tweet.write
users.read offline.access media.write` and stores the rotating refresh token in plugin state with
no promise of encryption at rest. Only allowlisted authors trigger replies, and unknown authors are
dropped silently unless guest mode is on. While streaming it still runs a safety poll every four
poll intervals (240 s by default) and deduplicates by post ID. X API spend (model tokens are
separate) is capped at $100 per UTC day and $1,000 per billing cycle by default. A cap of `0`
blocks paid requests. The worst-case cost of each request is reserved before it is sent. A
`failureRoute` reports failures on another channel such as Discord.

Two channel rules matter for untrusted input. The intro turn after the bot joins a group runs with
no tools, and bot-to-bot exchanges pass a sliding-window guard of 20 events per bot pair per 60 s.
Discord and Slack accept messages from other bots by default (**Docs**,
`openclaw/openclaw@66485231` `docs/channels/index.md` and `docs/channels/bot-loop-protection.md`).

### B4. Sessions, queue and restarts

By default every DM shares the agent's main session (`session.dmScope: main`). The docs recommend
`per-channel-peer`, which isolates each sender on each channel (**Docs**,
`openclaw/openclaw@66485231` `docs/concepts/session.md`). Inbound messages default to `steer` mode
with a 500 ms debounce, a queue cap of 20 and `drop: "summarize"`, so a message that arrives
mid-run is injected into the running turn. The global `main` lane runs up to max(8, CPU
parallelism × 4) turns at once (**Docs**, `openclaw/openclaw@66485231` `docs/concepts/queue.md`).

Conversations, transcripts, scheduled jobs and queued outbound messages live on disk. Every
shutdown, including a plain `SIGTERM`, preserves unfinished turns, and "A Gateway stopped today
resumes that work when it starts tomorrow, without a new user message" (**Docs**,
`openclaw/openclaw@66485231` `docs/gateway/restart-recovery.md`).

### B5. Heartbeat, cron and inbound hooks

The heartbeat is a periodic agent turn in the main session, every 30 minutes by default or every
hour with Anthropic OAuth. It is scheduled by the Gateway's automation scheduler, so with cron
disabled no scheduled heartbeat runs. Event-driven wakes are limited to one per 30 s with a flood
guard after five starts in 60 s. A run is skipped with no model call when the heartbeat's scratch
checklist is effectively empty, and "Heartbeats run full agent turns": `isolatedSession: true`
cuts a run from about 100K tokens of history to about 2K to 5K (**Docs**,
`openclaw/openclaw@66485231` `docs/gateway/heartbeat.md`).

Cron "Automations run inside the Gateway process", and "The Gateway must be running for schedules
to fire." Job state is in SQLite. On startup, overdue jobs are rescheduled rather than replayed and
missed ticks coalesce. A watchdog ends agent-turn jobs after 60 minutes, command jobs after 10 and
script jobs after 5 (**Docs**, `openclaw/openclaw@66485231`
`docs/automation/cron-jobs/how-it-works.md`).

Inbound webhooks (`/hooks`) are off by default. They take a token in the `Authorization` header or
`x-openclaw-token` and reject one in the query string. They answer 200 with `queued` or `coalesced`
and 503 when the session queue is full. The docs say to keep them behind loopback, a tailnet or a
trusted reverse proxy (**Docs**, `openclaw/openclaw@66485231`
`docs/automation/cron-jobs/webhooks.md`).

### B6. Memory

Memory is plain Markdown in the agent workspace (`~/.openclaw/workspace`): `USER.md`, `MEMORY.md`,
daily files `memory/YYYY-MM-DD.md` and `DREAMS.md`. The built-in index is a per-agent SQLite
database with FTS5 and vectors (**Docs**, `openclaw/openclaw@66485231` `docs/concepts/memory.md`
and `docs/concepts/memory-builtin.md`). Dreaming, a background consolidation pass, is on by default
and runs as one managed cron job (**Docs**, `openclaw/openclaw@66485231`
`docs/concepts/dreaming.md`). Compaction summarizes older turns into a compact entry, and session
pruning trims old tool results from the model's context (**Docs**, `openclaw/openclaw@66485231`
`docs/concepts/compaction.md` and `docs/concepts/session-pruning.md`).

### B7. Tools, sandboxing and secrets

Tools, skills and plugins are separate concepts (**Docs**, `openclaw/openclaw@66485231`
`docs/tools/index.md`). Sandboxing is off by default. It has the modes `off`, `non-main` and `all`.
Its scope is per agent, per session or shared. The backends are Docker, Podman, SSH, OpenShell and
Crabbox. The Gateway process always stays on the host and only tool execution moves into the
sandbox, which the docs call "not a perfect security boundary" (**Docs**,
`openclaw/openclaw@66485231` `docs/gateway/sandboxing.md`).

Session permission modes set a filesystem boundary and who reviews an exec escalation: none in
`read-only` (exec denied), a human in `guarded`, an LLM reviewer that can allow, deny or ask a
human in `workspace`, none in `full`. Restricted runs from chat ask the same chat for approval with
native cards or a `/approve <id> allow-once|deny` reply, and free text such as "yes" never counts
(**Docs**, `openclaw/openclaw@66485231` `docs/gateway/permission-modes.md`).

The optional secret egress proxy (off by default) puts a sentinel in the subprocess environment.
A Gateway-owned loopback proxy replaces it in request URLs, headers and streamed bodies
"immediately before egress", bound to exact HTTPS hosts with no wildcards. The stored values sit
unencrypted in `state/openclaw.sqlite` with mode 0600 (**Docs**, `openclaw/openclaw@66485231`
`docs/gateway/secrets/secret-store-and-egress.md`). For GitHub, `tools.github` gives agents a
shared managed `gh` identity, which can be a GitHub App installation (**Docs**,
`openclaw/openclaw@66485231` `docs/gateway/config-tools/github-identity.md`).

### B8. Nodes, pairing and remote access

A node is a companion device (macOS, iOS, watchOS, Android or headless) that connects to the
Gateway with `role: "node"` and exposes commands such as `camera.*`, `device.*` and `system.*`
(**Docs**, `openclaw/openclaw@66485231` `docs/nodes/index.md`). Nodes are peripherals: "channel
messages land on the gateway, not on nodes" (**Docs**, `openclaw/openclaw@66485231`
`docs/concepts/architecture.md`). Pairing is per device. Loopback clients are approved
automatically. Tailnet and LAN clients need approval. The connect handshake signs a challenge
nonce (**Docs**, same file). After device pairing, a separate capability approval decides which
commands a node may expose (**Docs**, `openclaw/openclaw@66485231` `docs/gateway/pairing.md`).

Remote access is a tailnet or an SSH tunnel to the loopback port. Tailscale Serve keeps the Gateway
tailnet-only, and Funnel makes it public but requires a password (**Docs**,
`openclaw/openclaw@66485231` `docs/gateway/remote.md` and `docs/gateway/stable-https-url.md`). For
"Laptops that sleep often but need the agent always-on" the remote guide lists an always-on
Gateway on a VPS or home server in the tailnet (**Docs**, `openclaw/openclaw@66485231`
`docs/gateway/remote.md`).

### B9. Where people run it and what idling costs

| Target | What stays running | Idle cost or sleep rule | Source (`openclaw/openclaw@66485231` unless noted) |
| --- | --- | --- | --- |
| Home machine or VPS | Gateway as a launchd or systemd service | The machine's cost. The VPS guide lists Azure, Daytona, DigitalOcean, exe.dev, Fly, GCP, Hetzner, Hostinger, Northflank, Oracle, Railway, Render, Raspberry Pi and Upstash Box. A macOS VM is needed for iMessage | `docs/vps.md` |
| Fly.io | One Machine with `auto_stop_machines = false` and `min_machines_running = 1` | About $10 to $15 per month for `shared-cpu-2x` with 2 GB | `docs/install/fly.md` |
| Cloudflare Containers (experimental) | Worker, one Durable Object, one `standard-2` Container (1 vCPU, 6 GiB, 12 GB), Litestream to R2 | About $40 to $50 per month always-on, "dominated by provisioned memory". "Socket channels keep the Container awake" and "A small always-on virtual machine is often cheaper". "Webhook-only installations sleep, and a sleeping Container bills nothing": with `OPENCLAW_WEBHOOK_ONLY=true` it stops after ten idle minutes, cold-starts on the next request and comes back with a fresh disk | `docs/install/cloudflare.md` |
| Daytona | Sandbox created with `--auto-stop 0` | Otherwise the sandbox stops when idle | `docs/install/daytona.md` |
| exe.dev | VM behind exe.dev's HTTPS proxy | Pitched as "a cheap always-on Linux host for the Gateway" | `docs/install/exe-dev.md` |
| Upstash Box | A keep-alive Box, dashboard over an SSH tunnel | Not stated | `docs/install/upstash.md` |
| Moltworker (Cloudflare's proof of concept) | OpenClaw in a Cloudflare Sandbox container | `standard-1` around the clock is about $34.50 per month. `SANDBOX_SLEEP_AFTER` defaults to never because cold starts take 1 to 2 minutes | `cloudflare/moltworker@7b00c1d7` `README.md` |

Measured on the Cloudflare template, restoring both SQLite databases into a fresh Container took
about 9 seconds and the Gateway was healthy about 13 seconds after start (**Docs**,
`openclaw/openclaw@66485231` `docs/install/cloudflare.md`).

### B10. Cloud workers: compute that sleeps behind an awake Gateway

OpenClaw can run a session's work on a paired device or on a disposable cloud worker leased
through Crabbox, while "model inference stays proxied through the Gateway" and provider credentials
never reach the remote machine (**Docs**, `openclaw/openclaw@66485231`
`docs/gateway/cloud-sessions.md`). The worker dials the Gateway's public TLS endpoint over an
outbound WebSocket. Setup secrets reach it as opaque placeholders that a protected egress path
replaces at approved HTTPS destinations (**Docs**, `openclaw/openclaw@66485231`
`docs/gateway/cloud-workers.md`).

Two settings make workers sleep (**Docs**, `openclaw/openclaw@66485231`
`docs/gateway/cloud-sessions.md`):

- `suspendAfter: "2h"` stops the worker after the session has been idle that long, after
  reconciling the workspace. "While suspended, you pay for retained snapshot storage only. The next
  message provisions a replacement automatically." Sessions with an active turn, queued messages or
  unreconciled results are skipped and checked again on the next sweep.
- `settings.warmImage` captures a reusable image of the checkout and runtime so the replacement
  starts warm.

The suggested worker profile has a 4-hour lifetime and a 45-minute idle timeout with suspend
(**Docs**, `openclaw/openclaw@66485231` `docs/gateway/cloud-workers.md`). A settled worker process
may stay idle for up to two minutes, at most two per node, to serve follow-up turns (**Docs**,
`openclaw/openclaw@66485231` `docs/gateway/cloud-workers/session-lifecycle.md`).

### B11. Security record and trust model

GitHub lists 722 published security advisories for `openclaw/openclaw` between 2026-01-31 and
2026-09-11: 14 critical, 249 high, 390 medium and 69 low (`gh api
repos/openclaw/openclaw/security-advisories`, read 2026-10-18). Examples that bear on hosting:

- GHSA-g8p2-7wf7-98mq (high): one-click RCE through token exfiltration from `gatewayUrl`.
- GHSA-g55j-c2v4-pjcg, CVE-2026-25593 (high): unauthenticated local RCE through the WebSocket
  method `config.apply`.
- GHSA-g5cg-8x5w-7jpm (critical): heartbeat context inheritance bypassed the sandbox.
- GHSA-jq3f-vjww-8rq7 (high): Telegram webhook bodies were read before the secret was checked.
- GHSA-68v4-hmwv-f43h (medium): media downloads followed cross-origin redirects with
  `Authorization` headers intact.
- GHSA-pchc-86f6-8758, CVE-2026-26316 (high): BlueBubbles webhook auth bypassed through loopback
  proxy trust.

The trust model supports "one trust boundary per gateway": a single operator or a mutually trusting
team. It does not cover a shared agent for mutually untrusted users. "Everyone who can message a
tool-enabled agent shares that agent's delegated tool authority" (**Docs**,
`openclaw/openclaw@66485231` `docs/gateway/security/trust-model.md`). Many agents can run in one
Gateway, each with its own workspace and `agentDir` holding auth profiles and an SQLite session
store. The docs say "Never reuse `agentDir` across agents" (**Docs**, `openclaw/openclaw@66485231`
`docs/concepts/multi-agent.md`).

### B12. Derivatives and competitors

| Project | Process model | Isolation | How it sleeps and wakes | Source |
| --- | --- | --- | --- | --- |
| Hermes Agent (Nous Research, MIT) | One long-running gateway process for Telegram, Discord, Slack, WhatsApp, Signal and email, with built-in cron | Seven terminal backends: local, Docker, SSH, Singularity, Modal, Daytona and Vercel Sandbox | The README says Modal and Daytona environments hibernate when idle and wake on demand. In code that applies to the terminal backend only: Modal snapshots the filesystem and terminates on cleanup, then restores from the snapshot. Daytona stops the sandbox and starts it again. The gateway itself stays up | `NousResearch/hermes-agent@5f274827` `README.md`, `tools/environments/modal.py`, `tools/environments/daytona.py`, `website/docs/developer-guide/gateway-internals.md` (**Code**) |
| NanoClaw (MIT) | One Node host process routes messages into a per-session `inbound.db`, wakes a container running the Claude Agent SDK and delivers from `outbound.db` | A Linux container per session. A credential gateway (OneCLI Agent Vault) injects credentials so agents never hold raw keys. Each Slack agent gets its own Slack app and bot identity | No warm pool. Containers start on demand and the host tears them down when idle. Messages wait as pending while the container is down. The host sweeps every 60 s and kills a container after 30 min of heartbeat silence, to restart on the next inbound message. Scheduled tasks can run a script gate first to avoid waking the agent | `nanocoai/nanoclaw@93cc7010` `README.md`, `docs/architecture.md` (**Docs**), `src/host-sweep.ts`, `src/reconcile-session.ts` (**Code**) |
| Moltworker (Cloudflare, Apache-2.0) | OpenClaw in a Cloudflare Sandbox container behind a Worker, an experimental proof of concept | Container | Sleep is off by default because cold starts take 1 to 2 minutes. R2 backup by cron every 5 minutes | `cloudflare/moltworker@7b00c1d7` `README.md` (**Docs**) |
| Letta Harness (formerly Letta Code) | CLI or App Server process, with channels in a separate server process | Built-in tools run where the harness runs | See §C2 | https://docs.letta.com/self-hosting (**Docs**) |

NanoClaw keeps channel adapters off its main branch: they live on a `channels` branch and install
through `/add-<channel>` skills for WhatsApp, Telegram, Discord, Slack, Microsoft Teams, iMessage,
Matrix, Google Chat, Webex, Linear, GitHub, WeChat and email via Resend (**Docs**,
`nanocoai/nanoclaw@93cc7010` `README.md`).

## C. Platforms built for long-lived agents

These platforms keep an agent's identity and state durable and run compute only when something
arrives. The table answers the four questions of the brief, and the subsections give detail.

| Platform | How an agent wakes | Where state lives between wakes | What an idle agent costs | How it is reached |
| --- | --- | --- | --- | --- |
| Cloudflare Agents SDK (Durable Objects) | HTTP request, WebSocket message, alarm or inbound email | SQLite inside the object: `this.state`, `this.sql`, schedules and fiber checkpoints | No duration billed while hibernated. SQLite storage $0.20 per GB-month past 5 GB. Hibernated WebSocket clients stay connected | A Worker routes to a named instance over HTTP, WebSocket or email routing |
| Letta | Cloud: a message or a server-side schedule timer. Self-hosted: a message to an App Server that is already running | Letta Cloud, or `~/.letta/lc-local-backend` on the host, with a git-versioned MemFS per agent | API plan: $0.10 per active agent per month plus $0.00015 per second of tool execution. Self-hosted: the always-on machine | Agent SDK over one WebSocket (`/ws`), or channel adapters that connect outward |
| LangSmith Deployment | An API request or a cron job. Serverless scales to zero and wakes on the next request | Postgres: threads, runs, cron jobs, per-step checkpoints and the long-term store | Serverless: compute billed while provisioned, including idle time before scale-down. Dedicated: always on | Agent Server HTTP API, with an optional webhook when a run finishes |
| Claude Agent SDK, self-hosted | Your app receives a request and starts or resumes a `claude` subprocess | The container filesystem by default, lost on restart. A `SessionStore` adapter persists transcripts | Whatever the container costs. The hybrid pattern spins containers down when idle | Your container's HTTP or WebSocket endpoint |
| OpenAI Agents API | Input to a session. A webhook asks your handler to start or reconnect the environment | The session on OpenAI's side, files in the environment | The environment compute you keep running | REST API plus webhooks. A self-hosted executor dials out over WSS |
| ChatGPT scheduled tasks | A schedule, or Gmail, Slack or GitHub events in Work | OpenAI's side | Part of the plan, capped at 3 to 15 active tasks | The ChatGPT apps |
| Temporal | A worker polls a task queue. Serverless Workers are started by Temporal | The Temporal Service, which persists timers and workflow state | "Workers consume no additional resources while waiting for a Timer" | A client starts or signals a workflow |
| Inngest | An event or a cron. `serve`: Inngest calls your endpoint. `connect`: your worker holds an outbound WebSocket | Inngest holds the run while it sleeps or waits | `step.sleep` keeps no process running | Send an event to Inngest |
| Restate | An HTTP call to the server's ingress, a typed client or Kafka | The server's journal and its K/V store for Virtual Objects | On FaaS, handlers suspend while they wait | HTTP to `:8080/restate/call/...` |

### C1. Cloudflare Agents SDK and Durable Objects

- **Model.** "Agents are durable identities, not always-on processes." An agent "consumes zero
  compute when hibernated", and the platform wakes it on an HTTP request, a WebSocket message, a
  scheduled alarm or an inbound email and loads its state from SQLite. State, SQL data, schedules
  and fiber checkpoints survive hibernation and restarts, while in-memory variables, timers, open
  fetches and closures do not. `keepAlive()` covers active work measured in minutes and
  `runFiber()` covers work that must recover (**Docs**,
  https://developers.cloudflare.com/agents/concepts/agentic-patterns/long-running-agents/).
- **When it sleeps.** That page draws "idle (~2 min)" before hibernation. The Durable Objects
  lifecycle page says an object hibernates after 10 seconds without a request or event, provided no
  `setTimeout` or `setInterval` callback is set and no request is in flight. An idle object that
  cannot hibernate is evicted after 70 to 140 seconds (**Docs**,
  https://developers.cloudflare.com/durable-objects/concepts/durable-object-lifecycle/).
- **WebSockets.** With the Hibernation API, clients stay connected to Cloudflare's network while
  the object is out of memory. "Hibernation is only supported when a Durable Object acts as a
  WebSocket server. Outgoing WebSockets do not hibernate." An open outbound WebSocket prevents
  eviction for up to 15 minutes, and the connection may stay open after that (**Docs**,
  https://developers.cloudflare.com/durable-objects/best-practices/websockets/). Agents enable
  hibernation by default (**Docs**,
  https://developers.cloudflare.com/agents/runtime/communication/websockets/).
- **Schedules.** `schedule()` takes a delay in seconds, a `Date` or a cron string, and
  `scheduleEvery()` takes an interval in seconds. Tasks live in a SQLite table and Durable Object
  alarms wake the agent. Cron schedules are idempotent (**Docs**,
  https://developers.cloudflare.com/agents/runtime/execution/schedule-tasks/). Each object has one
  alarm, delivered at least once and retried with exponential backoff from 2 s up to six times
  (**Docs**, https://developers.cloudflare.com/durable-objects/api/alarms/).
- **Channels.** Slack events reach a Worker through the Events API. The Worker verifies each
  request and routes by team or enterprise ID to an agent instance with its own state (**Docs**,
  https://developers.cloudflare.com/agents/communication-channels/slack/). The webhook example
  checks GitHub's `X-Hub-Signature-256` against a stored secret before routing to one instance per
  source (**Docs**, https://developers.cloudflare.com/agents/communication-channels/webhooks/).
  Inbound email arrives through an Email Service routing rule at `onEmail()` and replies leave
  through a `send_email` binding. A stable identifier in the reply address routes follow-ups
  (**Docs**, https://developers.cloudflare.com/agents/communication-channels/email/).
- **Replies that survive restarts.** The Think harness's Telegram messenger requires `secretToken`
  in webhook mode. It replies through an idempotent managed fiber. A restart before streaming
  starts replays the answer, and a restart after it starts posts an interruption message rather
  than a duplicate partial answer (**Docs**,
  https://developers.cloudflare.com/agents/harnesses/think/messengers/).
- **Cost.** Requests cost $0.15 per million past 1 million a month, counting WebSocket messages
  (incoming ones at a 20:1 ratio) and alarm invocations. Duration costs $12.50 per million GB-s
  past 400,000 and is billed while the object is active or idle but unable to hibernate, always
  for 128 MB. SQLite storage costs $0.20 per GB-month past 5 GB (**Docs**,
  https://developers.cloudflare.com/durable-objects/platform/pricing/).

### C2. Letta

The Letta Harness (formerly Letta Code) builds stateful agents that work across machines and
messaging platforms, with git-versioned memory (MemFS), schedules and "sleep-time subagents for
reflection and memory organization" (**Docs**, https://docs.letta.com/llms.txt). The memory docs
call this background pass dreaming: subagents review recent conversations and update memory after
a number of steps, on compaction or never (**Docs**, https://docs.letta.com/agent-sdk/memory).

- **Where it runs.** The local runtime runs agents in-process with all state on the device. A
  self-hosted App Server hosts local agents "on a central, always-on machine", and messaging
  channels run in a separate server process. Self-hosted agents "are not automatically backed up"
  (**Docs**, https://docs.letta.com/self-hosting). The App Server exposes one bidirectional
  WebSocket at `/ws`. A computer connected to Letta Cloud instead opens an outbound connection
  while agent state stays in the cloud (**Docs**, https://docs.letta.com/self-hosting/app-server).
- **Channels.** All adapters connect outward: Telegram long polling, Slack Socket Mode, Discord's
  gateway WebSocket, a WhatsApp linked-device WebSocket session and a local `signal-cli` bridge
  (**Docs**, https://docs.letta.com/self-hosting/channels).
- **Schedules.** Cloud schedules fire "Always (server-side timer)" in UTC. Local schedules live in
  `~/.letta/crons.json` and fire "Only while a CLI or server process is open", pausing until Letta
  restarts. A Cloud schedule that targets a connected computer falls back to the Cloud sandbox when
  that machine is offline. Each run starts a new conversation by default (**Docs**,
  https://docs.letta.com/configuration/schedules).
- **Cost.** The Free plan is limited to 3 stateful agents and Pro to 20. The API plan is $20 a
  month plus $0.10 per active agent per month and $0.00015 per second of server-side tool
  execution (**Docs**, https://docs.letta.com/pricing).

### C3. LangSmith Deployment

The brief's "LangGraph Platform" appears in the current docs as LangSmith Deployment, "a workflow
orchestration runtime purpose-built for agent workloads" that is framework-agnostic (**Docs**,
https://docs.langchain.com/langsmith/deployment). Its server is the Agent Server (**Docs**,
https://docs.langchain.com/langsmith/agent-server). I found no page that states the rename.

- **State.** Assistants, threads, runs and cron jobs live in PostgreSQL. Checkpoints are written
  at each step, so an interrupted run resumes from the last one. Redis carries only signaling,
  cancellation and streaming. "At least 1 queue worker must listen to the task queue at any time
  to ensure no runs are orphaned" (**Docs**, same page).
- **Background runs.** A client creates a run and polls its status or joins it. It can pass a
  `webhook` to be called when it finishes (**Docs**,
  https://docs.langchain.com/langsmith/background-run).
- **Cron.** Each tick creates a new thread with the given assistant and sends the same input.
  Schedules are in UTC, and the docs warn to delete unused cron jobs to avoid LLM charges
  (**Docs**, https://docs.langchain.com/langsmith/cron-jobs).
- **Double texting.** A message that arrives while a run is active is handled by one of four
  strategies: enqueue (the default), reject, interrupt or rollback (**Docs**,
  https://docs.langchain.com/langsmith/double-texting).
- **Interrupts.** `interrupt()` pauses a graph and the checkpointer saves state under the thread
  ID. A later `Command` on the same thread resumes it (**Docs**,
  https://docs.langchain.com/oss/python/langgraph/interrupts).
- **Idle cost.** Serverless "scales to zero after a period of inactivity and wakes on the next
  request". Compute is billed while provisioned, including idle time before scale-down, and the
  first request after scale-down is slower. Scale to zero is in beta and its inactivity window may
  change. Dedicated deployments are always on (**Docs**,
  https://docs.langchain.com/langsmith/cloud-platform-features). Existing customers keep the old
  per-run and uptime pricing until 2026-10-01 (**Docs**,
  https://docs.langchain.com/langsmith/billing).

### C4. Claude Agent SDK hosting

Anthropic's hosting guide starts from the process model: the SDK spawns a `claude` CLI subprocess
per session and talks to it over stdio, and that subprocess owns a shell, a working directory and
JSONL session files. Transcripts, memory files and the working directory live on the container's
filesystem. None of them survives a container restart, a scale-down or a move to another node
unless a `SessionStore` adapter or a volume persists them (**Docs**,
https://code.claude.com/docs/en/agent-sdk/hosting).

- **Patterns** (same page). Ephemeral containers per task. Long-running containers for, among
  others, "a chat bot that handles continuous traffic from a platform like Slack", with
  `startup()` to pre-warm subprocesses. Hybrid containers that hydrate from a `SessionStore` and
  spin down during idle periods. One container hosting several agents.
- **Sizing.** The guide calls 1 GiB of RAM, 5 GiB of disk and 1 CPU per agent a reasonable
  starting point. Memory grows with session length and tool activity. Agents per host = (host RAM
  minus overhead) / (per-session RAM ceiling), measured as peak RSS of a representative session
  (**Docs**, same page).
- **Network and secrets.** Outbound HTTPS to `api.anthropic.com`, an inbound HTTP or WebSocket
  port and `ANTHROPIC_BASE_URL` pointed at a proxy that injects the key outside the container. For
  production the guide routes outbound traffic through an egress proxy that enforces domain
  allowlists, injects credentials and logs requests (**Docs**, same page).
- **Alternative.** Managed Agents, where Anthropic hosts the loop and the application sends events
  and receives streamed results (**Docs**, same page).

### C5. OpenAI

- **Background mode.** A response created with `background: true` runs asynchronously and the
  client polls it. With `store=false` the data is kept on disk for about 10 minutes for polling
  (**Docs**, https://developers.openai.com/api/docs/guides/background).
- **Webhooks.** A delivery that gets no 2xx "within a few seconds" is retried "for up to 72 hours
  with exponential backoff". Duplicates are possible and `webhook-id` is the idempotency key.
  Signatures follow the Standard Webhooks scheme with a signing secret (`OPENAI_WEBHOOK_SECRET` in
  the examples) (**Docs**, https://developers.openai.com/api/docs/guides/webhooks).
- **Agents API sessions** (beta). Input to an idle session starts a turn, and webhooks report
  session events such as `agent.session.action_required` and `idle` (**Docs**,
  https://developers.openai.com/api/docs/guides/agents-api/sessions). A self-hosted executor
  registers with an environment ID and a restricted key and connects over WebSocket to
  `wss://codex-cloud-environments.chatgpt.com`. "All connections are outbound" (**Docs**,
  https://developers.openai.com/api/docs/guides/agents-api/environments/self-hosted).
- **Waking compute.** When input needs an environment, the API emits `action_required` with
  `environment_connection` and your webhook handler starts or reconnects the executor. The API
  waits up to five minutes for that connection. "An idle event alone is not a safe shutdown
  signal", and deleting a session neither stops its environment nor emits a webhook (**Docs**,
  https://developers.openai.com/api/docs/guides/agents-api/environments/lifecycle).
- **ChatGPT scheduled tasks.** Tasks run on a schedule. In Work they can also run on Gmail
  messages, Slack channel messages (with @ChatGPT added to the channel) or GitHub pull request
  activity. "Actions that require approval may pause the task." Active task limits are 3 for Free
  and Go, 5 for Plus, 10 for Business and Edu and 15 for Pro and Enterprise (**Docs**,
  https://help.openai.com/en/articles/10291617-scheduled-tasks-in-chatgpt).

### C6. Durable-execution engines: Temporal, Inngest and Restate

- **Temporal.** A worker process polls a task queue on the Temporal Service, runs the tasks it
  dequeues and reports results (**Docs**, https://docs.temporal.io/workers). Timers are persisted
  in the Temporal Service and fire once worker and service are back. "Workers consume no
  additional resources while waiting for a Timer to fire" (**Docs**,
  https://docs.temporal.io/workflow-execution/timers-delays). "With Serverless Workers, Temporal
  starts the Worker": a system workflow scales workers on AWS Lambda, Cloud Run or Bedrock
  AgentCore from task-queue conditions (**Docs**, https://docs.temporal.io/serverless-workers).
  Temporal Cloud bills actions from $50 per million and storage at $0.042 per GB-hour while a
  workflow is open and $0.00105 once closed (**Docs**, https://docs.temporal.io/cloud/pricing).
- **Inngest.** With `serve()` an app exposes one endpoint, usually `/api/inngest`, and "Inngest
  calls that endpoint to run functions and steps" (**Docs**,
  https://www.inngest.com/docs/durable-execution/deploying-functions/serve). With `connect` the app
  opens an outbound persistent connection instead, which requires "a long running server" and
  allows 3 concurrent worker connections on Free and 20 on paid plans (**Docs**,
  https://www.inngest.com/docs/durable-execution/deploying-functions/connect). `step.sleep()`
  resumes a run "without keeping your function process running" for up to a year, within a run
  limit of 30 days on Free, 90 on Pro and 366 on Business (**Docs**,
  https://www.inngest.com/docs/durable-execution/primitives/step-sleep). Approvals use
  `step.waitForEvent()` with a timeout such as 24 hours (**Docs**,
  https://www.inngest.com/docs/durable-execution/durable-agents/human-in-the-loop).
- **Restate.** The Restate Server sits in front of services "similar to a reverse proxy or message
  broker", journals every step and keeps per-key state for Virtual Objects with a single-writer
  guarantee. On FaaS "Restate can suspend functions while they wait for external events" (**Docs**,
  https://docs.restate.dev/foundations/key-concepts). Handlers are invoked over HTTP at the ingress
  (`localhost:8080/restate/call/...` in the examples), through typed clients or from Kafka
  (**Docs**, https://docs.restate.dev/foundations/invocations). Durable timers survive restarts,
  but "Restate does not yet include native cron support", so cron is built from timers and delayed
  messages (**Docs**, https://docs.restate.dev/develop/ts/durable-timers).

## D. Mention bots from coding-agent vendors (trigger path only)

In each product below the vendor's app receives the event and starts compute for that one task.
Nothing the user runs stays awake between mentions. What lasts is the thread on the chat or issue
surface and the vendor's record of the session.

| Product | Trigger | Who receives the event | Where the work runs | What lasts between triggers |
| --- | --- | --- | --- | --- |
| claude-code-action | `@claude` (`trigger_phrase`) in a comment or review. An issue assignment or label can also trigger it (`assignee_trigger`, `label_trigger`) | GitHub, which runs the repository's workflow on `issue_comment`, `pull_request_review_comment`, `issues` and `pull_request_review` | A fresh `ubuntu-latest` Actions runner per job | The issue or PR thread and the branch |
| Claude Code in Slack (the earlier app) | `@Claude` with a coding task | The Claude app for Slack, which detects coding intent | A Claude Code cloud session under the mentioning user's account | The cloud session |
| Claude Tag (public beta) | `@Claude` in a channel or thread, a scheduled routine, a watched channel or a followed pull request | The Claude app for Slack | An ephemeral sandbox per thread, released after a quiet period and rebuilt by the next reply | The thread, its transcript at Anthropic and Claude's notes for each channel |
| Codex | `@codex review` or other `@codex` text in a PR comment, automatic reviews, `@ChatGPT` in Slack, a Linear issue assigned to Codex or `@Codex` in a Linear comment | ChatGPT's GitHub, Slack and Linear integrations | A Codex Cloud task or cloud chat | The cloud chat |
| Cursor | `@cursor` in Slack, a Linear issue delegated to Cursor or `@Cursor` in a Linear comment | Cursor's Slack and Linear integrations | A Cursor Cloud Agent | The agent run |
| Devin | `@Devin` in Slack, a Linear ticket assigned to Devin, a playbook label or a mention | Devin's Slack and Linear integrations | A Devin session | The session |
| Copilot cloud agent | An issue assigned to Copilot, or `@copilot` in a PR comment. `@GitHub` in Slack or a request through the Teams, Linear, Jira or Azure Boards integration. An automation on a schedule or when an issue is created, a PR is opened or a PR is synchronized | GitHub and its integration apps | An ephemeral environment powered by GitHub Actions | The pull request, where a later `@copilot` comment starts a new session |

- **claude-code-action.** The trigger events and inputs come from the example workflow (**Docs**,
  `anthropics/claude-code-action@f27be6b6` `docs/usage.md`). Only users with write access can
  trigger it. GitHub Apps and bots cannot unless listed in `allowed_bots`. When
  `allowed_non_write_users` opens it to others, the docs call for the workflow token, which
  "expires when the job completes". They warn against a personal access token, a static token
  that "could be partially or fully recovered over time via prompt injection" (**Docs**, same
  commit, `docs/security.md`).
- **Claude.** The earlier Claude Code in Slack runs each session under the mentioning user's
  account and answers channel mentions only from Pro and Max accounts in workspaces not connected
  to Claude Tag (**Docs**, https://code.claude.com/docs/en/slack). Claude Tag works in "an
  ephemeral sandbox" that "is discarded when the conversation goes idle" (**Docs**,
  https://claude.com/docs/claude-tag/overview). After a quiet period the sandbox is released while
  the thread persists, and a new reply rebuilds it. A session also starts when a scheduled
  routine runs, and a task can follow a pull request and act when it changes. Claude keeps notes
  for each channel. A channel's top-level session is replaced after about an hour without
  activity (**Docs**, https://claude.com/docs/claude-tag/concepts/how-it-works).
- **Codex.** Its integration pages moved from `developers.openai.com/codex/integrations/` to
  `learn.chatgpt.com/docs/third-party/` (redirects observed on 2026-10-18). In a PR, `@codex
  review` gets a review and any other `@codex` request starts "a legacy cloud chat" with the PR as
  context (**Docs**, https://learn.chatgpt.com/docs/third-party/github). In Slack the entry point
  is `@ChatGPT`, which can send repository work to a separate Codex Cloud task if the workspace
  has a published environment (**Docs**, https://learn.chatgpt.com/docs/third-party/slack). In
  Linear, assigning or mentioning Codex creates a cloud chat that replies with progress (**Docs**,
  https://learn.chatgpt.com/docs/third-party/linear).
- **Cursor and Devin.** `@cursor` with a prompt starts a Cloud Agent (**Docs**,
  https://cursor.com/docs/integrations/slack). A Linear issue reaches Cursor through the assignee
  field or an `@Cursor` comment (**Docs**, https://cursor.com/docs/integrations/linear). Devin
  "responds in-thread" when tagged (**Docs**, https://docs.devin.ai/integrations/slack) and takes
  Linear tickets by assignment, playbook labels, mentions and automation triggers (**Docs**,
  https://docs.devin.ai/integrations/linear).
- **Copilot.** It works in "its own ephemeral development environment, powered by GitHub Actions"
  (**Docs**,
  https://docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-cloud-agent/customize-the-agent-environment).
  An assignment sends the issue's title, description and existing comments. After that Copilot
  "will not be aware of, and therefore won't react to, any further comments" on the issue. An
  `@copilot` comment on any pull request starts a new session, and Copilot answers only people
  with write access (**Docs**,
  https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/use-cloud-agent-on-github).
  The Slack integration (**Docs**,
  https://docs.github.com/en/copilot/how-tos/copilot-integrations/integrate-cloud-agent-with-slack)
  starts a session when someone mentions `@GitHub`. In a shared thread or channel Copilot creates
  artifacts under its app identity. In a DM it uses the user's linked GitHub account. Teams,
  Linear, Jira and Azure Boards have integrations too (**Docs**,
  https://docs.github.com/en/copilot/concepts/agent-capabilities/choosing-and-delegating-agent-work/about-copilot-integrations).
  Automations run on a schedule (hourly, daily or weekly) or when an issue is created, a PR is
  opened or a PR is synchronized. Each run bills Actions minutes and AI Credits to the user who
  created the automation. Its pull requests are attributed to that user, who therefore cannot
  approve them (**Docs**,
  https://docs.github.com/en/copilot/concepts/agent-capabilities/agent-powered-workflows-and-operations/about-automations).

Agents that run on the customer's own compute work the other way round. A process on that compute
polls the vendor for work, as `cloud-agents.md §E` describes under "Runners need an awake
dispatcher".

## E. The channel transport matrix

A channel reaches a bot in one of three ways. It calls the bot (a webhook). The bot calls the
channel and keeps the line open (a socket or a held HTTP stream). Or the bot asks again and again
(polling). A webhook reaches a receiver only if something answers at a public address within the
channel's deadline. E1 lists delivery and queueing. E2 lists where credentials travel. E3 gives
the sources.

### E1. Delivery, deadlines and queueing

| Channel | Delivery modes | What a webhook endpoint must do | Ack deadline | Retries | What waits while the receiver is down |
| --- | --- | --- | --- | --- | --- |
| Slack | Events API over HTTPS or Socket Mode (outbound WebSockets, up to 10 at once) | Public HTTPS. Echo the `url_verification` challenge at setup. Check `X-Slack-Signature` and reject timestamps more than 5 min off | HTTP: 2xx within 3 s. Socket: an ack with the `envelope_id` of each event | 3 retries: nearly immediately, after 1 min and after 5 min. The Delayed Events option adds hourly retries for 24 h. Subscriptions are disabled above 95% failures in 60 min, except for apps under 1,000 events an hour | A few minutes of retries, or 24 h with Delayed Events. By default an event more than 2 h late is not delivered |
| Discord | Gateway, an outbound WebSocket and the only path for guild messages. Interactions over the Gateway or an HTTPS endpoint, not both. HTTPS webhook events for a short list of app events (E3) | Answer `PING`. Check `X-Signature-Ed25519` and `X-Signature-Timestamp` with the app's public key | Interactions: first response within 3 s. Webhook events: 204 within 3 s | Webhook events: backoff for up to 10 min. Interactions: none stated | Gateway: Resume replays events since the last sequence number if the app reconnects in time. The window is not stated |
| Telegram | Long polling (`getUpdates`) or a webhook, mutually exclusive | HTTPS on port 443, 80, 88 or 8443. Optionally check `X-Telegram-Bot-Api-Secret-Token` | Not stated. A non-2xx status fails | Until "a reasonable amount of attempts" | Updates for up to 24 h, in either mode |
| WhatsApp Cloud API | Webhooks only | HTTPS with a valid certificate, not self-signed. Echo `hub.challenge` when `hub.verify_token` matches. Check `X-Hub-Signature-256`. mTLS optional | Not stated. Respond with 200 | Decreasing frequency for up to 7 days (36 h on the generic Graph API page). Retries can duplicate | Up to 7 days of retries |
| WhatsApp Web clients (Baileys) | An outbound WebSocket as a linked second device | None | None | None | Not documented by Baileys |
| X | Mentions polling, a held HTTP stream (the filtered stream or the X Activity API stream) or webhooks (§A4) | Public HTTPS with no port. CRC at registration and hourly. Check the signature header | 200 within 10 s | No published schedule | 24 h through a replay job (webhooks), 5 min of Activity API stream backfill, the 800 newest mentions by polling |
| GitHub | Webhooks (repository, organization or app) or REST polling | A URL GitHub can reach. Check `X-Hub-Signature-256` | 2xx within 10 s | None. Redelivery by hand or through REST | Failed deliveries stay redeliverable for 3 days (7 in the same page's introduction) |
| Inbound email | SMTP to the domain's MX, or a provider that POSTs parsed mail (Postmark) or runs code (Cloudflare `onEmail`) | Postmark: a reachable URL. Postmark does not sign, so the URL carries Basic auth and the receiver can allowlist Postmark's IPs | Postmark: a 200 | SMTP senders: every 30 min or more, for 4 to 5 days. Postmark: 10 retries from 1 min to 6 h, about 10 h in all, stopped by a 403 | The sending server's queue for days. Postmark's retries for about 10 h, then a manual retry |
| Linear | Webhooks, including agent session events | Public HTTPS, not localhost. Check `Linear-Signature` | 200 within 5 s. An agent also sends an activity within 10 s of `created` | 3 retries: after 1 min, 1 h and 6 h. A webhook that stays unresponsive may be disabled | Several hours of retries |
| Signal (signal-cli) | A third-party client, registered or linked, run as a daemon (JSON-RPC or D-Bus) or with `receive` | None | None | None | Not documented by signal-cli. The protocol expects regular receiving |
| iMessage | `imsg rpc` over stdio on the signed-in Mac (OpenClaw). Apple Messages for Business through a Messaging Service Provider's REST platform | Business: verify the HS256 JWT in `Authorization` | Not checked | Not checked | OpenClaw replays messages missed while its bridge or Gateway was down |

### E2. Where the credentials travel

| Channel | Outbound API calls | Persistent connection | Secret that verifies inbound calls | Credential only in an HTTPS `Authorization` header? |
| --- | --- | --- | --- | --- |
| Slack | `Authorization: Bearer` with the bot token, or a `token` field in a form POST body. Never the query string | `apps.connections.open` with the `xapp-` app token in `Authorization`, then a `wss://` URL whose `ticket` parameter admits the socket | The signing secret (HMAC-SHA256 over `v0:timestamp:body`). Socket events need no check | Web API: yes. Socket Mode: no, the ticket rides in the WebSocket URL |
| Discord | `Authorization: Bot <token>`. Interaction replies carry the interaction token in the URL path | The bot token inside the Identify payload (opcode 2) and the Resume payload (opcode 6), sent after the upgrade | The app's Ed25519 public key | No. The gateway token travels inside a WebSocket message |
| Telegram | The token in the URL path, `/bot<token>/METHOD_NAME`, and in file links, `/file/bot<token>/<file_path>` | Long polling is `getUpdates` with the same path token | `secret_token`, sent back in `X-Telegram-Bot-Api-Secret-Token` | No. The token is in the path |
| WhatsApp Cloud API | `Authorization: Bearer` with the access token, to `graph.facebook.com` | None | The App Secret (`X-Hub-Signature-256`), plus `verify_token` at setup | Yes |
| WhatsApp Web (Baileys) | None. The client holds the linked device's keys, which `useMultiFileAuthState` saves to a folder | The WebSocket session those keys authenticate | None | No. Session keys, not a token |
| X | OAuth 1.0a: `Authorization: OAuth` with an HMAC-SHA1 signature of each request, keyed by the consumer secret and the token secret. OAuth 2.0 and app-only: `Authorization: Bearer` | Streams: a held HTTPS GET with the app-only Bearer token | The OAuth 2.0 client secret (or the legacy consumer secret) keys CRC and event signatures | Bearer: yes. OAuth 1.0a: no, each header is a fresh signature that needs both secrets |
| GitHub App | `Authorization: Bearer` with an installation token (1 h). Minting one takes a JWT signed with the app's private key (RS256, at most 10 min). Git over HTTPS takes the token as the HTTP password | None | The webhook secret (`X-Hub-Signature-256`) | REST: yes once a token exists, but minting needs the private key. Git sends Basic credentials, base64 inside `Authorization` |
| Linear | API key as `Authorization: <API_KEY>` with no scheme. OAuth tokens as `Authorization: Bearer` | None | The webhook signing secret (`Linear-Signature`) | Yes |
| Email (Postmark) | The `X-Postmark-Server-Token` header | None | No signature. A Basic auth user and password set in the webhook URL | No. A custom header |
| Signal (signal-cli) | None. The account's password and keys live under `$XDG_DATA_HOME/signal-cli/data/` | The client's own session with Signal's servers | None | No. Account keys, not a token |
| iMessage | None on the `imsg` path. The Mac's signed-in account sends | stdio to `imsg rpc` | Business: the Messaging API secret, which signs an HS256 JWT sent in `Authorization` both ways | No. Business needs a JWT signed at least hourly |

### E3. Sources and details

- **Slack.** Delivery modes, the 3 s deadline, retries, the disable rule, Delayed Events and the
  cap of 30,000 events per workspace per app per hour (**Docs**,
  https://docs.slack.dev/apis/events-api). Socket Mode needs no public Request URL. Its URL
  refreshes "once every few hours" with a warning about 10 s before, an app may hold up to 10
  sockets and socket apps are "not currently allowed in the public Slack Marketplace" (**Docs**,
  https://docs.slack.dev/apis/events-api/using-socket-mode). Signing (**Docs**,
  https://docs.slack.dev/authentication/verifying-requests-from-slack). "Tokens cannot be sent as
  a query parameter", and JSON bodies need the bearer header (**Docs**,
  https://docs.slack.dev/apis/web-api).
- **Discord.** REST uses `Authorization: Bot <token>` (**Docs**,
  `discord/discord-api-docs@f7416a2c` `developers/reference.mdx`). On the Gateway, Hello gives
  the heartbeat interval and Ready gives a `resume_gateway_url`. Resume replays "any lost events
  starting from the last sequence number", but an app that "won't reconnect in time to Resume"
  gets Invalid Session and must Identify again. Identify is limited to 1,000 calls per 24 hours
  across all shards. Hitting the limit ends every session and resets the bot token (**Docs**,
  same commit, `developers/events/gateway.mdx`). Identify carries the token in its payload
  (**Docs**, `developers/events/gateway-events.mdx`). Interactions arrive one way only, need a
  first response within 3 s and carry a token valid for 15 minutes (**Docs**,
  `developers/interactions/receiving-and-responding.mdx`). The endpoint must answer `PING` and
  check the Ed25519 headers (**Docs**, `developers/interactions/overview.mdx`). HTTPS webhook
  events cover only app authorization, entitlements, quest enrollment, lobby messages and game
  direct messages, with a 204 due in 3 s and retries for up to 10 minutes (**Docs**,
  `developers/events/webhook-events.mdx`).
- **Telegram.** The token sits in the URL path of every call and every file link. Long polling and
  webhooks are "mutually exclusive", and updates "will not be kept longer than 24 hours". A failed
  webhook is retried until Telegram gives up "after a reasonable amount of attempts". The optional
  `secret_token` comes back in `X-Telegram-Bot-Api-Secret-Token` (**Docs**,
  https://core.telegram.org/bots/api, Bot API 10.3 of 2026-08-24).
- **WhatsApp.** The send-messages guide (**Docs**,
  https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/send-messages)
  calls the Cloud API with `Authorization: Bearer`. Endpoints need a valid certificate
  ("Self-signed certificates are not supported"), the `verify_token` handshake and
  `X-Hub-Signature-256` checked with the App Secret. The generic page drops deliveries after 36
  hours and offers mTLS (**Docs**,
  https://developers.facebook.com/docs/graph-api/webhooks/getting-started). The WhatsApp page says
  Meta retries "for up to 7 days", that retries "can result in duplicate webhook notifications"
  and that payloads reach 3 MB (**Docs**,
  https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/overview).
  Baileys is "a WebSockets-based TypeScript library" that logs in as a second WhatsApp client by
  QR or pairing code. It is not affiliated with WhatsApp and discourages "bulk or automated
  messaging usage" (**Docs**, `WhiskeySockets/Baileys@0af23862` `README.md`).
- **X.** Endpoints, prices, CRC, signatures and replay are in §A4. X documents no retry schedule
  for webhooks.
- **GitHub.** A delivery needs a 2XX within 10 seconds (**Docs**,
  https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks). "GitHub
  does not automatically redeliver failed deliveries" (**Docs**,
  https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries). The
  redelivery page says "the past 7 days" in its introduction and "the past 3 days" below (**Docs**,
  https://docs.github.com/en/webhooks/testing-and-troubleshooting-webhooks/redelivering-webhooks).
  Signatures use the webhook secret (**Docs**,
  https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries). An
  installation token goes in the `Authorization` header and "will expire after 1 hour". Git over
  HTTPS takes it "as the HTTP password" (**Docs**,
  https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation).
  Requesting a token takes a JWT "signed using the `RS256` algorithm" with the app's private key,
  expiring "no more than 10 minutes into the future" (**Docs**,
  https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app).
  Basic credentials are the base64 of `user:password` in `Authorization: Basic` (**Docs**,
  https://www.rfc-editor.org/rfc/rfc7617). Notification polling must obey `X-Poll-Interval`
  (**Docs**, https://docs.github.com/en/rest/activity/notifications).
- **Linear.** Endpoints must be "publicly accessible HTTPS, non-localhost". A delivery fails if the
  server is down, slower than 5 seconds or not 200. Retries come after 1 minute, 1 hour and 6
  hours, and an endpoint that stays unresponsive "might be disabled" (**Docs**,
  https://linear.app/developers/webhooks). An agent must answer its webhook within 5 seconds and
  send an activity within 10 seconds of a `created` event "to avoid the session being marked as
  unresponsive" (**Docs**, https://linear.app/developers/agent-interaction). Agents are app users
  that people mention or assign as a delegate (**Docs**, https://linear.app/developers/agents). API
  keys go in `Authorization` with no scheme, OAuth tokens as Bearer (**Docs**,
  https://linear.app/developers/graphql).
- **Email.** RFC 5321 says the retry interval SHOULD be at least 30 minutes and the give-up time
  generally at least 4 to 5 days (**Docs**, https://www.rfc-editor.org/rfc/rfc5321, section
  4.5.4.1). Postmark retries a failed inbound POST 10 times (1, 5, 10, 10, 10, 15 and 30 minutes,
  then 1, 2 and 6 hours) and stops at a 403. Then the message shows as Inbound Error for a manual
  retry (**Docs**, https://postmarkapp.com/developer/webhooks/inbound-webhook). Postmark does not
  sign webhooks and suggests Basic auth in the URL plus an IP allowlist (**Docs**,
  https://postmarkapp.com/developer/webhooks/webhooks-overview). Its API takes the server token in
  `X-Postmark-Server-Token` (**Docs**, https://postmarkapp.com/developer/api/overview). Cloudflare
  routes mail to an agent's `onEmail` (§C1).
- **Signal.** signal-cli is a third-party command-line client built on "a patched
  libsignal-service-java" and "primarily intended to be used on servers to notify admins". It
  registers a number or links to an account, runs as a JSON-RPC or D-Bus daemon and keeps its keys
  under `$XDG_DATA_HOME/signal-cli/data/`. The protocol "expects that incoming messages are
  regularly received", and official clients "expire after three months", so old releases may
  break (**Docs**, `AsamK/signal-cli@f107b8ff` `README.md`).
- **iMessage.** OpenClaw's Gateway spawns `imsg rpc` and "speaks JSON-RPC over stdio", with no
  separate daemon or port. Gateway and `imsg` share the signed-in Messages Mac, or the Gateway
  reaches it through an SSH wrapper. Setup grants Full Disk Access and Automation. After a bridge
  or Gateway restart OpenClaw replays the messages missed while it was down and dedupes them
  (**Docs**, `openclaw/openclaw@66485231` `docs/channels/imessage.md`). Apple Messages for
  Business reaches a business through a Messaging Service Provider (MSP) platform. Both
  directions carry an HS256 JWT in `Authorization`, signed with the Messaging API secret and
  generated "at least once every hour" (**Docs**,
  https://register.apple.com/resources/messages/msp-rest-api/, last updated 2026-06-03).

## F. Operating concerns

How the systems above handle the operating side of an always-on agent. Facts cited earlier are
referenced by section, and new sources are cited in full.

| Concern | What the systems in this note do | Sources |
| --- | --- | --- |
| Prompt injection from public input | In July 2025 existing X posts reached @grok's replies through a code path upstream of the model. OpenClaw's guidance says injection "does not require public DMs", since any web page, email or attachment the agent reads can carry instructions. It advises allowlists for DMs, mention gating in groups, sandboxed tools and the best model tier for agents with tools. Untrusted content can go through a read-only reader agent first. OpenClaw also runs a group's intro turn with no tools and limits bot-to-bot exchanges. claude-code-action and `@copilot` in pull requests respond only to people with write access | §A3, **Docs** `openclaw/openclaw@66485231` `docs/gateway/security/prompt-injection.md`, §B3, §D |
| Per-agent identity | Claude Tag has "its own account in each system it works in": the Claude app in Slack, the Claude GitHub App and a service account in every other connected tool. Its actions are attributed to those accounts, and other tools show them in their audit logs. A Linear agent installs with `actor=app` as an app user rather than as the installing user, which takes admin permissions. Each NanoClaw Slack agent gets its own Slack app and bot identity. OpenClaw gives each agent an `agentDir` that must never be shared. Copilot acts under its app identity in shared Slack threads and under the user's linked account in DMs. A Copilot automation's pull requests are attributed to its creator | **Docs** https://claude.com/docs/claude-tag/concepts/agent-identity, **Docs** https://linear.app/developers/agents, §B11, §B12, §D |
| Credentials and egress | A Claude Tag thread runs in a sandbox that holds no stored credentials. Agent Proxy injects credentials at the network boundary and blocks any request that no rule allows. Private IP ranges and cloud metadata endpoints stay blocked regardless. The proxy carries only HTTP and HTTPS, so SSH cannot cross it. An edit to a credential or domain entry applies within about a minute, in running threads too. Anthropic's hosting guide and NanoClaw also keep keys outside the agent, and OpenClaw can. §E2 lists the channel credentials that travel outside an `Authorization` header | **Docs** https://claude.com/docs/claude-tag/concepts/agent-identity, **Docs** https://claude.com/docs/claude-tag/admins/add-connections, §C4, §B7, §B10, §B12, §E2 |
| Approvals over chat | OpenClaw asks the originating chat through native cards or `/approve <id>`, and free text such as "yes" never counts. LangGraph's `interrupt()` saves a paused run until a `Command` resumes it. Inngest's `step.waitForEvent()` waits for an approval event with a timeout. ChatGPT tasks may pause on actions that require approval. The creator of a Copilot automation cannot approve its pull requests | §B7, §C3, §C6, §C5, §D |
| Budgets and rate limits | Claude Tag bills channel work to a usage balance under a spend limit an Owner sets. OpenClaw caps X API spend per UTC day and per billing cycle and reserves each request's worst-case cost first. OpenClaw's heartbeat runs a full agent turn every 30 minutes by default and skips the model call when its checklist is effectively empty. LangSmith says to delete unused cron jobs to avoid model charges. ChatGPT caps active tasks at 3 to 15 depending on the plan. Platform quotas: 3 million X Post reads a billing cycle on pay-per-use, 30,000 Slack events per workspace an hour for each app and 1,000 Discord Identify calls a day for each bot | **Docs** https://claude.com/docs/claude-tag/overview, §B3, §B5, §C3, §C5, §A4, §E3 |
| Kill switches | OpenClaw's `/stop` cancels only that agent's work in the selected conversation. Its cron watchdog ends agent turns after 60 minutes. NanoClaw kills a container after 30 minutes of heartbeat silence. Claude Tag's `!restart` archives a session at once and starts a fresh one that rereads the thread | **Docs** `openclaw/openclaw@66485231` `docs/concepts/session.md`, §B5, §B12, **Docs** https://claude.com/docs/claude-tag/concepts/how-it-works |
| Observability | Claude Tag keeps everything Claude read and said in a session transcript "on Anthropic's side". Deleting Slack messages does not remove them from it, and an edited message keeps both versions. OpenClaw keeps transcripts on disk and writes one rolling Gateway log a day under `/tmp/openclaw/` by default. Anthropic's guide has its egress proxy log requests. Copilot marks a comment it acts on with an eyes reaction and adds a "Copilot has started work" event to the pull request timeline | **Docs** https://claude.com/docs/claude-tag/concepts/how-it-works, §B4, **Docs** `openclaw/openclaw@66485231` `docs/gateway/logging.md`, §C4, **Docs** https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/use-cloud-agent-on-github |
| Memory growth | OpenClaw compacts older turns, prunes old tool results from the model's context and consolidates memory in a background dreaming job. An isolated heartbeat cuts a run from about 100K tokens of history to 2K to 5K. Letta's sleep-time subagents review recent conversations and update memory. Claude Tag replaces a channel's top-level session after about an hour without activity and keeps notes for each channel. Process memory grows with session length and tool activity, so Anthropic sizes hosts by peak RSS | §B6, §B5, §C2, §D, §C4 |
| Many agents per host | Anthropic: agents per host = (host RAM minus overhead) / (per-session RAM ceiling), from a 1 GiB floor. OpenClaw runs many agents in one Gateway within one trust boundary and up to max(8, CPU parallelism × 4) turns at once. NanoClaw runs a container per session. Agents behind one bot share its connection limits: one filtered-stream connection on X pay-per-use, two Activity API streams, ten Slack sockets and one Discord Identify budget | §C4, §B4, §B11, §B12, §A4, §E3 |

## What an always-on agent needs from its host

Each item names the systems in this note that need it. Section references lead to the primary
sources cited there.

1. **Answer a webhook within its deadline, even from a cold start.** Slack and Discord allow 3 s,
   Linear 5 s, GitHub and X 10 s (§E1). OpenClaw's Cloudflare template was healthy about 13 s
   after start, and Moltworker's cold starts take 1 to 2 minutes (§B9, §B12).
2. **Expose a public HTTPS endpoint that verifies every call and stays registered.** Slack's HTTP
   mode, Discord's HTTPS interactions, Telegram webhooks, the WhatsApp Cloud API, X webhooks,
   Linear and OpenClaw's SMS and Gmail hooks need one. GitHub and Postmark ask only for a URL they
   can reach (§E1, §B3). WhatsApp refuses self-signed certificates, X refuses a port in the URL and
   Telegram takes only ports 443, 80, 88 and 8443 (§E1). X repeats its CRC check every hour (§A4).
   Slack disables event subscriptions that fail more than 95% of deliveries in 60 minutes, unless
   the app gets under 1,000 events an hour. Re-enabling them is a manual step (**Docs**,
   https://docs.slack.dev/apis/events-api).
3. **Hold outbound connections while the agent sleeps and keep their sessions valid.** Discord
   guild messages, Slack Socket Mode, WhatsApp Web, Signal, X streams and Telegram long polling
   need a running client (§E1). So do Letta's channels, Inngest `connect` and OpenAI's self-hosted
   executor (§C2, §C5, §C6). Cloudflare cannot hibernate an outgoing WebSocket (§C1). Discord
   expects heartbeats, replays missed events only on a timely Resume and resets the token after
   1,000 Identify calls in a day (§E3). X's filtered stream expects a reconnect after 20 s of
   silence (§A4). Signal expects regular receiving, and official clients expire after three
   months (§E3).
4. **Queue events in something that stays awake, then wake the agent.** NanoClaw's host process
   stores each message and starts the session's container (§B12). OpenClaw's Gateway replaces a
   suspended worker on the next message (§B10). Cloudflare holds hibernated WebSocket clients
   while the object is out of memory (§C1). OpenAI waits up to five minutes for an environment to
   connect (§C5). In §D the vendor's app receives each event and starts compute for the task.
5. **Catch up after downtime and drop duplicates.** Missed updates wait 24 h at Telegram, and X
   replays 24 h of webhook events. WhatsApp retries for up to 7 days (36 h on one Meta page).
   GitHub never retries (§E1). WhatsApp and OpenAI may deliver twice (§E3, §C5). OpenClaw dedupes
   X posts by ID and replayed iMessages (§B3, §E3).
6. **Wake on a schedule.** OpenClaw's heartbeat and cron fire only while its Gateway runs, and
   Letta's local schedules only while a Letta process is open (§B5, §C2). Durable Objects,
   Temporal, Inngest, LangSmith, Letta Cloud, ChatGPT, Copilot automations and Claude Tag routines
   keep the timer in the platform (§C, §D). After a host sleep OpenClaw runs each due job once and
   does not replay missed ticks (§B2).
7. **Keep state across restarts and resume unfinished work.** OpenClaw keeps transcripts, jobs and
   queued outbound messages on disk and resumes unfinished turns (§B4). The Claude Agent SDK needs
   a `SessionStore` or a volume (§C4). Durable Objects keep SQLite but lose in-memory state (§C1).
   LangSmith checkpoints every step (§C3). Letta does not back up self-hosted agents (§C2).
8. **Give each agent its own identity.** Claude Tag and Linear agents get their own accounts,
   NanoClaw a Slack app per agent and OpenClaw an `agentDir` per agent (§F). OpenClaw's trust
   model covers one trust boundary per Gateway, not mutually untrusted users (§B11).
9. **Keep secrets outside the agent and filter its egress.** Claude Tag, Anthropic's guide and
   NanoClaw inject keys at egress, and OpenClaw can (§F). Some credentials skip the
   `Authorization` header or must be computed per request: Telegram (URL path), Discord's gateway
   (Identify payload), Slack Socket Mode (WebSocket URL ticket), Postmark
   (`X-Postmark-Server-Token`), X OAuth 1.0a (signature), GitHub Apps (JWT minting, base64 Basic
   auth for git), Apple's business API (hourly JWT) and the WhatsApp Web and Signal clients, which
   hold device keys (§E2). OpenClaw's proxy rewrites URLs and bodies as well as headers (§B7).
   Claude Tag blocks unlisted hosts, private IP ranges and cloud metadata endpoints (§F). Media
   downloads in OpenClaw once kept `Authorization` headers across cross-origin redirects (§B11).
10. **Cap spend and stop agents.** Claude Tag's spend limit, OpenClaw's X caps and ChatGPT's task
    limits bound usage. Heartbeats and cron make model calls with nobody watching. OpenClaw's
    `/stop`, its cron watchdog, NanoClaw's silence timeout and Claude Tag's `!restart` stop work
    (§F).
11. **Keep transcripts, logs and an audit trail that outlive the compute.** Claude Tag's
    transcripts stay at Anthropic, and its actions appear in each tool's audit log. OpenClaw
    keeps transcripts on disk and daily logs under `/tmp/openclaw/`. Anthropic's guide logs
    egress at the proxy (§F).
12. **Route approvals over chat and keep them pending in saved state.** OpenClaw asks in the
    originating chat. LangGraph and Inngest save the paused run until the answer comes. ChatGPT
    tasks may pause for approval. A Copilot automation's creator cannot approve its pull requests
    (§F).
13. **Size for concurrency, memory growth and shared bot limits.** Anthropic sizes by peak RSS from
    a 1 GiB floor. OpenClaw runs up to max(8, CPU parallelism × 4) turns per Gateway. Agents behind
    one bot share its X, Slack and Discord connection limits (§F).

## Implications for Hibernaut

This section is analysis, not research. It maps the list above onto Hibernaut as of main `ccbc18d`
(M12) and cites Hibernaut's own docs by file and line. `cloud-agents.md §E` and
`cloud-agent-internals.md` cover the cloud side.

**What fits already**

- A paused machine keeps its memory and disk, so an agent's process and in-memory state come back
  as they were (item 7). Waking one over SSH takes 0.6 to 0.7 s (`docs/overview.md:210`).
- Secrets stay on the host. A machine holds placeholders and the egress proxy swaps in the real
  values (`docs/spec.md:113-119`). Claude Tag, Anthropic's guide and NanoClaw use the same pattern
  (item 9).
- E2B's firewall already drops traffic from machines to private ranges, the host's services and
  Azure's metadata endpoint (`docs/spec.md:367`). Claude Tag blocks the same kinds of address.
- A small machine has 1 GiB (`docs/overview.md:69`), the floor in Anthropic's hosting guide
  (item 13).

**What's missing**

| Need (item above) | Hibernaut today | What it would take | Already planned |
|---|---|---|---|
| A public HTTPS endpoint that verifies each call, answers challenges and acks within 3 to 10 s (1, 2) | The internet reaches only the host's SSH port. Public URLs and integrations are non-goals (`docs/spec.md:35-36`, `docs/overview.md:196`) | An ingress in `hibd` with TLS and a domain. It checks each channel's signature with a secret from the vault, answers X's hourly CRC and acks before it wakes anything. A wake takes 0.6 s, but the first after E2B starts took 13 to 32 s (`docs/milestones/README.md:86`) and starting the host takes minutes (`docs/overview.md:214`) | Public URLs per machine (`docs/ideas.md:170`), which serve a machine's pages, not events |
| Keep events while the agent sleeps, wake it, deliver at least once and drop duplicates (4, 5) | Nothing receives events. A machine wakes only on SSH, `hib exec` or a link from a busy machine (`docs/spec.md:422-424`) | An inbox per machine on the host that stores each event before the ack, delivers it after the wake and dedupes by event ID. GitHub never retries, so an event lost here is gone | No |
| Hold sockets while the agent sleeps: Discord, Slack Socket Mode, Telegram polling and X streams (3) | An idle client isn't busy, so its machine pauses and the socket drops. `--idle 0` keeps it up at the cost of its memory, and its links wake other machines only while it's working (`docs/spec.md:425-426`) | Either a holder on the host that keeps the socket and wakes the machine on a message, or a supported awake gateway whose links always wake its workers | No |
| Wake on a schedule and run missed jobs once after downtime (6) | No timer wakes a machine. Cron inside one runs only while it's awake. Automations are a non-goal (`docs/spec.md:36`) | `hib cron` or a wake time kept by `hibd`, with OpenClaw's rule of one run per missed job | No |
| Tell the host "done, pause me" or "keep me up" | Nothing in a machine calls the host, by design (`docs/spec.md:429-430`, `docs/spec.md:597`) | A narrow path from machine to host for early pauses and leases. It breaks a design rule, so it's an open question | No |
| Survive restarts and crashes (7) | Paused machines survive. Running machines are lost if the host crashes, `--idle 0` ones included, and there are no backups (`docs/overview.md:53`) | Regular checkpoints of awake machines and backups off the host | No |
| An identity for each agent (8) | One vault, and every placeholder goes into every machine (`docs/spec.md:113`). Per-machine identity waits for multi-user (D3, `docs/spec.md:708`) | Secrets bound to machines, plus bot accounts: a GitHub App whose tokens `hibd` mints, and a Slack or Discord app per agent | The GitHub App (`docs/ideas.md:173`) |
| Credentials outside the `Authorization` header (9) | Only `Authorization` is swapped (`docs/spec.md:161`) | Swaps in URL paths (Telegram) and named headers (Postmark, `x-api-key`), plus OAuth 1.0a signing (X) and JWT minting (GitHub Apps, Apple). A token sent inside a WebSocket message (Discord) or a device key (WhatsApp Web, Signal) can't be swapped. It lives where the client runs, so only a socket holder on the host keeps it out of the machine | `x-api-key` for Claude Code (`docs/ideas.md:84`) |
| Filter egress (9) | Machines reach the whole internet (`docs/ideas.md:65`) | The outbound allowlist | G2 (`docs/ideas.md:63`) |
| Cap spend and stop agents (10) | No caps and no kill switch | Awake-hour budgets per machine, request budgets per site at the proxy and a kill switch | Awake hours, CPU caps and a kill switch for the try server (`docs/ideas.md:119-131`) |
| Logs that outlive the compute (11) | `hib log` keeps swaps and links on the host | A log of events and wakes: what arrived, what it woke, how long the machine ran and what it printed | No |
| Room for many agents (13) | About six small or three medium machines awake. `hib new` and wakes fail at once when memory runs out (`docs/overview.md:189-191`) | A queue for wakes or pausing the machine idle longest, and a bigger host | A bigger host under G5 (`docs/ideas.md:149`) |
| Frequent wakes | Every pause adds a snapshot, and by default trimming covers only the test machines (`docs/overview.md:199`). A heartbeat every 30 minutes pauses 48 times a day | Trimming every machine by default and merging snapshot chains | Merging chains, later (`docs/spec.md:541`) |
| Reconnecting after a wake | A woken client finds its sockets dead and notices only on its own timeout. E2B does reset the clock (`docs/research/e2b-images-and-fork.md:51`) | A hook `hibd` runs in the machine after each wake, so clients reconnect at once for Discord's Resume and X's 20 s rule | No |
| A host that stays on | The Azure lab host deallocates after 30 idle minutes and at 20:00 UTC (`docs/overview.md:133-135`). Webhooks sent while it's off are lost | A host that stays up. That's a cost setting of the lab host, not of the design | Install anywhere, G2 (`docs/ideas.md:69`) |

Item 12, approvals over chat, is the agent's own job. It needs only the inbox and a way to wake.

**Order (opinion)**

1. The ingress, the inbox and waking on an event. Every webhook channel and trigger needs them.
2. Schedules in `hibd`, the other half of an always-on agent.
3. A way to hold sockets, on the host or in an awake gateway.
4. Secrets bound per machine and swaps beyond `Authorization`, before bots get their own accounts.
5. Trimming every machine by default, since an agent woken by events pauses many times a day.

## Gaps

What this note could not verify, by section.

**§A.** The help.x.com "About Grok" page was blocked, and xAI publishes nothing on how @grok
detects, queues or rate-limits mentions. The start time of the July 2025 fault rests on TechCrunch
alone. Posts by xAI or X about @grok's image replies in January 2026 were not checked. X gives no
retry schedule for webhooks.

**§C.** LangSmith publishes neither its scale-to-zero window nor whether cron wakes a scaled-down
deployment. Cloudflare's Agents page shows about 2 minutes before hibernation and its Durable
Objects page 10 s. Letta's pricing does not define an "active agent". I found no page that states
the rename of LangGraph Platform to LangSmith Deployment or of Copilot coding agent to Copilot
cloud agent.

**§E.** Discord does not state its Resume window. Telegram states neither an ack deadline nor a
retry count, and Meta states no ack deadline. Meta gives 36 hours on one page and 7 days on
another, and GitHub's redelivery page says both 7 and 3 days. Linear's retry delays may or may not
be cumulative. Slack does not say what happens to events while no Socket Mode client is connected
or whether Delayed Events covers Socket Mode. WhatsApp's limit for a linked device whose phone is
offline and Signal's server-side retention for an offline device were not verified, because the
official pages rendered client-side or were blocked. Baileys and signal-cli document no offline
queueing. Ack and retry rules for Apple Messages for Business were not checked.
