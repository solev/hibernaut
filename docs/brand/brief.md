# Hibernaut logo brief

Hibernaut is a self-hosted tool that gives a developer small Linux computers for coding and running AI assistants. Give a computer a name, connect with `ssh app.hib`, and work with familiar tools. When idle, it saves its files and running programs and sleeps; connecting again resumes the same work. The host also shuts down when unused to stop compute charges. The v1 plan adds one-time GitHub and AI logins, with real credentials kept on the host and substituted into approved outgoing requests, plus saved ready-to-use machines and copies for parallel tasks.

Audience: individual developers, terminal users, infrastructure experimenters, and people running coding agents on their own hardware-isolated workspaces. Single user first, not a team cloud service.

Core ideas, ranked:
1. Work survives sleep. Resume the same files, processes, and terminal session.
2. A familiar, named computer. Ordinary SSH is the front door and wake action.
3. Credentials stay outside machines. Host-held secrets enable tools without entering snapshots.
4. Ready work can multiply. Save configured machines and fork their live state.
5. Compute runs only when needed. Busy agents stay awake; idle machines and host sleep.

Personality: calm, capable, economical, precise, curious, quietly playful.
Avoid: astronauts, rockets, planets, orbital rings, generic cloud icons, AI sparkles, robot heads, stock shields, padlocks inside machines, container imagery, aggressive cyber styling, gradients, tiny detail, photorealism, and product mockups. Do not imply sleep loses work, that AI is the product itself, or that credentials enter the guest.
Status: machines and idle/wake are built in M0–M2. Vault/proxy, connected GitHub/AI tools, and images/fork are v1 plans in M3–M6. Concepts express both the present product and that documented direction.

Concepts in moodboard reading order, left to right across each row:
1. Dormant terminal. A terminal prompt rests beneath a low horizontal lid; an accent cursor suggests wake on SSH. README, spec §5, M2.
2. Retained cursor. The lowercase `hibernaut` wordmark integrates pause bars and a persistent cursor, linking hibernation to continuing the same session. Spec §5, M2's tmux test.
3. Named door. A compact doorway with a prompt-shaped opening and `app.hib` label represents a computer addressed by name through SSH. Spec §2, M1, cmd/hib/main.go.
4. Memory folio. Two offset flat pages surround one continuous process line; memory and disk snapshots preserve running work across host shutdown. README's save-game explanation, M0.
5. External key. A guest square holds only a hollow token; a solid key remains visibly outside its boundary, expressing host-only credentials. Spec §4.1, M3.
6. Bound gate. A hollow request enters a narrow gate and becomes solid only on its approved outward route; the egress proxy swaps placeholders for bound sites. Spec §4.2, M3.
7. Forked h. A single stem branches into three matching endpoints, forming an abstract lowercase h; live state becomes parallel machines while the original continues. Spec §5, M6.
8. Golden imprint. A simple amber master tile and matching cutout copy represent saved images containing repos, dependencies, and running applications. Spec §2, M6.
9. Connected once. A continuous connector joins the letters of a custom `hibernaut` wordmark; one connection enables GitHub, Codex, and Copilot in every fresh machine. Spec §4.3–4.4, M4–M5.
10. Agent watch. A small terminal encloses an unbroken pulse with a steady dot; coding agents remain awake even while waiting for an LLM. Spec §5, M2.
11. Separate rooms. Two independent, non-touching guest squares sit within one host frame, expressing real microVMs with separate kernels on self-hosted infrastructure. README, spec §3, M1.
12. Quiet host. A low server-shaped bar closes around a preserved state dot; sleeping the host stops compute charges without discarding work. README's auto-off, spec §5, cmd/hib/remote.go.

Visual direction: one 4-column by 3-row board with equal tiles, neutral off-white background, and consistent caption placement. Mix symbols, wordmarks, and combinations; use distinct restrained palettes. Marks must be crisp, flat, simple, and plausible at terminal-icon size. Captions state each concept name and its project idea.
