# Modern AI Coding CLIs — Terminal UI Stacks & Rendering Techniques

- **Date:** 2026-10-04
- **Scope:** How current AI coding CLIs (Claude Code, OpenAI Codex, Gemini CLI, opencode, GitHub Copilot CLI, Aider, Sourcegraph Amp, Charm's Crush) build fast/dynamic terminal UIs, which frameworks/libraries they use, and the techniques (diffing, synchronized output, inline vs. alt-screen, streaming markdown, terminal protocols) that make them feel smooth.
- **Method:** Primary sources only — npm registry metadata, actual `package.json`/`Cargo.toml`/`go.mod` files, GitHub source (grepped bundles where closed-source), official READMEs/docs, and first-party changelog/release-note text. Third-party blog commentary is explicitly labeled as such and never treated as a verified claim.
- **Confidence markers:** claims are **Verified** (read directly from a primary source, cited) unless marked **Inferred** (reasoned from adjacent evidence) or **Unverified/third-party** (closed-source, or only a secondary/community source was found).
- **Citation format:** `owner/repo:path/to/file#Lstart-Lend` or a direct URL (npm registry, docs.rs, official docs page). Every substantive claim below carries one.

---

## Summary comparison table

| Tool | Language / runtime | UI framework | Rendering mode | Source / confidence |
|---|---|---|---|---|
| **Claude Code** (Anthropic) | Node.js (bundled, now ships as a native per-OS binary with no resident Node process) | Ink + React (fingerprinted in bundle: `ink-box`/`ink-root` DOM tags, `yogaNode`/Yoga, `rendererPackageName:"ink"`, React 19 reconciler) | Dual mode: default "classic" (native scrollback) renderer + opt-in "fullscreen" (alternate-screen) renderer; first-party changelog confirms a renderer rewrite + React Compiler adoption | **Verified** (bundle grep + `CHANGELOG.md`); exact internals ("custom Ink fork", frame budget) are **third-party only** |
| **OpenAI Codex CLI** (`openai/codex`) | Rust (`codex-rs/`), Cargo workspace; legacy TS/Ink CLI (`codex-cli/`) now a thin shim that execs the Rust binary | Ratatui 0.30.2 + crossterm 0.29.0, with a **hand-forked** `ratatui::Terminal` (`custom_terminal.rs`) | Hybrid: finished turns written straight into native terminal scrollback via cursor escape sequences; only the live "bottom pane" is a diffed Ratatui viewport; alternate-screen is configurable (`auto`/`always`/`never`) | **Verified** (`Cargo.toml`, `custom_terminal.rs`, `tui/src/tui.rs`, `insert_history.rs`) |
| **Gemini CLI** (`google-gemini/gemini-cli`) | Node.js / TypeScript | Ink (custom fork `@jrichman/ink@6.6.9`) + React 19.2.4, `ink-spinner`, `ink-gradient` | Both inline (scrollback) and alternate-screen buffer, config-toggled (`useAlternateBuffer`); hand-rolled streaming-aware Markdown renderer on Ink `<Box>`/`<Text>` | **Verified** (`package.json`, `App.tsx`, `AppContainer.tsx`, `MarkdownDisplay.tsx`) |
| **opencode** (`anomalyco/opencode`, formerly `sst/opencode`) | Pre-v1.0: Go. **v1.0+ (2025-10-31): Bun/TypeScript** | Pre-v1.0: Bubble Tea + Lip Gloss + Bubbles + Glamour (Go). **v1.0+: OpenTUI** (`@opentui/core` + `@opentui/solid` + SolidJS) | Custom native (Zig) renderer exposed to TS via flexbox-box/text primitives, declaratively driven by SolidJS reconciler | **Verified** (`go.mod` @ tag v0.1.0; current `package.json`/`app.tsx`; GitHub Release `v1.0.0` body quoted verbatim) |
| **GitHub Copilot CLI** (`@github/copilot`) | Node.js loader → compiled/bundled native binary | **Unknown** — closed-source; repo has no application source | Unknown | **Unverified** — only binary distribution metadata available |
| **Aider** (`Aider-AI/aider`) | Python | `prompt_toolkit` (input/completion/key-bindings) + `rich` (console/markdown/live streaming output) — **not** Textual | Inline scrollback, `rich.live.Live` for streamed markdown | **Verified** (`requirements.txt`, `aider/io.py`, `aider/mdstream.py`) |
| **Sourcegraph Amp** (`@ampcode/cli`) | Node.js loader → compiled binary | **Unknown** — closed-source, no public technical docs | Unknown | **Unverified** |
| **Charm's Crush** (`charmbracelet/crush`) | Go | Bubble Tea v2 + Lip Gloss v2 + Bubbles v2 (`charm.land/...` module path) + Glamour v2 + Chroma | Elm-architecture (Model-Update-View), cell-based renderer | **Verified** (`go.mod`) |
| **Warp** (context only — not a CLI-building framework) | Rust | N/A — it's a GPU-accelerated terminal emulator application, not a TUI library | Custom GPU rendering | **Verified** (docs.warp.dev, now AGPLv3 OSS) |

---

## 1. Claude Code (Anthropic)

### 1.1 Package metadata — no declared UI deps (bundled)

`@anthropic-ai/claude-code`'s `package.json` has **always** shipped `"dependencies": {}`, across every version checked (0.2.9 → 2.1.289) — expected for a bundled CLI; UI libraries are compiled into the shipped artifact rather than declared.
Source: `https://registry.npmjs.org/@anthropic-ai/claude-code/latest` and historical versions via `https://unpkg.com/@anthropic-ai/claude-code@<ver>/package.json`.

As of the current latest version (**2.1.289**), the npm package no longer ships a `cli.js` at all. `bin` points to `bin/claude.exe`, replaced at install time by a ~229MB native per-platform binary pulled from `optionalDependencies` (`@anthropic-ai/claude-code-{linux,darwin,win32}-{x64,arm64}[-musl]`), via a `postinstall: node install.cjs` step. `install.cjs`'s own comment states (quoted): *"no Node.js process stays resident"* after the native binary takes over.
Source: `https://unpkg.com/@anthropic-ai/claude-code@2.1.289/install.cjs`.

`engines.node` moved from `>=18.0.0` (through v2.1.0) to `>=22.0.0` (2.1.289). **Node vs. Bun for the native binary is unverified** — the shebang on all pre-2.1.289 `cli.js` builds is `#!/usr/bin/env node` (confirming Node, not Bun, for the bundle era), but whether the current compiled binary is a Node Single-Executable-Application or a Bun-compiled executable could not be determined without disassembling the ~229MB blob (out of scope).

### 1.2 Ink + React fingerprint in the bundled source (verified via grep)

The last version to still ship a plain `cli.js` (v2.1.0, 11MB) contains unambiguous Ink/React/Yoga fingerprints:

- Ink's internal "host component" DOM tags, found verbatim: `"ink-box"`, `"ink-link"`, `"ink-progress"`, `"ink-root"`, `"ink-text"`, `"ink-virtual-text"`.
- Yoga layout-node API usage on every DOM node: `yogaNode:A==="ink-virtual-text"?void 0:fD1.Node.create()` — i.e. a Yoga (flexbox) layout node is created for every element except virtual text nodes, matching Ink's known architecture.
- A React DevTools hook-registration string self-identifying the renderer: `vn.injectIntoDevTools({bundleType:0,version:"16.13.1",rendererPackageName:"ink"})`, with a second devtools shim reporting `reconcilerVersion:"19.2.0"` (i.e. a modern React 19.x reconciler underneath Ink's devtools-compat shim). `React.createElement` appears 128 times in the bundle.

Source: `https://unpkg.com/@anthropic-ai/claude-code@2.1.0/cli.js` (grepped directly; cross-checked against v2.0.0 with the same signatures present). No literal `yoga-layout`/`yoga-wasm-web` package-name string survived minification — Yoga's presence is a **verified API-fingerprint**, not a literal dependency-name match.

The bundle's own banner reads: *"Want to see the unminified source? We're hiring! https://job-boards.greenhouse.io/anthropic/jobs/4816199008"* — confirming the source is intentionally not published; `anthropics/claude-code` on GitHub contains only distribution/support material (`CHANGELOG.md`, `README.md`, examples, plugins) with **no `src/` or renderer directory at all**.

### 1.3 First-party changelog evidence of a renderer rewrite

`anthropics/claude-code:CHANGELOG.md` (fetched in full, 8,204 lines) contains direct, first-party statements:

- **v2.1.10**: *"Rewrote terminal renderer for buttery smooth UI."*
- **v2.1.15**: *"Improved UI rendering performance with React Compiler"* (and again later, confirming ongoing use of React Compiler, not a one-off).
- Later entries (unpinned to exact version numbers but present in the file) describe a **"fullscreen renderer"** (alternate-screen, opt-in, later made default on certain setups) alongside the default **"classic renderer"** (native scrollback), plus an escape hatch: *"Added `CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN=1` env var to opt out of the fullscreen alternate-screen renderer and keep the conversation in the terminal's native scrollback."* Also: *"Fixed terminal styling degrading in very long sessions by recycling the renderer's style pool."*
- The changelog **never names "Ink" explicitly** — all renderer entries use generic language ("renderer", "fullscreen renderer", "classic renderer"). The Ink/Yoga architecture is established only via the bundle fingerprint in §1.2, not via Anthropic's own prose.

Source: `https://raw.githubusercontent.com/anthropics/claude-code/main/CHANGELOG.md`.

### 1.4 What is NOT verified

No `anthropic.com/engineering` blog post specifically documents the Claude Code TUI/renderer architecture (checked `claude-code-sandboxing`, `claude-code-auto-mode`, the April 23 postmortem, and the plugins announcement — none discuss rendering). Specific architectural claims circulating online — e.g. "a custom Ink fork", "minimal diffing", "a ~16ms frame budget", "85% flicker reduction" — trace to **third-party blog analysis** (Peter Steinberger's personal blog, `steipete.me/posts/2025/signature-flicker`; AnalyticsIndiaMag; AI-generated DeepWiki reverse-engineering pages of community mirror repos) and are **not corroborated by any Anthropic primary source** found in this research. They are noted here only so the gap is explicit, not as fact.

---

## 2. OpenAI Codex CLI (`openai/codex`)

### 2.1 Original TypeScript/Ink CLI (`codex-cli/`) — now a thin shim

At the repo's early history (commit `bc4e6db`, 2025-04-30), `codex-cli/package.json` declared:
```json
"dependencies": {
  "@inkjs/ui": "^2.0.0",
  "ink": "^5.2.0",
  "react": "^18.2.0",
  "marked": "^15.0.7",
  "marked-terminal": "^7.3.0",
  ...
},
"devDependencies": { "ink-testing-library": "^3.0.0", "@types/react": "^18.0.32", ... }
```
`yoga-layout` is not a direct dependency (pulled in transitively by Ink 5.x).
Source: `https://raw.githubusercontent.com/openai/codex/bc4e6db7496ba3265e79e0f2ffb55b197d646f8a/codex-cli/package.json`.

At current HEAD, `codex-cli/package.json` is reduced to an npm wrapper (`bin/codex.js`) that shells out to the compiled Rust binary — no UI dependencies remain.
Source: `https://raw.githubusercontent.com/openai/codex/afb436df8b70bb5bc57b86d9a3e829968988cd21/codex-cli/package.json`.

### 2.2 Rust rewrite (`codex-rs/`) — Ratatui + crossterm, confirmed exact versions

Workspace root `openai/codex:codex-rs/Cargo.toml` pins (`[workspace.dependencies]`):
```toml
ratatui = { version = "0.30.2", default-features = false, features = [
    "crossterm", "layout-cache", "underline-color",
] }
crossterm = "0.29.0"
syntect = "5"
pulldown-cmark = { version = "0.10", default-features = false }
image = { version = "^0.25.9", default-features = false }
```
The TUI crate (`openai/codex:codex-rs/tui/Cargo.toml`) adds:
```toml
crossterm = { workspace = true, features = ["bracketed-paste", "event-stream"] }
ratatui = { workspace = true, features = [
    "scrolling-regions", "unstable-backend-writer",
    "unstable-rendered-line-info", "unstable-widget-ref",
] }
ratatui-macros = { workspace = true }
image = { workspace = true, features = ["jpeg", "png", "gif", "webp"] }
pulldown-cmark = { workspace = true, features = ["html"] }
syntect = { version = "5", default-features = false, features = ["parsing", "plist-load", "regex-onig"] }
two-face = { version = "0.5", default-features = false, features = ["syntect-onig"] }
```
`tui-textarea` is **not** used anywhere in the workspace — Codex rolls its own composer/input widgets.

### 2.3 Render loop — a hand-forked Ratatui `Terminal`

`openai/codex:codex-rs/tui/src/custom_terminal.rs#L1-22` states the file is **"derived from `ratatui::Terminal`"** (MIT header reproduced) — i.e. Codex forked Ratatui's terminal rather than consuming it unmodified. Key details:

- `custom_terminal.rs#L361-390`: the render callback **re-renders the full logical frame every call**; the terminal then diffs the new `Buffer` against the previous one and writes only the changes — same philosophy as stock Ratatui, reimplemented with a custom diff.
- `custom_terminal.rs#L598-685`: `enum DrawCommand { Put{x,y,cell}, ClearToEnd{x,y,bg} }` and `fn diff_buffers(a: &Buffer, b: &Buffer) -> Vec<DrawCommand>` — a bespoke cell-diff with a `ClearToEnd` optimization and wide-glyph handling beyond stock Ratatui.
- `openai/codex:codex-rs/tui/src/tui.rs#L29-45,100-110`: `pub type Terminal = CustomTerminal<CrosstermBackend<Stdout>>` — confirms crossterm as the backend underneath the custom terminal.
- `openai/codex:codex-rs/tui/src/insert_history.rs#L1-10` (doc comment, quoted): *"Codex uses the terminal scrollback itself for finalized chat history, so inserting a history cell is an escape-sequence operation rather than a normal ratatui render."* — finished turns are written straight into native scrollback via cursor-move escape sequences; only the live "bottom pane" (composer/status) is a diffed Ratatui viewport above the cursor.
- `openai/codex:codex-rs/tui/src/tui/scrollback.rs#L1-30`: `enum ScrollbackStrategy { Standard, Zellij, FullScreen }`, selected via terminal-type detection (special-cases Zellij and Windows Terminal) — the renderer actively manages an inline/scrollback hybrid per terminal, not a fixed full-screen repaint.
- `openai/codex:codex-rs/tui/src/lib.rs#L2124-2141`: alternate-screen mode is configurable (`tui_alternate_screen`: `always`/`never`/`auto`; `auto` prefers native scrollback over SSH in Terminal.app, alternate screen otherwise).
- Draws are scheduled event-driven via a broadcast channel (`draw_tx`/`draw_rx`, visible in `tui.rs` tests), i.e. debounced/event-triggered redraw rather than a fixed-rate render loop.

### 2.4 Rewrite rationale — gap

No first-party README/CHANGELOG/doc passage in `openai/codex` states the TS→Rust rewrite rationale in so many words (`docs/install.md` only notes Rust honors `RUST_LOG`; README and CHANGELOG have zero hits for "rewrite"/"binary size"/performance rationale). Third-party tech-press coverage (InfoQ, aibase.com) reported single-binary distribution and native OS sandboxing (Seatbelt/Landlock) as reasons, but **this is secondary/unconfirmed** — no OpenAI blog post or in-repo doc making this claim verbatim was found.

---

## 3. Gemini CLI (`google-gemini/gemini-cli`)

Root and `packages/cli/package.json` both pin Ink to a **custom fork**, applied via `overrides`:
```json
"overrides": { "ink": "npm:@jrichman/ink@6.6.9" },
"dependencies": {
  "ink": "npm:@jrichman/ink@6.6.9",
  "ink-gradient": "3.0.0",
  "ink-spinner": "5.0.0",
  "react": "19.2.4",
  "highlight.js": "11.11.1",
  "lowlight": "3.3.0"
}
```
Source: `google-gemini/gemini-cli:package.json`, `google-gemini/gemini-cli:packages/cli/package.json`.

Source confirms plain React-function-component architecture consuming Ink primitives directly:
```tsx
// packages/cli/src/ui/AppContainer.tsx
import { type DOMElement, ResizeObserver, useApp, useStdout, useStdin,
  type AppProps, AppContext as InkAppContext } from 'ink';
```
`google-gemini/gemini-cli:packages/cli/src/ui/AppContainer.tsx#L1-30`; `App.tsx#L1-33` shows the root component branching on `useAlternateBuffer()`/`isScreenReaderEnabled` to choose between `AlternateBufferQuittingDisplay`, `QuittingDisplay`, `ScreenReaderAppLayout`, and `DefaultAppLayout`.

Both inline and alternate-screen rendering are supported, config-toggled (`google-gemini/gemini-cli:packages/cli/src/ui/hooks/useAlternateBuffer.ts#L1-19`, mid-migration per its own code comment: *"This method is intentionally misleading while we migrate... Once getUseTerminalBuffer() is always enabled we will refactor to remove all instances of this method"*).

A hand-rolled, streaming-aware Markdown component is built entirely on Ink's `<Text>`/`<Box>` primitives (`google-gemini/gemini-cli:packages/cli/src/ui/utils/MarkdownDisplay.tsx#L1-45`), with an `isPending` prop that truncates/annotates code blocks ("... generating more ...") while a block is still streaming in and the terminal can't yet fit it.

No explicit "we use Ink+React" architecture prose was found in Gemini CLI's README — the confirmation rests entirely on source imports and dependency manifests (arguably stronger evidence than prose, but noted as a documentation gap).

---

## 4. opencode (`anomalyco/opencode`, formerly `sst/opencode`) and OpenTUI (`anomalyco/opentui`)

**Org rename, verified via HTTP**: `github.com/sst/opencode` and `github.com/sst/opentui` both now 301-redirect to `github.com/anomalyco/opencode` / `anomalyco/opentui`.

### 4.1 Pre-v1.0: Go + Bubble Tea

At tag `v0.1.0`, `packages/tui/go.mod`:
```go
module github.com/sst/opencode
require (
	github.com/charmbracelet/bubbles v0.21.0
	github.com/charmbracelet/bubbletea v1.3.4
	github.com/charmbracelet/glamour v0.9.1
	github.com/charmbracelet/lipgloss v1.1.0
	github.com/charmbracelet/x/ansi v0.8.0
	github.com/lrstanley/bubblezone v0.0.0-20250315020633-c249a3fe1231
)
```
Source: `anomalyco/opencode:packages/tui/go.mod` @ `refs/tags/v0.1.0`. Still present (in parallel with the new TS TUI) as late as tag `v1.0.10`.

### 4.2 v1.0.0 (2025-10-31): cutover to OpenTUI — directly announced

GitHub Release `v1.0.0` (published 2025-10-31T19:28:15Z), body quoted verbatim:
> "OpenCode 1.0 is a complete rewrite of the TUI.
> We moved from the go+bubbletea based TUI which had performance and capability issues to an in-house framework (OpenTUI) written in zig+solidjs.
> The new TUI works like the old one since it connects to the same opencode server."

By tag `v1.0.30` (2025-11-05), `packages/tui` (Go) no longer exists in the tree. Current `packages/tui/package.json`:
```json
{
  "name": "@opencode-ai/tui",
  "dependencies": {
    "@opentui/core": "catalog:",
    "@opentui/keymap": "catalog:",
    "@opentui/solid": "catalog:",
    "opentui-spinner": "catalog:",
    "solid-js": "catalog:"
  }
}
```
and `packages/tui/src/app.tsx#L1-15` imports `render, useRenderer, useTerminalDimensions` from `@opentui/solid`, `createCliRenderer, MouseButton` from `@opentui/core`, and SolidJS primitives (`createSignal`, `createEffect`, `Show`, etc.). Zero `charmbracelet/*` references remain anywhere in the repo (code search confirmed 0 hits). A maintained first-party script (`anomalyco/opencode:script/upgrade-opentui.ts`) bumps the OpenTUI packages across the monorepo, confirming it's an intentionally tracked core dependency.

### 4.3 OpenTUI itself — Zig core + TS/React/Solid bindings

`anomalyco/opentui:README.md`, quoted verbatim:
> "OpenTUI is a library to build terminal user interfaces.
> - It is written in Zig.
> - You write TypeScript directly or through React and Solid.
> - You arrange boxes and text with flexbox.
> - You add selects, inputs, and scroll boxes with keyboard and mouse controls.
> - You can play sounds, show images, and render 3D graphics.
> OpenCode uses OpenTUI in production for millions of users."

Package split per the same README: `@opentui/core` ("Native terminal UI library with TypeScript bindings"), `@opentui/react` ("React renderer..."), `@opentui/solid` ("Solid renderer..."), `@opentui/native` (private — "the Zig implementation, native tests, and benchmarks"). `anomalyco/opentui:packages/native/` contains `build.zig`/`build.zig.zon`, confirming the Zig build system backing the native core. OpenTUI is explicitly **not** a diffing layer bolted onto an existing TUI library (unlike Ink-on-Yoga) — it's a from-scratch native renderer exposing primitives directly, with React/Solid as thin declarative reconcilers on top. Deeper buffer-diffing internals were not found in the README (would require `opentui.com/docs`, not fetched in this pass — flagged as a gap).

---

## 5. Other CLIs

### 5.1 GitHub Copilot CLI (`@github/copilot`) — closed-source, unverifiable

npm metadata (`https://registry.npmjs.org/@github/copilot`, latest `1.0.91`) shows the published package is a 1.2KB loader (`npm-loader.js`) that `spawnSync`s a platform-specific compiled binary from `optionalDependencies` (`@github/copilot-{linux,linuxmusl,darwin,win32}-{x64,arm64}`, each ~158MB unpacked). Its only runtime dependency is `detect-libc`. The `github/copilot-cli` repo contains no source (`.github/`, `LICENSE.md`, `README.md`, `changelog.md`, `install.sh` only). **No TUI framework could be verified.** A now-superseded version (0.0.326, pre-binary) listed `node-pty`, hinting it may have wrapped a PTY session at that time — noted as **inferred**, not confirmed, and not necessarily representative of the current architecture.

### 5.2 Aider (`Aider-AI/aider`) — prompt_toolkit + rich, verified

`requirements.txt` (resolved from `requirements/requirements.in`, i.e. a direct, not transitive, dependency):
```
prompt-toolkit==3.0.52
rich==14.3.3
markdown-it-py==4.0.0   # via rich
pygments==2.19.2        # via rich
```
No `textual` anywhere in the resolved tree. Confirmed via actual imports:
- `aider/io.py`: `from prompt_toolkit.shortcuts import CompleteStyle, PromptSession`, `from prompt_toolkit.lexers import PygmentsLexer`, plus `from rich.console import Console`, `from rich.markdown import Markdown`.
- `aider/mdstream.py`: `from rich.live import Live` — drives incremental/streamed markdown re-rendering as LLM tokens arrive.
- `aider/commands.py`: `from prompt_toolkit.completion import Completion, PathCompleter` — powers slash-command/path autocompletion.

### 5.3 Sourcegraph Amp (`@ampcode/cli`) — closed-source, unverifiable

`https://registry.npmjs.org/@ampcode/cli/latest`: no `dependencies`, only `optionalDependencies` pointing to per-platform precompiled binaries (`"bin": {"amp": "bin/amp.exe"}` pattern, `postinstall: node install.cjs`). `"license": "SEE LICENSE IN LICENSE.md"` (proprietary-style). No open-source implementation repo exists; `ampcode.com/docs/cli` describes installation/usage only, with no mention of any internal rendering framework. **Could not verify anything about its rendering stack.**

### 5.4 Charm's Crush (`charmbracelet/crush`) — verified

`go.mod`:
```go
require (
	charm.land/bubbles/v2 v2.2.1
	charm.land/bubbletea/v2 v2.0.9
	charm.land/glamour/v2 v2.0.1
	charm.land/lipgloss/v2 v2.0.6
	github.com/alecthomas/chroma/v2 ...
)
```
(Charm migrated its Go module path from `github.com/charmbracelet/...` to `charm.land/...` for the v2 generation.) `charmbracelet/bubbles:README.md` itself states: *"These components are used in production in [Crush]..."* — confirmed bidirectionally.

### 5.5 Warp — context only

Not a TUI-building framework; it's a terminal emulator application. `docs.warp.dev`: *"Warp is... a fast, modern terminal built for coding with agents... Built with Rust for high performance."* Client is open-sourced under AGPLv3 (`github.com/warpdotdev/warp`).

---

## 6. The libraries themselves

### Ink (`vadimdemedes/ink`)
> "Ink provides the same component-based UI building experience that React offers in the browser, but for command-line apps. It uses [Yoga] to build Flexbox layouts in the terminal... Since Ink is a React renderer, all features of React are supported." — `vadimdemedes/ink:readme.md`

`package.json` (current, v8.0.0): `"react-reconciler": "^0.34.0"`, `"yoga-layout": "~3.2.1"` — confirms the custom-reconciler + Yoga-flexbox architecture directly.

**`<Static>`** permanently flushes completed output above a live region (doc comment, `src/components/Static.tsx`): *"`<Static>` component permanently renders its output above everything else. It's useful for displaying activity like completed tasks or logs... used by Tap and Gatsby."* Internally it renders `position: 'absolute'` and is only re-accumulated in debug mode or replayed on full clears on the alternate screen ("because there is no scrollback to keep it in" — `src/ink.tsx` doc comment).

### Ratatui (`ratatui/ratatui`, formerly `tui-rs`)
> "Ratatui... is a Rust crate for cooking up terminal user interfaces (TUIs)... can be used for command-line applications, dashboards, and other interactive console programs." — README.

Rendering pipeline (docs.rs `Terminal` struct, quoted): render callback populates a `Buffer` representing the full logical frame every pass → **"Diffs the current buffer against the previous buffer and writes the changes"** → swaps buffers for the next pass. `Buffer` (docs.rs `ratatui-core`): *"a grid where each cell contains a grapheme, a foreground color and a background color... used to output the appropriate escape sequences."*

`Viewport` modes (docs.rs, quoted): `Fullscreen` ("owns the whole terminal window"), `Inline` ("spans the full terminal width, anchored to column 0 of the current cursor row... reserves space for the requested height"), `Fixed` (not auto-resized). `Terminal::insert_before` (used for logs/status above an inline viewport): *"uses terminal scrolling regions to insert content above an inline viewport without clearing and redrawing it... if the viewport isn't yet at the bottom of the screen, inserted lines will push it towards the bottom."*

### crossterm (`crossterm-rs/crossterm`)
> "Crossterm is a pure-rust, terminal manipulation library that makes it possible to write cross-platform text-based interfaces... supports all UNIX and Windows terminals down to Windows 7." — README. Confirmed as Ratatui's default backend crate (`ratatui-crossterm`, per Ratatui's `ARCHITECTURE.md`).

### Bubble Tea / Lip Gloss / Bubbles (`charmbracelet/*`)
- **Bubble Tea**: *"The fun, functional and stateful way to build terminal apps. A Go framework based on The Elm Architecture... a high-performance cell-based renderer, built-in color downsampling, declarative views, high-fidelity keyboard and mouse handling, native clipboard support."* — Model/`Init()`/`Update(msg) (Model, Cmd)`/`View()` is the explicit Model-Update-View (Elm) pattern.
- **Lip Gloss**: *"Style definitions for nice terminal layouts... Users familiar with CSS will feel at home with Lip Gloss."* — CSS-like declarative styling/layout.
- **Bubbles**: *"Primitives for [Bubble Tea] applications. These components are used in production in [Crush]..."* — component library: Spinner, Text Input, Text Area, Table, Progress, Paginator, Viewport, List (fuzzy filter/pagination), File Picker, Timer, Stopwatch, Help, Key.

### OpenTUI (`sst`/`anomalyco` `opentui`)
Covered in §4.3 — Zig-native renderer core, TS/React/Solid bindings, used by opencode v1.0+.

### Textual & Rich (`Textualize/*`)
- **Textual**: *"Build cross-platform user interfaces with a simple Python API. Run your apps in the terminal or a web browser... Textual is an asynchronous framework under the hood."* CSS-like styling (`CSS = "Screen { align: center middle; }"`), widget gallery (buttons, tree, data tables, inputs).
- **Rich**: *"Rich is a Python library for rich text and beautiful formatting in the terminal... pretty tables, progress bars, markdown, syntax highlighted source code, tracebacks, and more — out of the box."* Textual is built by the same org (Textualize) atop Rich's rendering primitives (asserted by Textualize; not independently re-verified via Textual's own `pyproject.toml` in this research pass).
- **Not used by Aider** — Aider uses `rich` + `prompt_toolkit` directly (see §5.2), not the heavier Textual framework.

### prompt_toolkit (`prompt-toolkit/python-prompt-toolkit`)
> "`prompt_toolkit` is a library for building powerful interactive command line applications in Python." Features (quoted): "Syntax highlighting of the input while typing (e.g. via a Pygments lexer). Multi-line input editing. Advanced code completion. Both Emacs and Vi key bindings... Support for bracketed paste. Mouse support... Lightweight, the only dependencies are Pygments and wcwidth."

---

## 7. Techniques that make these UIs fast and smooth

### 7.1 Differential rendering / cell-buffer diffing
- **Ratatui**: render callback always draws the *entire* logical frame into a `Buffer`; Ratatui diffs that buffer against the previous one and writes only the changed cells (`Terminal::draw` → `Terminal::flush`, docs.rs). **Codex's forked terminal** (`codex-rs/tui/src/custom_terminal.rs#L598-685`) reimplements this with its own `diff_buffers`/`DrawCommand` enum (`Put`/`ClearToEnd`), adding wide-glyph handling.
- **Ink**: diffs the React *element tree* via `react-reconciler` (virtual-DOM-style tree diffing), re-computing Yoga flexbox layout each pass, rather than a flat cell-grid diff. Same end goal (minimal terminal writes), different data structure.

### 7.2 Synchronized output (DEC private mode 2026)
Spec (canonicalized at `github.com/contour-terminal/vt-extensions/blob/master/synchronized-output.md`, originally a gist by the Contour author), quoted: *"With applications updating the screen at a higher frequency this can cause tearing. This mode attempts to mitigate that... Use `CSI ? 2026 h` to enable batching output commands... `CSI ? 2026 l` when done with your current frame rendering."* Its own adoption table lists ✅ for Alacritty, Contour, **bubbletea**, foot, ghostty, iTerm2, Kitty, mintty, notcurses, Warp.

**crossterm** implements this directly in source (`crossterm-rs/crossterm:src/terminal.rs`): `pub struct BeginSynchronizedUpdate` / `EndSynchronizedUpdate`, writing `CSI ?2026h` / `CSI ?2026l` respectively. **Ratatui itself has no wrapper API for this** (GitHub code search for "2026" in `ratatui/ratatui` found no synchronized-output logic) — Ratatui apps must call crossterm's commands directly around `terminal.draw()`. This is a real gap in Ratatui's own surface area, not a missing feature of the stack overall.

### 7.3 Alternate screen vs. inline/scrollback rendering
Both Ink and Ratatui solve "keep finished output in native scrollback, only live-redraw the active region" — via different mechanisms:
- **Ink `<Static>`**: a React component with `position: 'absolute'` that accumulates permanently-rendered output above the live region; only replayed on full clears when using the alternate screen ("because there is no scrollback to keep it in").
- **Ratatui `Viewport::Inline` + `Terminal::insert_before`**: a dedicated API using real terminal scrolling-region escape sequences to insert content above an inline viewport without clearing/redrawing it.
- **Codex's TUI** goes further than both: it bypasses its own render pipeline entirely for finished turns, writing them straight into native scrollback via cursor-move escapes (`insert_history.rs`), and only runs a diffed Ratatui viewport for the live composer/status pane — with alternate-screen mode itself made configurable per terminal (`auto`/`always`/`never`, `lib.rs#L2124-2141`).
- **Claude Code** exposes both as user-facing named modes ("classic renderer" = scrollback, "fullscreen renderer" = alternate screen, user-toggleable via `CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN`).
- **Gemini CLI** also config-toggles between the two (`useAlternateBuffer`/`useTerminalBuffer`), mid-migration per its own source comment.

### 7.4 Throttled re-renders / frame-rate limiting of streaming output
**General technique, no single canonical primary source.** No debounce/frame-limiter API was found documented in Ink, Ratatui, crossterm, Bubble Tea, or Textual's own docs/source. Codex's TUI schedules draws via a broadcast channel (`draw_tx`/`draw_rx`) rather than a fixed-rate loop, which is evidence of event-driven (not naively-per-token) redraw scheduling, but no project documents an explicit "frames per second" cap for LLM token streaming as a named feature. Treat this as a widely-used but undocumented engineering pattern (batch stdout writes every N ms) rather than a citable library feature.

### 7.5 Streaming markdown rendering & syntax highlighting
- **syntect** (Rust): *"a syntax highlighting library for Rust that uses Sublime Text syntax definitions... Built-in output to coloured HTML `<pre>` tags or 24-bit colour ANSI terminal escape sequences."* Used directly by Codex's `codex-rs/tui` (`syntect = "5"`, plus the `two-face` theme bundle).
- **shiki** (TypeScript): *"A beautiful syntax highlighter based on TextMate grammar, accurate and powerful."* (Oniguruma/WASM engine detail is documented on `shiki.style`, not the README itself — not independently re-verified in this pass.)
- **highlight.js**: *"a syntax highlighter written in JavaScript... works in the browser as well as on the server... has automatic language detection."*
- **rich.live.Live** (Aider's `mdstream.py`) is the concrete mechanism for incremental markdown re-rendering as LLM tokens stream in, in a Python stack.
- **pulldown-cmark** is Codex's Rust markdown parser (`pulldown-cmark = { features = ["html"] }`), paired with syntect for fenced code blocks.
- Gemini CLI hand-parses markdown line-by-line in `MarkdownDisplay.tsx` rather than using a markdown AST library, special-casing in-progress ("pending") code blocks during streaming.

### 7.6 Terminal graphics/keyboard/paste protocols
- **Kitty graphics protocol** (`sw.kovidgoyal.net/kitty/graphics-protocol/`): transmits images and displays them via "placements" (`a=T` transmit+display, `a=p,i=<id>` redisplay), with z-index/cropping/cell-scaling support.
- **Sixel**: general bitmap-graphics terminal format of DEC VT340 origin; no single dedicated primary spec URL was fetched in this research (flagged as general/secondary knowledge).
- **iTerm2 inline images** (`iterm2.com/documentation-images.html`): proprietary `ESC ] 1337 ; key=value ^G` extension to the xterm protocol; iTerm2 also separately supports the Kitty graphics protocol via APC `G` sequences.
- **Kitty keyboard protocol** (`sw.kovidgoyal.net/kitty/keyboard-protocol/`): disambiguates key events via `press`/`repeat`/`release` event types and a modifier bitfield, solving cases legacy terminals can't (e.g. distinguishing ctrl+shift+a from ctrl+A).
- **Bracketed paste** (`invisible-island.net/xterm/ctlseqs/ctlseqs.html`, "Bracketed Paste Mode"): pasted text is wrapped in `ESC[200~ ... ESC[201~` so the application can tell pasted text apart from typed text. Both prompt_toolkit's and Codex's TUI crossterm backend (`features = ["bracketed-paste", ...]`) explicitly enable this.

---

## 8. Recommendation for a new CLI, by language

- **TypeScript/Node.js**: Start with **Ink + React** — it's the dominant choice (Claude Code, Gemini CLI, original Codex CLI all used it) and gets you a familiar component model, Yoga flexbox layout, and `<Static>` for scrollback-safe log/history output for free. Budget time to fork or deeply patch Ink if you need Claude-Code-grade smoothness at scale — both Anthropic (per changelog wording, "rewrote terminal renderer") and Google (custom `@jrichman/ink` fork) ended up patching or rewriting rendering internals rather than using stock Ink as-is. For markdown/code streaming, pair with `shiki` or `highlight.js`; enable crossterm-style synchronized-output equivalents manually if flicker becomes an issue (Ink itself has no built-in synchronized-output wrapper, verified absent from its README/source in this research).
- **Rust**: Use **Ratatui + crossterm** — it's the clear ecosystem default (Codex's Rust rewrite, generally the first choice for new Rust TUIs) and gives you buffer diffing, `Viewport::Inline`/`insert_before` for scrollback-safe history, and crossterm's native synchronized-output (`BeginSynchronizedUpdate`/`EndSynchronizedUpdate`) and bracketed-paste support out of the box. Only fork Ratatui's `Terminal` (as Codex did) if you need custom diff behavior (e.g. wide-glyph handling, `ClearToEnd` optimization) or a hybrid inline/scrollback strategy more sophisticated than stock `Viewport::Inline`. Add `syntect` + `pulldown-cmark` for markdown/code rendering, matching Codex's exact combination.
- **Go**: Use **Bubble Tea + Lip Gloss + Bubbles** (Charm's stack) — proven in production by Crush and opencode's pre-1.0 TUI, with first-party synchronized-output support (listed ✅ in the DEC 2026 spec's own adoption table) and an Elm-architecture model that scales well to complex, stateful chat UIs. Add Glamour for markdown and Chroma for syntax highlighting, matching Crush's exact dependency set. Be aware opencode migrated *away* from this stack in 2025 citing "performance and capability issues" (their own release-note wording) — evaluate whether your UI's complexity (images, 3D, audio, heavy custom widgets) outgrows what Bubble Tea's terminal-only cell renderer can do before committing long-term.
- **Python**: Use **prompt_toolkit** for interactive input (multi-line editing, completion, key bindings, bracketed paste) + **rich** (`rich.live.Live`) for styled/streamed markdown output — this is exactly Aider's proven combination and is lighter-weight than Textual. Reach for **Textual** instead only if you need a full app-like widget framework (CSS styling, async event loop, complex multi-pane layouts) rather than a conversational single-pane CLI.

---

## 9. Decision for `hib`

`hib` is written in Go and is a command-based CLI, not a full-screen chat app. We use Charm's
stack, only as much as we need: **Lip Gloss v2** for colour, **Bubble Tea v2** in inline mode
for live progress, and the existing `golang.org/x/term` for prompts. opencode left Bubble Tea
because of performance in a full-screen streaming chat UI, and `hib` doesn't have one. The
plan is in [M10 CLI friendliness](../milestones/m10-cli-friendliness.md).

---

## Gaps and open questions (carried over from research)

- Claude Code: exact current (2.1.289+) renderer internals cannot be grepped since no `cli.js`/`cli.mjs` is shipped anymore in that channel (native binary only); Node-SEA vs. Bun-compile for that binary is unverified.
- OpenAI Codex: no first-party rationale statement found for the TS→Rust rewrite (only third-party tech-press speculation).
- opencode/OpenTUI: OpenTUI's precise buffer-diffing algorithm was not found documented in its README (would require fetching `opentui.com/docs`, not done in this pass).
- GitHub Copilot CLI and Sourcegraph Amp: fully closed-source; no UI framework could be verified for either.
- Ratatui has no built-in synchronized-output (DEC 2026) wrapper; apps must call crossterm directly.
- Sixel and Shiki's Oniguruma/WASM engine claim were not confirmed against a single fetched primary-source URL in this pass (noted as general/secondary knowledge).
