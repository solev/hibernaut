# M8 Machine sizes

Status: **not started** · Spec: §5 (Sizes) · Needs: M2 (room on the host)

**Goal:** pick a machine's size when you make it: `hib new app --size small`. Smaller machines
mean more of them at once, for example several AI agents side by side.

It can come any time after M2: it builds on M2's room check, and nothing else depends on it.

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

## Tests

Unit:

- [ ] Each size name maps to its CPUs, RAM and template name. An unknown size is an error that
      lists the sizes.
- [ ] The room check with mixed sizes: what still fits.

Integration:

- [ ] `hibd new --size small` against the fake engine starts `hib-dev-small` and records
      `hib.size`. With no `--size`, a machine is medium.

End-to-end:

- [ ] `TestSizes`: in a machine made with `hib new --size small`, `nproc` is 1 and `MemTotal` is
      about 1 GiB, and `hib ls` says `small`. A machine made without `--size` still has 2 vCPUs
      and 2 GiB.

## Done when

- [ ] `make lint test`, CI and `make e2e` are green.
- [ ] README: the sizes, how many of each fit at once, and that a machine keeps its size.

## Changes

(none yet)
