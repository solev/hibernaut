# Hibernaut overview

Hibernaut gives one developer named Linux machines on a server they run, called the host. You
log in to a machine with plain `ssh`. When nobody uses a machine, it pauses with its memory and
files, and the next login wakes it where it left off. GitHub, Codex and the GitHub Copilot CLI
work in every machine, but the real credentials stay on the host.

Hibernaut is a self-hosted version of what [boxd.sh](https://boxd.sh) offers as a service. Each
machine is a Firecracker microVM, run by
[E2B's open-source infrastructure](https://github.com/e2b-dev/infra) on a single Azure VM.
Version 1 is complete. This page explains how Hibernaut works and where its limits are, for
people and AI agents who are new to it.

## What using it looks like

Set up once, from your laptop:

```bash
# Point hib at the host, and let ssh NAME.hib reach machines.
hib init --host azureuser@203.0.113.7 --azure-group rg-hib --azure-vm hib
hib image build dev       # build the image that machines start from
hib connect github        # approve a code in the browser
hib connect codex         # approve a code with your ChatGPT plan
```

Then, every day:

```bash
hib new app               # make a machine named app
ssh app.hib               # log in, waking app and the host first if they're asleep
  git clone https://github.com/you/project
  codex                   # already logged in
hib exec app -- 'cd project && codex exec "Add a health check"'
hib ls                    # machines, their state and size, and when each pauses
hib fork app --count 2    # app-1 and app-2, copies of app as it is now
hib rm app --yes          # delete app and its disk for good
```

## A paused machine keeps its work

Pausing a machine writes its memory and its disk changes to snapshot files on the host. Waking
it loads the snapshot back. Its files are as they were, and its processes, tmux sessions and dev
servers continue from where they stopped. Memory loads lazily. The host reads each page from the
snapshot only when the machine first touches it, so a wake takes under a second.

A machine pauses after 15 idle minutes, or after the idle time you give `hib new --idle`.
`--idle 0` turns pausing off. A machine counts as busy while someone is connected to it, while it
uses at least 10% of a CPU, or while a coding agent runs in it. The agents it knows are `codex`,
`copilot` and `claude`, and they count even while they wait on a model. A busy machine keeps
running. Logging in or `hib exec` wakes a paused one.

Paused machines survive the host shutting down. Before every clean shutdown, the host pauses each
running machine and waits until its snapshot is on disk. If the host crashes instead, the
machines that were running are lost.

## Each machine is a named computer you reach with SSH

A machine is a Firecracker microVM with its own Linux kernel, not a container. `ssh app.hib` logs
in to it as `user`, who has passwordless sudo. Behind that name, `hib` wakes the machine if it's
paused, starts the host's VM if it's off, and checks the machine's pinned host key. Local port
forwards work, so `ssh -L 3000:localhost:3000 app.hib` reaches a dev server in the machine.
Agent forwarding is off, so a machine can't use your laptop's SSH keys.

Machines come in three sizes. Running machines share the host's memory pool of 6.25 GiB, and
paused ones use only disk.

| Size | vCPUs | Memory | Running at once on the current host |
| --- | --- | --- | --- |
| small | 1 | 1 GiB | 6 |
| medium, the default | 2 | 2 GiB | 3 |
| large | 4 | 4 GiB | 1, plus a small or medium one |

Sizes mix in the one pool, so two medium and two small machines also fit. Every machine has
10 GiB of disk and keeps its size for life. The `dev` image is Ubuntu 24.04 with git, gh,
Node.js 24, Python 3, build tools, tmux, ripgrep, Codex and the GitHub Copilot CLI.

## Real credentials stay on the host

Hibernaut never puts a real credential in a machine, not on disk, not in an environment variable
and not in memory. Each credential is a bound secret, with a name, a real value, a placeholder
and the sites the real value may go to.

- The host keeps the real value sealed with AES-256-GCM, under a key only root can read.
- A machine gets the placeholder, a look-alike of the real token with the same prefix, such as
  `gho_` for GitHub, so tools accept it. The placeholder sits in the machine's environment, or in
  a tool's login file, as it does for Codex.
- Every TCP connection a machine makes to port 443 reaches the host's proxy first. The proxy
  reads the site's name without decrypting, and passes traffic to every unbound site straight
  through.
- For a bound site, the proxy decrypts the request with a certificate from the Hibernaut CA,
  which machines trust. It swaps the placeholder in the `Authorization` header for the real
  value, and turns the real value back into the placeholder in the response.
- Sent anywhere else, a placeholder is a useless string.
- `hib log` shows recent swaps, each with its time, machine, site, method, path and secret.

GitHub, Codex and Copilot use bound secrets:

- `hib connect github` runs GitHub's device flow on the host, with Hibernaut's own OAuth App. You
  approve a code in the browser, and the host gets its own token with the scopes `repo`,
  `read:org` and `workflow`. The token never touches your laptop, and you can revoke it on its
  own. `--from-gh` reuses your laptop's gh login instead. `--token` reads a token you made, such
  as a fine-grained one, from a hidden prompt or stdin, never from an argument. In machines,
  `git clone`, `git push` and `gh` work, and commits carry your GitHub name and noreply address.
- `hib connect codex` logs Codex in on the host with your ChatGPT plan. Only the host talks to
  OpenAI to refresh the login. When Codex in a machine asks for a refresh, the proxy answers it,
  so a new real token never reaches a machine.
- The GitHub Copilot CLI uses the GitHub connection, if your account has Copilot. The device
  flow's token and gh's both work. A fine-grained token needs the Copilot Requests permission,
  and classic `ghp_` tokens don't work.

For another tool that sends its key in an `Authorization` header over HTTPS, `hib secret add`
stores a bound secret of your own, for example an OpenAI API key bound to `api.openai.com`.

## Saved images and forks copy a running machine

`hib image save app myimg` saves a machine as an image, memory included. A machine made with
`hib new task --image myimg` starts with everything that ran in `app`, a dev server included,
without restarting it. `hib fork app --count 2` copies `app` as it is now into `app-1` and
`app-2`, or the lowest numbers not yet taken. One fork makes up to ten copies, as many as the
host has room for. Each copy gets its own name and SSH host key, and keeps the origin's size. A
running origin pauses for a few seconds while its snapshot reaches disk, then resumes, and SSH
sessions into it may drop. A paused origin stays paused.

An image holds placeholders, not real credentials. It does hold anything you typed into the
machine yourself, so treat an image like the machine it came from.

## The host runs only while it's needed

The host is one Azure VM, a D4s_v5 with 4 vCPUs and 16 GB of memory. It costs about $0.20 to
$0.23 an hour while it runs, and about $14 a month while it's off, for its disk and IP address.
Four layers turn it off:

- After 30 minutes with no running machine, no SSH login and a load average under 1, the VM
  deallocates itself.
- Every night at 20:00 UTC, the host pauses every machine, busy or not, and deallocates.
- Azure's auto-shutdown at 21:00 UTC is a backstop.
- `make down` does the same by hand.

Logging in to a machine, or a `hib` command that needs the host, starts the VM again. A cold
start takes a few minutes.

## How the pieces fit

```text
Laptop                    Host, one Azure VM                         Machines, microVMs
hib --------- ssh ------> hibd ----------> E2B ----------------------> create, pause, wake
ssh app.hib - ssh ------> hibd bridge ---> E2B's proxy --------------> sshd
                          vault, with the real secrets
                          egress proxy <------ outgoing HTTPS -------- git, gh, codex, copilot
                            | swaps placeholders for bound sites only
                            +----> github.com, chatgpt.com, *.githubcopilot.com
```

- `hib` is the CLI on your laptop. It talks to the host only over SSH. The host opens no other
  port to the internet, and its SSH accepts keys only.
- `hibd` runs on the host. It names machines and tracks their state, keeps busy machines running,
  wakes paused ones, holds the vault, runs the proxy, and deletes the snapshots that nothing
  needs.
- E2B boots, pauses and resumes the microVMs, and builds images. It pauses a machine when its idle
  time runs out. Hibernaut runs it unmodified, pinned to one commit.
- `hib` and `hibd` are written in Go. No Hibernaut service runs inside a machine. `hibd` sets a
  machine up and checks it for coding agents through envd, E2B's agent in the machine. A
  machine's end of the SSH path is sshd and websocat.

## What the design protects against

| If an agent or a prompt injection in a machine... | Then |
| --- | --- |
| reads environment variables or files | Hibernaut's credentials there are placeholders |
| sends a placeholder to its own server | the proxy doesn't swap it, so the server gets nothing usable |
| gets hold of an image or a fork | Hibernaut's credentials in the copy are placeholders |
| connects to the host's own services or Azure's metadata endpoint | it's blocked. Only E2B's internal service address and the host's SSH port, which accepts keys only, still answer. |
| uses a credential against the real site, such as deleting a repo or spending your AI quota | **Not prevented.** `hib log` shows each HTTP request the real value went into, but not what a WebSocket sends after it opens. A narrower token, such as a fine-grained GitHub token, limits the damage. |

The proxy swaps only in the `Authorization` header of HTTPS requests on port 443, on purpose. If
it swapped values inside request bodies, a machine could store the real value at the site, in a
gist for example, and read it back. So a tool that sends its key in another header, such as
Anthropic's `x-api-key`, can't use a bound secret yet. Nor can a tool that pins certificates or
trusts only its own built-in CAs, because it refuses the proxy's certificate.

Responses from bound sites are scrubbed back to placeholders, but WebSocket messages after the
upgrade aren't. A bound site that shows request headers back could still leak the real value,
through another site, in another encoding or in WebSocket messages. So bind a secret only to the
services that use it. Whoever controls the host controls the vault.

## Limits of version 1

- Hibernaut serves one user from one host, with no high availability. It's a lab, not a
  production service.
- About three medium machines run at once. When memory runs out, `hib new` and waking a machine
  fail at once, and the error names the running machines you could pause.
- The nightly shutdown pauses busy machines too. A long agent run stops at 20:00 UTC, and its
  network connections drop.
- A large machine can't be saved or forked on the current host. That needs room for two large
  machines at once, 8 GiB, and the whole pool is 6.25 GiB.
- Machines have no public URLs, and there's no web console. Reach a machine's servers with
  `ssh -L`.
- Claude Code and OpenCode aren't connected yet.
- Every pause adds a snapshot, so disk use grows. `hibd` trims old snapshots, but by default only
  those of the end-to-end tests' machines. Setting `HIBD_TRIM=all` in the host's
  `/etc/hibernaut/hibd.env` and restarting `hibd` turns trimming on for every machine. Back up
  the machines you can't lose first.
- E2B runs in its local development setup, with a fixed API key and no TLS. The internet reaches
  only the host's SSH port.

## Measured on the current host

| What | Result |
| --- | --- |
| Wake a paused machine with `ssh` | 0.6 to 0.7 s |
| First `ssh` into a new machine | 0.9 to 1.5 s |
| Wake a machine after a host reboot | about 1 s |
| Wake a machine after the nightly shutdown, once the host is up | about 3 s |
| Start the host from off | a few minutes |
| Snapshots of one machine after 10 pauses, before and after trimming | 3.9 GiB, then 553 MiB |

## Run your own

Hibernaut's source is in a private GitHub repository. With access, you need an Azure
subscription, the Azure CLI logged in with `az login`, Go 1.26.2 or later, make, rsync, OpenSSH
9.6 or later and an SSH key at `~/.ssh/id_ed25519`. The host needs a VM size with nested
virtualization, such as the D-series v5, because Firecracker needs `/dev/kvm`. B-series VMs don't
have it.

```bash
./azure/create-vm.sh      # once: the VM, its identity, its auto-shutdown and the host setup
make up                   # start the VM, install E2B the first time, deploy hibd
make build                # build hib and hibd into bin/
bin/hib init --host azureuser@VM_IP --azure-group RESOURCE_GROUP --azure-vm VM_NAME
```

Nobody has run `create-vm.sh` from scratch yet. It repeats the steps that built the current VM by
hand. `make down` pauses every machine and deallocates the VM.

## Notes for AI agents

To drive `hib`:

- Add `--json` to `ls`, `status`, `new`, `fork`, `image ls`, `secret ls` or `log`. Each prints one
  JSON object on stdout, with `v`, the format's version, which is 1. When the command fails, the
  object also has `error`. A mistake on the command line prints help on stderr instead, with no
  JSON.
- `hib` exits 0 when done, 1 when it failed, 2 when it was used wrongly and 130 on Ctrl-C.
  `hib status --json` also exits 1 when something on the host is failing, with no `error`. Its
  `status` field says what.
- `hib exec NAME -- CMD` runs one command, waking the machine first if needed, and exits with the
  command's own exit code.
- Without a terminal, `hib rm NAME` needs `--yes` to delete.
- `hib ls --json` reports `room.sizes`, the number of machines of each size that still fit.
- `hib status --json` never wakes the host. A host that's off gives `"host":{"state":"asleep"}`
  and exit 0.
- Stopping `hib` doesn't stop work it started on the host. Run `hib ls` or `hib image ls` before
  you retry `new`, `fork` or `image save`.
- `hib secret add` reads the value from stdin or a hidden prompt, for example
  `hib secret add npm --env NPM_TOKEN --bind registry.npmjs.org < npm-token.txt`.
- `hib help COMMAND` shows a command's flags and examples.

Inside a machine:

- `GH_TOKEN` and the other credentials in the environment are placeholders. They look real. Use
  them as they are, because git, gh, `codex` and `copilot` work with them through the proxy.
- A credential works only in HTTPS requests on port 443 to its bound sites, in the
  `Authorization` header.
- Run long jobs in tmux. A tmux session survives a dropped SSH connection and a pause.
- Anything you write in a machine, a pasted key included, goes into images and forks made from
  it.
- Codex runs with approvals off and full access. The machine is its sandbox.

## Commands

| Command | What it does |
| --- | --- |
| `hib init --host USER@HOST` | Set this laptop up for a host. |
| `hib new NAME [--image IMAGE] [--size SIZE] [--idle 15m]` | Make a machine. |
| `hib ls` | List machines, their state and size, and when each pauses. |
| `ssh NAME.hib`, or `hib ssh NAME [-- CMD]` | Log in to a machine, or run a command there. |
| `hib exec NAME -- CMD` | Run one command on a machine. |
| `hib pause NAME` | Pause a machine now. |
| `hib rm NAME --yes` | Delete a machine and its disk for good. |
| `hib fork MACHINE [--count N]` | Copy a machine, memory included. |
| `hib image build dev` | Build the dev image in every size. |
| `hib image save MACHINE NAME` | Save a machine as an image, memory included. |
| `hib image ls`, `hib image rm NAME` | List images, or remove a saved one that no machine uses. |
| `hib connect github`, `hib connect codex` | Give machines your GitHub or Codex login. |
| `hib disconnect github`, `hib disconnect codex` | Remove a login from the host. |
| `hib secret add NAME --env VAR --bind SITE` | Store a bound secret. `secret ls` and `secret rm NAME` list and remove them. |
| `hib log` | Show the audit log of secret swaps. |
| `hib status` | Show the health of the host, E2B, GitHub, Codex and each secret. |
| `hib completion bash`, `zsh` or `fish` | Print a shell completion script. |
