# Goals after v1

Where Hibernaut goes after [v1](milestones/README.md), in order. These are goals, not milestones
yet. Each one gets researched and split into milestones before work starts. The detail behind
them, and the ideas no goal has taken, are in [ideas](ideas.md).

## The goal

Traction in the AI community. Developers who work with coding agents hear about Hibernaut, try it
in minutes and talk about it. It's a personal project, shown in public through short videos of
real scenarios.

Signs it's working, with no targets set:

- views and shares of the videos
- GitHub stars, and issues and pull requests from people we don't know
- release downloads, which count installs without telemetry
- people who come back to the try server
- posts and videos about Hibernaut by others

Not goals for now: raising money (grants included) and a hosted business. A hosted service comes
back only if people ask for it (G5). The fundraising research is
[parked in ideas](ideas.md#parked-fundraising).

| # | Goal | Status |
| --- | --- | --- |
| G1 | [Show it in public](#g1-show-it-in-public) | not started |
| G2 | [Ready for strangers](#g2-ready-for-strangers) | not started |
| G3 | [A try server](#g3-a-try-server) | not started |
| G4 | [Open-source launch](#g4-open-source-launch) | not started |
| G5 | [Hosted service, if people ask](#g5-hosted-service-if-people-ask) | not started |

G1 starts now, on today's host, and keeps going after the launch. G2 and G3 come before G4,
because a project gets one Show HN, and it should be something people can try at once. G5 waits
for the launch to show whether anyone wants it.

## G1. Show it in public

Short videos of Hibernaut doing real work, filmed on today's host and posted online. The
[scenarios](ideas.md#videos) and [the pitch](ideas.md#the-pitch) are in ideas. The repository
stays private until G4, so the videos build an audience for the launch.

[M11 Bot faces](milestones/m11-bot-faces.md) puts the bot in `hib`, so the clips show it.

Done when three videos are posted. After that, each new feature gets one.

## G2. Ready for strangers

Anyone can install Hibernaut on their own Linux server in minutes, and it's safe there. Today it
needs Azure, whose network rules hide the ports E2B's local setup leaves open.

- Lock the host down: E2B's services listen on every interface, and machines can reach the host.
- An outbound allowlist for machines, with deny by default as an option.
- One command installs it on any x86 Linux server with KVM, Hetzner bare metal first. Nothing
  depends on Azure.
- Docs for installing, upgrading and backing up.
- Claude Code, connected like Codex.
- Prebuilt releases, so trying `hib` needs no Go.
- Ready to publish: a license, the README scrubbed, a fresh history.

The details are in [ideas](ideas.md#g2-ready-for-strangers). Done when a stranger installs it on
a fresh server from the docs alone, and a port scan of that server finds only SSH.

## G3. A try server

Developers try Hibernaut with nothing to install and no signup: `ssh NAME@try.DOMAIN`, where NAME
is their GitHub username. Each gets small machines and a daily allowance of awake hours. It runs
on a rented server of its own, apart from today's host.

The design, the abuse controls and the cost are in [ideas](ideas.md#g3-a-try-server). Done when
it runs with a privacy note and terms, and a kill switch stops new machines with one command.

## G4. Open-source launch

The repository goes public with a release on the day the try server opens. Then Show HN, X,
r/selfhosted and a pull request to the awesome-ai-coding-sandboxes list. A "want it hosted?" link
in the README and on the try server counts who asks for G5.

Done when the repository is public and on Show HN.

## G5. Hosted service, if people ask

Hibernaut as a service people sign up for, on rented bare-metal servers in the EU. It starts only
if the launch shows demand: people asking for it, or try-server users who want more than it
gives. Beyond what the try server has, it needs:

- user accounts, with each user's machines and vault kept apart
- quotas, and Stripe billing per machine-hour, sleeping machines' disk included
- free credits with a GitHub login, and more after adding a card
- protection from abuse, crypto mining above all
- snapshots copied off the server, and machines across several servers

The numbers and the risks are in [ideas](ideas.md#g5-hosted-service). Done when a paying user
runs machines without us doing anything by hand.
