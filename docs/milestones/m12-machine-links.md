# M12 Machine links

Status: **in progress** · Spec: §5 (Machines), §4.5, D6 · Needs: M10

**Goal:** a machine can reach a port on another of your machines. You publish the port from the
laptop with `hib port add api 8080`, and after that your other machines reach it as
`api.hib:8080`. Nothing is reachable until you publish it, and only you can publish.

It adds `hib port`, a link listener to `hibd proxy`, a virtual address per machine and a managed
`/etc/hosts` block. It changes `internal/egress/hook`, which today owns the only Hibernaut
iptables rule.

## Why

No goal in [G1-G5](../goals.md) needs machine links. But boxd and Fly open ports between your
own machines by default, so a reader compares, and Hibernaut's answer today is "they can't talk".
The gap is visible even though the feature isn't urgent.

The default has to be right before [G3](../goals.md#g3-a-try-server), where strangers share a
host, and before [G5](../goals.md#g5-hosted-service-if-people-ask), where tenants do. Choosing
"off, and you publish one port at a time" now means G3 keeps links off and G5 scopes them per
owner, instead of walking an open mesh back.

What it's for:

- a benchmark in one machine against a service in another.
- one database or mock API shared by machines forked from the same parent.
- a coordinator agent and worker agents over HTTP. [D6](../spec.md#6-decisions) gives a machine no
  CLI over other machines, so the network is their only channel.
- cluster tests.

Today's host runs about three `dev` machines at once (spec §5, Room), so a link is usually
between two or three machines, not a fleet.

### What we're not building

- **An open mesh between your machines**, the boxd and Fly default. E2B's envd watches LISTEN
  sockets on `127.0.0.1`, `localhost` and `::1` and runs
  `socat TCP4-LISTEN:PORT,bind=169.254.0.21,reuseaddr,fork TCP4:localhost:PORT` for each one
  (`packages/envd/internal/port/forward.go`). So a mesh would hand every agent the databases,
  debuggers and DevTools endpoints that are only bound to localhost because their owner thought
  that was private. One prompt-injected agent would reach all of them, and any machine could wake
  every other one.
- **E2B's `ALLOW_SANDBOX_INTERNAL_CIDRS`.** It's host-wide, it covers every port, the slot addresses
  it would have to name change on every resume and it can't wake a paused machine.
- **Pair-by-pair links** (`hib port add api 8080 --from worker`). Worth having, but the policy
  below is a strict superset of it, so it can be added later without changing anything written
  here.

## Design

The M12 code is built against this section. Code that has to differ updates it in the same commit.

### The shape

```
machine "worker"                      host                           machine "api"
  dial api.hib:8080                   hibd proxy                     sshd, your service
       |                                   |                              |
       |  /etc/hosts: 198.18.0.1 api.hib   |                              |
       v                                   |                              |
  TCP -> 198.18.0.1:8080                   |                              |
       |                                   |                              |
       +-- veth-7 -> nat PREROUTING ------->|  link listener :5444         |
           REDIRECT                         |  1 whose veth is this?       |
                                            |  2 what was the original dst?|
                                            |  3 is 8080 published on it?  |
                                            |  4 wake it if it's paused    |
                                            |  5 dial its slot address ----+--> 10.11.0.3:8080
                                            |  6 copy both ways, audit     |
```

Nothing in a machine knows the host is in the path. From the client's side it is a plain TCP
connection to `api.hib:8080`.

**The packages, and the seam between them.** Three are new, and the seam is in `internal/links`
so neither of the two that straddle it owns the other:

| Package | Owns |
| --- | --- |
| `internal/links` | **the contract**: the state document, the allocator, the store, every shared constant and error and the `Admission`, `Admitter`, `Closer`, `Controller`, `Runs`, `Gate`, `Guard`, `Dropper`, `Tokens`, `Keeper`, `Resolver`, `Slot`, `Store` and `Clock` types |
| `internal/linkproxy` | the listener: accept, check, dial, copy and audit |
| `internal/linkguard` | the nftables guard, run tokens and the Keeper |
| `internal/linkadmit` | admission, waking, rate limits, the activity clock, the run watcher, the sweep and the control socket |
| `internal/linkctl` | the control socket's client, used by every other hibd invocation |
| `internal/hostsblock` | rendering and merging the `/etc/hosts` block |
| `internal/statefile` | cancellable state locks and the fence's owner check, lifted out of `hibd` |

**The types are Go, not prose.** An earlier draft of this document wrote `Admission` out in four
places and the constants in two, which is three chances to disagree with the code and one more
with itself. So the contract is the code:

- [`internal/links/links.go`](../../internal/links/links.go): `Run`, `Admission`, `AdmitRequest`,
  `Admitter`, `Closer`, `Open`, `Stats`, `Controller`, `Runs`, `Gate`, `Guard`, `Dropper`,
  `Tokens`, `Keeper`, `Resolver`, `Slot`, `Store`, `Clock`, `Fresh` and `LeaseUntil`.
- [`internal/links/const.go`](../../internal/links/const.go): every constant, with the reason for
  each number next to it, and `Mark`/`Token`.
- [`internal/links/errors.go`](../../internal/links/errors.go): every refusal, which is also every
  `reason` the link log can write.
- [`internal/links/state.go`](../../internal/links/state.go): `Doc`, `Machine`, `Fence`,
  `FenceState`, the allocator and the publish and unpublish batches.
- [`internal/links/store.go`](../../internal/links/store.go): `NewStore` and `NewClock`, the
  production store and activity clock every slice goes through instead of opening the file.
- [`internal/statefile`](../../internal/statefile/statefile.go): the lock helpers lifted out of
  `internal/hibd/lock.go` with no behaviour change, so `links` can take the same locks without
  importing `hibd`, plus `Held`, which is how a fence's owner is checked.
- [`internal/linkproxy`](../../internal/linkproxy/linkproxy.go),
  [`internal/linkadmit`](../../internal/linkadmit/linkadmit.go),
  [`internal/linkctl`](../../internal/linkctl/linkctl.go) and
  [`internal/linkguard`](../../internal/linkguard/linkguard.go): the constructors, the `Config`s and
  the method sets, with the bodies left to the slices that own them. *Contract*, under Tasks, lists
  what each stub returns until then.
- [`internal/slots/slots.go`](../../internal/slots/slots.go) and
  [`internal/slots/resolver.go`](../../internal/slots/resolver.go): `Finder.Slot`, `Addr` and
  `Resolver`, the production `links.Resolver`, as stubs that return `links.ErrLinksOff`.
- [`internal/hibd/port.go`](../../internal/hibd/port.go) and `config.linkPort`/`config.linkLog` in
  [`internal/hibd/hibd.go`](../../internal/hibd/hibd.go): the dispatch entry and the two settings,
  so the CLI slice and the state slice do not both edit the switch.

This section says what those mean and why they are shaped that way. The files say what they are.
Where the two disagree, the files are right and this document is wrong, and the commit that
finds it fixes both.

**Everything Linux-only is an injected interface.** `linkproxy.Ingress` hides `hook.OnVeth`,
`hook.OriginalDst` and `slots.Finder`. `linkproxy.Dialer` hides the slot dial, `links.Runs` hides
the cgroups and `links.Guard` hides nftables. `links.Keeper` hides the guard from the listener and
the admitter, and `links.Resolver` hides `slots.Finder` from the Keeper. Slice workers run
`make test` on macOS, where the whole hook package is a stub whose functions all return
`errLinuxOnly` (`internal/egress/hook/hook_stub.go:15-33`) and `slots.Finder` is a concrete struct
that cannot be faked. Without that seam the listener's tests could not run where the listener is
written.

**`LinkPort=0` means links are off.** It was the default until the integration slice turned links
on, and `HIBD_LINK_PORT=0` still turns them off. Every slice before that shipped dark: the listener
wasn't started, the chain wasn't installed, the guard set wasn't created and `hib port` reported
that links were off. The integration slice changed the default to `links.Port`, 5444, once slices 1
to 6 had landed.

**With links off, every pause is M11's pause.** `hibd pause`, `hibd rm`, `pauseAll`, the leak scan
and `snapshot.go`'s internal pause raise no fence, send no `close`, never call `Drop` and set no
`PausedByHand`, and `pauseAll` creates no quiescing marker. `rm` and `cleanup` leave `links.json`
alone. Nothing serves links, so there is nothing to fence, and it is what keeps slices 1 to 6 dark:
until slice 7 a pause does not run as root, and `cmd/hib`'s tests run the real `hibd` in process as
the login user. Two things follow from it. A machine paused while links were off is not
`PausedByHand`, and the entry of a machine removed while links were off stays until the first
`cleanup` with links on. `cleanup` prunes `activity/<id>` files either way, because the activity
clock is fed whether links are on or not.

The proxy reads `HIBD_LINK_PORT` from `/etc/hibernaut/hibd.env` when it starts, and every other
`hibd` reads the same file through `HIBD_CONFIG` each time it runs, so a change takes effect with
the proxy's next restart, which `vm/setup-host.sh` does. A pause in between is still bounded by the
kernel guard, as E2B's own pauses are.

**`HIBD_LINK_PORT` is a port number or nothing.** Unset, empty and `0` all mean off, and a number
from 1 to 65535 means on. Anything else fails every `hibd` command with an error that names
`HIBD_LINK_PORT` and the value, rather than being read as off by one command and as on by another.
The readings can't be allowed to differ: a pause that read links as off would raise no fence while
the listener served.

### Names and virtual addresses

- A machine gets a **virtual address** from `198.18.0.0/15` the first time one of its ports is
  published. `198.18.0.0/15` is the benchmarking range (RFC 2544), which nothing routes and which
  E2B's per-sandbox deny set leaves alone.
- Addresses are handed out in order from `198.18.0.1` and **never reused**, not even after
  `hib rm`. A stale `/etc/hosts` line in a machine that was asleep through the change points at an
  address that belongs to nobody, and the listener refuses it.
- The name is `NAME.hib`, the same name `ssh NAME.hib` uses from the laptop. It resolves inside
  machines through `/etc/hosts`, not DNS, so there is no resolver to run and no image to rebuild.
- `hibd` keeps the address when a machine's last port is removed, so republishing is free and a
  machine's address is stable for its life.

### State

Published ports and virtual addresses live in **one file**, `$HIBD_STATE_DIR/links.json`
(`/var/lib/hibernaut/links.json`).

Not E2B metadata: labels are set at create and cannot be changed afterwards. There is no metadata
`PATCH` or `PUT`. Sandbox `PUT` only updates network, and `SandboxForkRequest` has only `Count` and
`Timeout` (`docs/research/e2b-images-and-fork.md` §4). Not SQLite: `hibd` has none, and the spec's
claim that it does is stale (this milestone fixes it).

It is keyed by **sandbox ID**, not by name, because the ID survives pause and resume and a name
can be removed and made again. `machines.pinPath` already keys SSH host keys this way, in
`$HIBD_STATE_DIR/host-keys/<id>`, so the pattern and its `idRE` guard exist.

The document is [`links.Doc`](../../internal/links/state.go), and these are the decisions in it:

- **An entry can exist before an address does.** `PausedByHand` and the pause fence are true of
  machines that have never published anything, and a machine that is only being fenced must not
  spend one of the 256 addresses. So `Doc.Entry` makes a bare entry and `Doc.Allocate` is a
  separate step, and `MaxMachines` counts machines **with addresses**, not entries.
- **Generations are per port.** `Machine.PortGen` carries one generation per published port, and
  there is no machine-wide generation beside it. An earlier draft had both, which meant
  publishing or unpublishing any port revoked the links running over all the others.
  `Machine.Gen(port)` is the only way to read one, and an admission records the generation of the
  one port it was granted for.
- **Addresses are never reused.** `Doc.Next` only goes up, and removing a machine does not give
  its address back. A machine that has been paused for a week still has the old `/etc/hosts` in
  it, and a reused address would silently point at a stranger.
- **Both batches are validated whole before anything is written.** `Doc.Publish` and `Doc.Unpublish`
  check and deduplicate the entire batch first, so a batch naming one reserved port leaves the
  machine as it was, and a first publish that fails validation leaves no entry behind. Ports already
  published, or already absent, come back as no-ops rather than failures.
- **The fence is in the document, and it names its owner.** `Machine.Fence` is set while hibd is
  pausing, saving, forking or removing a machine, and admission refuses while it is up, for **both**
  endpoints, not only the target. It is persisted rather than kept in memory because the pausing
  process is not the one serving links. The owner is a lock file the kernel releases when the
  pausing process dies, so a hibd killed mid-pause is told apart from a `save` that is legitimately
  ten minutes long. An orphaned fence is still refused until `hibd cleanup` has reconciled it with
  the machine's real state (*Waking, keeping awake and pausing*, item 3).
- **`omitzero`, not `omitempty`.** An allocated empty `Ports` means "this machine publishes
  nothing", and `omitempty` cannot tell that from a field that was never written.

**Ownership and mode.** This is the part that is easy to get wrong. `hibd port`, `hibd ls` and
`hibd secret ls` run as the **login user** over SSH (`cmd/hib/main.go`'s `remote` runs `hibd` on the
host, and `hibd`'s dispatch is `internal/hibd/hibd.go:209-255`). `pause`, `rm`, `fork`,
`secret add`, `secret rm`, `connect`, `disconnect`, `status` and `image save` run as **root**
through `sudo -n`, and `hibd daemon` and `hibd proxy` run as **root** under systemd. `pause` and
`rm` need root because their fallback drops the machine's gates in the kernel (*The kernel's own
fence*, 10), and slice 7 moves them to `sudo -n`. `internal/atomicfile` does not chown, so a
root-written file would be root-owned and the login user could not read it.

`internal/machines` already solved this, and M12 reuses the solution rather than inventing one:
`directoryOwner` (`internal/machines/ownership.go:18`) reads the state directory's owner and refuses
a directory others can write to, and `publishOwned` (`:37`) writes `0600`, chowns to that
owner when running as root and renames into place. `openLock` (`internal/machines/statedir.go:15`)
opens `hibd.lock` the same way, with a root branch and a non-root branch. Slice 4 lifted all three
into `internal/statefile` (`internal/statefile/owned.go:47,98,209`), and the `internal/machines`
names are now wrappers over them.

- `links.json` and `links.lock` are **mode `0600`, owned by the state directory's owner**, which
  is the login user. Root reads and writes them through the same ownership-aware path, so a file
  root writes is still the login user's. Root can read a `0600` file it doesn't own, so the
  proxy reading as root is fine.
- **Root creates M12's locks the way `openLock` creates `hibd.lock`**: through `publishOwned`, then
  checked to be a regular private file the owner holds. That covers `links.lock` and every
  `links.FenceName`. `statefile.OpenLock` does not chown, so a `links.lock` that the daemon's first
  idle pause created would be root's and the login user's `hibd port` could never take it.
  `statefile.Held` never creates a file, because a fence lock that is not there has no holder.
- These helpers are unexported in `internal/machines`, and the cancellable `waitLock` is unexported
  in `internal/hibd` (`internal/hibd/lock.go:12`). `internal/links` cannot import either without a
  cycle, because `hibd` imports both. **The contract commit extracts the locks** into a new package
  `internal/statefile` (`WaitLock`, `TryLock`, `OpenLock` and `Held`), moved verbatim, with `hibd`'s
  three wrappers delegating so its call sites are untouched (`internal/machines/machines.go:689`,
  `internal/hibd/hibd.go:1335`). `Held` is new: it takes `LOCK_SH|LOCK_NB` and reports whether an
  exclusive holder is alive, which is how a fence's owner is checked.
- **The ownership helpers are not extracted in the contract commit, and that is deliberate.**
  `stateRoot`, `publishOwned` and `ensureDirectory` are `*Manager` methods over the manager's own
  directory and uid, and their tests are root-only host tests
  (`internal/machines/ownership_host_linux_test.go`). Lifting them blind would be a "no behaviour
  change" claim nobody on this branch can check. Slice 4 does it, because slice 4 is the only code
  that writes `links.json`, and it takes `internal/machines/ownership.go` and
  `internal/machines/statedir.go` with it. Those two files belong to slice 4, not to slice 3, so the
  file ownership stays disjoint.
- **Lock order.** `links.lock` is its own file and is always taken **last**: after `hibd.lock` if
  a command needs both, and never while a login lock (`codex`, `github`, `cleanup`) is held. No
  code path today takes a lock while holding another, and M12 doesn't add one, so this is a rule
  to keep rather than a knot to untie.

**Reading, and what the proxy does when it can't.**

- The listener only reads. It caches the parsed file and reloads when the file's modification
  time, size or inode changes, so a busy link doesn't re-read per connection.
- **A failed or corrupt read fails closed, for open links too.** The cache is dropped, every new
  link is refused with `links.ErrStateUnreadable` and the error is logged once per minute rather
  than per connection. A file that can't be read can't say which machines are fenced, so the Keeper
  counts every one of them as fenced: it retires every gate, and the listener aborts the links that
  hold them (*The kernel's own fence*, 7). Links are admitted again once the file reads cleanly.
- **An unknown `V` fails closed** the same way. A file the running `hibd` doesn't understand is
  never rewritten, so downgrading can't silently drop someone's ports.
- **Out of addresses** (`Next` past the end of `links.CIDR`) is `links.ErrNoAddresses`. With
  `MaxMachines` at 256 it cannot be reached in v1. It is checked anyway because `Next` never goes
  back. The cap itself is `links.ErrTooManyMachines`, and it counts machines **holding an address**,
  not machines with an entry: pausing a machine that has never published creates an entry with no
  address and does not consume one.

**Batches are validated whole, then applied whole.** `hib port add api 8080 9090` either publishes
both or neither. [`Doc.Publish` and `Doc.Unpublish`](../../internal/links/state.go) validate and
deduplicate the **entire** batch (the range and reserved ports, and for a publish the per-machine
cap) before touching anything, so a bad port in the middle cannot leave half a batch, and a
**failed first publish leaves no entry at all**: the machine is not created on the way to finding
out that its ports were invalid. `Doc.Allocate` checks the address pool, inside the same
`Store.Update`, which writes nothing unless the whole function succeeds.

Adding a port that is already published, and removing one that isn't, are **not** errors. They
are returned in the `noop` list and reported as no-ops in the message, so scripts are idempotent.
A batch that is *entirely* no-ops is an error, because the caller asked for a change and got
none. `Unpublish` on a machine with no entry at all is the same no-op and never creates one.
`Ports` is kept ascending and deduplicated. `hib port ls` sorts by name and `links.json`, `hib
ls` and the audit all order the same way, so output is byte-stable between runs.

**Entries come and go with the sandbox.**

- `hib rm` removes the entry. Its address stays burned, because `Next` never goes back.
- `hibd cleanup` drops entries whose sandbox E2B no longer knows, so a machine killed outside
  `hib rm` doesn't leak one.
- A port's generation starts at 1 when it is first published and rises on every later change to
  that port, including its removal. It never resets, and it is kept after the port is gone, so a
  stale generation can never match a live one and republishing a port does not resurrect the
  links that were open over the old one.
- A machine with no entry still has `PausedByHand` to record. Pausing a machine that has never
  published creates an entry with its ports empty and **no address**: `Machine.Addr` stays
  invalid until the first publish, so pausing doesn't burn addresses. `hib port ls` ignores
  entries with no address.

**The activity clock is not in `links.json`.** It is written far too often to wait on `links.lock`.
Each machine that has non-link activity gets an empty file
`$HIBD_STATE_DIR/links/activity/<sandbox-id>` whose **mtime is the whole value**: no shared lock and
no parsing, only an `flock` on the file itself and one `futimes`.

**The mtime only moves forward.** Callers pass their own times, and not always the newest:
`busy.Config.Activity(id, at)` reports when it observed and the SSH heartbeat when it ticked, from
different processes, so two touches can arrive out of order. `Touch` opens the file through the
state root with `O_NOFOLLOW`, takes `LOCK_EX` on it, reads the mtime with `fstat` and calls
`futimes` only when `t` is later. `futimes` keeps microseconds and rounds up, so the file never
records a time earlier than `t`. The lock is per machine and held for those two calls, and `Active`
only stats. Setting an explicit time needs the file's owner or root, which holds because the file is
the state directory owner's. **A first touch records `t`, not the time the file was made.** Creating
the file would set its mtime to now and turn a late observation into fresh activity. So when there
is no file, `Touch` creates a temporary one in the same directory, sets its mtime to `t`, gives it
to the state directory's owner when it runs as root and publishes it with `link`, which never
replaces a name that exists. If another touch published first, the link fails with `EEXIST` and
`Touch` takes the locked path above. Either way it removes the temporary file, whose name starts
with a dot so that no machine's ID can match it.

The clock is [`links.Clock`](../../internal/links/links.go) and the production one is
`links.NewClock(stateDir)`: `Active(id)` returns a machine's last non-link activity, the zero time
when there has never been any, and `Touch(id, t)` records some. `linkproxy` never calls `Touch`.
Neither does a link waking a machine. [`links.Fresh`](../../internal/links/links.go) and
`links.LeaseUntil` are the only two rules built on it, written once so waking and renewing cannot
drift apart.

The directory is `0700` and the files `0600`, owned by the state directory's owner like
everything else. `hibd cleanup` deletes files for sandboxes E2B no longer knows. Losing the
directory is safe in the direction that matters: with no activity recorded, links renew nothing
and machines pause on their own timeouts.

### The hibd API

`hibd` gains one command with three subcommands. Like `ls` and `secret ls`, they run **as the login
user** over SSH, not as root: `cmd/hib`'s `remote` runs `hibd` on the host and
`internal/hibd/hibd.go:209-255` dispatches it. `pause`, `rm`, `fork`, `secret add`, `secret rm`,
`connect`, `disconnect`, `status` and `image save` run through `sudo -n`, and `hibd daemon` and
`hibd proxy` are root under systemd (*Ownership and mode*, above).

```
hibd port add NAME PORT [PORT...] [-json]
hibd port rm  NAME PORT [PORT...] [-json]
hibd port ls  [-json]
```

`-json` prints one `hibapi.Response`, as every other `hibd` command does. The new types are frozen
in [`internal/hibapi/hibapi.go`](../../internal/hibapi/hibapi.go): `Ports` is one machine's
published ports, `PublishedPort` is one port and how many links are using it right now and
`LinkEntry` is one line of the link log.

- `Response.Ports []Ports` carries every machine from `port ls`, and the single machine that
  `port add` or `port rm` changed. `Response.Links []LinkEntry` carries `hib log --links`. Both
  are **`omitzero`, not `omitempty`**, which is the whole reason the fields are shaped this way:
  an allocated empty slice means "nothing is published", and `omitempty` cannot tell that from a
  reply that was never about ports. So `port ls` with nothing published is exactly
  `{"v":1,"ports":[]}`, which a script can range over, and no other command's JSON changes.
- `Machine` gains `Ports []int \`json:"ports,omitzero"\`` so `hib ls --json` carries them.
- `PublishedPort.Open` is **not** a pointer and is never omitted. It is how a person finds out why a
  machine is awake *now*, rather than reading it out of a log afterwards, so a zero has to be
  visible. When the proxy doesn't answer, `hibd` says so in the text output and the field stays 0
  (see Messages and errors).
- `hibd version` adds `hibapi.FeaturePorts = "ports"` to `Features`. `hib port` says
  `this host's hibd is too old for hib port. Upgrade it` when it's missing, the way
  `cmd/hib/disconnect.go` already does for `FeatureDisconnect`.
- The decoder requires `v` (`internal/hibapi/hibapi.go:356`), so every example above carries it.

`port add` and `port rm` both:

1. resolve the name to a sandbox ID first, and fail with the usual `no machine called %q` if there
   isn't one. Everything after this point is keyed by **ID**, because a name can be removed and made
   again while the command runs.
2. validate the whole batch before changing anything: each port 1-65535, not in `links.Reserved` and
   the result within `MaxPorts`.
3. `Store.Update`: take `links.lock`, apply the batch, allocate an address if the machine has none
   and the batch publishes something, raise `Gen`, write.
4. for `port rm` only, **revoke what is already open** before reporting success (below).
5. call `pushSetup` and report its outcome rather than waiting for it (below).

**Revocation is the point of `port rm`** (security S1). Taking a port out of `links.json` stops new
links. It does nothing to a connection that is already open or to one sitting in a wake. So:

- `hibd proxy` listens on a **control socket**, `/run/hibernaut/links.sock`, mode `0600` and chowned
  to the state directory's owner like `links.json`, so the login user's `hibd` can talk to it and
  nothing else can. Machines have no path to it: it is a unix socket on the host. It is served
  whenever links are configured on, even while the listener is off (*Startup and shutdown*).
- The protocol is one JSON line in, one out, and the shapes are
  [`linkctl.Request`](../../internal/linkctl/linkctl.go) and `linkctl.Response`: `revoke` for an
  `(id, ports)` pair, `close` for a whole machine and `stat` for the counts `hib port ls` shows. A
  reply carries `closed`, or `err` when the call failed. `stat`'s reply counts links **per published
  `(target, port)`** as well as per machine, because that is the number `hibapi.PublishedPort.Open`
  prints.
- `revoke` cancels **pending admissions** for that `(id, port)` first (connections waiting on a wake
  or a dial) and then closes established ones, and only answers once both are done.
- The `Gen` and `PortGen` re-check happens after the wake and the dial and **before the first
  byte is forwarded**, so a revocation that lands mid-wake is caught even if the control message
  and the connection race. Because the generation is per port, revoking 8080 leaves open links on
  the same machine's 5432 alone.
- `hibd port rm` reports success only after the socket answers without an error. If the socket is
  missing or doesn't answer within `links.CtlTimeout`, the ports are still unpublished (the file
  was written first) and the command says so: `api:8080 is unpublished, but hibd proxy didn't
  confirm it closed open links: check hib port ls`. Exit code 1, because the caller asked for
  access to stop. This is the one caller that never downgrades to a warning.
- `hibd pause` and `hibd rm` send `close`, which is the same mechanism D3 needs for pausing, but
  they raise the fence first and, if the ack doesn't come, delete the machine's elements from the
  kernel guard rather than pause past sockets the proxy may still hold (*The kernel's own
  fence*, 10).
- The unit gains `ReadWritePaths=/run/hibernaut`. Not `RuntimeDirectory=hibernaut`: systemd
  deletes a `RuntimeDirectory` when the unit stops, and `/run/hibernaut` also holds the quiescing
  marker (`vm/lib/quiesce:11`), which must outlive a proxy restart.
- **`ReadWritePaths` needs the directory to exist before the unit starts.** systemd sets up the
  mount namespace before `ExecStart`, so a missing `/run/hibernaut` fails the unit with
  `226/NAMESPACE`, and a `mkdir` in the proxy is far too late to help. `/run` is a tmpfs, so
  after a cold boot it is always missing. M12 therefore ships
  `vm/systemd/hibernaut-tmpfiles.conf` (`d /run/hibernaut 0755 root root -`), installed into
  `/usr/lib/tmpfiles.d/`, which `systemd-tmpfiles-setup.service` runs before any of our units.
  The proxy still unlinks its own socket on the way out.
- The unit also gains `WatchdogSec=30` and `Restart=always`. The proxy pings the watchdog from a
  loop of its own, every third of `WATCHDOG_USEC`, and only while its serve goroutines run and,
  with links configured on, the control socket answers `stat` (`internal/hibd/watchdog.go`), so a
  wedged proxy is eventually replaced. That is recovery, not protection: the kernel guard has shut
  a wedged proxy's gates long before the watchdog fires (*The kernel's own fence*, *What was
  rejected*).

**Delivery is not promised** (D10). `pushSetup` returns without having reached every machine:
`internal/hibd/delivery.go:52-75` skips the whole push while the host is quiescing, gives the batch
30 s and only warns on failure. `port add` therefore **commits the file, pushes and warns if the
push failed**, and the daemon's next pass retries. The message never claims the name resolves yet:

```
api:8080 is published to your machines as api.hib:8080
warning: published ports saved, but updating web failed: <reason>
```

The warning is `pushSetup`'s, shared with `secret add` and `connect github`: it names what was
saved, the step that failed and why.

A machine that hasn't got the block yet can still reach the port by address. Only the name is late.

### The hib CLI

```
  port add NAME PORT...      publish a machine's port to your other machines
  port rm NAME PORT...       stop publishing a port
  port ls                    list published ports
```

They go in `cmd/hib/commands.go` after `exec` and before `connect github`, as a `port` parent
with three subs, exactly like `secret`. All three take `--json` and nothing else.
`cmd/hib/readme_test.go` compares the README's command reference with `hib help`, so **the README
changes in the same commit**.

```
$ hib port add api 8080
api:8080 is published to your machines as api.hib:8080

$ hib port add api 5432 6379
api:5432 and api:6379 are published to your machines as api.hib:5432 and api.hib:6379

$ hib port rm api 6379
api:6379 is no longer reachable from your machines

$ hib port ls
NAME  HOST     IP          PORTS       OPEN
api   api.hib  198.18.0.1  5432,8080   2
web   web.hib  198.18.0.2  3000        0
```

- `hib port ls` with nothing published prints **the header row and nothing else**, the way
  `hib secret ls` does: `cmd/hib/secret.go:54` calls `hibapi.PrintSecrets` unconditionally and
  `internal/hibapi/table.go:129` prints the header before the loop. It then adds
  `no published ports. hib port add NAME PORT publishes one` on stderr and exits 0.
- **`OPEN` is how many links are open right now**, from the proxy's `stat` call, and `-` on every
  row when the proxy didn't answer. `--json` then has `open` null on every port, as it always does
  in the replies to `port add` and `port rm`, which don't ask. It is what answers "why is this
  machine awake?" while it is happening, which an audit line written at close cannot (D8).
- Lists join with `,` and no space, like `hib secret ls`'s sites. Several names in a sentence use
  `cmd/hib/main.go`'s `and` joiner.
- Every value printed goes through `safe()` (`termsafe.Line`), like every other table.
- **Exit codes** follow the rest of `hib`: 0 on success, 2 for a usage mistake (a bad port, an
  unknown flag, a name that isn't a name), 1 for everything the host refused or couldn't do,
  including `port rm` that unpublished but couldn't confirm the close.
- `hib log --links` prints a link table with a header printed unconditionally, like
  `printLogEntries` (`cmd/hib/secret.go:131-144`): `TIME MACHINE TO PORT RESULT WOKE BYTES`. `PORT`
  shows `-` on a line with no port, which is a refusal while the host quiesces or a source's
  overflow line, and `--json` keeps 0. `hibd log --links` prints its table the same way, because
  both call `hibapi.PrintLinkEntries`. `--json` fills `Response.Links`, and `Corrupt` counts the
  lines it couldn't read. `hib log` without the flag is byte-for-byte what it is today. `--links` is
  gated on `FeaturePorts`, with a too-old message of its own that names `hib log --links`
  (`cmd/hib/secret.go:96`).
- **Warnings and the fork hint reach the CLI as text, not as structure.** `hibd` writes them to
  **stderr**, `hib` passes stderr through and `--json` leaves them out of the document entirely.
  This is what `hibd`'s `warn` already does (`internal/hibd/delivery.go:56-60`). No new field
  carries advice.

**Two places meet [M11](m11-bot-faces.md)**, which landed first (`fc1f7b5`):

- `hib ls` gains a `PORTS` column after `IDLE`, `-` when a machine has none. M11 puts a face in
  front of every row of the same table (`cmd/hib/main.go:278-288`). `faces.table` only prefixes
  each line, so the new column needs nothing from it.
- the fork hint is written to stderr by `hibd`, from `internal/hibd/fork.go` (slice 7). `hib`
  relays `hibd`'s stderr (`cmd/hib/remote.go:185-197`), and M11's live line prints it
  (`cmd/hib/liveline.go:42`, `live.Write` at 207-226). `hib` adds no text of its own, so
  `cmd/hib/fork.go`, which M11 rewrote, does not change and the hint keeps the live line and the
  face.

Keeping M12 to one column, one field and one extra line is what keeps both small.

**Completion is not free.** `cmd/hib/commands.go` drives help and mistakes from the command
table, but machine-name completion is a hand-written switch on the command path in
`cmd/hib/complete.go:207-211`, which listed `"rm", "pause", "ssh", "exec", "fork"`. Slice 4 added
`"port add"` and `"port rm"` there, so they complete a machine name in the first position. `port ls`
takes no argument and needs nothing.

### Delivering /etc/hosts

The hosts block rides `setupDelivery`, the same path that already writes the env block, git's
identity and Codex's login file: on create, after a resume and whenever the content changes. **It is
best-effort, and the design doesn't pretend otherwise.** `pushSetup` skips every machine while the
host is quiescing, gives the batch 30 s and only warns when a machine doesn't take it
(`internal/hibd/delivery.go:52-75`). The daemon's next pass, every 15 s by default, retries.
`hib port add` and `hib port rm` push at once rather than waiting for it. So: a port is published
the moment `links.json` is written, and the **name** follows when delivery succeeds. Nothing in the
design waits on a name resolving.

**The messages say only what is true.** `hib port add` reports that the port is *published*, not
that the machine is *reachable*, and a delivery failure names the machine that missed it and the
reason without naming a time. `delivery.go:52-75` guarantees neither reachability nor a deadline (it
skips quiescing machines entirely and only warns on failure), so neither is promised.

```
# BEGIN hibernaut machines: hibd manages this block and replaces edits
198.18.0.1 api.hib
198.18.0.2 web.hib
# END hibernaut machines
```

```go
// Package hostsblock renders and merges hibd's managed block in /etc/hosts.
package hostsblock

// Host is one line in the block.
type Host struct {
	IP   string // a dotted quad from 198.18.0.0/15
	Name string // "api.hib"
}

// Render returns the block, sorted by name, or nil when there are no hosts.
func Render(hosts []Host) ([]byte, error)

// Merge returns file with block put in place of any existing block, appended when there is
// none, and removed when block is nil. Lines outside the markers are untouched.
func Merge(file, block []byte) ([]byte, error)

// Errors: a name that isn't NAME.hib, a reserved name, an IP outside the range, a duplicate
// name, an unterminated block in file.
var ErrReservedName, ErrBadName, ErrBadIP, ErrDuplicate, ErrUnterminated error
```

```go
// In internal/machines, next to WriteBlock.

// WriteHosts writes block into id's /etc/hosts through envd as root, with the same
// compare-and-swap WriteBlock uses for /etc/environment.
func WriteHosts(ctx context.Context, e engine.Engine, id string, block []byte) error
```

- **`machines.WriteBlock` was not reusable as-is.** It was `/etc/environment`-specific: its doc
  comment said so and it called `readEnvironment` and `compareAndSwapEnvironment`. M12 adds
  `WriteHosts` beside it and factors the shared four-try compare-and-swap (read, sha256, write if
  unchanged, exit 75 on a mismatch) into `managedFile.write` (`internal/machines/env.go:42-79`),
  which both call with their own path and merge function.
- `internal/hostsblock` and `internal/envblock` **share only the idea**, not code: marker
  scanning and merging are the same shape, but `/etc/hosts` syntax and `/etc/environment` syntax
  are not, and the markers differ. They stay separate packages with their own tests.
- `machines.Setup` gains `Hosts []byte`. `Setup.Write` writes it after the env block.
  `Setup.Hash` gains the field and its version string goes to `"hibernaut machine setup v2"`, so
  every machine is rewritten once after the upgrade.
- The block is **the same for every machine**: the full list of machines with published ports,
  the reader included. So one hash covers the host, and `setupDelivery`'s existing cache works
  unchanged. A machine with no published ports still gets the block, because it may be a client.
- Lines outside the markers are left alone. The block never contains `localhost`, `127.0.0.1`,
  `::1` or the machine's own hostname, and every name in it ends in `.hib`.
- When no machine has a published port the block is empty, `Render` returns nil and `Merge`
  removes it.

### Traffic: one chain, one jump

Today `internal/egress/hook` keeps exactly one rule:

```
-A PREROUTING -i veth-+ -p tcp --dport 443 -m comment --comment hibernaut -j REDIRECT --to-ports 5443
```

`Ensure` inserts it at `nat PREROUTING 1` and then deletes every other rule commented `hibernaut`.
Worse, `parseHibernautRule` **returns an error** for any `hibernaut`-commented rule that isn't
exactly that shape, so `listRules` fails, `Ensure` fails, `Keep` logs and retries forever without
repairing the 443 hook and `Remove` deletes nothing. A second rule added beside it doesn't flap: it
breaks the egress proxy. So M12 changes the hook instead of adding to it.

**The layout.** One jump in `PREROUTING` and one chain that Hibernaut owns:

```
# nat PREROUTING
-I PREROUTING 1 -i veth-+ -m comment --comment hibernaut -j HIBERNAUT

# nat HIBERNAUT
-A HIBERNAUT -d 198.18.0.0/15 -p tcp -j REDIRECT --to-ports 5444
-A HIBERNAUT -p tcp --dport 443 -j REDIRECT --to-ports 5443
```

- The jump is at position 1, so it stays ahead of E2B's per-veth redirects, which `tcpfirewall`
  **appends** with `-A PREROUTING`. A packet the chain doesn't match returns and carries on
  through the rest of `PREROUTING`, so E2B's rules still see everything they see today.
- The link rule is first in the chain, so a machine that publishes port 443 is reached, not sent
  through the egress proxy (which denies `198.18.0.0/15` anyway and would reset it).
- `hook.Rule` grows from `{Port, Iptables}` to `{ProxyPort, LinkPort, LinkCIDR, Iptables}`.
  `Ensure`, `Keep` and `Remove` keep their names and their contract. `Remove` deletes the jump,
  flushes the chain and deletes the chain. **`Rule.Port` had a caller:** M11's
  `internal/hibd/proxy.go` built `proxyRule{hook.Rule{Port: port}}`, so the field rename was
  part of the contract commit, not of a slice. `proxyHook` now builds `hook.Rule{ProxyPort: port}`
  and adds the link fields when links are on (`internal/hibd/proxy.go:214-223`).

**Install and repair are one transaction.** Flushing a live chain and refilling it is *not* safe:
while `HIBERNAUT` is empty, every packet falls straight through to the rest of `PREROUTING`, 443
escapes interception and conntrack can pin that bypass for the life of the connection even after the
rules come back. So:

- `Ensure` builds the whole `*nat` table fragment and pipes it to **`iptables-restore
  --noflush`** in one call, touching only `HIBERNAUT` and its jump. One transaction, no empty
  window. `iptables-restore --noflush` leaves every other chain alone, which is what makes this
  safe to run next to E2B's rules.
- **Order inside the transaction:** create the chain if missing, write its rules, *then* insert the
  jump. A chain that nothing jumps to does nothing. A jump to an empty chain is the hole. On a
  repair the jump is already there, and replacing the chain's contents in one transaction never
  shows an empty chain.
- **The legacy rule is deleted last**, after the new path is complete and serving. An upgrade
  therefore goes from "old rule works" to "both work" to "new rule works", never through "neither
  works". An interrupted migration leaves both, which is harmless: the chain's 443 rule and the
  legacy rule redirect to the same port, and the next `Ensure` finishes the job.
- **Comparison is normalized.** `iptables-save` writes `-m tcp` that `iptables -A` doesn't require,
  and M11's parser already had to skip it. `Keep` canonicalizes both sides (drop `-m tcp`, normalize
  `--to-port`/`--to-ports`, sort nothing, `internal/egress/hook/rules.go:129-145`) and only
  rewrites when the canonical forms differ. That is what stops `Keep` rewriting and logging on
  every five-second pass.
- **Migration.** `Ensure` deletes a bare `-A PREROUTING … --comment hibernaut … --dport 443`
  rule left by the previous `hibd`. M11's strict parser is gone: matching is by comment and shape,
  and an unrecognised `hibernaut` rule is deleted rather than turned into an error, so one stray
  rule can never again stop the hook from being repaired.

**The listener.** It lives in a new package `internal/linkproxy`, and `hibd proxy` serves it, not
`hibd daemon`, because `hibd proxy` already owns the
iptables hook, the `slots.Finder`, `hook.OnVeth`, `hook.OriginalDst` and the audit log, and its
unit has `LogsDirectory=hibernaut` where `hibd.service` has none. It binds `0.0.0.0:5444`
(`HIBD_LINK_PORT`), because `REDIRECT` rewrites the destination to the veth's own host address,
which differs per slot.

Per connection, in order:

1. **refuse everything while the host is quiescing**, before any other check, and before any
   state is read. A pause-all must not race a wake.
2. `hook.OnVeth(conn)`: refuse anything that didn't arrive on a `veth-*` interface.
3. `hook.OriginalDst(conn)`: the address and port the client meant.
4. the destination must be in `198.18.0.0/15` and must be an address `links.json` has handed out.
   Otherwise refuse.
5. `slots.Finder.Sandbox(src, local)`: which machine is calling. This already checks that the source
   `10.11.x` slot and the local `10.12.x` slot agree, so a machine can't claim to be another by
   spoofing its source address. `SO_MARK` on the accepted socket gives the token of the run that
   was at the source slot when the SYN arrived (*The kernel's own fence*, 4). A socket with no link
   mark is refused with `links.ErrUnmarked`.
6. **refuse a self-link**: source ID equal to target ID. A machine reaching its own published
   port over the link path has a localhost it can use instead, and a self-link is the shortest
   way to keep a machine awake forever.
7. the port must be published on the target in the current `links.json`, and must not be in
   `links.Reserved`. Remember that port's generation.
8. **bind the connection to the source's run, then `Admit`.** `Runs.Current(source)`, then
   `Keeper.Gate` for that run: the gate's address must be the connection's source address and its
   token the one on the socket, or the connection is refused with `links.ErrWrongRun`. Then
   `Admit`, **always, whether the target is paused or running**. One atomic admission checks
   quiescing, `PausedByHand`, the source's own activity freshness, the wake rate limit, the
   per-source, per-target and host caps and the room reserve together under one lock, reserves what
   it needs and coalesces with any wake already in flight for the same sandbox ID. A running target
   is admitted too: skipping it would skip the caps, the revocation check and a by-hand pause that
   `save` is temporarily masking. It returns the target's run and gate and whether this call did
   the waking. The admitter asks the Keeper for the target's gate after any wake, so the gate is
   open before the dial. A wake takes about 0.6 s.
9. **dial the target's gate**: `TargetGate.Addr:<port>`, with `SO_MARK` set to
   `links.Mark(TargetGate.Token)` and `SO_LINGER 0`, both before connect. The Keeper resolved the
   address when it opened the gate, and a run never changes slot, so the address cannot be stale
   while the gate is open.
10. **re-check before the first byte moves**, all together: that both ends' runs are still the
    ones `Admit` returned, that the port's generation is unchanged and that neither gate has been
    retired. E2B returns a slot to the pool 3 s after a pause, and the Keeper retires a gate as soon
    as its run stops being live, so a retired gate is how a recycled slot shows up here. And
    `hib port rm` may have landed during the wake. Any mismatch aborts both halves and audits the
    reason.
11. **arm the aborts** before any byte moves: both runs' cgroups (`Runs.Watch`), both gates
    (`Keeper.Retired`) and `Admission.Ctx`. Any of them aborts both halves. The listener opens no
    gate itself. Both were open before the link's first packet: the source's because prerouting lets
    nothing through without it, and the target's because `Admit` waited for it.
12. copy both ways, renew the target's **link lease** while payload bytes move and while the
    source's own activity is fresh and write an audit line when the link opens as well as when it
    ends.

`internal/slots` gains the reverse of `Sandbox`, and the resolver built on it:

```go
// Slot returns the E2B slot that runs sandbox id.
func (f Finder) Slot(id string) (int, error)

// Addr returns the host address of a slot, 10.11.<slot>.
func Addr(slot int) (netip.Addr, error)

// Resolver is the production links.Resolver: Finder for the slot and Runs for the run.
type Resolver struct {
	Finder Finder
	Runs   links.Runs
}

func (r Resolver) Addr(id string) (netip.Addr, links.Run, error)
func (r Resolver) At(addr netip.Addr) (links.Slot, error)
```

`Slot` globs `sbx-<id>-*` under `Cgroups`, reads `cgroup.procs`, reads each PID's
`/proc/<pid>/ns/net` inode and finds the `/run/netns/ns-N` with that inode. It is the same walk
`sandboxForNetns` does, in the other direction, over the same three roots. `Resolver.Addr` reads the
run with `Runs.Current`, the slot with `Finder.Slot` and the run again, and fails with
`links.ErrNotRunning` unless the two reads agree. `Resolver.At` maps a slot address back to the
machine and run there, the same way. The Keeper calls them once per run, when it opens the run's
gate, and the close fallbacks call `Addr` to find what to drop.

**A refused connection sends no bytes before it closes.** `e2e/hostaccess_test.go:105-120` probes
the host from inside a machine by *writing first* (an SSH banner on port 22, a Postgres `SSLRequest`
elsewhere) and counting the port as reachable only if the far end answers. So the rule is about
refusals, not about who speaks first in general: on a refusal the listener closes without writing,
so a probe sees nothing. On an **allowed** link the proxy is a pipe, and a server-first protocol
(Postgres, MySQL, SMTP, an SSH banner) works normally, because the bytes come from the target, not
from `hibd`. `TestNoHostAccess` passes because 5444 refuses the probe, which never has an allocated
virtual address to aim at.

Links never go through the egress proxy. There is no TLS interception on a link, no placeholder swap
and no CA involved: `hibd` copies bytes.

### Waking, keeping awake and pausing

**A connection to a published port wakes a paused target.** This is new: until now only SSH
through the bridge woke a machine. Four things have to be true at once for that to be safe, and
each has its own mechanism.

**1. Admission is one atomic decision.** Copying `Manager.Dial`'s shape
(`internal/machines/machines.go:383-435`: lookup, `Idle`, `Renew`, `checkRoom`, `readPin`, dial)
would let two connections to two different paused targets both pass `checkRoom` and both take the
last slot, because nothing holds the decision while the resume runs. So `linkproxy` owns an
admitter:

[`links.Admission`](../../internal/links/links.go) is that decision, and
[`links.Admitter`](../../internal/links/links.go) is the one interface that hands them out. It is
in `internal/links` rather than in either of the packages that use it, because `linkproxy` asks
for admissions and `linkadmit` grants them and neither should import the other.

- **Every connection is admitted, running targets included.** There is no fast path that skips
  `Admit` because the target is already up. A running target that skipped admission would also skip
  the caps, the revocation check and `PausedByHand`: `save` and `fork` resume a by-hand-paused
  machine for a few seconds (`internal/machines/snapshot.go:150`), so "it is running" is not
  evidence that anyone wants it reachable.
- One mutex covers the quiescing check, `PausedByHand`, the source authorization, the wake rate
  limit, the per-source and global link caps and the room arithmetic. Nothing between them can
  interleave.
- **Reserve and count under the mutex, before waking. Release the mutex during the resume.** The
  room, the caps and the rate limit are all charged at the moment the reservation is taken, not when
  `Admit` returns, so a slow resume can't let a second caller spend the same slot. The resume itself
  happens with the mutex dropped, and every counter is rolled back together if it fails.
- **Concurrent wakes of the same target coalesce**: the first caller resumes, the rest wait on
  the same result. The rate limit counts one wake, not ten.
- The wake counter is incremented at admission and **rolled back** when the resume fails, so a
  failed wake doesn't burn the target's next 30 s.
- **Wake by sandbox ID, never by name.** A name can be removed and reused while a link is in flight.
  An ID can't. `machines.Manager` gains
  `Wake(ctx context.Context, id string, leaseUntil time.Time) (engine.Machine, error)`, which
  resumes by ID with an absolute lease, like `Admission.LeaseUntil`, and returns the machine as
  the engine now reports it. `Dial` can't be reused: it opens a WebSocket to sshd on 8081 and pins
  a host key, neither of which a link needs.
- **A link wakes only for a source that is itself doing something.** `Admit` requires the source's
  activity to be inside `LinkGrace`, exactly as renewal does, and refuses with `ErrSourceIdle`
  otherwise. Without this a ring re-wakes itself for ever: the renewal rule stops *extending* a
  timeout, the machines pause and then the next retry from a machine that is itself only awake
  because of a link wakes them all again. Waking and renewing are the same authorization asked at
  two moments.
- **The lease is absolute, re-derived and it never cuts a link.** The lease is
  [`links.LeaseUntil`](../../internal/links/links.go): the source's last non-link activity plus
  `LinkGrace`, as a `time.Time`. `Admission.LeaseUntil` carries only the value it had at admission.
  **Every renewal and every wake decision re-derives it from the clock**. A frozen lease would kill
  a healthy link after `LinkGrace` even while its source was plainly working, which is the opposite
  of the rule in part 4.
  - **The lease alone never closes an established link.** Once it passes, the link simply stops
    renewing and the target pauses on its own idle timeout, exactly as part 4 describes. Nothing
    aborts a socket because a clock moved.
  - A wake that **finishes after the lease current at that moment** forwards nothing. A pending
    wake is cancelled once the lease has passed.
  - The wake takes an **absolute** lease, not a duration. A relative clamp ("give it the lease's
    remaining duration") does not hold, because E2B computes `endTime = startTime.Add(timeout)` with
    `startTime` taken **after** placement (`create_instance.go:440-443` at the pinned ref), so a
    wake that takes 40 s of a 60 s lease would leave the target up for 60 s more.
  - After the resume, the admitter **reads back the real `EndAt`** and shortens it if it is past
    the lease. If the lease has already gone and nothing else is keeping the target up, the
    admitter **pauses it again behind its fence** rather than leaving a machine running that
    nobody asked for.

  A target that is also doing its own work renews normally through the ordinary idle path, so
  none of this binds a machine that has its own reason to be awake.
- **The room reserve is one default `dev` machine**, fixed for v1. No configuration knob. A
  refusal is immediate and says what it was waiting for.
  - **A wake is charged its target's size.** The admitter counts room in default machines.
    `linkadmit.Config.Room(target)` answers how many more may run after the reserve (`free`) and
    how many waking the target takes (`need`): its memory over a `dev` machine's, rounded up and
    at least 1. A wake is refused with `links.ErrNoRoom` when `free` less the room held by wakes
    in flight (`pendingWakes`) is below `need`, and it holds `need` until it ends. Charging every
    wake one default machine let a `large` target, twice the default, spend the reserve.
  - **Rounding up charges enough.** A machine with at most `k` times a `dev` machine's memory
    takes no more of the huge-page pool and no more pause disk than `k` of them
    (`internal/room/room.go`), so `Fits(dev) >= 1 + pendingWakes + k` means the target, every
    wake in flight and the reserve all fit, by memory and by disk.
  - **A size that can't be read can't be charged.** `hibd`'s `linkRoom` reads the target with
    the engine's `Get`, which reports `MemoryMB` for a paused machine as it does for a running
    one, and refuses a target with no size.

**2. Run identity is the run cgroup, and it is an event, not a poll.** The requirement is absolute:
**no link byte is sent or received after either endpoint's run ends**, whatever ends it: E2B's idle
timeout, `hib pause`, `hib rm`, `save`, `fork`, `pause-all`, a crash or the OOM killer.
`engine.Machine.StartedAt` can't carry that. Pausing doesn't change it (the engine sets it only on
resume, `internal/engine/fake/fake.go:280-282` against `:302-304`), it needs an API round trip to
read and a poll is always late.

E2B already gives us a local, per-run object. Each run of a machine has its own cgroup
`sbx-<id>-<run>` under `/sys/fs/cgroup/e2b`. `internal/busy/busy.go:240` and `docs/ideas.md:122`
both record that the suffix is new on every start and resume, and `internal/slots/slots.go:178`
already globs it (`sandboxID` at `:281` throws the suffix away, but links keep it). The repo's own
live test is the proof: *"While `s1-a` was paused its cgroup was gone. Immediately after resume it
mapped again, but to slot 3"* (`docs/research/e2b-capabilities.md:486-491`).

So a run is the directory `sbx-<id>-<run>`, and [`links.Run`](../../internal/links/links.go) is that
suffix. [`links.Runs`](../../internal/links/links.go) is the interface that reports the current run
of a machine and signals the end of a run. `linkadmit.RunWatcher` implements it over the real cgroup
tree, and the tests implement it over a map.

Four mechanisms, in the order they fire. Only the third is *safety*. The others are there because
they are faster, and because a guard nobody helps is a guard that drops traffic it shouldn't.

- **The event.** The proxy watches both endpoints' run cgroups and aborts the link when the
  cgroup's `cgroup.events` reports `populated 0`, or the directory goes away. Firecracker's
  processes leave the cgroup when the VM stops, which is before E2B returns the slot, so this
  fires ahead of any reuse. Presence of the directory is **not** enough on its own: `busy.go:241`
  records that cgroups of ended runs can linger, so liveness is `populated`, and identity is the
  directory name.
- **Hibernaut's own pauses** fence first and wait, below.
- **The kernel's own fence**, next, which holds even when nothing in user space does.
- **The sweep**, every 5 s, as a backstop only: for both ends, the machine still exists, is
  `Running` and its current run equals the admitted one. `Running` has to be checked explicitly
  precisely because a pause leaves `StartedAt` alone.

**There is no deadline from `EndAt`.** An earlier draft aborted each link at the earlier `EndAt` of
its two ends minus a margin, on the grounds that Hibernaut only ever extends `EndAt`. That was wrong
twice over. `machines.Renew` reads the `EndAt` it was handed by an earlier listing and then calls
`SetTimeout` (`internal/machines/machines.go:589-594`), so two processes renewing from two listings
can move it **backwards**. And E2B's own API shortens it: `KeepAliveFor` is called with `overwrite`
true, and a `Timeout` below zero becomes a duration of zero (`sandbox_timeout.go:52` at the pinned
ref). Since the kernel fence below bounds every way a run can end, nothing is lost by deleting the
deadline, so it is deleted and `LinkEndMargin` with it. Any future logic that does need `EndAt` has
to make its own writes monotonic across processes, with a race test. M12 has none.

**Aborting means `SO_LINGER 0`, then close.** Killing a process does not discard a socket's queued
bytes: the kernel keeps draining them to the peer address, which after a recycle is a different
machine. So every link socket sets `SO_LINGER` with a zero timeout **when it is created**, not when
it is aborted: an abort path that has to set an option first is a path that can fail halfway. An
abort then closes both legs, which sends RST and discards the queues. The ordinary end of a link is
different and stays graceful: `shutdown(SHUT_WR)` on the leg that saw EOF, a bounded wait for the
send queue to drain, then close.

#### The kernel's own fence

Everything above runs inside `hibd proxy`. A **stalled** proxy (`SIGSTOP`, a deadlock, a disc that
stopped answering) runs none of it, and its sockets do not care: the kernel goes on sending and
retransmitting whatever is queued on them. Meanwhile E2B hands the slot to the next sandbox
`ReturnDelay` after the old one ends, which is **3 s** (`pool.go:25-32` at the pinned ref), and the
teardown waits for Firecracker to exit before the slot-return callback runs (`sandbox.go:1833-1868`,
`:4105-4112`). `WatchdogSec=30` and a 30 s stop timeout lose that race by an order of magnitude, and
there is no bound on how long killing a process takes anyway.

So the guard that matters is **in the kernel, fail-closed and does not need the proxy to be alive**:

> Once a run ends or is fenced, or the proxy stalls, no link packet reaches or leaves that slot
> after `2*GuardTTL`, which with `GuardMargin` is inside `SlotReturnDelay` before the slot is
> reused, in **all four** directions.

The constants are in [`internal/links/const.go`](../../internal/links/const.go) and the interfaces
are [`links.Guard`](../../internal/links/links.go) and
[`links.Keeper`](../../internal/links/links.go). This section says what the kernel does, not what
the Go reads like.

**A slot address is not an identity.** The first draft keyed the gate on the slot address alone.
That is wrong in three ways, all of which this section fixes. The next occupant of a slot inherits
the gate. A target's replies carry no mark and so skip the drop. The first packet of a connection
has to pass *before* admission can have decided anything about it.

**1. Every run carries a token.** The Keeper (7, below) gives every live, unfenced run a token `t`
of its own from `links.Tokens`, a counter persisted under `links.RunDir` so that it stays monotonic
across restarts of the proxy. Tokens are 24 bits wide, start at 1 and are **never** reused within a
boot. A reboot may restart the counter, because no run survives one. The packet mark is
`links.Mark(t)`: `GuardMark` (`0x4c`) in the top byte and the token in the low 24 bits. A host that
runs out (`links.ErrTokensSpent`) fails closed until it reboots. The gates already open go on being
extended, and a run that needs a new one gets none, so its links are refused. At one new gate a
second that takes more than six months.

**2. The ruleset.** The set is keyed on the pair, and nothing in it reads conntrack:

```
table inet hibernaut-links {
  set link_live {
    type ipv4_addr . mark
    flags timeout
    timeout 1s
  }

  map link_src {
    type ipv4_addr : mark
    flags timeout
    timeout 1s
  }

  # Before NAT (-100), so the original 198.18/15 destination is still on the packet. A miss in
  # link_src ends the first rule with the mark still 0, and the second rule drops the packet.
  chain prerouting {
    type filter hook prerouting priority -150; policy accept;
    ip daddr 198.18.0.0/15 meta mark set ip saddr map @link_src
    ip daddr 198.18.0.0/15 ip saddr . meta mark != @link_live drop
  }

  # A link socket's packets carry the socket's mark.
  chain output {
    type filter hook output priority -150; policy accept;
    meta mark and 0xff000000 == 0x4c000000 ip daddr 10.11.0.0/16 \
      ip daddr . meta mark != @link_live drop
  }

  # Checked against the receiving socket's mark, not the packet's.
  chain input {
    type filter hook input priority -150; policy accept;
    ip saddr 10.11.0.0/16 meta l4proto tcp socket mark and 0xff000000 == 0x4c000000 \
      ip saddr . socket mark != @link_live drop
  }
}
```

An element admits only traffic carrying **that run's** token, so a new occupant of `S` never
inherits an old run's gate. `nft delete table inet hibernaut-links` is still a complete uninstall.
`Install` also sets `net.ipv4.tcp_fwmark_accept` to 1 in the host's network namespace (4, below)
and `Installed` checks that it still is. `Remove` leaves it at 1, because it changes nothing for a
packet without a mark. `hibd-proxy.service` must never gain `ProtectKernelTunables=yes`, which would
stop the proxy from writing it. If the host's `nft` refuses `socket mark` inside a concatenation,
`input` copies it first (`meta mark set socket mark`, under the same match) and drops on `ip saddr .
meta mark` instead. `TestGuardInputUsesSocketMark` covers whichever form ships.

**3. The first packet has somewhere to be.** `link_src` maps a slot address to the mark of the run
now at it. In `prerouting`, before NAT, every packet to `198.18.0.0/15` gets `meta mark set ip saddr
map @link_src`. A miss ends that rule with the mark still 0, and the next rule drops the packet
because no element carries mark 0. So a source with a live, unfenced run reaches the listener, and
one without never does. A machine cannot choose its own mark: crossing the veth into the host's
namespace clears `skb->mark`. **The proxy forwards nothing before admission.**

**4. Every link socket carries its run's mark.** The listener sets no `SO_MARK` of its own. With
`net.ipv4.tcp_fwmark_accept` at 1 the kernel keeps the SYN's mark with the pending connection
(`inet_request_mark` stores it in `ir_mark`), sends the SYN-ACK with it and gives it to the accepted
socket (`inet_csk_clone_lock` copies `ir_mark` to `sk_mark`). A syncookie connection takes it from
the final ACK of the handshake instead, which prerouting marked with the token current then. The
dialled leg gets `SO_MARK` = `links.Mark(t)` of the target's run, set before connect. The setting is
per network namespace and only ever copies a SYN's mark, so every socket the host accepts that did
not come through the link path keeps mark 0, the egress proxy's included.

**5. The drop runs in all four directions, against the socket's mark.** Each packet is checked
against the slot address on its link side and a mark the machine cannot set:

| Direction | Chain | Pair checked |
| --- | --- | --- |
| source to listener | `prerouting`, then `input` | `(S . link_src[S])`, then `(S . t)` from the accepted socket |
| listener to source | `output` | `(S . t)` from the accepted socket |
| listener to target | `output` | `(T . t_T)` from the dialled socket |
| target to listener | `input` | `(T . t_T)` from the dialled socket |

`input` reads the receiving socket's mark (`socket mark`) rather than the packet's, because
prerouting stamps every packet from `S` with the token of the run at `S` **now**. A socket accepted
from an earlier run keeps that run's token, so once its gate is gone every packet it sends or
receives is dropped, whichever run is at `S`. What the chains let through unchecked moves no link
byte: packets for a pending or closing connection or for no socket at all (the `socket` expression
finds no full socket, so the rule does not match), resets the kernel sends without a mark and the
traffic of every socket that is not a link's. A link socket accepted without a mark, which only
happens if the setting was off, is not checked in either direction. Admission refuses it
(`links.ErrUnmarked`) before it carries a byte.

**6. An accepted connection is bound to the run that opened it.** The token in the accepted
socket's mark names the run that was at `S` when the connection's SYN arrived. The listener reads it
with `SO_MARK` and admits only if it is the token of the Keeper's gate for the source's current run,
at the connection's source address. A mismatch is `links.ErrWrongRun`, a refusal and not a retry. So
a stalled listener that accepts, after `S` was recycled, a connection that belongs to the run that
has gone refuses it, and so does any backlog left from an earlier run. Nothing here reads
conntrack, so a lost or rebuilt conntrack entry changes nothing.

**7. The Keeper owns every gate.** One goroutine in `hibd proxy`, `linkguard.Keeper`, opens,
extends and retires gates, and nothing else in the proxy writes one. The listener and the admitter
read them through `links.Keeper`. Every `GuardKeep` the Keeper reads `Runs.Live()`, reads
`links.json` once and checks the owner of each fence in it, and then:

- extends the gate of every run that is still live and unfenced, all of them in one atomic batch.
  It extends first, so a slow open never delays a refresh.
- retires every other gate. A retired gate is not extended again and `Keeper.Retired` closes for
  it, so the listener aborts the links holding its token. That is cleanup, not safety: the kernel
  drops their packets once the gate expires either way. A retired gate is left to expire, and
  nothing drops it except an open at the same address.
- opens a gate for each live, unfenced run that has none. It resolves the run's address with
  `Resolver.Addr` and checks it back with `Resolver.At`, drops whatever is left at that address
  (`Guard.Drop`), takes a new token and opens the gate. The drop is needed because the map entry is
  create-only and a retired gate may not have expired yet. It then reads the run and its fence
  again, and if either has changed it drops the address and retires the new gate. A run never
  changes slot, so its address is resolved once, when its gate opens.

Every live run gets a gate, not only those with links, because a source's first SYN gets through
only on its own gate. A fence whose owner cannot be checked counts as up, and so does every fence
in a `links.json` that cannot be read, which shuts every gate. `Store.Read` never waits for the
lock, so a long publish cannot starve the Keeper. `Keeper.Gate` nudges a cycle and waits for it, so
a run a wake has just resumed has its gate before the admitter returns.

**8. Opening is create-only, refreshing is extend-only.** `Guard.Open` creates the set element and
the map entry with `NLM_F_EXCL`, so it fails rather than touch anything already there. It records
its token as spent before it sends and refuses a token at or below the highest it has sent
(`links.ErrGateSpent`), so no retry and no bug can open a gate twice. `Extend` deletes each element
and adds it again with its timeout, the set element first and its `link_src` entry second, in one
atomic batch, and never creates one. Re-adding an element that is already there does **not** reset
its timeout (the nftables wiki is explicit about this under *Element timeouts*, and `nft reset
element` needs nftables 1.0.6 with kernel 6.8), so delete-then-add is the only form that extends.

Deleting an element that has expired or has already gone **fails with `ENOENT`, which aborts the
whole batch**. That is the point, not a nuisance: a refresh that was delayed past the expiry can
never resurrect a gate. On kernel master, `nft_rhash_cmp` treats an expired element as absent,
`nft_rhash_deactivate` looks it up with that comparator and `__nft_setelem_deactivate` returns
`-ENOENT`. A missing table gives `ENOENT` too, so the Keeper checks `Installed` first. If the table
is intact it finds the gates that vanished with `Has`, retires them and sends the rest again in the
same cycle. A vanished gate is never re-added, and its run gets a new token only through the open
path above.

So one invariant holds: **no socket carries a token before its gate exists, and a gate that has
gone never comes back.** `link_src` is written only by `Open` and `Extend`, so prerouting hands out
only the tokens of open gates, and the listener dials only with a token the Keeper gave it.

**9. The bound.** An element outlives its run by less than `2*GuardTTL`: the run check happens
before the run ends, and the refresh can land just before the expiry. A fence is the same, since a
cycle may read `links.json` just before the fence is written and extend once more. The requirement
is `2*GuardTTL + GuardMargin <= SlotReturnDelay`, which with `GuardTTL` = 1 s and `GuardKeep` =
250 ms leaves a second of slack. A contract test asserts the arithmetic so a later tuning cannot
break it quietly. A Keeper stall longer than `GuardTTL - GuardKeep` therefore shuts its own gates,
which is the fail-closed behaviour we want, and `hibd-proxy.service` gets a `CPUWeight` above the
default so that a busy host does not cause one.

**10. The `hibd` fallback deletes and reads back.** If the proxy doesn't acknowledge a `close`
within `CtlTimeout`, the pausing `hibd` calls `Guard.Drop(S)`. It deletes **every `(S . *)` element
and `S`'s `link_src` entry**, and reads them back until none are left, because one can expire
between the read and the delete. Nothing can recreate a deleted gate, and the Keeper opens a new one
only for an unfenced run, so the empty read-back means something. A cycle that read `links.json`
just before the fence can open one new gate after the drop. Its own re-check shuts it again, no
socket held from before carries its token and admission refuses anything new from a fenced machine.
`Install` is a single atomic replace of the whole table, so the proxy starts with both sets empty
and a crash cannot leave a gate open across a restart.

**A missing table is an error, with one exception.** A pause cannot tell a table that was never
installed from one flushed under a wedged proxy that still holds sockets, so `Drop` fails on it.
The exception is `links.TokenFile`, which is in `/run` and written before any token is used: when
it does not exist, no gate has been opened since boot, so no link socket can exist and `Drop`
returns nil. Without it a host whose proxy never got as far as installing the guard could pause
nothing, quiesce would never deallocate it and the `ExecStop` `pause-all` would leave E2B to stop
every running machine without a snapshot.

Deleting needs `CAP_NET_ADMIN`, so every `hibd` that pauses or removes a machine runs as root: the
daemon and `pause-all` under systemd, and `hib pause` and `hib rm` through `sudo -n`, like `fork`.
A `hibd` without it, from an older `hib` or run by hand, still pauses when the proxy acknowledges.
When the proxy does not, it fails closed: it leaves the machine as it was, clears the fence, exits 1
and says that root is needed.

**11. Losing the protection at runtime.** If `Extend` fails with anything but an element's
`ENOENT`, or `Installed()` reports the table, a set, a chain or the setting missing or different,
the Keeper retires every gate, so the listener aborts every link, and reinstalls atomically before
it opens any gate again. Meanwhile `Keeper.Gate` returns `links.ErrGuardDown`, and a reinstall that
fails is retried every `GuardSelfCheck`. A change that makes refreshes fail is noticed at the next
refresh, within `GuardKeep`. E2B's `flush ruleset` is one, because it deletes the table. A change
that leaves refreshes working, such as a hand-edited chain, is noticed by the self-check within
`GuardSelfCheck`.

**12. The kernel facts are tested on the host, not argued here.** Each of these is a named
slice-1 host test, and **links stay off by default until they all pass**. Every one logs the
host's kernel version, because that is the variable.

| Host test | What it proves |
| --- | --- |
| `TestGuardExpiredDeleteAbortsBatch` | deleting an expired element returns `ENOENT` and aborts the batch |
| `TestGuardDeleteAddResetsExpiry` | delete-then-add in one batch resets the timeout |
| `TestGuardOpenIsCreateOnly` | `Open` fails on an element or map entry that is already there, and changes neither |
| `TestGuardDroppedGateStaysGone` | after `Open` and `Drop`, `Open` of the same token is `ErrGateSpent`, `Extend` fails and `Has` is false |
| `TestGuardReopenAfterExpiry` | a run whose gate expired gets a new token through `Drop` and `Open`, and the old token stays shut |
| `TestGuardMapMissLeavesMarkZero` | a miss in `meta mark set ip saddr map @link_src` leaves the mark 0 and the SYN is dropped |
| `TestGuardAcceptTakesSynMark` | with `tcp_fwmark_accept` at 1 the SYN-ACK and the accepted socket carry the SYN's mark, with `tcp_syncookies=2` as well |
| `TestGuardInputUsesSocketMark` | `input` checks the accepted and the dialled socket's mark after `REDIRECT`, and drops an old run's socket while the new run's gate is open |
| `TestGuardEgressKeepsMarkZero` | with the setting on, the egress proxy's accepted sockets still have mark 0 |
| `TestGuardConntrackLossFailsClosed` | `SIGSTOP` the proxy, end the run and `conntrack -D` the link's entries: nothing passes in any direction |
| `TestGuardSetBackendIsRhash` | the set backend is `rhash`, with no interval flag |
| `TestGuardDropsAllFourWaysUnderStop` | `SIGSTOP` the proxy, end the run and all four directions drop within `2*GuardTTL` |

**What was rejected.** The conntrack mark, which round 5 used: an entry lost to `conntrack -D`,
table pressure or a module reload is rebuilt mid-stream with `ct mark 0` (`nf_conntrack_tcp_loose`
is on by default), which skipped every drop, and a lookup by tuple could read a newer entry stamped
with another run's token. A socket's mark cannot be rebuilt, and nothing outside the process that
owns the socket can change it. `TCP_USER_TIMEOUT` below 3 s: `tcp(7)` says it also aborts
connections stalled on a zero window, so a benchmark whose reader is slower than its writer (one of
the uses this milestone exists for) would have its healthy links killed. A shorter `WatchdogSec`
alone: there is no bound on how long it takes to kill a process, so it cannot meet a 3 s requirement
at all. The watchdog stays, demoted to recovery: `hibd-proxy.service` keeps `WatchdogSec=30` and
`Restart=always` so a wedged proxy is eventually replaced and links start working again, but by the
time it fires the gate has been shut for 29 s.

**Why not the slot address alone, or a pathname check.** Both were in the previous draft. A pathname
check says nothing about what the kernel will carry. The slot address alone cannot tell two runs
apart, and the whole race is that a slot outlives the run that was in it.

**Startup and shutdown.** `hibd proxy` builds the links side in one order, in
`internal/hibd/proxy.go` and `internal/hibd/linkside.go`, and each piece is given what it needs
rather than reaching for it:

1. the guard and the token counter, `linkguard.New()` and `linkguard.NewTokens()`.
2. the run watcher, `linkadmit.NewRuns("")`.
3. the resolver, `slots.Resolver{Finder, Runs}`, over the same `slots.Finder` the egress proxy
   uses.
4. the store and the activity clock, `links.NewStore` and `links.NewClock` over `HIBD_STATE_DIR`.
5. the Keeper, `linkguard.NewKeeper`, with the guard, the tokens, the runs, the resolver, the store
   and `statefile.Held` as the fence check.
6. the admitter, `linkadmit.New`, with the engine, the run watcher, the Keeper, the store, the
   clock, the resolver, the guard as its `Dropper` and `linkRoom` as its `Room`. `New` refuses a
   configuration missing any of the first five.
7. the listener, with the Keeper, then `admitter.SetCloser(listener)`.
8. `admitter.Serve`, which serves the control socket, and `Keeper.Run`, each in its own goroutine.
9. `listener.Serve`, once `Ready` is closed and the startup checks pass: the control socket
   answers a `stat`, `sandbox-firewall.nft` has the drops and the orchestrator has the
   `IPAddressDeny` (`internal/hibd/linkchecks.go`). The link port is bound only then. Until then
   links stay off and `hibd proxy` goes on serving egress without them. It re-runs the checks once
   a minute and logs the ones that failed each time, so a host whose E2B pieces arrive after the
   proxy gets links without a restart. A Keeper whose first `Install` failed has returned and its
   `Ready` never closes, so that one keeps links off until the proxy restarts.

**The control socket is served whenever links are configured on, even while the listener is
off.** A listener that is not serving holds no sockets, so it answers `close` and `revoke` at once
with nothing closed and `stat` with zero counts, and `hibd proxy` has logged why links are off.
Without it, a proxy that had handed out tokens earlier in the boot and then failed to install the
guard again would leave every pause to fall back to `Drop` on a missing table, which fails (*The
kernel's own fence*, 10).

**The first `make up` of M12 runs the E2B install again.** Slice 7 changed
`vm/systemd/e2b-orchestrator.service` and `vm/e2b/sandbox-firewall.nft`, which `vm/install-e2b.sh`
hashes into its `install_id` (`vm/install-e2b.sh:15-16`). So that `make up` pauses every machine
through `quiesce`, rebuilds E2B at the same ref, installs the units and the firewall file and
restarts E2B. Until the orchestrator has restarted with them the checks fail and links stay off,
and `hibd proxy` logs `links are off, checking again in 1m0s` with the checks that failed, once a
minute. Egress goes on meanwhile, and links come on at the first check after E2B is back.

Shutdown runs the other way. `Serve` closes every link socket and flushes the link log, the Keeper
retires its gates and they expire within `GuardTTL`, and the control socket is unlinked.
`hibd-pause-all.service` is ordered after `hibd-proxy.service`, so at shutdown `pause-all` runs
while the proxy still answers rather than racing its stop.

**3. Closing links is cross-process, and there is one mechanism.** `hibd pause` and `hibd rm` run as
separate `hibd` invocations (`internal/hibd/hibd.go:209-255`), E2B pauses machines on their idle
timeout without telling anyone and `save` and `fork` **resume a machine internally** before
snapshotting it (`internal/machines/snapshot.go:133-163` always calls `m.e.Resume`, even for a
machine that was paused, and pauses it again afterwards). A check made after dialling does nothing
for any of these. So:

- the control socket from the API section is the single mechanism. `close` for a whole machine,
  `revoke` for one `(id, port)`. Both wait until the proxy has cancelled pending admissions and
  closed established links before they answer.
- Hibernaut's own pauses (`hibd pause`, `hibd rm`, `pause-all` and quiesce, the leak scan and `save`
  and `fork`'s internal pause), with links on, **raise the fence first, then send `close`, then
  wait for the ack and only then pause.**
- **The fence is a field in `links.json`, it names an owner and it is checked for both endpoints.**
  It is [`links.Fence`](../../internal/links/state.go), written before the close is sent by
  **every** pause Hibernaut starts, and admission refuses a link whose **source or target** is
  fenced. It has to be persisted rather than held in memory, because the process doing the pausing
  is not the process serving links, and a proxy restarted in the middle of a pause would otherwise
  come back with an open gate.
  - Every path raises it: `hibd pause`, `hibd rm`, `pauseAll` (`internal/hibd/hibd.go:849-891`,
    which is also where the quiesce marker is now created), the leak scan's direct pause
    (`internal/hibd/leakscan.go:38-47`) and `snapshot.go`'s internal pause for `save` and `fork`. A
    pause that does not raise it is a bug, and the slice that owns each path owns that too.
  - It is **cleared once the pause settles, whether it succeeded or failed.** A pause that fails
    and leaves the fence up would make the machine unreachable for no reason.
  - **Age is not the staleness rule.** An earlier draft ignored a fence older than two minutes. That
    is wrong: `save` and `fork` legitimately run for ten minutes
    (`internal/machines/snapshot.go:17-18`), so age cannot tell a long save from a crashed `hibd`.
    Instead the fence records an **owner that dies with the operation**: an `flock` on
    `links.FenceName`, held for the whole of the pause. A checker takes `LOCK_SH|LOCK_NB` on it:
    failing means the owner is alive. This works inside `hibd-proxy.service`'s sandbox, which
    `/proc`-based schemes would have to prove first.
  - **A fence is live while its owner lives, whatever its age.** Age only produces a log line and
    a reconcile. [`links.Machine.Fenced`](../../internal/links/state.go) returns `FencedLive` for
    a live owner, `FencedOrphaned` when the owner is gone or cannot be checked, and admission
    refuses in **both** cases: an orphaned fence is cleared only after the machine's real state
    has been reconciled, never by simply being ignored.
  - **`hibd cleanup` does the reconciling.** It looks only at fences that have been up for
    `links.FenceStale` (2 min) or longer. A live owner gets a log line and keeps its fence, and an
    owner that can't be checked keeps it too, with an error. For an orphaned fence cleanup asks E2B
    about the machine. One E2B has forgotten is left to the prune, which drops its whole entry, and
    one E2B can't be asked about keeps its fence, with an error. Otherwise cleanup takes the fence
    over through `Store.Fence`, waiting at most 5 s, releases it, which clears it the way a pause
    clears its own, and reads `links.json` back to confirm. A take-over that times out means an
    operation is fencing the machine right now, so the fence is left to it. `cleanup -n` only says
    what it would clear.
  - **A machine still pausing reads as running.** E2B reports only `running` and `paused`
    (`internal/engine/e2b/e2bapi/e2bapi.gen.go:165-166`), so cleanup can't tell a machine whose
    pause is still in flight from one that never paused, and it clears the fence either way. That is
    safe because a fence only holds off new links: when the run ends, the run watcher ends any link
    admitted meanwhile and the Keeper retires the run's gate.
  - `PausedByHand` is a different thing and stays: the fence is "a pause is happening now", and
    `PausedByHand` is "a person wants this machine to stay down". `save` and `fork` keep
    `PausedByHand` across their internal resume. The fence goes up and comes down within it.
- **A pause never proceeds past a proxy that may still hold sockets.** If the ack doesn't come
  within `CtlTimeout`, the pause does not warn and continue, and it no longer restarts the proxy
  either: it **deletes every `(S . *)` element and `S`'s `link_src` entry itself**, in one batch,
  reads them back to confirm they are gone and only then pauses. The sockets may still exist in a
  wedged proxy, but the kernel will not carry another packet of theirs. If deleting the elements
  fails, the pause fails, says so and lifts the fence.
- `hib port rm` keeps its own stricter rule: ack or error, never "warn and continue". Unpublishing
  that silently left links open would be a security failure, not an inconvenience.
- **Close deadlines, and what "closed" means.** On `close` the proxy aborts every link in its scope.
  Every link socket was made with `SO_LINGER 0`, so closing it discards what is queued and sends a
  reset. The handshake is frozen in [`links.Closer`](../../internal/links/links.go): the proxy
  answers a `close` or `revoke` **only after every socket in its scope has been closed**, and the
  reply carries how many links that was. "Acknowledged" means "there is no socket left", not "I have
  begun". Anything weaker and the caller above would be pausing past live sockets while believing it
  wasn't. The admitter waits `LinkCloseGrace` (5 s) for the listener's answer and then drops the
  machine's gates itself, and answers with the links it had admitted. Half-close and the bounded
  drain (`links.DrainTimeout`, 2 s) belong only to a link that ends on its own.

**4. Link traffic alone cannot keep anything awake.** This is the one that costs money:
`vm/idle-deallocate/idle-deallocate:34-41` treats **any** running machine as activity and touches
the idle file, so two machines holding each other up stop the host deallocating. Checking the
source's `EndAt` doesn't help, because links renew `EndAt`. Three parts:

- **Self-links are refused.** The shortest cycle is a machine linking to itself, and it has
  localhost for that anyway.
- **Non-link activity is tracked separately from `EndAt`.** `hibd` touches
  `<state>/links/activity/<sandbox-id>` (an empty file whose mtime is the value, moved forward only
  and never behind `links.lock`) when, and only when, something that isn't a link happens to that
  machine. The proxy **never** touches it. `hibd cleanup` removes files for machines that no longer
  exist. Exactly four things feed it:
  - a **successful** `hib new` or `hib fork`: the point where the machine exists and is running, not
    the `machines.New` constructor, which only builds a `Manager`.
  - an **open SSH or `hib exec` session**, on a heartbeat of 30 s or less for as long as it is open.
    This can't ride `Manager.keepalive`: with `idle=0` that function blocks on `<-ctx.Done()` and
    never ticks at all (`internal/machines/machines.go:555-560`), so the heartbeat is its own
    ticker, independent of `idle/4`.
  - every **`internal/busy` observation**, including machines with `idle=0`. M11's `busy` skipped
    those in the same condition that skipped non-running machines. M12 changes it to *observe* them
    and still not *renew* them: an `idle=0` machine never pauses, so renewing is meaningless, but
    whether it is working is exactly what a link needs to know.
  - a resume a person asked for.
- **A link renews its target only while its source has non-link activity.** While bytes move the
  proxy renews the target's timeout, but only if the *source's* activity file is newer than
  `LinkGrace` (5 min). Past that the link keeps working and stops renewing, and the target pauses
  on its own idle timeout. The same freshness gates `Admit`, so the ring can't restart itself.

**What the activity clock is not.** It is not independent of the guest and it is not unforgeable.
A machine can busy-loop to look busy to `internal/busy`, exactly as it can today. M12 does not
try to fix that, and the doc should not pretend otherwise. What M12 owes is a narrower
invariant, and it holds:

> **Links add no host runtime beyond what `busy` and SSH already cause, and never weaken
> quiesce.**

A guest that fakes activity could already keep *itself* running and the host allocated, with no
links at all. With links it can additionally keep its *targets* awake (which costs RAM, not a new
category of runtime) and that is bounded by `MaxPerSource`, the per-target cap, the room reserve and
`PausedByHand`. The 20:00 UTC quiesce still ends every run, because quiescing is step 1 of admission
and stops renewals. An owner-set runtime budget would bound the forged case properly. That is a
separate question for Martin, not an M12 blocker.

What that buys: a benchmark machine with an SSH session open, or one burning CPU, keeps its
downstream target awake for as long as it is working, because its own activity is fresh. A cycle of
any length with nothing but link traffic in it stops being renewed within `LinkGrace` of its last
real activity, every machine in it pauses on its idle timeout and, because the same freshness gates
waking, a retrying client in that cycle cannot wake them again. The bound is `LinkGrace` plus the
longest idle timeout in the ring, after which the ring is wholly paused and stays paused. A machine
that is awake only because of links is therefore bounded, and the host deallocates.

**Renewal is counted from the last payload byte, not from the last close.** The proxy asks for a
renewal at most once per `LinkRenew` (30 s) for each source and target pair, and only when payload
has moved since that pair's last one. The admitter renews a target at most once per `LinkRenew`,
whichever source asks, and refuses a stale source without spending that window, so a stale source
moving bytes can't starve a fresh one's renewal of the same target. A link that is open but silent
renews nothing, so a parked connection holds nothing. Closing the last link changes nothing by
itself: the clock has been running since the last byte.

**A machine paused by hand stays paused.** `hib pause` sets `PausedByHand` for that sandbox ID in
`links.json`, and `Admit` refuses while it is set. It is cleared only when a **person** starts the
machine (`hib ssh`, `hib exec`, anything that goes through the bridge), never by observing that the
machine is running, because `save` and `fork` resume a paused machine for a few seconds and pause it
again. During that window the machine is running and still marked, and a link is still refused.
Without this, `hib pause api` could be undone a second later by a worker's retry loop.

**No links while quiescing, and the fence is at the top.** The check is step 1 of the connection
path, before any state is read, and **renewals stop too**: a link already open stops renewing its
target the moment the marker appears, or `pause-all` would never converge. Two corrections to how
quiescing works today, both of which M12 has to fix for this to hold:

- `internal/pauseall/pauseall.go:52-66` pauses **every running machine**, not only idle ones. The
  earlier draft of this document said otherwise and was wrong.
- the marker is created by `vm/lib/quiesce` and by nothing else. `pauseAll` never creates it, and
  `vm/systemd/hibd-pause-all.service:14` calls `hibd pause-all` straight from `ExecStop`, so a
  shutdown-triggered pause-all runs with **no marker at all** and the listener would happily wake
  machines underneath it. With links on, M12 moves marker creation into `pauseAll` itself, so every
  way in is fenced, and leaves `vm/lib/quiesce`'s `touch` in place as a harmless no-op.

### Limits and constants

| What | Value | Where |
| --- | --- | --- |
| Virtual address range | `198.18.0.0/15` | `links.CIDR` |
| First address | `198.18.0.1` | `links.First` |
| Link listener port | 5444 by default, `HIBD_LINK_PORT`. **0 turns links off** | `links.Port`, `hibd.go` |
| Never publishable | 8081 (Hibernaut's SSH bridge), 49983 (E2B's agent) | `links.Reserved` |
| Ports per machine | 16 | `links.MaxPorts` |
| Machines holding a virtual address | 256 | `links.MaxMachines` |
| Links per source machine | 64 | `links.MaxPerSource` |
| Links per target machine | 64 | `links.MaxPerTarget` |
| Links on the host | 512 | `links.MaxLinks` |
| Accepts per source | 20/s, burst 40 | `links.AcceptRate`, `links.AcceptBurst` |
| Wakes per target | 1 per 30 s | `links.WakeEvery` |
| Admission timeout | 2 s | `links.AdmitTimeout` |
| Dial timeout | 10 s | `links.DialTimeout` |
| Wake timeout | 60 s | `links.WakeTimeout` |
| Renewal, while payload moves | at most 1 per 30 s per target, asked at most once per 30 s per source and target pair | `links.LinkRenew` |
| Source activity grace | 5 min | `links.LinkGrace` |
| Admitter's wait for a close before it drops the gates | 5 s | `links.LinkCloseGrace` |
| Drain when a link ends on its own | 2 s | `links.DrainTimeout` |
| Guard element timeout | 1 s | `links.GuardTTL` |
| Keeper cycle (gate extend) | 250 ms | `links.GuardKeep` |
| Guard chain priority | -150 | `links.GuardPriority` |
| Guard packet mark | `0x4c` (top byte) | `links.GuardMark` |
| Guard table, set and map | `hibernaut-links`, `link_live`, `link_src` | `links.GuardTable`, `links.GuardSet`, `links.GuardMap` |
| Guard token width | 24 bits | `links.GuardTokenBits` |
| Guard self-check | 5 s | `links.GuardSelfCheck` |
| Guard margin under the slot return | 1 s | `links.GuardMargin` |
| E2B's slot return delay, which the guard has to beat | 3 s | `links.SlotReturnDelay`, E2B's `pool.go:25` |
| Run sweep, the backstop | 5 s | `links.SweepEvery` |
| Proxy watchdog, recovery only | 30 s | `WatchdogSec` in `hibd-proxy.service` |
| Control socket reply | 7 s | `links.CtlTimeout` |
| Idle link timeout | none, TCP keepalive 30 s | `links.Keepalive` |
| Link log | `/var/log/hibernaut/links.jsonl`, 10 MiB, one rotation | `HIBD_LINK_LOG` |
| Refusal lines per source in a `links.FoldWindow` (10 s) | 64, then one line per reason for the rest | `links.MaxFoldsPerSource` |

**The caps are M12's own.** The earlier draft said they were taken from the egress proxy. They are
not, because the egress proxy has none: `internal/egress/egress.go:105-117` is a bare `Accept` loop
that starts a goroutine per connection with no limit anywhere. `linkproxy` counts every live link in
the admitter, under the same mutex as the room check, and refuses past the cap. Per-source and
per-target counts are decremented by `Release`, in one `defer`, so a panic in the copy loop can't
leak a slot. The room a wake holds goes back when the wake ends, not at `Release`.

**How the caps are counted.** A link occupies a slot from the moment the admitter **reserves** it,
which is under the mutex and before the wake begins, until `Release`. The wake happens with the
mutex dropped, so a slot that was counted only once `Admit` returned would look free to a second
caller while it was already spoken for. `MaxPerSource` and `MaxPerTarget` are counted by sandbox ID,
so renaming a machine doesn't reset them. `AcceptRate` is counted before `Admit`, on the source ID
from `slots.Finder.Sandbox`, and a connection over the rate is closed without reading. `MaxLinks` is
the backstop for the whole host. Past it the listener stops accepting for 100 ms rather than
accepting and closing, so the kernel's backlog does the queueing.

**`Release` keeps the rate history of a successful wake.** The 30 s wake rate limit is per target
and survives `Release`, so a source that opens and closes a link in a loop cannot wake a machine
every second by giving its reservation back each time. Only the reservation and the caps are
returned. The rate accounting is not.

**The reserve is one `dev` machine.** The admitter's room is how many machines of the default image
(`images.Default`) fit, by memory and by disk, less one (`linkRoom`,
`internal/hibd/linkside.go:152-177`). A wake is charged its target's size in those machines, its
memory over a `dev` machine's rounded up. It is refused unless that fits beside the room the wakes
in flight hold, and it holds that much until it ends. A `large` target, twice the default, needs
room for two besides the reserve, so no link-woken target can spend it. `linkRoom` refuses a target
whose size it can't read.

**Why `LinkGrace` is 5 minutes.** It has to be long enough that a person who stops typing for a
moment doesn't lose their links, and short enough that an abandoned ring costs little. Five minutes
bounds an abandoned ring at five minutes plus one idle timeout: about 20 minutes at the default
`idle=15m` (`machines.DefaultIdle`, `internal/machines/machines.go:37`), and the 20:00 quiesce is
the hard backstop behind that. It is a guess, and the ring e2e test is what confirms it. If the
number moves, it moves here first.

Every name in the table's third column is in
[`internal/links/const.go`](../../internal/links/const.go), with the reason for its value next to
it, except the three that belong to systemd and to `hibd`'s own flags. `HIBD_LINK_PORT` and
`HIBD_LINK_LOG` join the list in `hibd`'s usage text.

### Audit

Link connections go to their **own** log, `/var/log/hibernaut/links.jsonl`, not
`audit.jsonl`. `audit.Entry`'s `Site`, `Method`, `Path` and `Secrets` have no `omitempty` and
`hib log --json` is a stable shape, so a link record there would print empty columns in every
`hib log`.

The record is [`audit.LinkEntry`](../../internal/audit/links.go), and the log is
[`audit.LinkLog`](../../internal/audit/links.go): `OpenLinks(path, maxBytes)`, `Write(LinkEntry)`,
`Close()` and `ReadLinks(path, n)`. A nil `*LinkLog` drops entries rather than failing, so a
listener whose log could not be opened still carries traffic: losing a log line is not worth losing
a link. The file is written here rather than restated: an earlier draft of this document had the
struct inline and it had already drifted from the code.

Some fields are worth saying twice, because they are decisions and not shapes:

- `Reason` is always the `Error()` of one of the errors in
  [`internal/links/errors.go`](../../internal/links/errors.go), never a string written at the
  call site. That is why those strings read as log values rather than as sentences.
- `Event` is `"open"`, `"close"` or `"refused"`, and the first two are both written for every
  link.
- `From` and `To` hold sandbox IDs when the listener writes them, because it knows machines only by
  ID. `hibd log --links` puts the names in as it reads the log, and keeps the ID for a machine that
  has none.

**The error behind a refusal goes to the proxy's journal.** A link log line can only carry one of
`internal/links`'s errors, so the listener hands the real one, a failed dial or a run it couldn't
read, to `linkproxy.Config.Logf`. It writes at most one line per step, source and error in a
`links.FoldWindow`, and at most 64 different lines in a window, so a retry loop or a scan of the
port can't flood the journal. `hibd proxy` wires it the way it wires the egress proxy's `Logf`.

Why it is shaped that way:

- **Two lines per link, not one: `open` and `close`.** A line written only at close can't answer
  "why is this machine awake and costing me money *right now*", which is exactly the question a link
  that has been open for an hour raises. `open` carries `Woke`. `close` carries the bytes and the
  duration. `hib port ls`'s `OPEN` column answers the same question without reading a log.
- **Refusals are folded.** Identical refusals (same source, target, port and reason) within 10 s
  become one line with `Count`, keyed on `(fromId, toId, port, reason)` and flushed when the window
  ends or the process shuts down. A retry loop writes a line every 10 s, not thousands. `open` and
  `close` are never folded. **A source has at most `links.MaxFoldsPerSource` (64) refusals folding
  at once.** Its other refusals fold into one overflow line per reason, with no target and port 0,
  whose `Count` covers them all. The per-source accept rate limit's own refusals and the ones made
  before it are written at whatever rate the guest connects, most with the port it dialled, so
  without the cap a machine trying every port would write a line and hold a timer per port, enough
  to rotate the owner's history out of the 10 MiB log and its one old file in seconds. Sources are
  counted by sandbox ID and refusals with no source share one count, so one machine's scan leaves
  another's lines alone. Writing a fold's line gives its source the place back, so the next window
  starts with all 64. It mirrors the journal's limit of 64 different lines in a window, in the
  paragraph above.
- The writer flushes each line before returning, as the secret log does, and the proxy flushes
  every pending fold on shutdown so nothing is lost on a restart.
- `hib log --links` reads it. `--json` puts the entries in `Response.Links`. `hib log` without the
  flag stays exactly as it is.
- `internal/audit` grows `OpenLinks(path string, maxBytes int64) (*LinkLog, error)`,
  `(*LinkLog) Write(LinkEntry) error`, `(*LinkLog) Close() error` and
  `ReadLinks(path string, n int) (LinkReadResult, error)` (`internal/audit/links.go:84,118,153,173`)
  alongside the secret log, and shares one JSON-lines writer and reader with it,
  `internal/audit/jsonl.go`, so the two have one rotation and one line-length cap.

### Messages and errors

| Where | Text |
| --- | --- |
| `hib port add api 8080` | `api:8080 is published to your machines as api.hib:8080` |
| `hib port add` on a localhost-only port | `… as api.hib:8080. api listens on 127.0.0.1:8080, so its clients will look local to it` |
| `hib port add api 8081` | `port 8081 is Hibernaut's SSH bridge and can't be published` |
| `hib port add api 49983` | `port 49983 is E2B's agent and can't be published` |
| `hib port rm api 8080` | `api:8080 is no longer reachable from your machines` |
| `hib port rm`, close unconfirmed | `api:8080 is unpublished, but hibd proxy didn't confirm it closed open links: check hib port ls` |
| `hib port rm` an unpublished port | `api:8080 wasn't published` |
| `hib port add` past the cap | `api already publishes 16 ports, the most there is room for` |
| `hib port add` past the cap in one batch | `that would make 18 ports on api, and 16 is the most there is room for` |
| `hib port add` past the machine cap | `256 machines already publish ports, the most there is room for` |
| `hib port add` out of addresses | `no virtual addresses left. addresses are never reused, so this needs a new host` |
| a bad port number | `8080000 isn't a port. Ports are 1 to 65535` |
| `hib port ls`, nothing published | `no published ports. hib port add NAME PORT publishes one` |
| state is corrupt | `hibd can't read its published ports: %v. No machine can reach another until it's fixed` |
| state is a newer version | `hibd can't read its published ports: they were written by a newer hibd. Upgrade it` |
| delivery failed | `warning: published ports saved, but updating web failed: %v` |
| `hib fork`, parent has ports | `copies don't inherit published ports. hib port add task-2 8080 publishes one` |
| hibd too old | `this host's hibd is too old for hib port. Upgrade it` |
| hibd too old, `hib log --links` | `this host's hibd is too old for hib log --links. Upgrade it` |

**The listener's refusals send no bytes.** The connection is closed without a write and the
reason goes to the link log. A client sees a reset, which is what it would see if nothing were
listening.

**Exit codes.** 0 on success. 2 for a usage mistake caught in `hib`: a port that isn't a number, a
port outside 1-65535, no ports given, an unknown flag. 1 for everything the host refused or couldn't
finish, which includes an unknown machine, a reserved port, the per-machine cap and a `port rm` that
unpublished but couldn't confirm the close. `hib` prints the host's message as it came, as it does
for every other `hibd` error.

`hib port add` checks whether the port is bound to `127.0.0.1` in the machine, through envd, and
adds the localhost warning when it is. **It never refuses on that ground.** The check is advisory:
if the machine is paused or the check fails, the port is still published and the warning is left
out.

## Security

Each item is the risk, then what we decided or do about it, then the test that proves it.

**1. Lateral movement.** A prompt-injected agent in one machine reaches every service published
from the others. *Decision:* off by default and published one port at a time, from the laptop
only. `--from NAME` comes later and narrows it further. The README and the spec say plainly that
publishing a port trusts every one of your machines with it. *Test:*
`TestMachineToMachine/unpublished_port_is_refused`, and a `cmd/hib` unit test that the README
carries the warning.

**2. Source identity.** A machine could claim to be another by spoofing a source address.
*Mitigation:* the source comes from `slots.Finder.Sandbox(src, local)`, which maps the ingress
veth to a slot and requires the `10.11.x` source slot and the `10.12.x` local slot to agree, and
`hook.OnVeth` refuses anything that didn't arrive on a veth at all. The client's claimed address
is never used for authorization. *Test:* `internal/slots` unit tests for a mismatched pair, and a
`linkproxy` unit test that a non-veth connection is refused.

**3. The localhost relay.** envd relays every `127.0.0.1` LISTEN socket to `169.254.0.21` through
socat, so publishing a port also publishes a service that only ever bound to localhost, and its
clients arrive looking like `127.0.0.1`. *Decision:* both detect and warn. `hib port add` asks the
machine what the port is bound to and warns when it's localhost-only, but still publishes: the
person asked, and the check can't run on a paused machine. The docs say the same thing.
*Test:* a `cmd/hib` unit test that the warning appears for a localhost bind and not for `0.0.0.0`.

**4. Wake amplification and RAM.** Link wakes can eat the room a person needs, and can keep
`vm/idle-deallocate` from ever shutting the host down, which costs money.
`vm/idle-deallocate/idle-deallocate:34-41` treats **any** running machine as activity, so one
machine held awake by a link holds the whole host. *Mitigations:* a **reserve** of one `dev`
machine's memory that only a person can use, taken under the same lock as the admission so two
concurrent wakes can't both spend it. Each wake is charged its target's size in `dev` machines,
rounded up, so a bigger target can't spend it either. A **rate limit** of one wake per target per
30 s, charged at admission and rolled back on failure. **Per-source, per-target and global link
caps** (64, 64, 512). An audit line when a link **opens**, carrying `Woke`, plus an `OPEN` column
in `hib port ls`, so "this machine is awake because of a link from X" is answerable while it is
happening rather than after it ends. A refusal is immediate and says `no room`. *Test:*
`TestMachineToMachine/no_room_refuses_fast`, a unit test that N concurrent admissions for different
targets never take the reserve and a unit test that concurrent admissions for the same target
coalesce into one wake. `TestABigTargetNeverSpendsTheReserve` refuses a target that needs two `dev`
machines when there is room for one besides the reserve and wakes it when there is room for two,
and while that wake is held a default-size target is refused. `TestLinkRoomChargesATargetItsSize`
checks `hibd`'s side: a target twice the default size or just over it needs 2, one the default size
or smaller needs 1, `free` is `Fits - 1` and a failed `Get` or a missing size is an error.

**4b. Links that hold each other up.** Two machines linking to each other, or a machine linking to
itself, renew each other for ever, and `vm/idle-deallocate` then never shuts the host down. Checking
the source's `EndAt` doesn't catch it: `EndAt` is itself renewed by the link. *Decision, four
parts:* **self-links are refused** outright. **Non-link activity is tracked separately from
`EndAt`**, as an mtime under `<state>/links/activity/<sandbox-id>` with the four feeds listed in the
design, never the proxy. **A link renews its target only while its source's activity is newer than
`LinkGrace` (5 min)**. **The same freshness gates waking**, not only renewing, so a retrying client
that is itself awake only because of a link cannot restart the ring it is part of. Gating renewal
alone is not enough: the ring would pause, then the first retry would wake it all again. A
link-woken target is also held to the source's lease, which is **absolute** and **re-derived at
every renewal and wake decision**, not a duration frozen at admission: E2B computes a resumed
machine's end from a start time taken after placement (`create_instance.go:440-443`), so a relative
clamp lets a slow wake outlive the lease that justified it, and a frozen lease would cut a healthy
link whose source was still working. The lease by itself never closes an established link: it stops
renewal and the target pauses on its own timeout. A pending wake is cancelled once the lease passes,
the target's `EndAt` is read back and shortened after the resume and a target whose lease is already
gone is paused again behind its fence. That pause doesn't wait for the proxy to close the target's
links as `hib pause` does: no link can reach a machine while its wake is in flight, because those
admissions wait on the wake and fail with it, and a link it opened since the resume ends with its
run. A cycle of any length with nothing but link traffic therefore stops within `LinkGrace` plus the
longest idle timeout in it, and stays stopped. A benchmark machine with SSH open or CPU burning
keeps its downstream target awake for as long as it is actually working, which is the case this has
to keep. The clock is not unforgeable (see the design's note on what it is not), but a guest that
fakes activity gains only its targets' RAM, bounded by the caps and the reserve, and quiesce still
ends everything. *Test:* a `linkproxy` unit test that a self-link is refused and one that `Admit`
refuses a stale source with `ErrSourceIdle`. `linkadmit` tests for a wake slower than the lease, for
the reconcile after the resume and for the rollback. Slow e2e tests that a two-machine and a
three-machine ring with **retrying** clients, SSH closed, no agents and CPU under the busy
threshold, both stop within the stated bound and do not come back, and that a source with an open
SSH session keeps reaching its targets throughout.

**5. Human pause intent.** After `hib pause api`, a worker's retry could wake it a second later.
*Decision:* `hib pause` sets `PausedByHand` for that sandbox ID, and `Admit` refuses while it is
set, including when the target happens to be **running**, since every connection is admitted and
none skips the check. It is cleared only when a person starts the machine, never by observing that
it is running: `save` and `fork` resume a paused machine for a few seconds
(`internal/machines/snapshot.go:150` calls `Resume` unconditionally) and pause it again, and a link
must not slip into that window. Every pause Hibernaut starts, by hand or not, raises `Machine.Fence`
before the `close` is sent and **clears it once the pause settles either way**, so a failed `save`
doesn't leave a machine permanently unreachable. Admission refuses while either endpoint is fenced.
*Test:* a unit test that `pause` sets the mark, that `save` and `fork` leave it set across their
internal resume, that a running-but-marked target is refused, that a fenced **source** is refused as
well as a fenced target, that a failed pause clears the fence, that an old fence with a live owner
still refuses, that `hibd cleanup` reconciles an orphaned fence rather than ignoring it and
`TestMachineToMachine/a_machine_paused_by_hand_is_not_woken_by_a_link`.

*Residual:* a pause can lose to a link wake that is already resuming the machine. The `close`
cancels the wake, but E2B may finish a resume it has started, and a `Pause` sent mid-resume is
answered as not running, which `Manager.Pause` counts as done (`internal/machines/machines.go:356`).
The machine then runs to the end of its lease, at most `LinkGrace`, or longer while the busy watcher
renews it (`internal/busy/busy.go:189`), which doesn't read `PausedByHand`. Nothing can link to it
meanwhile, since the mark is set, and running `hib pause` again pauses it. Closing the gap would
take two changes: the wake path pausing again a machine that comes up fenced or marked, and busy
skipping marked machines. The component security review found it and rated it low, and M12 leaves it
open.

**5b. Quiescing.** `pause-all` loops until nothing is running. A link that woke machines would stop
it converging and keep the host from deallocating. *Decision:* the quiescing check is the **first**
thing the connection path does, before any state is read, and renewals stop while the marker exists
as well: refusing new links but renewing old ones would not converge either. Two holes in today's
fencing are closed in the same change: `internal/pauseall/pauseall.go:52-66` pauses **every**
running machine, not only idle ones, and `pauseAll` never creates the marker, so the shutdown path
`vm/systemd/hibd-pause-all.service:14` runs with no marker at all. M12 moves marker creation into
`pauseAll`, so every way in is fenced. *Test:* a unit test that `pauseAll` creates the marker, and a
`linkproxy` unit test that a link is refused and an open link stops renewing while it exists.

**6. Exhausting hibd.** `hibd proxy` also runs the egress proxy, and `hibd` runs the SSH bridge. A
machine opening thousands of links could starve both. *Mitigation:* `linkproxy`'s **own** caps and
timeouts, listed in Limits. They are not inherited from the egress proxy, which has none:
`internal/egress/egress.go:105-117` accepts in a loop and starts a goroutine per connection with no
limit at all. A link over the cap is closed without a read. A source over the accept rate is closed
without a read. The host cap stops the accept loop for 100 ms instead. *Test:* `linkproxy`'s
`TestPerSourceCap` checks that the 65th link from one source is refused and the first 64 keep
working. Its `TestHostCapStopsAccepting` checks that the host cap stops the accept loop.
`internal/hibd`'s `TestProxyServesEgressWhileTheLinkCapIsSaturated` fills a real `hibd proxy`'s link
listener to `links.MaxLinks` and checks that the egress proxy still serves.

**7. The listener's exposure.** A new listening port on the host. *Mitigations:* it accepts only
from `veth-*` interfaces. E2B's per-sandbox firewall drops `10.0.0.0/8`, so machines can't reach the
host's own addresses. Azure's NSG allows only 22 from outside. The rule that keeps
`TestNoHostAccess` passing is **a refused connection sends no bytes**: the test's probe
(`e2e/hostaccess_test.go:105-120`) writes first and counts a port only when the far end answers, so
a listener that closes in silence is invisible to it. This is about refusals only. An allowed link
carries a server-first protocol normally, because those bytes come from the target. *Test:*
`TestNoHostAccess`, unchanged, plus a `linkproxy` unit test that every refusal path writes zero
bytes before closing.

**8. The unused range.** `198.18.0.0/15` isn't E2B's, so nothing else guards it. *Mitigations:* the
listener refuses any address `links.json` hasn't handed out. `vm/e2b/sandbox-firewall.nft` gains a
rule dropping everything `veth-*` sends to `198.18.0.0/15`, so nothing machines send to the range
is forwarded out of the host. Link TCP never reaches it, because it is redirected in
`nat PREROUTING` first and delivered locally. The rule drops every other protocol, SCTP or raw IP
for example. It also drops TCP the redirect missed, because NAT skips a packet conntrack marks
invalid. `e2b-orchestrator.service` gains `IPAddressDeny=198.18.0.0/15` next to its existing
denies. The last one matters: E2B's `tcpfirewall` redirects all other guest TCP to a CIDR-only
listener which then dials the original address **from the host**, and `198.18.0.0/15` is in
neither its allow nor its deny set. If `hibd`'s rule were missing, that path would be the one
answering. With the `IPAddressDeny` it can't be, so a missing rule gives a reset, not a surprise.
*Test:* `TestMachineToMachine/an_unallocated_address_is_refused`, a `vm/systemd` unit test that the
deny line is in the unit and `TestSandboxFirewallDropsTheLinkRange`, which checks the drop names no
protocol.

**9. Rule ordering.** Two owners fighting over `nat PREROUTING`, and a window where nothing is
intercepted. *Decision:* one owner and one transaction. The jump and the chain are installed,
kept and removed by the same `hook.Rule`, and the install is a single `iptables-restore
--noflush` that touches only `HIBERNAUT` and its jump, never a flush of a live chain, because while
the chain is empty every packet falls through to the rest of `PREROUTING`, 443 escapes interception
and conntrack can hold that bypass open afterwards. Inside the transaction the chain is filled
before the jump is added, and the legacy bare 443 rule is deleted **last**, so an interrupted
upgrade leaves two working rules rather than none. `Keep` compares canonical forms (`-m tcp`
dropped, `--to-port` and `--to-ports` normalized, as M11's parser already had to,
`internal/egress/hook/rules.go:129-145`) so it doesn't flap or log on every pass. The strict
parser that made any unexpected `hibernaut` rule fatal was replaced by matching on the comment, and
an unrecognised one is deleted.
*Test:* `internal/egress/hook` host tests for install, for a foreign rule inserted above the jump,
for a stale bare 443 rule from the previous version, for an **interrupted** migration (jump present,
chain half-written) and its repair and for `Keep` being silent when `iptables-save` renders a rule
with `-m tcp`.

**10. Slot reuse.** E2B returns a slot to the pool `ReturnDelay` (**3 s**) after a run ends
(`pool.go:25-32`, and teardown waits for Firecracker to exit before the slot-return callback runs,
`sandbox.go:1833-1868`, `:4105-4112`), so a slot address can belong to another machine, and under G5
to another tenant. The requirement is absolute: **no link byte moves after either endpoint's run
ends**, whatever ends it, and that has to hold when `hibd proxy` is stalled, because a stalled
process's sockets go on sending.

*Mitigations, in the order they fire.* The slot comes from the Keeper, which resolves it once per
run, when it opens the run's gate, and a run never changes slot. The run identity is the E2B run
cgroup `sbx-<id>-<run>`, whose suffix is new on every start and resume and whose emptying is an
**event** the proxy watches, so an abort beats the 3 s recycle instead of racing a poll. Every link
socket is created with `SO_LINGER 0`, so an abort discards queued bytes instead of letting the
kernel deliver them to whatever inherited the slot. Hibernaut's own pauses raise a persisted fence,
send `close` and wait for the acknowledgement before pausing. And behind all of them, the one that
does not need the proxy to be alive: the **kernel guard**, an nftables set of live
`(slot address, run token)` pairs whose elements expire after `GuardTTL` (1 s) unless the Keeper
re-proves the run is live and unfenced every 250 ms, with drops in all four directions for anything
not in it. The drop is checked against the socket's own mark, so a lost conntrack entry changes
nothing. nftables checks an element's expiry at lookup, before any garbage collection, so the gate
shuts on its own when nothing holds it open. A stalled or killed proxy therefore loses its slots
within about a second, comfortably inside the 3 s. The 5 s sweep stays as a backstop, re-checking
for **both** ends that the machine exists, is `Running` and is the same run: `Running` explicitly,
because a pause leaves `StartedAt` untouched (`internal/engine/fake/fake.go:280-282`).

The `EndAt`-minus-margin deadline of the previous draft is **gone**, because its premise was false:
`machines.Renew` writes back a stale `EndAt` (`internal/machines/machines.go:589-594`) and E2B's API
shortens timeouts (`sandbox_timeout.go:52`). And because the kernel guard covers what it was for.

*Test:* `linkproxy` unit tests with a fake clock and fake run events for each path (cgroup emptied,
cgroup removed, sweep mismatch, source leg as well as target leg) plus a fake Keeper that retires a
gate between the admission and the dial. A `make test-host` case that `SIGSTOP`s the proxy, ends the
run and asserts packets are dropped on both legs before 3 s. And an e2e that lets a target pause on
its own timeout while traffic is flowing and asserts that no link byte reaches the target after its
run ends: the cgroup event aborts the link first, the kernel guard drops anything left within
`2*GuardTTL` at worst and nothing carries into the next run.

**11. Virtual address reuse.** A stale `/etc/hosts` in a long-asleep machine could point at a
reused address. *Decision:* addresses are never reused. `Next` only goes up, and `hib rm` burns
the address. A stale line therefore points at an unallocated address, which is refused. *Test:* a
`links` unit test that an address is not handed out again after the machine is removed.

**12. Tampering with /etc/hosts.** A machine can edit its own `/etc/hosts` and point `api.hib`
somewhere else. *Decision:* this only affects the tamperer, because authorization uses the virtual
address, which `hibd` allocated, and never the name. Editing it is a way to break your own machine's
resolution, not to reach anything new. `hibd` rewrites the block on the next delivery. The block
does reveal your machines' names to every one of your machines. Under G5 it is scoped per owner.
*Test:* `TestMachineToMachine/a_rewritten_hosts_line_reaches_the_right_machine_or_none`.

**13. Name collisions.** The block could shadow something. *Decision:* it only ever contains
`NAME.hib` lines, never `localhost`, `127.0.0.1`, `::1` or the machine's own hostname, and lines
outside the markers are preserved by the merge. *Test:* `internal/hostsblock` unit tests for a file
with user lines above and below, for a file with no block and that no reserved name can enter the
block.

**14. The egress proxy.** A link must not be mistaken for egress. *Decision:* links never reach
it. The chain redirects `198.18.0.0/15` to the link port before the 443 rule, and the egress proxy
denies `198.18.0.0/15` anyway. So there is no TLS interception on a link, no placeholder swap and
no secret in reach. The docs say it: **secrets are not swapped on a link**, and a machine that
sends a placeholder over one sends the placeholder. *Test:* an `internal/egress` unit test that
`198.18.0.0/15` is denied, which exists, plus
`TestMachineToMachine/a_published_port_on_443_is_reached_not_intercepted`.

**15. Where we dial.** A client must not be able to steer the dial at a host service. *Decision:*
the target comes only from `hibd`'s own chain (virtual address to sandbox ID to current slot to
`10.11.<slot>`) and the port must be in the published list. The client supplies nothing but the
address and port it asked for, both of which are checked against the map first. `hibd`'s own dial
goes out of a host process, not a veth, so the `-i veth-+` jump can't catch it and send it back
round. *Test:* `linkproxy` unit tests that a destination outside `198.18.0.0/15`, an unallocated
address and an unpublished port are each refused before any dial.

**16. Flooding the log.** A retry loop could write millions of lines, and a machine trying every
port a line per port. *Mitigation:* identical refusals fold into one line with `Count` over a 10 s
window, and the log rotates at 10 MiB with one old file kept, like the audit log. A source has at
most `links.MaxFoldsPerSource` (64) lines folding at once and folds the rest into one line per
reason with no target or port. *Test:* an `internal/audit` unit test that 1000 identical refusals
in a window produce one line, and that a different port starts a new one.
`TestLinkRefusalsFromOneSourceAreCapped` has one machine refused on 1000 ports write 64 lines and
an overflow line with `Count` 936, which `Close` writes, while all of another machine's 64 lines in
the same window are written. `TestLinkRefusalCapEndsWithTheWindow` checks the next window has all
64 again.

**17. Connection hygiene.** Half-open connections and trickled bytes. *Mitigation:* TCP keepalive on
both legs at 30 s. Half-close is propagated, so a client that shuts down writing lets the server
finish its reply. Renewal is counted from the **last payload byte**, at most once per 30 s, not from
when the connection closes, so an open but silent link holds nothing. Trickled bytes renew the
target only while the source has non-link activity of its own (item 4b). *Test:* a `linkproxy` unit
test for half-close in each direction, and
`TestMachineToMachineSlow/an_idle_link_doesn't_hold_a_machine_awake`.

**18. Lifecycle edges.** The thread through all of them is that **pausing, removing and unpublishing
happen in other processes**: `hibd pause` and `hibd rm` are separate invocations
(`internal/hibd/hibd.go:209-255`), E2B pauses machines on their idle timeout with no involvement
from `hibd` at all (the daemon at `internal/hibd/hibd.go:1407` only watches and renews, and it never
calls `Pause`) and `save` and `fork` pause and resume a machine inside one command. So every
Hibernaut-initiated one raises the fence, sends `close` or `revoke` on the proxy's control socket
and waits for the answer before pausing. E2B's own timeout is caught by the run-cgroup watch
instead, which is why that watch and not the socket is the primary mechanism, and the kernel guard
is what holds when neither of them runs.

- `hib port rm`: **revocation, not just unpublication.** Taking a port out of the file stops new
  links and nothing else. The command also cancels pending admissions and closes established links
  for that exact `(sandbox ID, port)`, and reports success only once the proxy confirms. A
  revocation that lands during a wake is caught by the generation re-check before the first byte.
  *Test:* `TestMachineToMachine/port_rm_cuts_an_open_link` and
  `TestMachineToMachine/port_rm_cuts_a_link_waiting_on_a_wake`.
- `hib pause`, `pause-all`, nightly quiesce and the leak scan: links to and from that machine are
  aborted behind the fence, and the pause goes ahead only once the proxy says no socket is left or
  the machine's guard elements are gone.
- `hib save` and `hib fork`: the machine is paused first, so its links close with it. And because
  `prepareSnapshot` resumes it again a moment later, `PausedByHand` has to survive the round trip
  (item 5). Saved images carry no ports.
- `hib fork`: the copy has a new sandbox ID and so no entry, no address and no ports. `hib fork`
  prints the hint.
- `hib rm`: the entry goes, the address is burned and open links close. `rm` pushes nothing, so the
  machine's line leaves the other machines' hosts blocks on the daemon's next delivery pass, about
  15 s later. Until then a stale line points at an address that is never handed out again, which
  is refused (item 11).
- A host reboot: running machines are gone anyway. `links.json` survives, and addresses with it.
- A `hibd proxy` restart: `ExecStopPost` runs `hibd proxy -unhook`, which removes the jump and the
  chain, so links fail closed while it's down. Open links die with the process, and the control
  socket is unlinked so a stale path never looks live.
- *Test:* `TestMachineToMachine` covers pause and rm. `internal/hibd` unit tests cover fork's hint,
  save, cross-process cancellation and the cleanup of entries for sandboxes E2B has forgotten.

**19. Out of scope.** Reaching a published port **from the laptop**. `ssh -L 8080:localhost:8080
api.hib` already does it, and adding a second path would mean opening a host port to the outside.
The docs point at `ssh -L`.

## Tasks

Build order: **the contract first**, alone, and it is already written: it is the commit on top of
this one. It adds the packages and types every slice shares *and updates their existing callers*, so
the tree builds before anyone starts and no slice waits for another's rename. It freezes every
cross-slice signature, with a callable body for each call that crosses a slice boundary, so slice
6's dispatch doesn't wait for slice 4's handler and slice 4's `--links` flag doesn't wait for slice
7's logger. Then slices 1-6 in parallel: every file listed below has exactly one owner, and no two
slices touch the same file. Test files count too: a slice owns `x_test.go` for each `x.go` it owns
and adds new test files of its own, and any other existing test file it has to change is listed by
name. Then one integration step, slice 7, which turns the listener on (`LinkPort` stops being 0),
ships the fail-closed firewall pieces with it and wires delivery to the state. Then slice 8: the e2e
tests and the docs. Slices 1 and 2 are the long poles and should start first.

Everything before slice 7 ships **dark**: the listener isn't started, the chain isn't installed and
nothing a machine can reach changes. A half-finished M12 in `main` is inert, not insecure.

### Contract

One commit, **written before any slice starts**, and already on this branch. Nothing is served yet.
Everything compiles and the existing tests pass. The point of it is that the shared ABI is Go rather
than prose: a worker reads `internal/links` and there is nothing left to interpret.

Frozen by the commit:

- [x] **`internal/links`** (new): the seam, and the only home of anything two slices share.
      - `links.go`: `Run` and run liveness, `Admission` (with `LeaseUntil` and `PortGen`, and
        **only the admitted port's generation**, not a machine-wide one, plus the two runs' gates
        `SourceGate` and `TargetGate`), `AdmitRequest` (with the source's run and gate),
        `Admitter`, `Closer`, `Open`, `Stats`, `Controller`, `Runs` (with `Live`), `Gate` (with a
        24-bit token in a `uint32`), `Guard`, `Dropper`, `Tokens`, `Keeper`, `Resolver`, `Slot`,
        `Store`, `Clock`, `Fresh` and `LeaseUntil`. `Closer`'s contract
        is the completion handshake: a `close` or `revoke` is acknowledged **only after every socket
        in scope has been closed or aborted**. `Stats` counts per `(target, port)` as well as per
        machine, which is what `hibapi.PublishedPort.Open` needs.
      - `const.go`: every constant, including the guard's table, set, map, mark, token width,
        priority, TTL, keep period, self-check and margin, with the reason for each number beside
        it, and `Mark`/`Token` over 24-bit tokens.
      - `errors.go`: every refusal, including `ErrGateSpent` and `ErrUnmarked`. `Error()` is also
        the `reason` the link log writes, so the two cannot drift.
      - `state.go`: `Doc`, `Machine`, `Fence`, `Alive`, `FenceState`, `Fenced`, `Stale`, `Gen`,
        `Lookup`, `Entry`, `Allocate`, `Publish`, `Unpublish`.
      - `store.go`: `NewStore` and `NewClock`, the one store every slice goes through (read, locked
        read-modify-write and fence set and clear with an owner) and the production activity clock.
        Slice 4 owns the file for `NewClock`'s bodies.
- [x] **`internal/statefile`** (new): `WaitLock`, `TryLock`, `OpenLock` and `Held`, lifted from
      `internal/hibd/lock.go` **with no change in behaviour** and with their tests. `hibd`'s
      three wrappers delegate, so its twenty-odd call sites are untouched. It is in the contract
      commit because slices 3, 4 and 6 would otherwise all have to cross `machines.go:689` and
      `hibd.go:1335` to get at a lock. `Held` is the fence-owner oracle: `LOCK_SH|LOCK_NB`
      failing means the pause that raised the fence is still running.
- [x] **Constructors and clients, with their bodies left to the slices**, so no slice waits on
      another's build: `linkproxy.New`, `Serve`, `AbortMachine`, `AbortPorts` and `Stat`.
      `linkadmit.New`, `Serve`, `Admit`, `Renew`, `Release`, `NewRuns` and `SetCloser`. `linkctl`'s
      `Client`, `Dial`, `Revoke`, `Close` and `Stat` with the stat wire response. `linkguard`'s
      `Guard`, `Tokens` and `Keeper` (`NewKeeper`, `Run`, `Ready`, `Gate`, `Retired`). No slice
      changes a signature. A slice that has to updates it here, in the same commit, and says so.
      Until its owner fills it in, a stub does this:
      - the constructors return working values: `linkproxy.New`, `linkadmit.New`, `NewRuns`,
        `linkctl.Dial`, `linkguard.New`, `NewTokens`, `NewKeeper` and `links.NewStore`.
        `links.NewClock` is a working clock held in memory, and it only moves forward.
      - every other `linkguard` body returns `links.ErrGuardDown`. `Keeper.Ready` never closes and
        `Keeper.Retired` is already closed.
      - `RunWatcher.Current` and `Watch` return `links.ErrNotRunning`, and `RunWatcher.Live` returns
        an empty map.
      - `linkadmit.Serve` returns `links.ErrNoCloser` until `SetCloser` has been called. `Renew` and
        `Release` do nothing, and `linkproxy.Stat` returns an empty `links.Stats`.
      - everything else returns `links.ErrLinksOff`, including the store's `Read`, `Update` and
        `Fence`, the `slots` stubs and `linkadmit.Serve` after `SetCloser`, and `hibd port` reports
        it.
- [x] **`linkadmit.Config.Closer` is bound late.** The listener needs an admitter to be built and
      the admitter needs the listener to cancel links, so `linkadmit.SetCloser` exists and
      `Serve` refuses with `links.ErrNoCloser` until it has been called. Forgetting is a startup
      failure rather than a link nothing can close, and there is a test for it.
- [x] **The admitter's seams are injected**: `Config.Keeper` (the target run's gate after a wake),
      `Config.Dropper` (the close fallback), `Config.Resolver` (a machine's slot address for that
      fallback's `Drop`) and `Config.Store`. The listener gets `Config.Keeper` and never touches the
      guard. The `Doc` and `Fence` closures of the earlier draft are gone. Everything goes through
      the one store.
- [x] **`internal/hibapi`**: `Ports`, `PublishedPort`, `LinkEntry`, `Response.Ports`,
      `Response.Links`, `Machine.Ports`, `FeaturePorts`. They use `omitzero`, not `omitempty`:
      an allocated machine with nothing published has an empty slice, which `omitempty` would
      drop, and the decoder needs to tell that from an absent field. `DecodeResponse` requires
      `v` (`internal/hibapi/hibapi.go:356`), so the empty document is `{"v":1,"ports":[]}`.
- [x] **`internal/audit`**: `LinkEntry`, `LinkLog`, `OpenLinks`, `Write`, `Close`, `ReadLinks`.
- [x] **`internal/busy`**: `Config.Activity` and `Config.StateDir`, so slice 3 can write the
      activity clock and slice 6 can wire it at `internal/hibd/hibd.go:1426-1429` without the two
      meeting in the same file.
- [x] **`cmd/hib`**: `log()` takes the links flag and ignores it, and `commands.go` passed `false`
      until slice 4 registered `--links`. Slice 4 can add `--links` to the command table and slice 7
      can write the body, in either order.
- [x] **`internal/egress/hook`**: `Rule.Port` is renamed `ProxyPort`, and `LinkPort` and
      `LinkCIDR` are added, **with no change in behaviour**. The one caller, in
      `internal/hibd/proxy.go`, is updated here. It has to be here rather than in slice 1,
      because `proxy.go` belongs to slice 7 and slice 1 renaming the field would break slice 7's
      build. `LinkPort=0` means links are off, and that stayed the default until slice 7.
- [x] **`internal/slots`**: `Finder.Slot(id)` and `Addr(slot)` in `slots.go`, and `Resolver`
      (`Finder` and `Runs`, with `Addr` and `At`) in the new `resolver.go`. Stubs that return
      `links.ErrLinksOff`. Slice 2 owns both files. Pure additions, no callers.
- [x] **`internal/hibd/hibd.go`**: `HIBD_LINK_PORT` (default `0`, which is off) and
      `HIBD_LINK_LOG` in the config struct and the usage text, read but unused, plus the `port`
      case in the dispatch switch. Slice 6 owns `hibd.go` afterwards.
- [x] **`internal/hibd/port.go`** (new): the `hibd port` handler, reporting that links are off.
      It is here with its dispatch entry so that slice 4 can write the CLI and slice 6 can keep
      `hibd.go` without either editing the other's file.
- [x] Unit tests for `links`: allocation order and never reusing an address, exhaustion, the caps,
      reserved ports, whole-batch validation, the no-op cases, a failed first publish leaving no
      entry, deduplication, **per-port** generations, a fence whose owner is alive, `Fresh` and
      `LeaseUntil`, the empty document's exact JSON, `Mark`/`Token` round-tripping over 24 bits
      (token 0, a foreign top byte and `GuardMaxToken`) and that `2*GuardTTL + GuardMargin` fits
      inside `SlotReturnDelay`. A publish over the cap leaves no entry, and an unpublish of a
      reserved port is refused like a publish of one.
- [x] **Done, and owned by no slice**, so no slice edits them: `docs/goals.md`, `docs/ideas.md`,
      `internal/hibapi/hibapi.go`, `internal/hibd/lock.go` and
      `internal/links/{const,errors,links,links_test,state}.go`. `internal/links/store.go` stays
      slice 4's for the bodies.

### Slice 1: the firewall, the iptables chain and the kernel guard

Owns `internal/egress/hook/` and `internal/linkguard/` (new). Depends on the contract.
**It owns the whole kernel guard** (the nftables table, its set and map, the chains, the Keeper and
the `Drop` that hibd's pause fallback calls) because it already owns the firewall layer and because
a guard with two owners is a guard nobody owns. The listener and the admitter reach it through
`links.Keeper`, and hibd's pause fallback through `links.Dropper`.

- [ ] `Ensure` installs the jump and the `HIBERNAUT` chain in **one** `iptables-restore
      --noflush` transaction: chain filled, then jump, then the legacy bare 443 rule deleted.
- [ ] `Keep` compares canonical forms (`-m tcp` dropped, `--to-port` normalized) and rewrites
      only on a real difference. `Remove` takes the jump, flushes the chain, deletes the chain.
- [ ] Delete the strict parser. An unrecognised `hibernaut` rule is deleted, never fatal.
- [ ] `linkguard.Install` **atomically replaces** `table inet hibernaut-links`: the `link_live` set
      (`ipv4_addr . mark`, `flags timeout`, `timeout` `links.GuardTTL`), the `link_src` map and the
      `prerouting`, `output` and `input` chains at `links.GuardPriority`. Both sets start empty, so
      a crash cannot leave a gate open across a restart. `Install` also sets
      `net.ipv4.tcp_fwmark_accept` to 1 in the host's namespace. `Installed` reports whether it is
      all there with this version's rules and the setting still 1. `Remove` deletes the table and
      leaves the setting alone.
- [ ] `Open`, `Extend`, `Drop` and `Has` over `links.Gate` pairs. `Open` creates the set element
      and the `link_src` entry with `NLM_F_EXCL` in one batch, and keeps a high-water mark: it
      records a token as spent before it sends and refuses one at or below the highest it has sent
      (`links.ErrGateSpent`). `Extend` submits **one atomic batch** per cycle, deleting and
      re-adding each element because adding alone does not reset a timeout. It never creates one
      and lets `ENOENT` abort the batch, so a late extension cannot resurrect a gate. `Drop` lists,
      deletes every `(addr . *)` element and `addr`'s map entry and lists again until the read-back
      is empty, tolerating `ENOENT` from elements that expired in between, because hibd's pause
      fallback pauses on the strength of it. A missing table is an error unless the guard's token
      file, `links.TokenFile` from `New`, does not exist, when `Drop` returns nil (*The kernel's own
      fence*, 10). A false from `Has` stays false.
- [ ] `linkguard.Tokens`: a never-reused 24-bit counter in `links.TokenFile` that starts at 1 and
      is monotonic across proxy restarts. It writes the counter before it returns a token, so the
      file exists whenever a token is in use. At the end it returns `links.ErrTokensSpent` and
      links fail closed until the host reboots: no restart of the counter and no reinstall.
- [ ] **The Keeper**, `linkguard.Keeper` (`NewKeeper`, `Run`, `Ready`, `Gate` and `Retired`), which
      lives here and not in the listener, so the guard has one owner. It is one serialized loop
      that, every `GuardKeep`, extends the gates of live, unfenced runs first, retires the rest and
      then opens the missing ones, as *The kernel's own fence*, 7 describes. `Gate` nudges a cycle
      and waits for it. `Retired` is closed for a gate the Keeper does not hold.
- [ ] The self-check: every `links.GuardSelfCheck` the Keeper calls `Installed`. On an `ENOENT`
      from `Extend` it calls `Installed` first, and if the table is intact it retires the gates
      `Has` says have vanished and resends the rest. On any other failure it retires every gate,
      `Gate` returns `links.ErrGuardDown` and it reinstalls, retrying every `GuardSelfCheck`.
      `links.ErrTokensSpent` stops new gates and leaves the open ones extended.
- [ ] Unit tests with a fake nftables backend. `Open` is create-only and refuses a spent token with
      `ErrGateSpent`. `Drop` re-reads until empty, fails on a missing table and returns nil for one
      only when there is no token file. `Tokens` writes the file before the first token leaves it.
      The Keeper opens gates for live, unfenced runs, drops before it opens and re-checks after. It
      retires a gate on a fence, on a run ending and on `ENOENT`. It reinstalls on an `ENOENT` with
      the table missing. Spent tokens fail closed, `Gate` waits for a cycle, `Retired` is closed for
      an unknown gate and a failed `Store.Read` shuts every gate.
- [ ] Root tests in `test-host` for the iptables chain: install from nothing, repair after a
      foreign rule above the jump, migrate from the bare 443 rule, repair an **interrupted**
      migration, `Keep` silent when `iptables-save` renders `-m tcp`, `Remove` leaving nothing.
- [ ] **The guard's kernel gates**, each a named `test-host` case logging the kernel version, and
      all of which must pass before slice 7 may turn links on: the table in *The kernel's own
      fence*, 12. They are `TestGuardExpiredDeleteAbortsBatch`, `TestGuardDeleteAddResetsExpiry`,
      `TestGuardOpenIsCreateOnly`, `TestGuardDroppedGateStaysGone`, `TestGuardReopenAfterExpiry`,
      `TestGuardMapMissLeavesMarkZero`, `TestGuardAcceptTakesSynMark`,
      `TestGuardInputUsesSocketMark`, `TestGuardEgressKeepsMarkZero`,
      `TestGuardConntrackLossFailsClosed`, `TestGuardSetBackendIsRhash` and
      `TestGuardDropsAllFourWaysUnderStop` (`SIGSTOP` the proxy, end the run and assert that all
      four directions drop within `2*GuardTTL`, which is inside `SlotReturnDelay`).
- [ ] Install and uninstall are idempotent, and the table coexists with `hibernaut-sandboxes` and
      E2B's `slot-firewall` without changing what either of them does, including across an E2B
      ruleset reload.

### Slice 2: the link listener

Owns `internal/linkproxy/` and `internal/linkctl/` (both new): the client and the server, so the
wire and its only two speakers change together. Also owns `internal/slots/slots.go` and
`internal/slots/resolver.go`. Depends on the contract. Uses `links.Admitter` and
`links.Runs`, so it is built and tested against fakes before slice 3 exists, and it builds on macOS
because `Ingress` and `Dialer` hide everything Linux-only (`hook_stub.go:27-33`).

- [ ] Accept loop with the accept rate limit and the host cap.
- [ ] Per connection: quiescing, `Ingress.Identify` (veth, original destination, source lookup and
      the token in the accepted socket's `SO_MARK`), the address check, the self-link check, the
      published-port check, **the run binding** (the socket's token against `Keeper.Gate` for the
      source's current run, at the connection's source address, refused with `links.ErrUnmarked` or
      `links.ErrWrongRun`), `Admit`, which returns the target's gate already open, the dial to
      `TargetGate.Addr`, the re-check of both runs, `PortGen` and both gates before the first byte,
      copy with half-close and keepalive, byte counts, renew on payload at most once per `LinkRenew`
      for each source and target pair, `open` and `close` audit lines, in the order of the
      listener's twelve numbered steps.
- [ ] **Every link socket is created with `SO_LINGER 0`**, before connect on the target leg and
      on accept for the source leg, so an abort discards queued bytes instead of letting the
      kernel deliver them to whatever inherits the slot. The real `Dialer` sets `SO_MARK` to
      `links.Mark(TargetGate.Token)` and `SO_LINGER 0` before connect.
- [ ] **Abort paths**: `Admission.Ctx` cancelled, either end's `Runs.Watch` channel closed or
      either gate's `Keeper.Retired` channel closed. All of them abort. The ordinary EOF path stays
      graceful (`shutdown(SHUT_WR)`, bounded drain, close). There is no `EndAt` deadline.
- [ ] Every refusal closes without writing a byte.
- [ ] `Config.Logf` gets the real error behind a refusal or an abort whose link log line can only
      name a `links` error, at most once per step, source and error in a `links.FoldWindow` and at
      most 64 different lines in a window.
- [ ] `AbortMachine`, `AbortPorts` and `Stat` work whether or not `Serve` is running. A listener
      that is not serving, before `Serve` or after it has returned, holds no sockets, so the aborts
      answer 0 with no error and `Stat` answers empty counts. The control socket relies on this
      while links are off (*Startup and shutdown*).
- [ ] Unit tests over a loopback pair with a fake `Ingress`, a fake `Dialer`, a fake admitter, a
      fake clock, fake run events and a fake Keeper: every refusal reason and zero bytes written on
      each, the caps, half-close in both directions, abort on the **target's** run ending and on the
      **source's**, the gate-retired and generation-changed races, renew only on payload and per
      source and target pair, folding of refusals and the socket-mark binding. A fake `Ingress`
      returns a token: a mismatch with the Keeper's gate is `links.ErrWrongRun` and no token is
      `links.ErrUnmarked`. A gate the fake Keeper retires the moment a run ends aborts the link. An
      unreadable `links.json` refuses every new link with `links.ErrStateUnreadable`, and the links
      already open are aborted once the fake Keeper retires every gate. The aborts and `Stat` answer
      0 and empty counts before `Serve` and after it returns. These run on macOS.
- [ ] `internal/slots`: `Finder.Slot(id)` (the walk `sandboxForNetns` does, in the other
      direction), `Addr(slot)` (the inverse of `slotFromSource`) and `Resolver.Addr` and
      `Resolver.At`, which read the run on both sides of the slot lookup and fail with
      `links.ErrNotRunning` unless the two reads agree. Unit tests over fake cgroup, proc and netns
      roots.

### Slice 3: admission, waking, the run watcher and the activity clock

Owns `internal/linkadmit/` (new), `internal/machines/wake.go` (new),
`internal/machines/machines.go`, `internal/machines/forkplan.go`, `internal/busy/busy.go` and
`internal/audit/links.go` (new). Depends on the contract.

- [ ] The admitter: one mutex over quiescing, `PausedByHand`, the source's activity freshness, the
      wake rate limit, the per-source, per-target and host caps and the room reserve. **Every**
      connection admitted, running targets included. Reserve and count taken under the mutex before
      the wake, mutex released during the resume, all counters rolled back together on failure.
      Coalescing per sandbox ID. `Admission.Ctx` is cancelled by an unpublish, a fence, a pause, a
      removal or a run ending, and never by the lease. After any wake `Admit` asks `Keeper.Gate` for
      the target's run and fills `TargetGate`, so the gate is open before the listener dials.
      `Release` gives back the caps only, and keeps the rate history of a successful wake. A wake
      holds its room until it ends.
- [ ] The close fallback: when the listener does not acknowledge a `close` within
      `LinkCloseGrace`, the admitter finds the machine's address with `Resolver.Addr` and drops its
      gates with the `Dropper`.
- [ ] The **run watcher**: map a sandbox ID to its `sbx-<id>-<run>` cgroup, and report the run's end
      when `cgroup.events` says `populated 0` or the directory disappears. Identity is the directory
      name. Liveness is `populated`, because ended runs' cgroups can linger
      (`internal/busy/busy.go:241`). `RunWatcher.Live` returns every machine that has a run now,
      read from the cgroups at the time of the call.
- [ ] The control socket: `/run/hibernaut/links.sock`, `revoke`, `close`, `stat`, owned by the state
      owner at `0600`, removed before binding if a killed proxy left it behind and unlinked by the
      proxy on the way out. `Serve` serves it from startup step 8 whenever links are configured on,
      whether or not the listener ever serves (*Startup and shutdown*). It cancels its own pending
      admissions and then always asks the closer, which answers for its own sockets. It never
      answers for the listener.
- [ ] The 5 s sweep as a backstop: for both ends, the machine exists, is **`Running`** and its run
      matches. `Running` explicitly: a pause leaves `StartedAt` alone
      (`internal/engine/fake/fake.go:280-282`).
- [ ] `machines.Manager.Wake(ctx, id string, leaseUntil time.Time)`: resume **by sandbox ID**, with
      an **absolute** lease, not a duration. A pending wake is cancelled once the lease passes.
      After the resume the real `EndAt` is read back and shortened if it is past the lease. If the
      lease has already gone and nothing else is keeping the target up, the machine is paused again
      behind its fence. A relative clamp does not hold, because E2B computes `endTime` from a
      `startTime` taken after placement (`create_instance.go:440-443`).
- [ ] **The lease is re-derived, not frozen.** `Admission.LeaseUntil` is only the value at
      admission. Every renewal and every wake decision recomputes `links.LeaseUntil` from the clock,
      so a source that is still working keeps its links. The lease alone never closes an established
      link: it stops the renewal and the target pauses on its own timeout.
- [ ] The renewal rule: renew the target only while the source's activity is inside `LinkGrace`,
      counted from the last payload byte, at most once per `LinkRenew`.
- [ ] **The activity clock's feeds**, which is why this slice owns `machines.go` and `busy.go`: an
      SSH/`exec` session heartbeat on its own ≤30 s ticker, **not** `Manager.keepalive`, which never
      ticks at all when `idle=0` (`internal/machines/machines.go:555-560`). Every `busy`
      observation, with `idle=0` machines now **observed but still not renewed** (M11's `busy`
      skipped them entirely). A successful `hib new` or `hib fork`: the touch goes in
      `forkplan.go`'s `makeCopy` after it has succeeded (`internal/machines/forkplan.go:97-113`),
      not in the `machines.New` constructor, which only builds a `Manager`. The proxy never touches
      the clock. `busy.Config.Activity` and `StateDir` are frozen in the contract, and slice 6 wires
      them at `internal/hibd/hibd.go:1426-1429`, so the two slices never meet in a file.
- [ ] Unit tests: concurrent admissions for different targets never take the reserve. Concurrent
      admissions for one target coalesce into one wake. A failed wake rolls back the rate limit and
      the room together. `revoke` cancels an open link and one waiting mid-wake. A stale source is
      refused with `ErrSourceIdle` **and** stops renewing. A self-link is refused. A running target
      still goes through `PausedByHand`. The run watcher fires on `populated 0` and on removal. The
      sweep catches a paused machine whose `StartedAt` didn't move. An `idle=0` machine is observed
      and not renewed. A wake **slower than the lease** is cancelled. The reconcile after a resume,
      the rollback that pauses a target whose lease had already passed and that a fenced **source**
      is refused, not only a fenced target. `Live` lists only populated runs. `Admit` fills
      `TargetGate` from a fake Keeper after the wake. A `close` left unacknowledged past
      `LinkCloseGrace` reaches a fake `Dropper`. `Release` keeps the rate history. With a fake
      closer that is not serving, `close` and `revoke` answer 0 and `stat` answers empty counts, and
      an error from the closer comes back as `err`, never as 0.

### Slice 4: state commands and the CLI

Owns `internal/hibd/port.go`, `internal/links/store.go`, `internal/statefile/`,
`internal/machines/ownership.go`, `internal/machines/statedir.go`, `cmd/hib/port.go` (new),
`cmd/hib/commands.go`, `cmd/hib/complete.go` and `README.md`. Depends on the contract, which
already carries the `internal/statefile` extraction and the empty `hibd/port.go` handler.

- [ ] Lift `stateRoot`, `publishOwned`, `ensureDirectory` and `openLock` out of `internal/machines`
      into `internal/statefile`, unchanged except that `openLock` takes the lock's name, so `links`
      can write an owned file and take an owned lock. `statefile` gets its own copy of `checkDir`,
      which stays in `machines.go`. In `internal/machines`, `stateOwner`, `directoryOwner` and the
      `*Manager` methods stay as thin wrappers that pass `m.euid` and `m.chownFile`, so
      `machines.go`, `saves.go` and `lock.go` are not touched and `saves.go`'s reads of `owner.uid`
      (`internal/machines/saves.go:193,227`) still compile. The machines tests stay where they are
      and keep passing. The `statefile` tests are new, among them a root host test: a lock that root
      creates belongs to the state directory's owner, and the login user can take it afterwards.
- [ ] `Held` opens the lock without creating it, and a lock that is not there has no holder.
- [ ] The bodies of `links.NewStore` and `links.NewClock`: read with the owned-file rules, take
      `links.lock` and every fence lock through the owned open rather than `statefile.OpenLock`,
      update under `links.lock` with `WaitLock`'s cancellable wait, write `links.json` with
      `publishOwned` and set and clear a fence with an owner that `statefile.Held` can check.
      Failing and corrupt reads and an unknown `v` all fail closed. Every other slice goes through
      this, so the lock order and the version check have one home.
- [ ] The activity clock's bodies in `links.NewClock`, as *The activity clock is not in
      `links.json`* and *The mtime only moves forward* describe. `Touch` creates the directory
      (`0700`) as needed and opens everything through the state root with `O_NOFOLLOW`. On a file
      that exists it takes `LOCK_EX` on the file itself and never `links.lock`, reads the mtime with
      `fstat` and calls `futimes` only when `t` is later. With no file it creates a temporary one
      (`0600`, `O_EXCL`, a name starting with a dot), sets its mtime to `t` and publishes it with
      `link`, takes the locked path on `EEXIST` and removes the temporary file either way. As root
      it chowns the directory and the file to the state directory's owner, the file before it is
      published, so the login user's `hibd` can set explicit times on it afterwards. It ignores an
      ID that is not a single path element or that starts with a dot, and it is best effort.
      `Active` only stats the file, and a missing file is the zero time. Unit tests for each, a
      first touch with a past `t` recording that `t`, and two races that both leave the latest time
      and no temporary file: concurrent `Touch` calls on separate descriptors with out-of-order
      times, and concurrent first touches.
- [ ] `hibd port add|rm|ls`: resolve the name to an ID, validate the batch, update the store,
      `revoke` through the control socket for `rm`, `pushSetup`, warn on a failed push.
- [ ] `hib port add|rm|ls` in the command table with help, examples and `--json`, modelled on
      `secret`. The `--links` flag registered on `log`.
- [ ] `cmd/hib/complete.go`'s `positional()` gains `"port add"` and `"port rm"` for machine-name
      completion. This is a hand-written switch and does **not** come from the command table.
- [ ] The localhost-bind check and its warning. It never refuses.
- [ ] `README.md`: the `### Command reference` block, which `cmd/hib/readme_test.go` compares with
      `hib help` byte for byte, **and** the `hib port` prose (the lateral-movement warning, the
      localhost-relay note and `ssh -L` for reaching a port from the laptop). This slice owns the
      whole file: the reference and the prose can't be split across two owners, and the reference
      has to land with the commands or `readme_test` fails.
- [ ] Unit tests in `internal/hibd` and `cmd/hib`: every message and exit code in Design, the
      reserved ports, the caps, the `--json` shapes including the empty one, the old-hibd path,
      completion for `port add`.

### Slice 5: virtual addresses in /etc/hosts

Owns `internal/hostsblock/` (new), `internal/machines/hosts.go` (new),
`internal/machines/env.go`, `internal/machines/setup.go` and `internal/hibd/delivery.go`.
Depends on the contract.

- [ ] `internal/hostsblock`: `Render`, `Merge`, markers, reserved names, user lines kept, the error
      set. It shares only the idea of marker scanning with `envblock`. The syntaxes stay separate
      and no code is common.
- [ ] Factor the compare-and-swap retry loop out of `machines.WriteBlock`, which was
      `/etc/environment`-specific (the loop is reusable, the
      `readEnvironment`/`compareAndSwapEnvironment` pair is not), and add `machines.WriteHosts` on
      top of it. This slice owns `env.go` for that reason.
- [ ] `machines.Setup.Hosts`, written after the env block. `Setup.Hash` gains the field and the
      version goes to v2.
- [ ] `delivery.go` builds the block from `links.json`, so a port change pushes it, and warns
      rather than failing when a machine doesn't take it.
- [ ] Unit tests: merge into a file with user lines above and below, into one with no block,
      removing the block when nothing is published. No reserved name can enter. The hash changes
      with the field and the version. The delivery cache invalidates on a port change.

### Slice 6: hibd's core, dispatch, version, pause, list, rm and cleanup

Owns `internal/hibd/hibd.go`, `internal/hibd/leakscan.go`, `internal/pauseall/pauseall.go`,
`vm/systemd/hibd-pause-all.service` and `vm/lib/quiesce`. Depends on the contract. It calls the
contract's `hibd port` handler signature, so it doesn't wait for slice 4's body. `clean` and
`pauseAll` both live in `hibd.go` already, so this slice owns them with the rest of the file.

- [ ] Dispatch for `port`, and `FeaturePorts` in `version`.
- [ ] `ls` gains the `PORTS` column in hibd's response and `ls --json` the `ports` field. The
      laptop-side rendering is slice 7's.
- [ ] `pause` and `rm`, with links on: raise `Machine.Fence`, send `close`, wait for the ack and
      only then pause. If the ack doesn't come within `CtlTimeout`, delete the machine's elements
      from the guard set and confirm they are gone rather than pausing past sockets the proxy may
      still hold. Clear the fence once the pause settles, succeeded or failed. If `Drop` fails, the
      fallback fails closed: it leaves the machine as it was, clears the fence and exits 1, and
      says that root is needed when the error is `EPERM`.
- [ ] With links off, `pause`, `rm`, `pauseAll` and the leak scan stay M11's, as *With links off,
      every pause is M11's pause* describes: no fence, no `close`, no `Drop`, no `PausedByHand`
      and no marker, and `rm` and `cleanup` leave `links.json` alone. `cleanup` still prunes
      `activity/<id>` files.
- [ ] `vm/systemd/hibd-pause-all.service` gains `After=hibd-proxy.service` and nothing that
      requires it, so at shutdown `pause-all` runs while the proxy still answers `close`, and it
      still runs on a host whose proxy is not.
- [ ] `pause` sets `PausedByHand`. It is cleared only by a person-initiated start.
- [ ] `rm` removes the entry and burns the address.
- [ ] `cleanup` drops entries and `activity/<id>` files for sandboxes E2B has forgotten, and the
      temporary files whose names start with a dot that a crashed `Touch` left behind. A sandbox
      missing from E2B's listing counts as forgotten only once a `Get` for it says not found,
      because the listing reads running and paused machines page by page and a machine paused
      meanwhile can fall between them. One that `Get` can't answer for is kept, with an error.
      Removing one that a `Touch` is still using only loses that touch, which errs toward less
      activity. It **prunes after releasing the cleanup lock**: `clean` already holds `lockCleanup`
      across the whole run and nests `lockCleanupRead` inside it (`internal/hibd/hibd.go:940,986`),
      and taking `links.lock` underneath that would add a third level to the order in Design.
- [ ] `cleanup` reconciles every fence up for `links.FenceStale` or longer, as item 3 of *Waking,
      keeping awake and pausing* describes, sharing one `Get` per machine with the prune.
- [ ] `pauseAll` **creates** the quiescing marker itself, so the `ExecStop` path
      (`vm/systemd/hibd-pause-all.service:14`) is fenced too, and the doc comment stops claiming it
      pauses only idle machines: `internal/pauseall/pauseall.go:52-66` pauses every running machine.
- [ ] `leakscan` pauses through the same fenced path instead of calling `e.Pause` directly
      (`internal/hibd/leakscan.go:38-47`), so a machine the leak scan takes down can't be left
      with links open.
- [ ] The busy wiring at `internal/hibd/hibd.go:1426-1429` passes `busy.Config.Activity` and
      `StateDir`, so slice 3's clock is fed. The hook and the field are frozen in the contract,
      so the two slices don't collide.
- [ ] Unit tests: dispatch and usage, `version`, the `ls` field, `pauseAll` creating the marker,
      `pause` setting the mark, a pause that can't get an ack deleting the guard elements, a failed
      pause clearing the fence, the fallback failing closed with a fake `Dropper` that returns
      `EPERM`, the leak scan's fenced pause, cleanup pruning outside the lock and only what a `Get`
      confirms is gone, the fence reconcile in each of its cases with a real `flock` for the live
      owner. With links off: `pause`, `rm`, `pauseAll` and the leak scan touch no fence, send no
      `close`, never reach the `Dropper`, set no mark and create no marker, and `cleanup` leaves
      `links.json` as it was and still prunes activity files.

### Slice 7: integration, and turning links on

Owns `internal/hibd/proxy.go`, `internal/hibd/fork.go`, `internal/hibd/log.go`,
`internal/machines/snapshot.go`, `cmd/hib/main.go`, `cmd/hib/secret.go`, `vm/setup-host.sh`,
`vm/systemd/hibd-proxy.service`, `vm/systemd/hibernaut-tmpfiles.conf` (new),
`vm/systemd/e2b-orchestrator.service` and `vm/e2b/sandbox-firewall.nft`, and three test files that
are not named after a file it owns, `cmd/hib/hib_test.go`, `cmd/hib/rm_test.go` and
`internal/machines/scriptlock_test.go`. Starts when slices 1-6 have landed.

It is also the **one** slice allowed to touch `internal/hibd/hibd.go` after slice 6 has landed, and
only for two things: giving `new` the hosts block, and changing the `HIBD_LINK_PORT` default from 0
to 5444, which is the value `loadSettings` falls back to, the usage text and the `linkPort` comment.
The default is the commit that turns links on, and it cannot be anywhere else.

- [ ] `hibd proxy` serves **both** listeners: the egress proxy on 5443 and the link listener on
      5444, sharing one `hook.Rule`, one `slots.Finder` and one shutdown. This is the commit that
      sets `LinkPort` away from 0 and turns links on, and it may not land until **every one of slice
      1's guard kernel gates has passed on the host**. A kernel that does not behave as the guard
      assumes keeps links off. The gates are how that is noticed, not an argument in this document.
- [x] Build the links side in `internal/hibd/proxy.go` and `internal/hibd/linkside.go` in the order
      *Startup and shutdown* gives (guard and tokens, run watcher, resolver, store and clock,
      Keeper, admitter, listener, then `admitter.Serve` and `Keeper.Run`, then `listener.Serve`),
      with the control socket and the link log, and unlink the socket on the way out. The control
      socket is served even while the listener is off, and egress goes on either way.
- [x] `hibd-proxy.service` gains `ReadWritePaths=/run/hibernaut` (**not** `RuntimeDirectory`, which
      systemd would delete on stop, taking the quiescing marker with it) and `WatchdogSec=30`,
      beside the `Restart=always` it already had. It gains `CPUWeight=200`, twice the default, so a
      busy host does not starve the Keeper into a stall longer than `GuardTTL - GuardKeep`. It
      never gains `ProtectKernelTunables=yes`, which would stop `Install` from setting
      `net.ipv4.tcp_fwmark_accept`.
- [x] `vm/systemd/hibernaut-tmpfiles.conf` creates `/run/hibernaut` at boot, installed as
      `/usr/lib/tmpfiles.d/hibernaut.conf`. Without it the unit fails `226/NAMESPACE` on a cold
      boot, because systemd builds the mount namespace for `ReadWritePaths` before `ExecStart` runs
      and `/run` is a tmpfs.
- [x] **The fail-closed firewall pieces land here, with or before the commit that turns links
      on**, never after: `vm/e2b/sandbox-firewall.nft` drops everything from `veth-*` to
      `198.18.0.0/15`, and `e2b-orchestrator.service` gains `IPAddressDeny=198.18.0.0/15`.
      Shipping the range live before the drops exist would open it unprotected for a release.
- [x] With links on, `snapshot.go`'s internal pause and resume fence, close and wait like any other
      pause, and keep `PausedByHand` across the round trip. If the pause fails, the fence is lifted.
      With links off it is M11's pause (*With links off, every pause is M11's pause*). `hibd` hands
      the fence in as `machines.Config.Fenced` (`internal/hibd/snapshotfence.go`), which waits at
      most 30 s for it.
- [x] **The proxy checks its protections at startup and refuses to serve links without them**:
      the Keeper's `Ready` (the guard installed and its first cycle run), the control socket
      answering a `stat`, the `sandbox-firewall.nft` drops and the orchestrator's `IPAddressDeny`
      (`internal/hibd/linkchecks.go`). It binds the link port only once they all pass.
      `azure/up.sh` deploys before `install-e2b.sh`, so ordering the files is not enough on a fresh
      host: a boot can reach `hibd proxy` before the E2B pieces exist. If any check fails the
      listener stays off and egress goes on. The proxy re-runs the checks once a minute, logs the
      ones that failed each time and starts the listener once they all pass, so a host whose E2B
      pieces arrive after the proxy gets links without a restart. `azure/` itself is not edited.
- [x] `vm/setup-host.sh` installs the tmpfiles entry and runs `systemd-tmpfiles --create`
      **before** `systemctl restart hibd-proxy.service` (`vm/setup-host.sh:40-47`), so the first
      start after an upgrade already has `/run/hibernaut`.
- [x] `hibd fork` writes the no-inherited-ports hint to stderr from `internal/hibd/fork.go`. `hib`
      relays hibd's stderr (`cmd/hib/remote.go:185-197`) and M11's live line prints it
      (`cmd/hib/liveline.go:42`, `live.Write` at 207-226), so `cmd/hib/fork.go` does not change and
      **M11's live line and face stay as they are**.
- [x] `hibd new` and `hibd fork` deliver the hosts block with the rest of the setup, so a new
      machine has its names from the start rather than from the daemon's next pass. `loadSetup` has
      no engine to map IDs to names, so the block is built from `links.json` and the machine list
      the way `updateSetup` builds it (`hostsBlock`, `internal/hibd/delivery.go:116`), and only with
      links on (`internal/hibd/newhosts.go`). When it can't be built, they warn and start the
      machine without the names. `internal/machines/scriptlock_test.go` lists the hosts script next
      to the env script.
- [x] `hibd log --links` puts machine names in `From` and `To`, which the listener writes as sandbox
      IDs, and keeps the ID for a machine that has none.
- [x] `hib pause` and `hib rm` call `hibd` through `remoteSudo` (`cmd/hib/main.go:359,396`), like
      `fork` and `status`, so the pause fallback's `Drop` has `CAP_NET_ADMIN`.
- [x] `cmd/hib`'s fake host keeps up. It runs `sudo -n hibd` with EUID 0 whether or not `sudoApp`
      is set and calls `sudoApp` only when it is, since every test that pauses or removes now goes
      through sudo (`cmd/hib/faces_test.go:186-187` among them). The overrides that match `rm` and
      `pause` (`cmd/hib/hib_test.go:752-753,779-782`) also match them behind `sudo -n hibd`, and the
      ones that look for `sudo` itself (`cmd/hib/secret_test.go:54-60`) still see it.
      `cmd/hib/rm_test.go:84` expects `sudo -n hibd 'rm' 'app' '--json'`, and the `ls` header test
      (`cmd/hib/hib_test.go:572`) gains `PORTS`. In the commit that changes the default, the fake
      host's environment sets `HIBD_LINK_PORT=0`, which wins over the file
      (`internal/hibd/hibd.go:1694-1702`), so the in-process `hibd` keeps links off. A test that
      wants them on sets it in `h.env`.
- [x] `cmd/hib/main.go`'s `ls` rendering gains the `PORTS` column (`cmd/hib/main.go:279`), and the
      body of `log` in `cmd/hib/secret.go` learns the `links` flag the contract already added to its
      signature. `hib log` without the flag is unchanged. Slice 4 registers the flag, slice 7 makes
      it do something and neither waits for the other.
- [x] Unit tests: both listeners up and down together, the socket path, the tmpfiles entry, the
      startup order, the control socket answering while the listener is off, the startup self-check
      refusing to serve when a protection is missing and starting the listener once a later check
      passes, the fork hint on hibd's stderr, `hib pause` and `hib rm` sending SSH commands that
      start with `sudo -n hibd`, the `ls` column, the link log printer, `snapshot` keeping the mark
      with links on and taking M11's path with links off.

### Slice 8: e2e, test targets and docs

Owns `e2e/machinelink_test.go` (new), `e2e/restart_test.go`, `e2e/hibd_test.go`, `Makefile`,
`docs/spec.md`, `docs/milestones/README.md` and this file. Starts when slice 7 has landed.
`README.md` belongs to slice 4, not here.

- [ ] `TestPauseAll` (`e2e/hibd_test.go:17`) runs `sudo -n hibd pause-all --timeout 2m`, because
      with links on `pauseAll` creates the quiescing marker in the root-owned `/run/hibernaut` and
      fences and closes like any other pause. It removes the marker in a `t.Cleanup`, as
      `e2e/quiescing_test.go:24-32` does, because a marker left behind fails every later `hib new`
      (`awake`, `internal/hibd/hibd.go:1590-1603`) and skips delivery.
- [ ] `TestMachineToMachine`, split deliberately: the fast subtests run under plain `make e2e`,
      and the ones that have to wait out a timeout run only under `make e2e-slow`
      (`Makefile:80-91`), because `e2e` doesn't set `HIB_E2E_SLOW`. The host-reboot case joins
      the existing slow guard in `e2e/restart_test.go` (`:25-27`), which this slice edits.
- [x] `Makefile`'s `test-host` and `test-host-remote` targets gain the new root test packages,
      `internal/statefile`'s `TestHost` test and `internal/linkguard`'s `TestGuard` tests
      (`Makefile:28-64`). `test-host` checks for `nft` and `conntrack` first, `test-host-remote`
      installs them on the host when they are missing and CI's `test-host` job installs them before
      it runs `make test-host`.
- [x] `docs/spec.md` and this document reconciled with what was actually built.
- [ ] Measure the hop's added round-trip latency on the host (warm p50, p95 and p99, with connection
      setup timed apart from a wake) and write the numbers into this file's Changes. No number goes
      into the spec.
- [ ] Tick the boxes and set the status here and in `docs/milestones/README.md`.

## Tests

Unit (no host):

- [ ] `internal/links`: allocation in order, never reused, the caps, the reserved ports,
      `Store.Update` under the lock, a corrupt file reported rather than silently emptied, an
      unknown version refused, `PortGen` rising on every change and the ownership and mode of both
      the file and the lock.
- [ ] `internal/links` batches: a whole batch validated and deduplicated before anything is written.
      A failed **first** publish leaving no entry at all. Publishing a port that is already
      published, and unpublishing one that is not, both no-ops. A batch that is entirely no-ops
      refused.
- [ ] `internal/links` fences: an **old fence with a live owner still refuses**, and so does an
      orphaned one. `Stale` only picks a fence for the reconcile and never lets one through.
- [ ] `internal/links` arithmetic: `Mark` and `Token` round-trip for 1, 4242 and `GuardMaxToken`,
      and `Mark(GuardMaxToken)` keeps `0x4c` in its top byte. `Token` refuses `Mark(0)`, a mark with
      another top byte and 0 itself. `2*GuardTTL + GuardMargin <= SlotReturnDelay`.
- [ ] `internal/links` state: a publish refused over the cap leaves no entry for the machine, and
      unpublishing a reserved port is `ErrReservedPort`.
- [ ] `internal/links` activity clock: `Touch` moves the mtime forward only, a first touch with a
      past time records that time, concurrent touches with out-of-order times leave the latest
      whether or not the file existed and leave no temporary file, a missing file is the zero time
      and an ID that is not one path element or that starts with a dot is ignored.
- [ ] `internal/linkadmit`: `Serve` refuses with `ErrNoCloser` until `SetCloser` has been called,
      and accepts a closer passed in the config.
- [ ] `internal/statefile`: the lock tests from the contract commit, plus new tests for what slice 4
      lifts, while the `internal/machines` tests stay where they are and keep passing. `Held` on a
      missing lock reports no holder and creates nothing.
- [ ] `internal/egress/hook`: the jump at position 1, the chain's rules in order, a foreign
      rule inserted above the jump repaired by `Keep`, a stale bare 443 rule deleted last, `Keep`
      silent when `iptables-save` renders `-m tcp`, an interrupted install repaired, `Remove`
      leaving no jump and no chain.
- [ ] `internal/slots`: `Slot` and `Addr`, over a fake tree, including a sandbox with no process
      and a slot with no netns. `Resolver.Addr` and `Resolver.At` failing with
      `links.ErrNotRunning` when the run changes between the two reads.
- [ ] `internal/linkguard`, over a fake nftables backend: `Open` create-only and `ErrGateSpent`,
      `Drop` re-reading until empty, `Drop` on a missing table failing while the token file exists
      and returning nil when it does not, `Tokens` writing the file before the first token and the
      Keeper's cycle, as slice 1 lists it.
- [ ] `internal/linkproxy`, all with a **fake clock and fake run events**, all running on macOS:
      every refusal reason and **zero bytes written on each**, the caps, half-close both ways, byte
      counts, renewal only on payload, a gate retired between the admission and the dial, the
      generation-changed race, a resume that lands the target on a **different slot** mid-link, an
      abort on the target's run ending and on the source's and a gate the fake Keeper retires as
      soon as a run ends. The socket-mark binding: a token that is not the gate's is
      `links.ErrWrongRun` and no token is `links.ErrUnmarked`. An unreadable `links.json` refuses
      new links with `links.ErrStateUnreadable`, and the open ones abort once the fake Keeper
      retires their gates. The aborts and `Stat` answer 0 and empty counts while the listener is not
      serving.
- [ ] `internal/linkadmit`: concurrent admissions for different targets never spend the reserve,
      and nor does a target bigger than the default, which is charged its size. Concurrent
      admissions for one target coalesce into one wake. A failed wake rolls the rate limit and all
      the room it was charged back together. `revoke` cuts an open link and one waiting mid-wake,
      and leaves links on the machine's **other** ports alone. **Cross-process** cancellation over
      the control socket. A stale source is refused with `ErrSourceIdle` and stops renewing. A
      self-link is refused. A **running** target still goes through `PausedByHand`. The run
      watcher fires on `populated 0` and on the directory disappearing, and `Live` lists only
      populated runs. The sweep catches a paused machine whose `StartedAt` never moved. An `idle=0`
      machine is observed by `busy` and still not renewed. `Admit` fills `TargetGate` from a fake
      Keeper after the wake. A `close` left unacknowledged past `LinkCloseGrace` reaches a fake
      `Dropper`. `Release` keeps the rate history. Over a closer that is not serving, the control
      socket answers `close` and `revoke` with 0 and `stat` with empty counts, and a closer's error
      comes back as `err`.
- [ ] `internal/hostsblock`: merge, remove, user lines kept, reserved names refused.
- [ ] `internal/machines`: `Wake` by ID with and without room, a wake **slower than the lease**
      being cancelled, the reconcile that brings a woken target's end back to `LeaseUntil`, the
      rollback that pauses it again when the lease has already passed, `Setup.Hash` with `Hosts`,
      that `save` and `fork` leave `PausedByHand` set across their internal resume, that a failed
      pause clears the fence and that the SSH heartbeat ticks for an `idle=0` machine where
      `keepalive` does not.
- [ ] `internal/audit`: link entries, `open` and `close` pairs, folding identical refusals,
      flushing a fold on shutdown, rotation, a source capped at `links.MaxFoldsPerSource` lines in
      a window with an overflow line counting the rest, another source in the same window
      unaffected and the cap ending with the window.
- [ ] `internal/hibd`: `port add|rm|ls` including every error and exit code, `pushSetup` called and
      its failure warned rather than fatal, `cleanup` dropping only the entries a `Get` confirms E2B
      has forgotten and reconciling a stale fence in each of its cases, `pause` setting the mark and
      sending `close`, the pause fallback failing closed with a fake `Dropper` that returns `EPERM`,
      `fork` writing its hint to stderr, `pauseAll` creating the quiescing marker. With links off,
      `pause`, `rm`, `pauseAll` and the leak scan touch no fence, send no `close`, never reach the
      `Dropper`, set no mark and create no marker, and `cleanup` leaves `links.json` alone and still
      prunes activity files. `hibd proxy` serves the control socket while the listener is off, so a
      pause goes through with a Keeper whose first `Install` failed, and it starts the listener once
      a check that failed passes. `linkRoom` charges a target its size in default machines and
      refuses one whose size it can't read. `hibd log --links` shows `-` for a line with no port.
- [ ] `cmd/hib`: the command table, help, **completion for `port add` and `port rm`**, every message
      in Design, `--json` shapes including the empty one, the old-hibd path, `hib pause` and
      `hib rm` sending SSH commands that start with `sudo -n hibd`, the `PORTS` column in the `ls`
      header, `-` for a link line with no port and the README check.

`make test-host` (Linux root, no E2B). `Makefile:28-40` runs these, among them
`internal/statefile`'s `TestHost` test and every `TestGuard` test in `internal/linkguard`. The
linkguard tests need `ip`, `iptables` and `nft`, and `TestGuardConntrackLossFailsClosed` needs
`conntrack` too (`newHostRig` and `underStop` in `internal/linkguard/guard_host_test.go`). They fail
rather than skip when one is missing. `make test-host` checks for `nft` and `conntrack` and names
the package to install, `make test-host-remote` installs `nftables` and `conntrack` on the host
when either is missing and CI's `test-host` job installs both first. `vm/install-e2b.sh` installs
neither: only the tests need them, and the script hashes itself into its `install_id`, so editing
it would make `make up` reinstall E2B.

- [ ] The chain is installed, kept and removed against real `iptables` in a private network
      namespace, including the migration from the bare 443 rule and the repair of an interrupted
      one.
- [ ] The listener accepts on a veth-named interface and refuses on another.
- [ ] The proxy survives a restart: the socket is unlinked and re-created, and links fail closed
      while it is down.
- [ ] A pause that gets no ack deletes every `(S . *)` element and `S`'s `link_src` entry, reads
      them back until none are left and only then pauses.
- [ ] `Drop` against a real table that has been deleted fails while the guard's token file exists
      and returns nil once it does not.
- [ ] **`TestGuardDropsAllFourWaysUnderStop`.** With a link carrying traffic, `SIGSTOP` the proxy
      and end the target's run. Assert that packets are dropped in **all four** directions (source
      leg in and out, target leg out and in) within `2*GuardTTL`, which is inside `SlotReturnDelay`,
      measured with the set's own counters and with a socket on each side. Then `SIGCONT` and assert
      the proxy cleans up.
- [ ] **The kernel facts the guard assumes**, each its own named case logging the host's kernel
      version, and all of which must pass before slice 7 may turn links on: the rest of the table
      in *The kernel's own fence*, 12. `TestGuardExpiredDeleteAbortsBatch` (deleting an expired
      element returns `ENOENT` and aborts the batch), `TestGuardDeleteAddResetsExpiry`,
      `TestGuardOpenIsCreateOnly`, `TestGuardDroppedGateStaysGone`, `TestGuardReopenAfterExpiry`,
      `TestGuardMapMissLeavesMarkZero`, `TestGuardAcceptTakesSynMark`,
      `TestGuardInputUsesSocketMark`, `TestGuardEgressKeepsMarkZero`,
      `TestGuardConntrackLossFailsClosed` and `TestGuardSetBackendIsRhash`.
- [ ] The guard's table installs beside `hibernaut-sandboxes` and E2B's `slot-firewall` without
      disturbing either, survives an E2B ruleset reload and `nft delete table inet
      hibernaut-links` removes every trace.
- [ ] A gate that stops being extended stops matching within `GuardTTL`, measured, and an
      extension delayed past the expiry **cannot** bring it back.
- [ ] A slot recycled to a new run does not inherit the old run's gate: a socket holding the old
      token is dropped even though the address is unchanged.
- [ ] `SO_LINGER 0` is actually set on both legs: an aborted link sends RST and the peer sees no
      further bytes.
- [ ] The run watcher sees a real cgroup empty and disappear.

End-to-end. The fast subtests go in **`make e2e`**. The ones that have to wait out a timeout go
behind `HIB_E2E_SLOW` and so run only under **`make e2e-slow`** (`Makefile:80-91`), because plain
`e2e` doesn't set it. The reboot case joins the existing slow guard at `e2e/restart_test.go:25-27`
rather than inventing a second one. The subtests below are named as they are in
`e2e/machinelink_test.go`.

- [ ] `TestMachineToMachine`, in `e2e`:
  - a published port is reachable from another machine by name.
  - an unpublished port on the same machine is refused.
  - a paused target is woken by a link and serves the request.
  - a machine paused by hand is not woken by a link, including while `save` has it temporarily
    running.
  - 8081 and 49983 are refused by `hib port add` and by the listener.
  - a self-link is refused.
  - with no room, the refusal is immediate and says so.
  - an unallocated `198.18.x` address is refused.
  - a port published on 443 is reached, not intercepted by the egress proxy.
  - `hib port rm` cuts a connection that is already open.
  - the hosts block holds only `.hib` names and the machine's own lines survive.
  - a hosts line rewritten to another machine's address reaches that machine, and one rewritten to
    an address nobody has is refused.
  - a fork inherits no ports.
  - `hib rm` closes the links and removes the entry.
  - `hib port rm` cuts a connection that is **waiting on a wake**, not only an established one.
  - with the guard's table removed by hand, the open link is cut, `hibd proxy` says the guard is
    down and puts it back, and a new link works once it has.
  - with the proxy stopped, a link fails closed rather than falling through to anything.
  - `latency` logs the numbers below.
- [ ] `TestMachineToMachineSlow`, in `e2e-slow`:
  - an idle open link doesn't hold a machine awake.
  - a **two-machine and a three-machine ring**, with retrying clients, SSH closed, no agents and CPU
    under the busy threshold, both stop within `LinkGrace` plus the longest idle timeout in the
    ring, and do **not** come back.
  - a source with an open SSH session keeps reaching its targets for as long as it is open, which is
    the case the rule above must not break.
  - a finite-idle machine pauses within the grace-plus-idle bound.
  - a target paused under an open link is cut off, and when it resumes after another machine has
    been made, possibly on another slot, neither the old link nor a new one reaches the wrong
    machine.
  - with a target left to pause on **E2B's own idle timeout** while traffic is flowing, no link byte
    reaches the target after its run ends: the cgroup event aborts the link first, the kernel guard
    drops anything left within `2*GuardTTL` at worst and nothing carries into the next run.
  - a link-woken target whose wake took longer than the source's lease is paused again rather than
    left running for a fresh idle.
  - an interrupted chain migration is repaired on the next `Keep`.
- [ ] `TestSurvivesHostRestart`, in `e2e-slow`: a host reboot leaves a machine's address and
      published ports intact, and the boot, which starts with an empty `/run`, brings the proxy
      up serving links with `/run/hibernaut` present and no quiescing marker. A link from a machine
      the reboot paused then wakes its target.
- [ ] `TestPauseAll` runs `hibd pause-all` through `sudo -n` and leaves no quiescing marker behind.
- [ ] `TestNoHostAccess` passes unchanged, with the link listener running.
- [ ] `TestMachineNetwork` passes unchanged.

Latency is **measured, not promised**. `TestMachineToMachine/latency` logs, from inside a machine,
p50, p95 and p99 for 20 connection setups (a connect, one line and its answer) and for 1000 warm
round trips over one link to `api.hib`. It logs the same against `127.0.0.1` inside the target, the
cost without a link, and apart from both how long a link takes to wake a paused machine and get its
answer. The numbers go in this file's Changes. No number is written into the spec.

## Done when

- [ ] `make lint test` is green. CI is parked, so it stands in for CI.
- [ ] `make test-host` is green.
- [ ] `make e2e` and `make e2e-slow` are green on the host.
- [ ] The README and the spec match what was built.

## Changes

Entries are in order, and where a later entry disagrees with an earlier one the later one holds.
Superseded statements are marked inline.

- 2026-10-09: Designed, from the published-ports decision. Three corrections to the proposal it
  started from. A second `hibernaut` iptables rule doesn't flap, it breaks the egress hook, because
  `internal/egress/hook`'s parser treats any unexpected `hibernaut` rule as an error, so M12
  rewrites the hook around one chain instead of adding a rule beside it. The listener belongs to
  `hibd proxy`, which already owns the hook, the slot finder and a logs directory, not to
  `hibd daemon`, which has none of them. Link bytes can't be an `internal/busy` signal, because
  `busy` polls cgroups and `pgrep`. They renew the target's timeout the way the SSH bridge does.
  Link records get their own log, because `audit.Entry`'s fields are request-shaped and
  `hib log --json` is a stable shape.
- 2026-10-09: Astra's design and security review: `hib port rm` now revokes open and mid-wake links
  through a control socket instead of only unpublishing. Link traffic can no longer hold machines
  awake for ever, because self-links are refused and a link renews its target only while its source
  has non-link activity. The chain is installed in one `iptables-restore --noflush` transaction
  rather than flushed and refilled. `links.json`'s ownership, mode and lock order are pinned down
  and the locking moves into a new `internal/statefile` below `hibd`. Admission is one atomic
  decision keyed on sandbox ID with coalesced wakes and a run identity. Pausing, saving and removing
  are recognised as other processes and cancel links across them. Links are refused before any other
  check while quiescing, and `pauseAll` now creates the marker that the `ExecStop` path was missing.
  The slices are re-cut so every file has exactly one owner and the contract commit updates its
  callers. The audit log gains an `open` entry and `hib port ls` an `OPEN` column, so a machine
  awake on a link can be explained while it is happening. Three claims that were simply wrong are
  fixed: `e2e/hostaccess_test.go`'s probe writes before it reads, so the rule is that a **refusal**
  sends nothing, not that the listener never speaks first. The egress proxy has no connection caps
  to inherit, so M12 defines its own. `hibapi` tables do print headers when they are empty. Astra's
  five decisions on the open questions are folded in: latency is measured and not promised,
  `hib log --links` stays, `hostsblock` and `envblock` share no code, the reserve is one `dev`
  machine with no knob and `hib port add` warns about a localhost-only service but never refuses.
- 2026-10-09: Astra's second review. The run identity stops being `(ID, StartedAt)` and becomes the
  E2B run cgroup `sbx-<id>-<run>`, watched as an **event**: `StartedAt` doesn't move when a machine
  pauses, and a five-second poll loses to E2B's three-second slot recycle, so an open link could
  have been handed a slot that already belonged to another machine. Aborts now come from the cgroup
  emptying or disappearing, from the control socket and from the sweep as a backstop that checks
  `Running` as well as the run. (This round also added a deadline at the earlier `EndAt`. The next
  round removed it.)
  Every link socket is created with `SO_LINGER 0`, because closing a socket without it leaves the
  kernel delivering queued bytes to whatever inherited the address. Hibernaut's own pauses fence,
  close and wait rather than pause past sockets the proxy may still hold. (This round answered a
  stalled proxy with a systemd watchdog and a restart. The next round replaced that with a kernel
  guard.)
  Waking, not only renewing, now requires the source to have its own non-link activity, and a
  link-woken target's timeout is clamped to the source's remaining lease: gating renewal alone let a
  ring pause and then be restarted by the first retry. Every connection is admitted, running targets
  included, so none skips the caps, revocation or a by-hand pause that `save` is briefly masking.
  The activity clock is no longer described as independent or unforgeable. What M12 owes instead is
  the narrower invariant that links add no host runtime beyond what `busy` and SSH already cause,
  and its four feeds are pinned down, including an SSH heartbeat that doesn't ride `keepalive`
  (which never ticks at `idle=0`) and `busy` observing `idle=0` machines without renewing them.
  Generations are per port, so revoking one port leaves links on the others alone. `hibapi.Ports`
  uses `omitzero`, because `omitempty` would drop the empty slice that distinguishes "allocated,
  nothing published" from "absent". The seam is widened for parallel work: `Ingress`, `Dialer` and
  `Runs` are injected interfaces, so the listener builds and its tests run on macOS where the whole
  hook package is a stub. The contract carries callable stubs for every cross-slice call.
  `LinkPort=0` keeps links off until integration. `rm` and `cleanup` move to the slice that owns
  `hibd.go`, the laptop's `ls` rendering and `snapshot.go`'s internal pause to the integration slice
  and the `/etc/environment` CAS loop to the hosts slice. A `tmpfiles.d` entry ships
  `/run/hibernaut`, without which `ReadWritePaths` fails the unit with `226/NAMESPACE` on a cold
  boot, and the fail-closed firewall pieces land with or before the commit that turns links on
  rather than after it. Messages no longer imply reachability or a delivery deadline. Nothing in the
  review was rejected: every citation was checked against the code first.
- 2026-10-09: Astra's final check, and the contract commit that follows this one. The ABI stops
  being prose: `Admission` had been written out in four places and the constants in two, so
  `internal/links` is now the single source of truth and this document points at it instead of
  restating it. `Admission` carries an absolute `LeaseUntil` and a **per-port** generation. Against
  a stalled `hibd proxy`, whose kernel sockets go on sending while none of its handlers run, the
  guard moves into the kernel: an nftables set of live slot addresses (superseded by round 5) whose
  elements expire after a second unless the listener re-proves the run is live (superseded by round
  6), with drops in both directions (superseded by round 5) for anything else. nftables checks an
  element's expiry at lookup, before any collection, so it fails closed with no process involved,
  and it beats E2B's three-second slot return by a wide margin. The `EndAt`-minus-margin deadline is
  gone, and with it the claim that `EndAt` only grows: `machines.Renew` writes back a stale value
  (`internal/machines/machines.go:589-594`) and E2B's API shortens timeouts
  (`sandbox_timeout.go:52`), so the claim was false as well as unnecessary. The pause fallback
  deletes the machine's elements from the set rather than restarting the proxy, and the watchdog is
  demoted to recovery. A link-woken target is held to an absolute lease rather than a relative
  clamp, because E2B computes a resumed machine's end from a start time taken after placement
  (`create_instance.go:440-443`), so a slow wake could outlive the lease that justified it. The
  pause fence becomes a persisted field checked for **both** endpoints, raised by every pause
  Hibernaut starts (including the leak scan and `snapshot.go`'s internal one), cleared when the
  pause settles either way and ignored once stale (superseded by round 5). Ownership gaps closed:
  the guard to slice 1, the busy wiring and the leak scan to slice 6, the fork activity touch to
  slice 3, `statefile` to slice 4 and `log`'s body and `setup-host.sh` to slice 7, which is also the
  only slice allowed to change `hibd.go`'s link-port default. On a fresh host `azure/up.sh` deploys
  before `install-e2b.sh`, so ordering files isn't enough: `hibd proxy` checks at startup that the
  guard, the firewall drops and `IPAddressDeny` are live and keeps links off until they are. Nothing
  in the review was rejected.
- 2026-10-09: Astra's round-5 check. **The guard is redesigned around per-run tokens**, because
  keying it on a slot address alone was wrong in three ways: the next occupant of a slot inherited
  the gate, a target's replies carried `ct mark 0` and matched no drop at all (including under
  `SIGSTOP`) and the first packet of a connection had to pass before admission could decide anything
  about it. Each run now gets a never-reused 16-bit token (superseded by round 6) from a counter
  under `/run`. `link_live` is keyed `ipv4_addr . mark`. A new `link_src` map gives a new connection
  its source run's token in `prerouting`, and a miss leaves `ct mark` 0 so the SYN is dropped
  (superseded by round 6). The output rule copies the mark onto the conntrack entry so replies carry
  it (superseded by round 6). The drop runs in all four directions. At admission the proxy reads the
  accepted connection's `ct mark` over **ctnetlink** from its original tuple and refuses anything
  that is not the current token of the run at that slot, which closes the stale accept queue
  (superseded by round 6). The target's gate is opened before the dial, the dial carries the target
  run's token and both runs are re-checked after the dial and before the first byte. Refreshing
  becomes **extend-only**: one serialized refresher (superseded by round 6), one atomic batch per
  cycle, each element deleted and re-added (adding alone does **not** reset an nftables element's
  timeout, which an earlier draft claimed it did) and `ENOENT` on a vanished element aborts the
  batch, so a late refresh can never resurrect a gate. The bound is now
  `2*GuardTTL + GuardMargin <= SlotReturnDelay`, with a contract test on the arithmetic. Seven
  kernel facts (superseded by round 6) the design rests on become named `test-host` gates that must
  pass before links may be turned on, instead of being argued here.
  **The fence stops trusting a clock.** Ignoring a fence older than two minutes was wrong: `save`
  legitimately runs for ten (`internal/machines/snapshot.go:17-18`). A fence now records an owner (a
  lock file held for the whole operation) and holds while that owner lives, whatever its age. An
  orphaned fence still refuses until the machine's state has been reconciled, rather than being
  ignored. `internal/statefile.Held` is the check.
  **The lease is re-derived rather than frozen.** An immutable `Admission.LeaseUntil` would have
  killed a healthy link after `LinkGrace` even while its source was plainly working. Every renewal
  and wake decision now recomputes it from the activity clock, and the lease by itself never
  closes an established link: it stops the renewal and the target pauses on its own timeout.
  **The contract commit grows** to cover the gaps the slices would otherwise have raced on: the
  `hibd port` handler and its dispatch entry, `HIBD_LINK_PORT` and `HIBD_LINK_LOG`, one `Store` and
  one production `Clock` every slice goes through, an injected `Resolver`, a late-binding
  `SetCloser` that makes a missing listener a startup failure, `Stats` counting per published port
  so `hibapi.PublishedPort.Open` has a source and `internal/statefile` pre-extracted from
  `internal/hibd/lock.go` so slices 3, 4 and 6 no longer cross `machines.go:689` and `hibd.go:1335`.
  Batches are specified and tested as validate-then-apply, with already-published and already-absent
  ports as no-ops and a failed first publish leaving no entry. `CtlTimeout` moves to 7 s so it
  outlasts `LinkCloseGrace`, and `Release` keeps a successful wake's rate history. This document
  stops restating Go types, constants and JSON and names the identifier and its file instead, which
  fixed a drifted socket path, a drifted control reply, a drifted audit record and the stale wording
  around `rm`, the relative wake and "before pause".
  **One thing was rejected, in part.** The ownership helpers were not lifted into
  `internal/statefile` in the contract commit. They are `*Manager` methods over the manager's own
  directory and uid, and their tests are root-only host tests that cannot run from this branch, so a
  "no behaviour change" extraction could not be verified here. The locks (which is what the two
  cited call sites needed) were lifted, and the ownership half goes to slice 4 along with
  `internal/machines/ownership.go` and `statedir.go`, which keeps file ownership disjoint.
- 2026-10-09: Astra's round-6 check. **The guard stops reading conntrack (S1).** A conntrack entry
  lost to `conntrack -D`, table pressure or a module reload is rebuilt mid-stream with `ct mark 0`,
  because `nf_conntrack_tcp_loose` is on by default, and the source side's `output` drop keyed on a
  ct mark with the link prefix, so it skipped every packet. A lookup by tuple could also read a
  newer entry stamped with another run's token. Identity now comes from marks the kernel keeps on
  the socket, which nothing outside the owning process can change. `Install` sets
  `net.ipv4.tcp_fwmark_accept`, so the accepted socket takes the SYN's mark from `link_src`. The
  dialled socket gets `SO_MARK` from the target's gate. `input` checks the receiving socket's mark
  and `output` the mark the socket put on the packet. The listener reads the token with `SO_MARK`,
  compares it with the Keeper's gate for the source's current run and refuses a socket with no link
  mark (`links.ErrUnmarked`). `links.Marks`, `linkguard.Marks`, `ErrNoConntrack` and
  `GuardListenerToken` are gone.
  **Gates are created once and the Keeper owns them (S2, G6, G9).** `Guard.Open` could recreate a
  pair after the `hibd` fallback had dropped it and read it back, which re-enabled old sockets. It
  now creates with `NLM_F_EXCL` and refuses a token at or below the highest it has sent
  (`links.ErrGateSpent`). `Extend` never creates, `Drop` re-reads until the address is empty and a
  false from `Has` stays false. One `linkguard.Keeper` in `hibd proxy` opens, extends and retires
  every gate: it extends first, retires what is no longer live and unfenced and then opens the
  missing ones, dropping before it opens and re-checking after. That settles who owns the refresher,
  which the slices and `linkproxy.go` disagreed on, and gives the listener and the admitter one API
  in `links.Keeper` (`Ready`, `Gate` and `Retired`). Aborting through `Retired` is cleanup, because
  the kernel drops a retired gate's sockets once it expires.
  **Tokens are 24 bits and fail closed (G1).** A 16-bit counter runs out in under a day at one new
  gate a second, and restarting it would hand out tokens again. `GuardMark` shrinks to the top byte,
  and a host that runs out keeps the gates it has and opens none until it reboots, which at that
  rate takes about 194 days.
  **`hib pause` and `hib rm` run `hibd` as root.** The fallback's `Drop` needs `CAP_NET_ADMIN`, so
  both now go through `sudo -n`, like `fork` and `status`, and a `hibd` without it fails closed
  rather than pausing past the proxy. Rejected: waiting out `2*GuardTTL`, because that depends on
  the proxy's Keeper honouring the fence. Also rejected: a sudo re-exec helper and a daemon control
  socket, which `sudo -n` already makes unnecessary.
  **The rest of the seam is frozen.** `slots.Finder.Slot`, `slots.Addr` and a production
  `slots.Resolver` that reads the run on both sides of the slot lookup give the Keeper a run's
  address (G8). `hibd proxy` builds the links side in one written order and shuts it down in the
  other (G3). The listener's steps used to dial before any gate was open. They now bind the
  connection to the source's run, admit (which waits for the target's gate), dial with that gate's
  mark and re-check both runs, `PortGen` and both gates before the first byte, carried by
  `Peer.Token`, `AdmitRequest`'s source run and gate and `Admission`'s two gates in place of its
  addresses (G4, G5).
  **Smaller fixes.** `Publish` checks the cap before it creates an entry and `Unpublish` refuses
  reserved ports too (B1, B2), and the doc gives the address-pool check to `Allocate`, which is
  where it is (B3). `Wake` takes an absolute lease (L1). `Admission.Ctx` is never cancelled by the
  lease (L2). `Release` gives back room and caps only. The doc now says who runs `hibd` as whom,
  that the fork hint comes from `hibd`'s stderr, that the activity clock is one file per machine
  under `links/activity`, the audit signatures as built, `links.ErrStateUnreadable`, `Store.Update`,
  the default `idle=15m` and that the e2e asserts no link byte after a run ends rather than an abort
  before an autonomous pause. The files the contract finished and no slice owns are named, and so
  are slice 8's milestones README and restart test. With `pause` and `rm` now root, the doc also
  closes a gap the ownership rules already implied: `statefile.OpenLock` and `atomicfile` do not
  chown, so slice 4 creates `links.lock` and the fence locks the way `openLock` creates
  `hibd.lock`, writes `links.json` with `publishOwned` and makes `Held` stop creating files.
  Otherwise a lock the daemon created first would be root's and `hibd port` could never take it.
- 2026-10-09: Astra's round-7 check. Security found nothing. Design found three contradictions and
  two loose ends, and fixing them turned up two more.
  **The lease is re-derived in the code too (MF1).** `linkadmit.Renew`'s comment still renewed to
  `Admission.LeaseUntil`. It now recomputes the lease from the clock as the doc does, a stale source
  stops renewing, the target pauses on its own timeout and link bytes never feed the clock.
  **Unreadable state shuts open links as well (MF2).** A file that can't be read can't say which
  machines are fenced, so leaving open links alone let a pause go ahead past gates the Keeper was
  still extending. The Keeper now counts every machine as fenced and retires every gate, the
  listener aborts the links that hold them and links come back once the file reads cleanly.
  **The activity clock only moves forward (MF3).** The busy loop and the SSH heartbeat touch it from
  different processes with their own times, so an older touch could land last and pull a machine's
  activity back. `Touch` now takes an `flock` on the machine's file, reads the mtime with `fstat`
  and calls `futimens` only for a later time, and the in-memory clock does the same. It still never
  waits on `links.lock`.
  **The contract lists what each stub returns (SF1)** instead of claiming every body returns
  `links.ErrLinksOff`. **Test files have owners (SF2):** a slice owns the test files of the files it
  owns, and slices 7 and 8 name the three others they change.
  **With links off, every pause is M11's (X1).** The doc never said so, and without it slices 1 to 6
  could not ship dark. `pause`, `rm`, `pauseAll`, the leak scan and the internal pause of `save` and
  `fork` raise no fence, send no `close` and never call `Drop`, and `rm` and `cleanup` leave
  `links.json` alone.
  **A proxy without its listener no longer fails every pause (X2).** A proxy whose first `Install`
  failed never served the control socket, so every pause fell back to `Drop`, which fails when the
  table is missing, and the shutdown `pause-all` could pause nothing. Now the control socket is
  served whenever links are configured on, and a listener that is not serving answers `close` and
  `revoke` with nothing closed, which is true because it holds no sockets. `hibd-pause-all.service`
  is ordered after `hibd-proxy.service`, so at shutdown `pause-all` runs while the proxy still
  answers. `Drop` stays strict on a missing table except when there is no token file, because then
  no gate and no link socket can have existed this boot (X2b). `Tokens.Next` writes the counter
  before it returns a token, which is what makes that hold. The proxy re-runs its startup checks
  once a minute and starts the listener when they pass, so a host whose E2B pieces arrive after the
  proxy gets links without a restart. `hib port` no longer claims to say why links are off, because
  nothing carries the reason to it. The proxy logs it.
  **Smaller fixes.** Slice 7's fake host runs `sudo -n hibd` as root without a `sudoApp` and sets
  `HIBD_LINK_PORT=0`. Slice 8's `TestPauseAll` runs through `sudo -n` and removes its marker. Slice
  4's lift keeps thin wrappers in `internal/machines`, so slice 3's `machines.go` and the unowned
  `saves.go` and `lock.go` stay untouched. `Guard` gains an unexported `tokenFile` so a test can
  point `Drop` at its own, and the control socket replaces one a killed proxy left behind.
- 2026-10-09: Astra's round-8 check. Security found nothing, and design confirmed the round-7 fixes.
  **A first touch records its own time (MF3 again).** Creating the activity file set its mtime to
  now, so a late first observation became fresh activity, which the in-memory clock never did.
  `Touch` now creates a temporary file with its mtime already at `t` and publishes it with `link`,
  which never replaces a file, and takes the locked update when another touch got there first. The
  `Clock.Touch` comment says so, and `cleanup` removes the temporary files a crashed `Touch` left
  behind.
- 2026-10-09: Slices 2 to 6 landed, and this records what they settled. **A close aborts.** The
  listener answers `close` and `revoke` by resetting every link in scope, and half-close and the
  drain belong only to a link that ends on its own. `LinkCloseGrace` is how long the admitter waits
  for that answer before it drops the gates itself. **Renewal is asked per pair.** A window per
  target let a stale source spend it, because the admitter refuses a stale source without renewing,
  and a fresh source's renewal of the same target then waited out the window. The proxy now asks at
  most once per `LinkRenew` for each source and target pair. **Cleanup reconciles orphaned fences**
  after asking E2B about the machine, and prunes a machine only once a `Get` confirms E2B has
  forgotten it, because the listing can miss a machine paused between its pages. **`HIBD_LINK_PORT`
  is read one way.** The slices parsed it three ways, so a pause could have read links as off while
  the listener served. A malformed value now fails every `hibd` command. **Create and fork get the
  hosts block in slice 7**, which may now touch `new` in `hibd.go` for it. **Smaller corrections.**
  The activity clock calls `futimes`, which keeps microseconds and rounds up, not `futimens`. A
  failed push prints `pushSetup`'s shared warning, which names the step that failed and why, and a
  batch past the port cap has its own message. `OPEN` is null on every port or on none, so the
  example no longer mixes a count with `-`. The listener's `Logf` and the sandbox IDs in the link
  log are written down, and `hibd log --links` maps the IDs to names in slice 7.
- 2026-10-09: Slice 1 landed, and this records what it settled. **The guard speaks nfnetlink
  itself**, over `golang.org/x/sys/unix` with one socket per operation, so `go.mod` gains no module.
  **With links off the hook is M11's rule.** `Ensure` keeps the bare 443 rule and installs no chain,
  and turning links off again migrates back in one transaction. `iptables-restore` waits up to 5
  seconds for the xtables lock. **A rule the hook cannot delete is logged, not fatal.** When a
  transaction fails, `Ensure` tries again without the owned rules whose shape it does not know, and
  returns `hook.ErrStrayRule` when one of them stays. The proxy logs it and keeps repairing. It logs
  it again only when the message changes. With links off an unwanted chain such a rule jumps to is
  emptied and kept, since the kernel will not delete a chain a rule still jumps to. `Remove` does
  the same, and a clean stop of `hibd proxy` logs the rule and exits 0. Making `hibd proxy -unhook`
  warn about it is slice 7's. **The token file holds the next token**, and `Tokens.Next` writes the
  one after before it returns the one it read. **An unreadable `links.json` fences every machine**
  with plain `ErrFenced`, and a failed `Runs.Live` retires every gate. A cycle that fails opens no
  gate, and the gates a cycle opens are checked with one more read of the runs and the fences.
  `Drop` gives up after 8 read-backs, and `NewKeeper` refuses a `Keep` that is not below `GuardTTL`.
  **A batch that runs out of memory ends at its one ack.** On ENOMEM the kernel drops the acks it
  had queued and sends one against the first message, so `batch` stops there rather than waiting out
  its timeout, and a read that times out after an error has arrived returns that error. A failed
  commit is reported as the commit's and not as its first message's. The host tests keep their
  counters in a table of their own, `inet hlgtest`, stand in for E2B's firewall in the test's host
  namespace and stop a helper process in the proxy's place with `SIGSTOP`. They count `EPERM` or a
  timeout as refused. They need `ip`, `nft` and `iptables`, and one of them needs `conntrack`. None
  of them has run yet, so no box in this slice's list is ticked. The `Makefile` targets that run
  them are slice 8's.
- 2026-10-09: The component security review of `528850b..d405126` found nothing at confidence 7 or
  above. It confirmed both design-review fixes in the code. Tokens exist only as socket marks, so
  losing a conntrack entry refuses the link rather than opening it, and `Guard.Open` refuses a token
  at or below the highest it has seen, so a gate opened again always gets a new one. Its one low
  finding, a pause losing to a wake that is already resuming the machine, is recorded as a residual
  under Security item 5.
- 2026-10-09: Slice 7 landed without its last commit, the one that turns links on, and this
  records what it settled. **The watchdog has a loop of its own.** `hibd proxy` pings systemd every
  third of `WATCHDOG_USEC` while every serve goroutine runs and, with links on, while the control
  socket answers a `stat` (`internal/hibd/watchdog.go`). The doc had the sweep ping it, and with
  links off nothing sweeps. **A save or a fork waits at most 30 s for its fence**
  (`snapshotFenceWait` in `internal/hibd/snapshotfence.go`), to raise it and close the machine's
  links. A pause or `rm` of the same machine holds the fence while it waits for `hibd.lock`, which
  the save holds, so neither would ever go on. Past 30 s the save leaves the machine as it was and
  says why. **The link port is bound only once every check passes**, and the control socket
  answering a `stat` is one of them, beside `Ready`, the firewall drops and `IPAddressDeny`. The
  socket is how a pause closes a machine's links, so links are not served before it answers.
  **`hibd new` and `hibd fork` warn and start the machine without the names** when the hosts block
  can't be built, as the daemon does, because its next pass delivers them. **The reserve is counted
  in default machines.** The admitter's `Room` is the number of `images.Default` machines that fit,
  less one (`linkRoom`, `internal/hibd/linkside.go:152-177`), so a link that wakes a machine bigger
  than the default can use up part of the reserve. Security item 4 and Limits record it as a
  limitation. The 2026-10-10 security review, below, fixed it.
  **`hib log --links` has its own too-old message.** It checks for `FeaturePorts`, because a `hibd`
  without the port command keeps no link log, and the shared message names `hib port`.
  **What it added.** `machines.Config.Fenced`, which `hibd` sets only with links on. The new files
  `internal/hibd/linkside.go`, `linkchecks.go`, `watchdog.go`, `snapshotfence.go` and
  `newhosts.go`, and `vm/systemd/links_test.go`. `vm/systemd/hibernaut-tmpfiles.conf`, which
  `vm/setup-host.sh` installs as `/usr/lib/tmpfiles.d/hibernaut.conf`. `CPUWeight=200` in
  `hibd-proxy.service`. `hibd proxy -unhook` warns about a stray rule and exits 0, which slice 1
  left to it. **The formats.** `hibd log -links` and `hib log --links` print the columns
  `TIME MACHINE TO PORT RESULT WOKE BYTES`, and `--json` prints `{"v":1,"links":[...],"corrupt":N}`,
  leaving `corrupt` out when it is 0. `hib ls` has a `PORTS` column between `IDLE` and `PAUSES IN`,
  the published ports joined with commas, or `-` when a machine publishes none or `hibd` has links
  off. **The tests.** `internal/hibd`'s `newApp` and `newLeakscanApp` set `HIBD_LINK_PORT=0`, so
  their tests keep links off under the new default. The commit that turns links on sets it in
  `cmd/hib`'s fake host too, changes the unset row of `TestLoadConfigReadsTheLinkPortOneWay` to
  expect links on and makes `links.Port` the default, with a comment that says so. **The
  rollout.** Slice 7 changed two files `vm/install-e2b.sh` hashes into its `install_id`, so the
  first `make up` of M12 runs the E2B install again and links stay off until it has (*Startup and
  shutdown*).
- 2026-10-09: Slice 8 wrote the end-to-end tests and the test targets, and reconciled this file and
  the spec with the code. **Nothing that needs the host has run.** `TestMachineToMachine`,
  `TestMachineToMachineSlow`, the reboot case and `TestPauseAll` through `sudo -n` compile under
  `GOOS=linux go vet -tags e2e,host ./...` and have never run, and neither have the
  `internal/statefile` and `internal/linkguard` root tests that `make test-host` and
  `make test-host-remote` now run. Their boxes stay open. **The latency numbers are not taken yet.**
  To take them, run `make e2e` on the host with links on and copy what
  `TestMachineToMachine/latency` logs: p50, p95 and p99 of 20 connection setups and of 1000 warm
  round trips from one machine to `api.hib:8080` over a link, the same inside `api` against
  `127.0.0.1`, which is the cost without a link, and the seconds a link takes to wake a paused
  machine and get its answer. The hop adds the difference between the first two. The numbers go here
  and never in the spec. **The timeouts grow.** `make e2e` runs for up to 70 minutes and
  `make e2e-slow` for up to 270, up from 40 and 180, because `TestMachineToMachine` adds about 30
  minutes and the slow subtests about 60, waiting out idle timeouts and link leases. **Where the
  tests differ from the list in Tests, and why.** Two subtests have to land an event inside a wake
  whose length only the host knows: `hib port rm` while a link waits on a wake, and a lease that
  ends during a wake. Each guesses when, adjusts after every try and gives up after 6 tries with
  `t.Skip`, which says the run couldn't tell rather than that it passed. The lease subtest and the
  no-byte subtest make a source stale by setting its activity file's mtime back with `touch -m`,
  rather than waiting out `LinkGrace` on every try. The target whose wake outlives the lease has an
  idle timeout of an hour, so being paused again within two minutes can't be E2B's idle timeout. The
  no-byte subtest counts the lines the target's server logged before and after its resume, since a
  packet capture inside E2B's network isn't open to the test. A target resumed after another machine
  has been made may or may not get a new slot, which E2B picks, so the subtest checks that nothing
  reaches the wrong machine and not that the slot changed. While `save` runs a machine paused by
  hand, links to it are refused as paused by hand or as a pause in progress, because the save holds
  the machine's fence for the whole round trip. The guard's table removed by hand cuts the open link
  and `hibd proxy` puts the table back by itself, so the subtest checks the cut and the repair, and
  the refusal to serve without protections is the startup check's, which unit tests cover. A fork's
  copy publishes nothing and so has no address, which makes its no-reply check trivial, and the
  subtest's weight is on `hib port ls` and the hint. The reboot case covers the cold boot too,
  because `/run` is empty after any boot. The two own-activity subtests run in parallel to save an
  idle cycle. The rewritten hosts line, which Security item 12 names, and the latency subtest are
  additions to the list. **`hib rm` pushes nothing.** Security item 18 had `hib rm` push the hosts
  block. `hibd rm` delivers no setup, so the line goes at the daemon's next delivery pass, about 15
  seconds later, and until then it points at an address that is never reused, which is refused. The
  subtest waits for that pass.
- 2026-10-10: The delta security review of M12's integration found one issue and two hardening
  items, and all three are fixed. **The reserve holds whatever size a link wakes.** The admitter
  counted room in default machines and charged every wake one, so waking a `large` target passed
  when two `medium` ones fit and left the owner nothing. `linkadmit.Config.Room` now takes the
  target and answers how many default machines may run after the reserve and how many the target
  takes, its memory over a `dev` machine's rounded up. A wake is refused unless its charge fits
  beside the wakes in flight, and it holds the charge until it ends. `hibd`'s `linkRoom` reads the
  size with `Get`, which reports `MemoryMB` for a paused machine, and a target whose size it can't
  read is refused. Tests: `TestABigTargetNeverSpendsTheReserve`,
  `TestAFailedWakeRollsBackTheRateAndTheRoom` for a wake that needed 2 and
  `TestLinkRoomChargesATargetItsSize`. **The firewall drops everything machines send to the link
  range.** It dropped only UDP and ICMP, so other protocols and any TCP the redirect missed, which
  NAT skips when conntrack marks it invalid, were forwarded out of the host.
  `TestSandboxFirewallDropsTheLinkRange` checks the new rule, and the proxy's startup check, which
  already accepted a drop with no protocol, still accepts the UDP and ICMP pair. **A machine can't
  flood the link log.** The accept rate limit's own refusals and the ones made before it are
  written at whatever rate the guest connects, most with the port it dialled, so a scan at line
  rate wrote a line and held a timer per port in each window, enough to rotate the owner's history
  away in seconds. A source now has at most `links.MaxFoldsPerSource` (64) refusals folding at
  once, and the rest fold into one line per reason with no target and port 0, which
  `hib log --links` shows as `-`. Tests: `TestLinkRefusalsFromOneSourceAreCapped`,
  `TestLinkRefusalCapEndsWithTheWindow` and `TestLogLinksShowsNoPortAsADash` in `cmd/hib` and
  `internal/hibd`.
- 2026-10-10: The code review of `528850b...98d6914` checked the code against the coding standards
  and against this doc. **It found one gap against this doc.** Security item 6 promised a test that
  egress serves while the link cap is saturated, and none existed.
  `TestProxyServesEgressWhileTheLinkCapIsSaturated` now checks it. **What changed for the
  standards.** Comments say what the code does rather than how it was built. `hib` and `hibd` share
  `hibapi.FormatPorts`, `ParsePorts`, `PrintPorts` and `PrintLinkEntries`, so the ports table and
  the link-log table print `-` for an empty field in both. The token counter is written with
  `atomicfile`, which also syncs the directory. The listener no longer sets a refusal's `Count`,
  which the link log always reset. `linkadmit.New` refuses a configuration missing any of `Engine`,
  `Runs`, `Keeper`, `Store` or `Clock`, and the admitter no longer answers `links.ErrLinksOff`
  itself, because `hibd` builds no link side with links off. `Admit`, `Renew` and `Release` hold
  their own bodies. `hibd pause` and `hibd rm` go through `byName` with links on too. `links` keeps
  one list of the errors that cross as text, and `linkctl` now hands back every error on it, where
  it named 14. `links.Reserved` maps each port to what holds it, and `hibd`'s refusal reads from it.
  `hostsblock` checks names with `hibapi.CheckName`. The secret log and the link log share one
  JSON-lines writer and reader, `internal/audit/jsonl.go`. **A late wake's pause doesn't close links
  first.** This doc said in three places that a target whose wake outlived its lease is paused again
  through the fenced path, which closes the machine's links before it pauses. `pauseAgain` raises
  the fence and pauses. The code is right, because no link can reach a machine while its wake is in
  flight, so Security item 4b, the design and the slice 3 task now say what it does. **The slice 8
  merge ranges over the sorted keys of `links.Reserved`** in the e2e tests, since it became a map.
  **Left for later.** The state booleans in `linkproxy`'s `link`, the Keeper's `over` and the
  sweep's `verdict`. `reserve`'s five return values. The overlap between `recheck` and `ctxReason`
  in `linkproxy`. One type for sandbox IDs. Typed names for the link log's events. The `vm/systemd`
  tests that pin lines of the unit files.
