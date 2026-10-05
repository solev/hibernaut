# M8 Machine sizes

Status: **in progress** · Spec: §5 (Sizes) · Needs: M2 (room on the host)

**Goal:** pick a machine's size when you make it: `hib new app --size small`. Smaller machines
mean more of them at once, for example several AI agents side by side.

It can come any time after M2: it builds on M2's room check, and nothing else depends on it.

## Design

The M8 code is built against this section. Code that has to differ updates it in the same commit.

**Sizes.** `images` holds the table: `small` (1 vCPU, 1024 MiB), `medium` (2 vCPU, 2048 MiB, the
default) and `large` (4 vCPU, 4096 MiB). Disk stays per image: 10 GiB for every size. hib checks a
size name against the table, and hibd checks it again before it becomes part of a template name, a
label or a message. An unknown size, an empty one included, is an error that lists the sizes.

**Templates.** Each size of a built-in image is its own template. Medium keeps today's name,
`hib-dev`, so today's build and machines keep working. The others are `hib-dev-small` and
`hib-dev-large`. No two images and sizes share a template name. Saved images and forks are one
template each, at the size of the machine they came from.

**A machine's size** is its `hib.size` label, written for every new machine:

- `hib new` with a built-in image: the size asked for, or medium.
- `hib new` with a saved image: the size whose RAM matches the image's template. No label if none
  does.
- A fork's copies: the origin's size, read as below, so a copy of a machine made before M8 gets
  medium.

To read it (`hib ls`, forks), hibd takes the label if it names a size, else the size whose RAM
matches the machine's, else none. Machines made before M8 have no label and 2 GiB, so they're
medium. The room check keeps using each machine's RAM as E2B reports it, never the label.

**`hib new NAME --size SIZE`.**

- hib sends `--size` only when it's given, so a newer hib still works with an older hibd. An older
  hib gets medium machines from a newer hibd.
- hibd tells an omitted `-size` from an empty one.
- A saved image's machines keep its size, so `--size` with a saved image is refused, even a
  matching one: "image NAME is a saved image, and its machines are the size of the machine it was
  saved from (small). Leave out --size".
- A size that isn't built yet: "image dev isn't built in size small yet (run: hib image build
  dev)".

**`hib image build dev` builds every size**, one at a time: medium first, so the default is ready
first, then small and large.

- The sizes share the build steps. E2B's step cache doesn't key on CPUs or RAM (E2B's source at
  the pinned commit), so each size adds only its final boot and snapshot.
- `-force` rebuilds every size. Each size logs its time on stderr: `built dev (small) in 41s`.
- The build boots a machine of each size, so before each one hibd checks there's room for it. It
  uses the room check itself, not the shortcut for a machine that's already running.
- The first failure stops the build. Its error says which sizes are built and which failed.
  Running it again rebuilds only what changed.
- The one-hour limit covers every size.
- The JSON reply keeps `Template` (medium's) and adds the sizes built. hib prints them, or today's
  line when an older hibd sends none.

**Room.** The check itself doesn't change: it already uses each running machine's RAM.

- When `hib new` with a built-in image finds no room, its message also says which sizes still
  fit, from the same reading of the host: "… Sizes that still fit: small." That holds too when E2B
  refuses and the second check finds no room.
- An operation that can't fit even with every machine paused says so, instead of suggesting
  `hib pause`. On today's host (6.25 GiB of huge pages) that's saving or forking a large machine,
  which needs 8 GiB at once.
- `hibapi.Room` keeps `Fits` and `Image` (for medium) and adds `Sizes`: how many more of each size
  fit, in table order, all from one reading. Each count is on its own, not a mix.
- `hib ls` ends with, for example: "Room for more machines: 4 small, 2 medium or 1 large. 4.1 GiB
  of 6.2 GiB memory free, 70 GiB disk free." An older hibd sends no `Sizes`, and hib prints
  today's line.

**`hib ls`**, hib's table and hibd's, has a SIZE column after IMAGE. An unknown size is `-`.

**Out of scope:**

- `hib image ls` stays as it is, so a built-in image shows medium.
- `hibd leakscan -image dev` scans medium's build only: every size runs the same steps, and builds
  see no secrets.
- No `--size` for `hib image build`, and no resizing, which E2B can't do.
- No change to E2B's tier limits unless the host refuses a size. E2B's defaults allow 8 vCPU and
  8 GiB.

## Tasks

- [ ] Sizes in `images/images.go`: `small` (1 vCPU, 1 GiB), `medium` (2 vCPU, 2 GiB: the
      default, and today's `dev`), `large` (4 vCPU, 4 GiB). E2B fixes CPU and RAM per image
      build, because a machine's memory snapshot is that size, so each size of an image is its
      own E2B template: `hib-dev` stays medium, next to `hib-dev-small` and `hib-dev-large`.
- [ ] `hib image build dev` builds every size. They share the build steps, so the extra sizes
      should come mostly from E2B's step cache. Check that, and log each size's build time.
- [ ] `hib new app --size small`. The size goes into E2B metadata (`hib.size`).
- [ ] `hib ls` shows each machine's size.
- [ ] The room check (M2) uses each machine's own RAM, and the "no room" message says which
      sizes still fit.
- [ ] A machine keeps its size for life: E2B can't change a snapshot's RAM or CPUs. The README
      says how to move to another size (a new machine, then copy files over).
- [ ] Saved images and forks (M6) keep the size of the machine they came from, so `--size`
      together with a saved image's `--image` is refused with an explanation.
- [ ] An operation that can't fit even with every machine paused, such as saving or forking a
      large machine on today's host, says so instead of suggesting `hib pause`.

## Tests

Unit:

- [ ] Each size name maps to its CPUs, RAM and template name. An unknown size is an error that
      lists the sizes.
- [ ] The room check with mixed sizes: what still fits.

Integration:

- [ ] `hibd new --size small` against the fake engine starts `hib-dev-small` and records
      `hib.size`. With no `--size`, a machine is medium.
- [ ] A machine made before M8, with no `hib.size`, resumes, lists and forks as medium.
- [ ] `hib image build dev` builds medium, small and large in that order. A failure in one says
      which sizes are built.
- [ ] hib without `--size` works with a hibd from before M8, and hib from before M8 gets medium
      machines.

End-to-end:

- [ ] `TestSizes`: in a machine made with `hib new --size small`, `nproc` is 1 and `MemTotal` is
      about 1 GiB, and `hib ls` says `small`. It still is after a pause and a wake. A machine made
      without `--size` still has 2 vCPUs and 2 GiB. The three templates have their size's CPUs
      and RAM, checked without starting a large machine.

## Done when

- [ ] `make lint test`, CI and `make e2e` are green.
- [ ] README: the sizes, how many of each fit at once, and that a machine keeps its size. A large
      machine can't be saved or forked on today's host.

## Changes

- 2026-10-05: Added the Design section, after an advisor's review of the plan. New from it: an
  operation that can never fit says so (a large machine's save or fork needs 8 GiB at once),
  `hib image build` checks room before each size, and tests for machines and hib from before M8.
