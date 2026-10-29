# Ports between machines

A user publishes a port on one machine with `hib port add`. Their other machines can then reach it by name as `NAME.hib:PORT`. Ports that aren't published are refused, a few ports are reserved, and every link open, close and refusal lands in `hib log --links`. `hib port rm` unpublishes a port and cuts its links.

## Sub-features

- `port-add` publishes a port and prints the `NAME.hib:PORT` address.
- `port-ls` lists machine, host name, virtual IP, published ports and open link counts.
- `port-reach` lets another machine reach `NAME.hib:PORT`.
- `port-unpublished` refuses a port that isn't published, logged with reason `not published`.
- `port-reserved` refuses to publish `8081`, the SSH bridge, and `49983`.
- `port-links-log` shows `open` and `close`, with bytes, in `hib log --links`.
- `port-rm` unpublishes the port, and later connections are refused.
- `port-wake` wakes a paused target on a link, with `woke: true` in the log. This is from the e2e suite and not yet driven here.

## How to get to it (user POV)

- `hib port add NAME PORT [PORT...]`, `hib port rm NAME PORT`, `hib port ls [--json]`.
- From inside any of the user's machines: `curl http://NAME.hib:PORT/` or any TCP client.
- `hib log --links [-n N] [--json]`.

## Driving it with control-hib

Preconditions:

- Baseline passes, with room for two small machines.
- `C=$(control-hib name client)`, `T=$(control-hib name web)`.

- **Make two machines.** Run `control-hib hib client-new -- new "$C" --size small`, then `control-hib hib web-new -- new "$T" --size small`. Both exit `0`.
- **Serve 8080 and 9090 in the target.** Run `control-hib ssh serve "$T" -- 'mkdir -p /tmp/www && echo hello-from-web > /tmp/www/hello.txt && for p in 8080 9090; do setsid -f python3 -m http.server $p --directory /tmp/www </dev/null >/dev/null 2>&1; done; sleep 1; curl -sf -m 5 http://127.0.0.1:8080/hello.txt'`. Stdout is `hello-from-web`.
- **Publish.** Run `control-hib hib port-add -- port add "$T" 8080`. Exit `0`, and stdout is `$T:8080 is published to your machines as $T.hib:8080`.
- **List.** Run `control-hib hib port-ls -- port ls --json`. `.ports[]` has `{"machine":"$T","addr":"198.18.x.y","published":[{"port":8080,"open":0}]}`. The text table header is `NAME HOST IP PORTS OPEN`.
- **Reach it.** Run `control-hib ssh link-ok "$C" -- "curl -sf -m 15 http://$T.hib:8080/hello.txt"`. Exit `0`, and stdout is `hello-from-web`.
- **Unpublished is refused.** Run `control-hib ssh link-refused "$C" -- "curl -s -m 15 http://$T.hib:9090/hello.txt"`. Exit `56`, with no body, even though the server is listening on 9090.
- **Reserved is refused.** Run `control-hib hib port-reserved -- port add "$T" 8081`. Exit `1`, and stderr is `hib: port 8081 is Hibernaut's SSH bridge and can't be published`.
- **Link log.** Wait at least 10 seconds, then run `control-hib hib links -- log --links -n 50 --json`. `.links[]` with `.to == $T` holds an `open` and a `close` on port 8080 from `$C`, where the close has `bytesIn` and `bytesOut`. It also holds a `refused` on port 9090 with `"reason":"not published"`.
- **Unpublish.** Run `control-hib hib port-rm -- port rm "$T" 8080`. Exit `0`, and stdout is `$T:8080 is no longer reachable from your machines`. `port ls --json` no longer lists `$T`, and is `{"v":1,"ports":[]}` when nothing else is published. Then `control-hib ssh link-after-rm "$C" -- "curl -s -m 15 http://$T.hib:8080/hello.txt"` exits `56`. Record the result with `control-hib note ports "..."`.
- **Tidy.** Run `control-hib hib rm-c -- rm "$C" --yes` and `control-hib hib rm-t -- rm "$T" --yes`.

## Gotchas

- Refusals reach the link log only after a 10-second fold window, so query too early and you won't see them. Opens and closes appear at once.
- Only 8081 and 49983 are reserved. Port 22 is publishable: `port add NAME 22` succeeds.
- `hib log --links` covers the whole host. Older entries may name machines by E2B ID instead of name. Filter with `.to == $T`.
- `port ls --json` keys entries by `.machine`, not `.name`.
- The server must keep running after the ssh session ends. Use `setsid -f ... </dev/null >/dev/null 2>&1`, or it dies with the session.
- curl exit `56` means the connection was reset. Treat any non-zero exit with an empty body as refused, and use the link log for the reason.
