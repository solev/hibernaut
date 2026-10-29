# Images and fork

A user saves a configured machine as a named image with `hib image save` and makes new machines from it with `hib new --image`, each starting with the saved files. `hib fork` copies a running machine into numbered siblings in one step. An image can't be removed while machines use it.

## Sub-features

- `image-save` saves a running machine as `IMG`. The source keeps running.
- `image-ls` lists `IMG` as kind `saved`, with memory and creation time. Built-in images like `dev` show too.
- `image-new` makes a machine from `IMG` that holds the source's files, at the source's size.
- `image-size-refused` refuses `--size` with a saved image.
- `image-rm-inuse` refuses to remove an image that machines use.
- `image-rm` removes it.
- `fork` makes `NAME-1 ... NAME-N` from a running machine, with `FROM`/`forkOf` naming the source.
- `fork-large` refuses to save or fork large machines. This is from the e2e suite and not yet driven here.

## How to get to it (user POV)

- `hib image save NAME IMG`, `hib image ls [--json]`, `hib image rm IMG`.
- `hib new NEW --image IMG`.
- `hib fork NAME [--count N]`.
- `hib ls` shows the `IMAGE` and `FROM` columns.

## Driving it with control-hib

Preconditions:

- Baseline passes, with room for four small machines.
- `W=$(control-hib name web)`, `IMG=$(control-hib name img)`, `X=$(control-hib name fromimg)`.

- **Source machine with a marker.** Run `control-hib hib web-new -- new "$W" --size small`, then `control-hib ssh img-mark "$W" -- 'echo from-image > /home/user/imgmark'`. Both exit `0`.
- **Save.** Run `control-hib hib image-save -- image save "$W" "$IMG"`. Exit `0` in about 1 to 2 seconds, and stdout is `saved image $IMG (1 GiB). use it with: hib new NAME --image $IMG`. `ls --json` still shows `$W` running.
- **List.** Run `control-hib hib image-ls -- image ls --json`. `.images[]` has `{"name":"$IMG","kind":"saved","memoryMB":1024,"cpus":1,"created":...,"built":true}`.
- **Size is refused.** Run `control-hib hib new-sized -- new "$X" --image "$IMG" --size small`. Exit `1`, and stderr is `hib: image $IMG is a saved image, and its machines are the size of the machine it was saved from (small). Leave out --size`.
- **Make from it.** Run `control-hib hib new-from-image -- new "$X" --image "$IMG"`. Exit `0`. `ls --json` shows `$X` with `"image":"$IMG","size":"small"`. Then `control-hib ssh img-check "$X" -- 'cat /home/user/imgmark'` prints `from-image`.
- **In-use refusal.** Run `control-hib hib image-rm-inuse -- image rm "$IMG"`. Exit `1`, and stderr is `hib: image $IMG is used by machines $X: remove them first`.
- **Fork.** Run `control-hib hib fork -- fork "$W" --count 2`. Exit `0` in about 8 seconds, and stdout is `made $W-1, $W-2 from $W. connect with: ssh $W-1.hib`. `ls --json` shows both running with `"forkOf":"$W"`, and the text `FROM` column says `$W`.
- **Remove.** Run `control-hib hib rm-x -- rm "$X" --yes`, then `control-hib hib image-rm -- image rm "$IMG"`. The second prints `removed image $IMG`, and `image ls --json` no longer lists it. Record the result with `control-hib note images "..."`.
- **Tidy.** Remove `$W-1`, `$W-2` and `$W`, each with `rm NAME --yes`.

## Gotchas

- If `new --image` fails, nothing uses the image, so `image rm` then succeeds and the in-use refusal isn't proven. Check that `$X` exists before asserting the refusal.
- A saved image keeps the source's size. Passing `--size` is a mistake, not an override.
- Fork names are `$W-N`. They carry the run prefix, so the guard and `cleanup` handle them.
- Each fork is a running machine and uses room. With `--count 2` from a running source, three small machines run at once.
- `image ls` lists every saved image on the host, including other people's. Only remove `$PREFIX-*` images.
