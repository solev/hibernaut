# M11 Bot faces

Status: **in progress** · Spec: §2 (The experience) · Needs: M10

**Goal:** `hib` shows each machine as the Hibernaut bot, a face two columns wide at the start of
the lines about it. A running machine's face is awake and a paused one's is asleep. While `hib`
waits, the face on the live line moves: it wakes up as a machine is made and dozes off as one
pauses.

It changes `cmd/hib`, and adds `internal/bot`, which draws the bot. Faces are only for people at a
terminal. Pipes, scripts and `--json` get the same output as before.

## Why

[G1](../goals.md#g1-show-it-in-public) shows Hibernaut in short videos, and most of each one is a
terminal. The [bot](../brand/bot/) is the project's face on the web. Putting it in `hib` makes the
clips recognisable, and a machine's state readable at a glance.

A throwaway prototype answered how `hib` should show its bots in a terminal. It's on the
`prototype/hib-bots` branch at `3780e81`, in `cmd/hib/prototype-bots`, and
`go run ./cmd/hib/prototype-bots` plays it. It replays one `hib` session three ways:

- **A Faces:** a face on every line about a machine, and only the live line's face moves.
- **B Moments:** output stays as it is, and a big bot shows only while `hib` makes you wait.
- **C Cards:** results print a card for each machine, and `hib ls` prints them all as cards.

Verdict, 2026-10-08: **A Faces**. Output keeps its shape, so tables, copying and habits still work,
and the faces add life without taking room.

## Design

The M11 code is built against this section. Code that has to differ updates it in the same commit.

**Where faces go.** With text faces, `hib ls` looks like this:

```
$ hib ls
    NAME  STATE    IMAGE  SIZE    IDLE  PAUSES IN  STARTED           ID        FROM
▪▪  app   running  dev    medium  10m   7m         2026-10-08 09:12  ab12cd34
▬▬  api   paused   dev    small   10m   -          2026-10-07 18:40  ef56ab78
```

- `hib ls` starts each machine's row with its face and two spaces, and the header with four
  spaces, so the columns still line up. Without the first four columns, the table is the same as
  before, byte for byte.
- The results of `hib new` and `hib fork` start with an awake face and a space:
  "made app; connect with: ssh app.hib". The result of `hib pause`, "paused app", starts with an
  asleep face.
- The live line starts with a face and a space.
- `hib rm`, the room line, notes, hints and errors don't get faces.

**Two kinds of face.**

- **Image faces** draw the bot with the kitty graphics protocol, two cells wide and one high.
  Ghostty and kitty get them.
- **Text faces** are the bot's eyes: `▪▪` open and `▬▬` shut. An asleep face is dim unless
  `NO_COLOR` is set. Every other terminal gets them.

**Which kind.** `hib` picks once, from its environment, before it runs a command. It asks the
terminal nothing:

1. `HIB_FACES=off` turns faces off, and `HIB_FACES=text` picks text faces. Other values count as
   unset.
2. With `NO_COLOR` set, text faces.
3. In a multiplexer, text faces: `TMUX`, `STY` or `ZELLIJ` is set, or `TERM` starts with `screen`
   or `tmux`. A multiplexer draws its own screen and doesn't pass images on.
4. Image faces in Ghostty and kitty: `TERM_PROGRAM` is `ghostty` or `kitty`, or it isn't set and
   `TERM` is `xterm-ghostty` or `xterm-kitty`. `KITTY_WINDOW_ID` doesn't count on its own, since
   programs started from kitty inherit it.
5. Otherwise text faces. That includes WezTerm and Konsole, whose support for kitty images is
   partial.

Then, at use time, faces on stdout need stdout to be a terminal, no `--json` and a `TERM` other
than `dumb`, as colour does. Faces on the live line need the live line. `hib bridge`, which ssh
runs, never shows faces.

**Colours.** `hib` draws the bot in fixed colours, the same in every terminal. The body is cream
`#f5efe5`, with dark eyes `#14110e` and a dark edge one pixel wide at 75%. On a dark background
the edge disappears, and on a light one it outlines the bot. The snapshot outlines are `#e8a94c`,
a deeper amber than Lamplight's `#f3c77c`, so they show on white too.

Matching the terminal's own colours would mean asking it (OSC 10 and 11). The answer comes back
on the same input as your keys, and an answer that comes after `hib` stops waiting for it lands in
the shell as if you had typed it.

**Cell size.** An image face is drawn at the size of two cells, so the terminal doesn't scale it.
`hib` divides the window's size in pixels by its rows and columns, from the kernel's window size
for stdout or else stderr (`TIOCGWINSZ`). That asks the kernel, not the terminal. Without a size in
pixels, or for cells under 4 by 8 pixels, it assumes 10 by 20. It caps cells at 64 by 128.

**Faces in printed lines.** An image face is one kitty graphics command and two spaces:

```
ESC _G a=T,f=100,q=2,C=1,c=2,r=1 ; PNG in base64 ESC \
```

- `a=T` sends the image and shows it at the cursor. `q=2` stops the terminal answering, and with no
  image id the image can't clash with another program's.
- `c=2,r=1` fits the image to two cells, and `C=1` leaves the cursor where it was, so the two
  spaces move it on. A terminal that ignores the command still lines up.
- A PNG longer than 4096 bytes in base64 goes in chunks, with `m=1` on all but the last.
- `hib` builds each state's command once per run. `hib ls` builds its output in memory and writes
  it in one go.

**The live line's face** is drawn with the protocol's Unicode placeholders, which kitty has had
since 0.28. They're text, so the face moves with the line when Bubble Tea prints lines above it.
An image placed by position wouldn't.

- `hib` picks a random image id from 1 to 16777215. The first frame goes out before Bubble Tea
  starts, as a virtual placement: `a=T,U=1,f=100,i=ID,p=1,c=2,r=1,q=2`. So Bubble Tea's first
  write is still its first frame, which `hib` waits for before it prints above the line.
- The line starts with two placeholder cells, U+10EEEE with the diacritics for row 0 and columns 0
  and 1, in a foreground colour that encodes the id. Bubble Tea runs with the TrueColor profile,
  so it keeps that colour. With text faces it runs with the ASCII profile, which keeps the dim.
- Each new frame is sent under the same id, which replaces the image and keeps its placement.
- Once Bubble Tea has stopped, `hib` deletes the image: `a=d,d=I,i=ID,q=2`. Not before, since
  Bubble Tea writes the frames it still holds as it stops, and a later frame would bring the image
  back.
- The face and its space take three columns. On a line too narrow to leave two columns for the
  text, the face is left out.

**The animation** is part of the live line's Bubble Tea model. Once the line shows, only Bubble
Tea's event loop touches the face.

- Every 50 ms a tick moves the bot on by the clock, in steps of 1/60 s. After a late tick it moves
  a second at most.
- The ticks start after Bubble Tea's first write. Each time it draws, Bubble Tea writes the frames
  it was handed before the line. So an earlier frame would be that first write, and the lines
  `hib` prints next could land at the top of the screen.
- Image faces: after each move, the model draws a frame. When the pixels changed, it hands Bubble
  Tea the command to write as it is (`tea.Raw`) and then the next tick, in sequence
  (`tea.Sequence`). So an old frame can't be written after a newer one. Bubble Tea writes the
  frames, and the lines printed above the live line, from different goroutines. So `hib` gives it
  a writer with a lock, and their bytes never mix.
- Text faces: the eyes are part of the line, and change when the bot blinks or dozes off.

Each live line has a script, timed from when the line shows:

| Live line | Face |
| --- | --- |
| making NAME | asleep, wakes at 1.6 s |
| waking the host | asleep |
| forking NAME | awake, forks at 0.4 s |
| pausing NAME | awake, hibernates at 0.3 s and stays asleep |
| building NAME, and its steps | working |

- A script's first state starts from wherever the bot is, so a new script never jumps.
- Waking the host is a wait inside another live line. It plays its own script, and when it ends
  the line's script starts again.
- New text on the line, such as a build's next step, keeps the script running.
- Before the line shows, only the newest script counts. It starts when the line shows.

`hib fork` and `hib pause` get live lines, as `hib new` has: "forking app (8s)" and
"pausing app (3s)", after 2 s. `hib rm` doesn't, so `hib pause` no longer shares its code.

**The bot.** `internal/bot` ports [hibernaut-bot.js](../brand/bot/hibernaut-bot.js): the same
shapes, springs and timings, stepped by time instead of by the browser. It has no terminal code,
and draws into an `image.NRGBA`.

- It keeps the six states `hib` uses: asleep, wake, idle, working, fork and hibernate. It leaves
  out the Zs, the fork's clone and the thinking and happy states, which don't read at two cells.
- `New` starts a bot settled in a state, and `Become` moves it to another. `Advance` steps time,
  and does nothing for no time or less. `Shut` says whether its eyes read as shut. `Draw` draws a
  frame at a size, in a palette of body, eyes, accent and edge colours.
- A text face shows `▬▬` when `Shut` is true and `▪▪` otherwise, so it blinks when the bot does.
- Below 48 pixels high, the eyes snap to whole pixels, so a small face stays crisp.

**Dependencies.** None new. `github.com/charmbracelet/colorprofile`, which Bubble Tea already uses,
becomes a direct dependency of hib. The test that hibd links no Charm library checks that it
doesn't link `internal/bot` either.

**Out of scope:**

- images in other terminals: iTerm2's protocol, sixel, WezTerm and Konsole
- images inside tmux, screen or Zellij
- matching the terminal's colours
- faces that move anywhere but the live line
- the bot's other states, its Zs and its clone
- bigger bots like B Moments and C Cards

## Tasks

**The bot**

- [ ] `internal/bot`: the engine and its drawing, ported from the prototype's tight bot.

**Picking faces**

- [ ] The face kind, from the environment in `main`, with the checks at use time. `HIB_FACES`
      turns faces off or picks text faces.
- [ ] Fixed colours that read on dark and light backgrounds, and the cell size from the window
      size.

**Faces**

- [ ] `hib ls` rows, and the results of `hib new`, `hib fork` and `hib pause`.
- [ ] Live lines for `hib fork` and `hib pause`.
- [ ] The live line's face, its scripts and its animation, for text and image faces.

**Docs**

- [ ] The README: faces, `HIB_FACES` and what `NO_COLOR` and `TERM=dumb` do to them.

## Tests

Unit (in `internal/bot` and `cmd/hib`, no host):

- [ ] The bot: frames are the same for the same seed and time, and change as it moves. Settled
      awake and asleep bots read as open and shut, and a blink shuts the eyes. The eyes can be
      filled and the body edged. Advancing by no time or less does nothing.
- [ ] Picking faces: a table over the environment. No faces on stdout in a pipe, with `--json` or
      with `TERM=dumb`.
- [ ] `hib ls` with text faces: without its first four columns, the output is the table without
      faces, and the room line has none. The results of `hib new`, `hib fork` and `hib pause`
      get faces. `hib rm` doesn't, and nor does a fork that made nothing.
- [ ] Image faces: the commands have the right keys and chunks, and the PNG decodes at the cell
      size. The cell size comes from the window size, with a default and a cap.
- [ ] The live line, through a real Bubble Tea program: the first frame comes before the line
      and later frames only after it. The placeholders keep their colour and diacritics. The
      image is deleted when the line stops, Ctrl-C included. A line that never shows sends no
      image.
- [ ] The live line's script: the text face wakes when its script says. A wait plays its own
      script and then the line's again. New text keeps the script running and the newest script
      starts when the line shows. The face leaves room for the text, and only new frames are sent.

End-to-end:

- [ ] None new. Faces are only for terminals, and the e2e tests have none. The existing tests stay
      green.

## Done when

- [ ] `make lint test` is green. CI is parked, so it stands in for CI.
- [ ] `make e2e` is green on the host.
- [ ] The README matches what was built.

## Changes

- 2026-10-08: Started, from variant A of the prototype.
- 2026-10-08: What Astra's review and building it changed. Fixed colours instead of asking the
  terminal, whose late answer would land in the shell, and the cell size from the kernel's window
  size. The animation moved into Bubble Tea's model, with frames written as they are through a
  writer with a lock. The first frame goes out before Bubble Tea starts, and the image is deleted
  once it stops. The palette gained a dark edge and a deeper accent, so the bot reads on light
  backgrounds. `KITTY_WINDOW_ID` alone no longer picks image faces. `make e2e` waits for the host.
- 2026-10-08: What the reviews changed. The face starts moving after Bubble Tea's first write, so
  that write is always the line. Frames go to Bubble Tea in sequence, so an old one can't be
  written after a newer one. Bubble Tea writes the frames it holds as it stops, which is why the
  image is deleted after.
