# M6 Golden images and fork

Status: **not started** · Spec: §2 (Golden images), §5 (Images and fork)

**Goal:** save a set-up machine as an image and start new machines from it in about a second, or
clone a running machine N times.

## Tasks

- [ ] `hib image save <machine> <name>` → E2B snapshot with memory; `hib image ls / rm`.
- [ ] `hib new <name> --from <image>`.
- [ ] `hib fork <machine> --count N` → E2B fork; new machines are `<machine>-1…N`.
- [ ] Machines from images and forks get the current placeholder block and git identity (secrets
      may have changed since the image was saved), and the idle policy.
- [ ] Images recorded in the store and in E2B metadata, like machines.

From M2: `hibd`'s snapshot cleanup assumes E2B only puts new builds into images and machines,
which E2B's snapshots do. If images use E2B's tags API, which copies an existing build, make
tagging and cleanup take turns (see M2's Changes).

## Tests

Unit:

- [ ] Fork and image naming.

Integration:

- [ ] Save, new-from and fork against the fake engine.

End-to-end:

- [ ] `TestImageAndFork`:
  - a machine with the fixture repo cloned, dependencies installed and a dev server running;
  - `hib image save` → `hib new --from` → files in place, and the server answers without being
    restarted;
  - `hib fork --count 3` → all three answer; the original keeps running.
- [ ] `TestImageLeakScan`: the saved image contains zero real values.

## Done when

- [ ] `make lint test`, CI and `make e2e` are green.
- [ ] README: images and fork.

## Changes

(none yet)
