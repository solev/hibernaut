# M6 Golden images and fork

Status: **in progress** · Spec: §2 (Golden images), §5 (Images and fork, Room on the host), §9
(S3) · Research: [e2b-images-and-fork](../research/e2b-images-and-fork.md)

**Goal:** save a set-up machine as an image and start new machines from it in about a second, or
clone a machine N times. Copies keep files, installed packages and running processes, and get
their own name, host key, current secrets setup and idle policy.

## Design

The M6 code is built against this section. Code that has to differ updates it in the same commit.

**What E2B gives us** (live on our host and in E2B's source, both at E2B 5cfcf3282ddb):

- A named snapshot (`POST /sandboxes/{id}/snapshots` with a name) saves a running machine as a
  template, in 0.4 to 2 s for 2 GB. A paused machine gets 404. A machine created from the
  template gets only the labels passed to its create, and the origin's CPUs, memory and disk.
  Saving again to the same name adds a build.
- Native fork copies every label, `hib.name` included. E2B has no call to change labels, and it
  refuses to fork a paused machine. We don't use it.
- `GET /templates` hides saved templates. `GET /snapshots` lists them, at most 100 a page, with
  the next token in a header, and `GET /templates/aliases/{alias}` finds one. Names match
  `^[a-z0-9-_]+$`, and saved templates carry no labels.
- `DELETE /templates/{id}` returns 400 while a running or paused machine uses the template. The
  delete is soft, and `hibd cleanup` frees the files after its one-hour window.
- Save and fork share E2B's checkpoint, which failed once in about 21 tries on our host. The API
  returned 500 and left the machine paused, and resuming rolled it back to its last good
  snapshot, losing about 9 s. When a machine's first checkpoint fails, there is nothing to roll
  back to. No upstream fix.
- A pause returns before its snapshot is on disk. A save starts the new Firecracker before it
  stops the old one, so it holds the machine's memory twice for a moment. A failed save can leave
  its alias behind.
- Copies don't share randomness, because vmgenid reseeds the kernel RNG. Removing the host key
  pair and running machine-init re-keys a live copy, and sshd serves the new key at once.

**Names:**

- Three namespaces, so a saved image can never take over a built-in one. Built-in images are
  `hib-<name>` as now, saved images `hibimg-<name>`, and hidden fork templates
  `hibfork-<origin ID>-<unix ms>`.
- `images.Template(name)` resolves a name. A catalog name gives `hib-<name>`, any other name
  `hibimg-<name>`. Create, room checks, `image ls` and the leak scan all use it. `image rm`
  always means the saved one.
- Saved names follow `hibapi.CheckName`. Saving refuses catalog names and saved names that exist.
- If a later catalog adds a name someone saved, the catalog wins. `hib image ls` shows the saved
  one as shadowed, and `hib image rm` can still remove it.

**How a save and a fork work:** `hib image save web myimg` saves `web` as template
`hibimg-myimg`. `hib fork web --count 3` saves `web` as a hidden `hibfork-` template, then
creates three ordinary machines from it, each with its own labels. Storage costs the same as a
native fork, because E2B shares the build with the origin's own snapshot.

**The guard around E2B's checkpoint.** Promise: a failed save or fork costs the origin at most
what it did since the save began, and `hibd` never claims a state it hasn't read.

1. Refuse while the host is going to sleep, or if `Config.Durable` isn't wired.
2. Take the state lock, and hold it to the end.
3. Look the origin up, and remember whether it was running.
4. Check room for the whole operation (Room, below).
5. Make a snapshot to fall back to, and make sure it's on disk. A running origin has its timeout
   renewed, is paused, then passed to `Durable`. A paused one only goes to `Durable`, because its
   last pause may still be landing. `Durable` waits until the origin's newest snapshot build is
   ready and its files are complete. `hibd` wires it with `leakscan.SnapshotBuild` and
   `leakscan.WaitBuild`, with a 2 minute limit.
6. Resume it, and renew its timeout so E2B's idle pause can't land mid-save.
7. Check again that the host isn't going to sleep, then save into the new alias.
8. On failure, delete the new alias if it exists, and never retry automatically.
   - `ErrCheckpoint`: the origin is paused at the snapshot from step 5. If it was running,
     resume it, and say "E2B couldn't save web (its checkpoint failed). web is running again as
     it was when the save began. Nothing was saved. Try again."
   - Any other error: the outcome is unknown. Read the origin's state and report it as read.

   Cleanup and restore run on `context.WithoutCancel` with a timeout, so Ctrl-C can't strand the
   origin.
9. Put the origin back. If it was paused, pause it again. A forked origin is paused again before
   the copies start.

If the guard itself fails, stop with nothing saved:

- The pause fails: report the origin's state as read. E2B kills a machine whose pause can't be
  written, as it does for any pause.
- `Durable` fails or times out: resume the origin if it was running, and say `hibd` couldn't
  confirm its snapshot reached disk.
- The resume fails: "web is paused at a safe snapshot. Wake it with: ssh web.hib".

It costs one pause, a wait for its snapshot to land, and a resume: a few seconds for 2 GB. SSH
sessions into the origin may drop. `hib image save -h` and `hib fork -h` say so.

**Room:** `internal/room` gains `Plan`, what an operation adds to the host in machines of one
size, and `Host.CheckPlan`. `Check` keeps its signature and messages, and equals `CheckPlan` with
`Plan{MemoryMB: m, Running: 1, Snapshots: 1}`.

| Operation (origin of size M) | Running | Snapshots |
| --- | --- | --- |
| `hib new` | 1 | 1 |
| save, running origin | 1 (the checkpoint's second copy) | 2 (the guard's pause, the save) |
| save, paused origin | 2 (the resume, the checkpoint's copy) | 2 (the save, the pause again) |
| fork N, running origin | max(1, N) | 2 + N |
| fork N, paused origin | max(2, N) | 2 + N |

Before each copy, check again for the copies still to make. Wait up to 15 s while the check says
full: the checkpoint's old copy frees its memory in the background, and a machine may have woken
meanwhile.

**Images:**

- `hib image save MACHINE NAME` refuses catalog names, and saved names that exist ("image myimg
  exists: remove it first with: hib image rm myimg"). It prints the image, its memory and how to
  use it.
- An image carries everything in the machine. `hibd` puts only placeholders and your public key
  into machines, so a machine set up through `hib` holds no real secrets. A secret you typed into
  the machine yourself goes into the image too. `hib image save -h` and the README say so.
- `hib image ls` lists built-in images, built or not, and saved ones (from `GET /snapshots`,
  `hibimg-` only). Its columns are NAME, KIND (built-in or saved), MEMORY and CREATED. A shadowed
  saved image says so.
- `hib image rm NAME` deletes saved image NAME, never a built-in one. While machines use it, it
  says "image myimg is used by machines a, b: remove them first", naming the machines whose
  template is the image's, or else gives E2B's message.
- `hib new NAME --image SAVED` takes the memory for its room check from the saved image's
  template, not from `images.MemoryMB`. The machine gets the label `hib.image=myimg`.

**Fork:**

- `hib fork MACHINE [--count N]`, with N from 1 to 10, default 1.
- Copies are named `MACHINE-k` with the lowest free k, chosen under the lock. If a name would
  break `CheckName`, fail before touching anything.
- Each copy is whole or gone: create, re-key, machine-init, setup. If a step fails, kill that
  copy.
- Keep the copies made. Stop at the first failure and report "made web-1 and web-2 from web.
  couldn't make web-3: ..." with a non-zero exit. `--json` returns the made machines and the
  error.
- Labels: `hib.name`, the origin's `hib.idle` and `hib.image`, and `hib.forkof` with the origin's
  name, so `hib ls` can show where a copy came from in a FROM column.
- Output: "made web-1, web-2, web-3 from web. connect with: ssh web-1.hib".

**Every machine made from a saved image or a fork template:**

- Re-key: remove the host key pair as root, by a separate Exec with the paths as arguments, so no
  shell. Then machine-init makes a fresh key and rewrites authorized_keys, and `hibd` pins the
  key. `repin`, for a lost pin, never re-keys.
- Setup: always call `Setup.Write`, even when it's empty, so the copy's env block matches the
  host now. Git identity and the Codex login file are written only when the host has them.
  Otherwise the origin's stay. Both hold placeholders, not secrets. M10's `hib disconnect` has to
  clear them in copies too.
- The idle policy and timeout, as in Create.

**Unknown outcomes:**

- When the engine's create fails, look the name up and kill what's there. The name was free
  under the lock, so anything holding it now is ours. `hib new` gets the same fix, because e2b's
  `Create` POSTs, then GETs, and a failed GET leaves a machine the caller can't name by ID.
- A failed save deletes its new alias (the guard, above).
- Report anything left behind, such as "also couldn't remove web-3 (ID ...): remove it with: hib
  rm web-3".

**Hidden fork templates and cleanup:**

- `SweepForks` deletes every `hibfork-` template. It tries the state lock, and skips when it's
  busy. The sweeper tries again a minute later, as after `cleanup.ErrBusy`. A fork holds the
  lock from its save to its last copy, so no age grace is needed.
- E2B refuses the delete while copies use the template, so `ErrTemplateInUse` is ignored. The
  template goes once its last copy is removed, since the sweeper runs whenever a machine
  disappears.
- M2's note that cleanup assumes E2B only puts new builds into images and machines holds. A save
  makes one new build, in two envs: the origin's snapshot env and the new template's. Cleanup
  keeps a build while any env holding it exists, and builds that headers reference. A cleanup
  unit test pins this shape. M6 doesn't use E2B's tags API.

**Concurrency:**

- Save and fork hold the state lock from start to finish. `Pause` and `Remove` take it too, so
  `hib pause web` and `hib rm web` wait for a save or fork to finish. `Dial` doesn't, so waking
  by SSH stays instant. A wake racing a save only extends the origin, because the engine's resume
  returns 200 for a running machine.
- Whether the host is going to sleep is checked at the start, before the save, and before each
  copy.

Image save and fork run as root to confirm snapshot files reached disk. The state directory stays
owned by the login user. Root can use this trusted state only while it and `host-keys` are privately
writable and share an owner. New directories, locks and pins keep the state owner's UID and GID.
Pins and locks get their owner before they are published, so ordinary user commands still work.

**Packages** (the contract commit adds the types, the stubs and the fake engine):

- `internal/engine`: `TemplateInfo`, and `Engine` gains `SaveTemplate`, `Template`,
  `SavedTemplates` and `DeleteTemplate`, with `ErrCheckpoint` and `ErrTemplateInUse`. The e2b
  client gains `GET /snapshots` (the `snapshots` tag).
- `internal/engine/fake`: saves, copies and deletes as E2B does, and loses a machine whose first
  checkpoint fails. Tests fail saves and creates with `FailNextSave(err)` and
  `FailCreateAfterStart(metadata)`.
- `images`: `Template(name)` resolves a name. `IsBuiltIn`, `SavedTemplate`, `SavedName`,
  `ForkTemplate` and `IsForkTemplate` name and read the namespaces.
- `internal/room`: `Plan` and `Host.CheckPlan`.
- `internal/machines`: `Manager.SaveImage`, `Images`, `RemoveImage`, `Fork` and `SweepForks`,
  `MaxCopies`, `ErrBusy`, `Config.Durable` and `Config.Awake`, which `hibd` wires to its check
  for going to sleep. `Create` and room checks accept saved images.
- `internal/hibapi`: `Image`, `Response.Image`, `Response.Images` and `Machine.ForkOf`. All are
  optional, so the protocol stays at version 1.

**Known gaps** (Later):

- If `hibd` dies between creating an alias and finishing, the alias may never show in
  `GET /snapshots`, so nothing removes it.
- Nightly `pause-all` doesn't take the state lock. A save or fork that began before the quiesce
  may see its machines paused, and fails honestly. `hib new` has the same gap.

## Tasks

**Contract**

- [x] Design, spec §2, §5 and §9, and the research doc.
- [x] The engine's types and stubs, the fake engine, the resolver, the room and Manager stubs, and
      the hibapi types.

**Engine**

- [ ] e2b `SaveTemplate`, `Template`, `SavedTemplates` and `DeleteTemplate`, tested against E2B's
      measured answers, with 500, 400 and 409, paging and namespaced names.
- [ ] Engine contract cases, run against the fake and, in e2e, against E2B.

**Machines**

- [ ] `SaveImage`, `Fork`, `Images`, `RemoveImage` and `SweepForks`, with the guard and its
      failure paths.
- [ ] `CheckPlan`, and the check before each copy.
- [ ] Re-key, and `Setup.Write` always for copies.
- [ ] Kill by name after a failed create, in `Create` too.
- [ ] `Pause` and `Remove` under the state lock.

**hibd and hib**

- [ ] `hibd image save|ls|rm` and `hibd fork`, with `-json` and usage.
- [ ] `Durable` wired, and `SweepForks` in the sweeper.
- [ ] `hibd leakscan -image` scans saved images through the resolver.
- [ ] `hib image save|ls|rm`, `hib fork`, `hib new --image` with saved images, and a FROM column
      in `hib ls`.
- [ ] README: images and fork, and what an image carries.

## Tests

Unit:

- [ ] Names: the lowest free k, the length limit, the resolver, catalog refusal and shadowing.
- [ ] Room plans: the table above, and the messages.
- [ ] The sweep: a busy lock is skipped, in-use is ignored, and `hibimg-` is never touched.
- [ ] Re-key arguments.
- [ ] Every guard failure: the pause, `Durable`, the resume, the checkpoint and an unknown error.
- [ ] A failed create is cleaned up by name.
- [ ] Cleanup keeps a build that a machine's snapshot env and an image env share, and frees it
      once both are gone. A paused copy's diff whose header names it keeps it.

Integration (the fake engine, through Manager and through `hibd`):

- [ ] Save, then `new --image`.
- [ ] Fork 2 from a running origin, and from a paused one, which is paused again afterwards.
- [ ] A failed save: the origin is running again, the message is honest, and no alias is left.
- [ ] A fork failing on copy 2 of 3: copy 1 is kept, copy 2 is killed, and the exit is non-zero.
- [ ] `image rm` while copies use the image, then after they're gone.
- [ ] `hib pause` waits for a fork.

Host (as root, through `make test-host-remote`):

- [ ] Root's first sweep, then a create, a save and a fork, leave the lock, `host-keys` and pins
      owned by the state's owner.

Engine contract (against the fake, and against E2B in e2e):

- [ ] Saving a running machine works, and saving a paused one is refused.
- [ ] A machine created from a saved template has only its own labels.
- [ ] Deleting a template in use, a free one and a missing alias.

End-to-end:

- [ ] `TestImageAndFork`, within the huge-page pool of three 2 GB dev machines:
  1. `web` with the fixture repo cloned, dependencies installed and a dev server running.
  2. `hib image save web myimg`, then `hib new web2 --image myimg`. The files are in place, the
     server answers without a restart, and the host key differs. `hib image rm myimg` refuses.
     `hibd leakscan -image myimg` finds no real values.
  3. `hib rm web2`, then `hib image rm myimg` works.
  4. `hib fork web --count 2` from running `web`, three machines at the peak. Both copies answer,
     with their own names and host keys. `hib ls` shows FROM web, and `web` keeps running. Then
     the copies are removed.
  5. `hib pause web`, then `hib fork web`. The copy answers, and `web` is paused again.

## Done when

- [ ] `make lint test`, `make test-host-remote`, a clean `make generate` and `make e2e` are green.
      CI is parked, so `make lint test` stands in for it.
- [ ] Spec: §5 Images and fork matches the code, and S3 is answered.
- [ ] README: images and fork.

## Changes

- 2026-10-04: Live spikes and E2B's source showed that native fork copies every label,
  `hib.name` included, can't change them, and refuses a paused machine. So fork is a hidden named
  snapshot plus ordinary creates. E2B's checkpoint failed 1 in about 21 times, so save and fork
  first pause the origin and wait until that snapshot is on disk. After a plan review: separate
  namespaces for built-in, saved and fork templates, room checks for an operation's peak,
  re-keyed copies, and `Pause` and `Remove` under the state lock.
- 2026-10-04: `hib new --from` is dropped, because `hib new` already has `--image`, which now also
  takes saved images. `TestImageLeakScan` became step 2 of `TestImageAndFork`. Images aren't
  recorded in a store: E2B holds them, and the alias is the record. Later:
  `hib image save --replace`, and a new `/etc/machine-id` for copies.
- 2026-10-04: Save and fork run as root, but the state directory belongs to the login user. So
  root may use that state while it stays private, and gives what it creates to the state's owner.
  `make test-host` and `make test-host-remote` also run the machines `TestHost` tests as root.
