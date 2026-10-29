# Secrets and the swap proxy

A user stores a secret once with `hib secret add`, binding it to the sites that may receive it. Every machine then sees only a placeholder in an environment variable. When a machine sends the placeholder to a bound site, the host's proxy swaps in the real value, scrubs it from the response, and records the swap in `hib log`. The real value never enters a machine or a log.

## Sub-features

- `secret-add` stores a value read from stdin as `NAME`, exposed as `ENV` and bound to sites.
- `secret-ls` lists name, env var, sites and the placeholder.
- `secret-env` puts the placeholder in `$ENV` in every machine, within about a minute.
- `secret-intercept` makes TLS to a bound site present a `Hibernaut CA` certificate from inside machines.
- `secret-swap-scrub` makes a bound site that echoes the header back show the placeholder, not the value.
- `secret-log` adds a `hib log` line: time, machine, site, method, path, secret names.
- `secret-rm` removes it.
- `secret-no-leak` keeps the real value out of every output and evidence file.

## How to get to it (user POV)

- `printf %s VALUE | hib secret add NAME --env VAR --bind site1,site2`, or an interactive prompt on a terminal.
- `hib secret ls [--json]`, `hib secret rm NAME`.
- Inside a machine: `$VAR` in any tool, such as curl, `gh` or an SDK.
- `hib log [-n N] [--json]`. `hib status` also lists secrets with a health state.

## Driving it with control-hib

Preconditions:

- Baseline passes, with room for one small machine.
- `S=$(control-hib name sec)`, `SEC=$(control-hib name echo)`, `ENV=$(echo "${SEC//-/_}" | tr a-z A-Z)`, `VALUE="sk-vfy-$(openssl rand -hex 16)"`. Keep all of these in one shell, and never echo `$VALUE`.
- `postman-echo.com` is reachable from the host. It's the public echo site the e2e suite uses.

- **Make a machine.** Run `control-hib hib sec-new -- new "$S" --size small`. Exit `0`.
- **Store it.** Run `printf %s "$VALUE" | control-hib hib secret-add -- secret add "$SEC" --env "$ENV" --bind postman-echo.com`. Exit `0`, and stdout is `stored $SEC as $ENV for postman-echo.com`.
- **List it.** Run `control-hib hib secret-ls -- secret ls --json`. The entry whose `.name` is `$SEC` has `"env":"$ENV"`, `"sites":["postman-echo.com"]` and a `"placeholder"`. Save it as `PH=$(... | jq -r --arg s "$SEC" '.secrets[] | select(.name==$s) | .placeholder')`. `PH` is not `$VALUE`.
- **Placeholder in the machine.** Poll `control-hib ssh printenv "$S" -- "printenv $ENV"` every 5 seconds for up to a minute. Stdout equals `$PH`.
- **Interception is on.** Run `control-hib ssh issuer "$S" -- 'openssl s_client -connect postman-echo.com:443 -servername postman-echo.com </dev/null 2>/dev/null | openssl x509 -noout -issuer'` until stdout contains `Hibernaut`. The observed output is `issuer=O = Hibernaut, CN = Hibernaut CA`.
- **Swap and scrub.** Run `control-hib ssh echo-auth "$S" -- "curl -sf -m 30 -H \"Authorization: Bearer \$$ENV\" https://postman-echo.com/headers | jq -r .headers.authorization"`. Exit `0`, and stdout equals `Bearer $PH`. Compare in the shell, as in `[ "$out" = "Bearer $PH" ]`, rather than reading it.
- **Audit line.** Run `control-hib hib log-json -- log -n 20 --json`. `.logs[]` has an entry with `"machine":"$S"`, `"site":"postman-echo.com"`, `"method":"GET"`, `"path":"/headers"` and `"secrets":["$SEC"]`.
- **Remove it.** Run `control-hib hib secret-rm -- secret rm "$SEC"`. Exit `0`, and stdout is `removed $SEC`. `secret ls --json` no longer lists it.
- **No leak.** Run `grep -rl -- "$VALUE" "$(control-hib env | sed -n 's/^EVIDENCE=//p')"`. No files match. Record the result with `control-hib note secrets "..."`.
- **Tidy.** Run `control-hib hib sec-rm -- rm "$S" --yes`.

## Gotchas

- Secrets are global. Every machine on the host, including other people's, gets `$ENV`, and traffic to the bound site from every machine goes through the proxy. Bind only to `postman-echo.com`, use a throwaway value, and remove it in the same session.
- Pass the value on stdin, never as an argument. The evidence file's `$` line records arguments.
- The placeholder looks like the value, sharing the `sk-...` shape. Agent and terminal tooling may mask either as `******`, so assert with shell comparisons, not by eye.
- `hib log` is the whole host's log, holding other people's machines and older e2e runs. Filter by `machine == $S`.
- Interception and `$ENV` can lag `secret add` by up to a minute. A curl sent before the issuer check passes may go out without a swap.
- Codex and GitHub logins show up as secrets such as `codex-access` or `github`. They belong to the user. Never remove or rebind them.
