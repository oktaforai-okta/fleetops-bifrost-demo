# Troubleshooting

Organised by **symptom**, because a symptom is all you have when you arrive here.

Almost every entry below has a symptom that points somewhere other than its cause. That is
why each one is written down: the expensive part was never the fix, it was believing the
error message.

Where a longer explanation already exists, this page links to it rather than restating it.

> **Unsupported sample code.** Nothing here is an Okta product and none of it carries an
> SLA. See [CONTRIBUTING.md](../CONTRIBUTING.md#support-expectations).

---

## The one check to run first

Before diagnosing anything else, establish whether the gateway's **live** tool registry is
populated. Most confusing failures in this repo are downstream of an empty registry, and
this is the only trustworthy way to see it.

```bash
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
```

Count what came back:

```bash
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' \
  | grep -o '"name":"[^"]*"' | wc -l
```

| Result | Meaning |
|---|---|
| **3 tools** | Registry is live. Go. |
| **`"tools":[]`** | Registry is empty. `make clean && make up`. Nothing else will work until this returns 3. |

The three names are `fleetops_command-dispatch_vehicle`, `fleetops_read-get_telemetry` and
`fleetops_read-list_routes`.

**Do not substitute any other readiness check.** In particular do not use
`GET /api/mcp/clients`, and do not use an HTTP status code. Both lie, for different reasons
covered below.

---

# Symptoms at the gateway

## `tool not found` on a tool you know exists

The single most expensive trap in this repo, because the symptom points nowhere near the
cause.

**What is happening.** Bifrost discovers an MCP client's tools when that client is **first
registered**, and caches the result in its sqlite store. On a plain restart it reloads the
clients from sqlite **without re-running discovery**. The live in-memory registry therefore
comes up empty while the database still holds the old answer.

The result is a gateway that reports health it does not have:

| Source | What it says | Truth |
|---|---|---|
| `GET /api/mcp/clients` | all three tools, client healthy | read from sqlite, says nothing about the live registry |
| `tools/list` against `/mcp` | `"tools":[]` | this is the live registry, and it is empty |
| every tool call | 404 `tool not found` | the consequence |

There is **no log line** saying discovery was skipped. You will check the plugin, then Okta,
then your bindings, and all three will be fine.

**See the contrast for yourself.** These two commands read from the two different places:

```bash
# sqlite's view: reports client names AND their cached bare tool names
curl -s http://localhost:8080/api/mcp/clients | grep -o '"name":"[^"]*"'

# the live registry's view: namespaced tool names, or [] if discovery never ran
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' \
  | grep -o '"name":"fleetops[^"]*"'
```

On a healthy stack the first prints both client names, each followed by the three bare tool
names cached for it, and the second prints three namespaced names:

```
"name":"fleetops_read"          "name":"fleetops_command-dispatch_vehicle"
"name":"dispatch_vehicle"       "name":"fleetops_read-get_telemetry"
"name":"get_telemetry"          "name":"fleetops_read-list_routes"
"name":"list_routes"
"name":"fleetops_command"
"name":"dispatch_vehicle"
"name":"get_telemetry"
"name":"list_routes"
```

**When the registry is empty, the left-hand output is completely unchanged and the right-hand
one prints nothing.** That asymmetry is the whole diagnosis. Both clients list all three tools
because both point at the same upstream server; only the namespaced form on the right tells
you what the gateway can actually route.

**The fix.**

```bash
make clean && make up
```

`make clean` runs `docker compose down -v`. **The `-v` is the part that matters**: it drops
the `bifrost-data` volume holding the sqlite store, which is what forces registration, and
therefore discovery, to happen from scratch. A `down` without `-v` leaves the store in place
and reproduces the same empty registry on the way back up.

Never use `docker compose restart`.

### Which container you may safely recreate

This distinction saves real time, because a full `make clean && make up` rebuilds the plugin
and re-renders the config.

| Container | Recreating it |
|---|---|
| `fleetops` (the upstream MCP server) | **Fine.** Costs a few seconds. Bifrost's registry is untouched |
| `bifrost` (the gateway) | **Loses the registry.** Requires `make clean && make up` |

So iterating on the MCP server is cheap:

```bash
docker compose up -d --build fleetops     # this service only
```

Naming the service limits Compose to it. `fleetops` has no dependencies of its own, so nothing
else is recreated, and `bifrost` keeps the registry it already has. Re-run the readiness check
afterwards anyway, because it costs nothing.

It is restarting **Bifrost** that is expensive.

**One other cause of the same symptom.** If you have never had a working registry on this
config, check `allow_connect_without_caller`. Left at `false`, registration itself is
refused, no tools are ever discovered, and every call fails as `tool not found` rather than
as anything resembling an authorization error.

```bash
grep -n 'allow_connect_without_caller' bifrost/config.json
```

It must be `true`. Why that is not a bypass is covered in
[../README.md](../README.md#why-allow_connect_without_caller-true-is-required-and-is-not-a-bypass).

## The MCP server rejects a token the plugin clearly minted

Reads as a minting bug. Almost never is one.

**Cause.** An `auth_type` other than `none`. `none` is the only value whose resolver returns
no headers. Every other auth type resolves upstream headers of its own, and those
**overwrite the `Authorization` header the plugin already set** on the connect request. The
plugin mints correctly, Bifrost replaces the header, and the upstream rejects a credential
the plugin never sent.

```bash
grep -n 'auth_type' bifrost/config.json
```

Every MCP client must read `"auth_type": "none"`. There are two of them in this config.

`per_user_headers` fails for a second, independent reason: it requires pre-stored per-caller
credentials and aborts the connect when it finds none. There are none to store, because the
credential is minted per call rather than kept anywhere.

Ignore any guidance that says caller context requires `per_user_headers`, `per_user_oauth`
or `token_exchange`. Full reasoning in
[../README.md](../README.md#auth_type-must-be-none).

## `wrong audience` from the MCP server

**Validate against the resource URL that was sent as `resource`, not against the
authorization server's `audiences` setting.** In this repo that means `FLEETOPS_AUDIENCES`
must contain the target's resource URL.

The server's own error text tells you this, and prints both sides:

```bash
docker compose logs fleetops | grep -i 'wrong audience' | tail -3
```

```
wrong audience: token is for [...], this server accepts ...
(this server validates aud against the resource url sent on the exchange,
 not the authorization server's audiences setting)
```

The check lives in `server/auth.go`, in `Validator.Validate`.

> **What is observed, and what is not established.** The issued token's `aud` is observed to
> equal the `resource` value sent on the exchange. That is **not** evidence that `resource`
> is what *determines* `aud`: both bindings here share one authorization server, so if that
> server's configured `audiences` holds the same string then `resource` and `audiences`
> predict the same `aud` and no observation here can separate them. The mechanism is an open
> question. The operational instruction, validate against the resource URL, is correct
> either way. See "Open question: what determines `aud`" in the
> [plugin README](https://github.com/oktaforai-okta/okta-bifrost-plugin#open-question-what-determines-aud),
> which also lists the two experiments that would settle it.

**Byte-compare the URLs.** A trailing slash, a case difference, or a hyphen where a colon
belongs all produce a mismatch. This is one of the few places where copying and pasting is
better than typing.

## `no caller identity token`

No `Authorization: Bearer` header reached the gateway. The caller's own token is the
**subject** of the exchange, so the gateway cannot proceed without it.

```bash
TOKEN=$(./scripts/get-caller-token.sh)
```

If that script itself fails, the failure is in step 1 of the exchange and has nothing to do
with the plugin. It prints Okta's response on stderr.

## A refusal naming a scope the call never requested

For example a `get_telemetry` call refused for lacking `task.dispatch`, which it never asked
for.

**Cause.** A verdict-cache key collision. The cache key was originally the authorization
server id alone, and both bindings in this demo sit on the **same** authorization server, so
a denial recorded for `fleetops_command` was reported back for `fleetops_read`.

**Fixed** at the root, in `Binding.verdictKey()` in `plugin/config.go` in the plugin repo.
The key is now the authorization server id plus the target resource URL plus the **sorted**
scope set, NUL-joined.

**Seeing this symptom means you are running a build that predates that fix.** It is
misleading rather than unsafe, but it sends whoever is debugging to the wrong object. Rebuild
the plugin:

```bash
cd ../okta-bifrost-plugin && make plugin PLATFORM=linux/arm64   # or linux/amd64
cd ../fleetops-bifrost-demo && make clean && make up
```

Note that grepping the `.so` for the symbol proves only that the **file** contains it. See
[A code change appears to have no effect](#a-code-change-appears-to-have-no-effect).

---

# Okta refuses

All of these come back as Okta's own `error` and `error_description`, passed through rather
than composed by the gateway or the plugin. That is deliberate: Okta's wording separates
cases that look identical from the outside.

Start with the driver, not the gateway. It runs the same exchange with nothing in the way,
so you see Okta's answer rather than a gateway's interpretation of it:

```bash
make demo          # both outcomes in one run
make demo-read     # expect ISSUED
make demo-command  # expect REFUSED, and that is correct, not a broken target
```

## `invalid_scope`, naming a scope

```
The following scopes are not allowed for this request: [task.dispatch].
```

**This is a permission refusal, and in this demo it is the intended result** for
`dispatch_vehicle`.

When it is *not* intended, the fix is on the agent's managed **connection**, not on the
authorization server. Publishing a scope on the server and putting it on the server's policy
rule is **not sufficient**. The connection's scope list is what an exchange is validated
against. A server fully configured for a scope is still refused if the connection does not
list it, and the message reads like a server misconfiguration.

**When you see this, check the connection.** [RUNBOOK.md step 6](RUNBOOK.md#6-connect-the-acting-agent-to-the-target)
has the console path and the API call.

**Okta does not down-scope.** One ungrantable scope fails the **whole** request rather than
returning the grantable subset. So an over-broad ask is a clean, legible failure rather than
a partial success you might accidentally accept. The practical consequence: adding one extra
scope to a binding that already works can break it entirely.

## `invalid_target`

No **ACTIVE** connection matches the `resource` value that was sent. Reads like a permission
problem. Is almost always configuration, and usually one of two things:

1. **The URL does not match byte for byte.** Compare `FLEETOPS_COMMAND_RESOURCE_URL` against
   the resource indicator on the connection. Trailing slash, case, `api://` against
   `https://`.
2. **The connection is staged rather than ACTIVE.**

## `access_denied`, policy evaluation failed

Either the caller is not a listed client of that authorization server, or the acting agent is
deactivated.

Two things about the client list that cost time:

- **The `clients` condition is at POLICY level, not on the rule.**
- **It must name the caller**, meaning the party presenting credentials at that endpoint. On
  the Acting Agent's server that is the Caller Service. On the Target's server it is the
  Acting Agent.

**A related trap with a different symptom.** A rule whose `people.groups` names a specific
user group will never fire at all, because an agent is a workload principal and not a user.
Use EVERYONE. See [RUNBOOK.md step 2](RUNBOOK.md#access-policies-and-rules).

## `invalid_client`

Reads like a broken credential. It is sometimes a policy decision instead, which is what
makes this one worth reading twice.

| Where it happened | Likely cause |
|---|---|
| Step 1, the caller minting for itself | The Caller Service's client id or secret |
| Steps 2 and 3, inside the plugin | The agent's key does not match the key registered on the agent |
| Either | **The agent is DEACTIVATED** |

That last row is the surprise. A deactivated agent has been observed to fail as
`invalid_client`, which reads as a credential problem rather than as the policy decision it
actually is. Before re-issuing keys, confirm the agent is ACTIVE in
**Directory > AI Agents**.

> **The two error codes disagree, and that is worth knowing rather than resolving.**
> `access_denied` is attributed to a deactivated agent elsewhere in this repo, in
> [RUNBOOK.md](RUNBOOK.md#reading-a-failure), while the `invalid_client` behaviour above was
> observed directly. We have not established which conditions produce which code. So treat a
> deactivated agent as a candidate cause of **either**, and check agent status before you
> start regenerating credentials, because that check is free and re-issuing a key is not.

If the agent is active and this is coming from the plugin, the key is the thing to check. The
private key is shown exactly once when the key pair is generated; if it was missed, generate
a new pair rather than trying to recover the old one.

## `'subject_token' is invalid`

The caller presented the wrong kind of token. It must be an **access token** from a **custom**
authorization server, carrying a resource-scoped `aud`.

Two things produce this:

- an ID token was presented instead of an access token, or
- the token came from the **org** authorization server rather than a custom one.

`./scripts/get-caller-token.sh` mints the right shape. Compare against it.

---

# The plugin does not load, or does not behave as built

## The plugin does not load, with no useful error

A native Go plugin is a `.so` shared object, and Go refuses to load one unless the plugin and
the host binary agree on **three** things. All three, not two.

| Must match | Note |
|---|---|
| Go version | Currently `1.27.0` |
| **Every** shared dependency version | `bifrost/core` v1.8.4 against a v1.8.3 host does not load. Patch versions count |
| Architecture | An arm64 `.so` will not load into an amd64 host |

**Do not guess any of them.** Read them out of the image you are actually loading into:

```bash
cd ../okta-bifrost-plugin
make compat BIFROST_IMAGE=bifrost:dynamic-local
```

```
inspecting bifrost:dynamic-local ...
  go:   go1.27.0
  core: v1.8.3
```

Then pin and rebuild:

```bash
make pin BIFROST_CORE=<the core version it printed>
make plugin PLATFORM=linux/arm64        # or linux/amd64
```

**Confirm the plugin actually loaded** before believing anything else:

```bash
docker compose logs bifrost | grep "plugin status"
```

```
plugin status: okta-agent-identity - active
```

Several other plugins report status on the same lines. Look for `okta-agent-identity`
specifically. Anything other than `active` for that name, or its absence from the list,
means the `.so` did not load.

## `plugin.Open: Dynamic loading not supported`

The host Bifrost binary is **statically linked**, and no Go plugin can load into one. This is
[documented behaviour on Maxim's side](https://docs.getbifrost.ai/plugins/building-dynamic-binary),
not a defect.

**Bifrost's published images are statically linked.** Running a plugin means building your
own binary:

```bash
docker build -f bifrost/Dockerfile.dynamic -t bifrost:dynamic-local .
```

Then set `BIFROST_IMAGE=bifrost:dynamic-local` in `.env`.

`bifrost/Dockerfile.dynamic` differs from Bifrost's own build by exactly one linker flag:
it drops `-extldflags '-static'`. It also **fails the build deliberately** if the output
comes out static, precisely so this runtime symptom does not have to be diagnosed later.

If you did build your own image and still see this, check that `.env` actually points at it:

```bash
grep -n 'BIFROST_IMAGE' .env
docker compose config | grep -A1 'image:'
```

## A code change appears to have no effect

You edited the plugin, rebuilt, and the running gateway behaves as before.

**Cause.** `plugin.Open` runs **once**, at Bifrost startup. The process runs whatever the
`.so` contained at that moment. Replacing the file on disk afterwards changes nothing about
the already-loaded plugin, and no reload happens.

**The trap inside the trap.** Grepping the `.so` for a symbol confirms the **file**, not the
running process:

```bash
grep -ca verdictKey ../okta-bifrost-plugin/bin/okta-agent-identity-arm64.so
```

A non-zero count here tells you the artifact on disk contains the symbol. It tells you
nothing about what Bifrost loaded. This is a genuinely convincing false positive.

**The `-a` is not optional.** Without it, grep treats the `.so` as binary, prints nothing and
exits 1, so a symbol that is present reads as absent. On macOS:

```
$ grep -c verdictKey bin/okta-agent-identity-arm64.so
$ echo $?
1
$ grep -ca verdictKey bin/okta-agent-identity-arm64.so
4
```

So this check can mislead you in **both** directions: a false positive about the running
process, and a false negative about the file. Prefer the two checks below.

**Distinguish it properly.** Compare the `.so`'s mtime against the container's start time.
Read the mtime from **inside** the container, because that prints UTC and so does
`docker inspect`, which makes the two directly comparable. A host `ls -l` prints local time
and invites an off-by-timezone conclusion:

```bash
docker inspect -f '{{.State.StartedAt}}' fleetops-bifrost-demo-bifrost-1
docker exec fleetops-bifrost-demo-bifrost-1 \
  ls -l --full-time /etc/bifrost/plugins/
```

```
2026-09-01T18:52:48.039234471Z
-rw-r--r-- 1 root root 25292328 2026-09-01 15:01:59 +0000 okta-agent-identity-arm64.so
```

An `.so` mtime **older** than the container start time means the running process has that
file's contents. An mtime **newer** than the start time means the file on disk has moved on
and the process has not. The container ships BusyBox `ls`, so use `--full-time`;
`--time-style=full-iso` is a GNU option and is rejected there.

**Better still, test behaviourally.** Timestamps establish that a rebuild landed. They do not
establish that the code does what you intended. Prefer the driver, which calls the plugin's
own exchange code directly and therefore cannot be fooled by a stale load:

```bash
make demo-read
```

**The fix is always the same.** A new `.so` needs a fresh Bifrost process, and a fresh
Bifrost process needs the registry rebuilt:

```bash
make clean && make up
```

## `PLATFORM` mismatch, including the case where the build succeeds and is still wrong

This one deserves its own entry because its worst failure mode is silent.

The demo Makefile derives `PLATFORM` from the host on purpose, because
`scripts/render-config.sh` **independently** derives the plugin filename from `uname -m`. Two
places derive the same fact, so they must agree.

| Starting state | What happens when they disagree |
|---|---|
| Clean checkout, only one `.so` present | The build produces one architecture, the rendered config names the other, and the plugin **fails to load**. Loud, and therefore cheap |
| An `.so` of the *other* architecture already on disk | **The build SUCCEEDS while being wrong.** The artifact nobody loads is rebuilt, Bifrost loads the stale file already there, and a plugin source change looks applied when it is not |

That second row is the dangerous one, and it is easy to end up in: after building for both
architectures once, `bin/` holds both files permanently.

**Check which architecture is in `bin/` and which one the rendered config names:**

```bash
ls -l ../okta-bifrost-plugin/bin/
grep -n 'okta-agent-identity' bifrost/config.json
```

```
okta-agent-identity-amd64.so
okta-agent-identity-arm64.so
"path": "/etc/bifrost/plugins/okta-agent-identity-arm64.so"
```

With both files present, the filename in the config is the only thing deciding which one is
loaded, and a `make plugin PLATFORM=...` for the other architecture will report success while
changing nothing that runs.

**Cost when it silently falls back to emulation:** the driver measured **39.6s under
emulation against 9.3s native** for the same run. Around thirty seconds of dead air per run,
which is a useful tell in itself.

**Do not "fix" the plugin repo to derive `PLATFORM` from the host too.** The two repos want
different answers and both are right. Here, the build and the rendered config both run on
this machine, so the host is the correct source. In the plugin repo the artifact must match
the architecture of the **Bifrost image it will be loaded into**, which is frequently not the
build machine: an Apple Silicon laptop producing a plugin for amd64 Linux is the normal case.
That is why the plugin repo defaults to `linux/amd64`. See
[../README.md](../README.md#step-2-build-the-plugin-matched-to-that-host).

---

# Configuration and environment

## `make config` stops on a missing key file

```
missing secrets/sentinel-intake-key.jwk
```

**There is one canonical name: `secrets/sentinel-intake-key.jwk`.** It is what
`bifrost/config.template.json` names as `private_key_jwk_file`, what
`scripts/render-config.sh` checks for, and what the Makefile's `AGENT_KEY_FILE` points at for
the `make demo-*` targets.

```bash
cp /path/to/downloaded.jwk secrets/sentinel-intake-key.jwk
chmod 600 secrets/sentinel-intake-key.jwk
```

`render-config.sh` deliberately checks the **same** path the template names. Checking a
different file would be worse than not checking: it would pass, and the plugin would then
fail at `Init` on a path the script had just declared fine.

> **Stale references to be aware of.** An older name, `secrets/agent-key.jwk`, still appears
> in `.env.example`, `secrets/README.md`, and in comments in the Makefile and
> `render-config.sh`. It is not the name that is loaded. If both files happen to exist on
> your machine, things work for the wrong reason and will stop working on a clean checkout.
> Treat `bifrost/config.template.json` as the source of truth:
> ```bash
> grep -n 'private_key_jwk_file' bifrost/config.template.json
> ```

## `render-config.sh` requires two variables it does not use

`OKTA_READ_LANE_AS_ID` and `FLEETOPS_READ_RESOURCE_URL` must be **non-empty** or rendering
fails, and they are substituted **nowhere**.

Verify it yourself rather than taking this on trust:

```bash
# validated: both appear in the script's VARS list
grep -n 'READ_LANE_AS_ID\|FLEETOPS_READ_RESOURCE_URL' scripts/render-config.sh

# substituted: neither appears in the template
grep -o '\${[A-Za-z_][A-Za-z0-9_]*}' bifrost/config.template.json | sort -u
```

The template references only `OKTA_DOMAIN`, `OKTA_AGENT_ID`, `OKTA_AGENT_RESOURCE_URL`,
`OKTA_COMMAND_LANE_AS_ID`, `FLEETOPS_COMMAND_RESOURCE_URL` and `PLUGIN_ARCH`.

**Set them anyway.** They are a leftover from the agent-to-API topology, where read and
command were separate lanes. In the agent-to-agent shape this repo runs, give them the
Target's values. The value has no effect; only its presence does. Renaming them is a pending
cleanup, noted in [RUNBOOK.md](RUNBOOK.md#0-decide-which-topology-you-are-building).

## `.env` cannot be shell-sourced

```
task.read: command not found
```

or a JSON parse error some distance from anything you changed.

**Cause.** At least one value contains a space. Sourcing runs `.env` as a shell script, so:

```
OKTA_SERVICE_CLIENT_SCOPE=agent.invoke task.read
```

parses as "run the command `task.read` with that variable set". Several variables here are
documented as space separated by design, including `OKTA_SERVICE_CLIENT_SCOPE` and
`SENTINEL_TASKING_SCOPES`, so this is not a mistake in the file.

Docker's `--env-file` accepts the same line happily, splitting on the first `=` and treating
the rest as literal bytes. So the file is known-good everywhere else and breaks only in the
shell.

**Use the parser.** `scripts/load-env.sh` **parses** rather than sources, deliberately:

```sh
. ./scripts/load-env.sh
```

> ### Do not "fix" this by quoting the value
>
> It looks like the obvious repair and it breaks the containers.
>
> This repo passes `.env` to **`docker run --env-file`**, in the Makefile's `DRIVER`
> definition and in the containerised sentinel API command. `docker run --env-file` takes
> everything after the first `=` **literally, including quote characters**, so
> `SCOPE="agent.invoke task.read"` becomes a scope value with quotes embedded in it, and
> Okta refuses a scope that does not exist.
>
> Worse, it would then be inconsistent: `load-env.sh` **does** strip a wholly-quoted value's
> outer quotes, so the shell path and the container path would disagree about the same line.
> One broken path is easier to debug than two paths that differ.
>
> Note that Docker **Compose** does strip quotes when interpolating `${VAR}` from `.env`,
> which is why this is easy to get wrong: the same quoted line behaves differently depending
> on which of the two mechanisms consumes it. A comment in `load-env.sh` saying "docker
> strips them too" is true of Compose and not of `docker run --env-file`.
>
> Leave the values unquoted and use the parser.

## The agent key must never go in `.env`

A JWK pasted unquoted into an env file is destroyed the moment that file is sourced: the
double quotes are stripped and the commas are treated as brace expansion. The result is a
mangled key and a JSON parse error nowhere near the cause.

The key lives in `secrets/`, read directly from disk. That also means the rendered Bifrost
config never contains key material, so `bifrost/config.json` is not itself a secret in the
key-material sense. It does still hold every tenant identifier, and it is gitignored.

---

# TLS, ports and things that look like outages

## `certificate signed by unknown authority`

```
tls: failed to verify certificate: x509: certificate signed by unknown authority
```

Your network intercepts TLS and re-signs it with a root the container does not trust. This
affects Bifrost's own startup fetches and, more importantly, the plugin's calls to Okta.

**What works is `SSL_CERT_FILE` pointing at a path that is actually mounted in the container
that needs it.** For the Compose stack that is `/certs/...`, because `docker-compose.yml`
mounts `./certs` at `/certs` in both services and maps `.env`'s `EXTRA_CA_BUNDLE` onto the
container's `SSL_CERT_FILE`:

```
EXTRA_CA_BUNDLE=/certs/ca-bundle.crt
```

Building the bundle is covered in [../certs/README.md](../certs/README.md). It must contain
your proxy's root **and** the normal public roots, because it replaces the default trust store
rather than adding to it.

> ### Do not chase `EXTRA_CA_BUNDLE` outside the Compose stack
>
> `EXTRA_CA_BUNDLE` is **not** a variable anything reads directly. It is only meaningful
> because `docker-compose.yml` translates it into `SSL_CERT_FILE`. Confirm that for yourself:
> ```bash
> grep -n 'CA_BUNDLE\|SSL_CERT_FILE' docker-compose.yml
> ```
>
> Any container started outside Compose gets no such translation. The containerised sentinel
> API in [../sentinel/README.md](../sentinel/README.md#running-it-locally) is the case that
> bites: it passes `--env-file ../../.env`, so `EXTRA_CA_BUNDLE=/certs/ca-bundle.crt` is set
> in the container and **does nothing**, because nothing maps it to `SSL_CERT_FILE` and
> `/certs` is not mounted there either. That command mounts only `$HOME/code` at `/w`.
>
> For that container, set `SSL_CERT_FILE` explicitly, to a path inside a directory that is
> genuinely mounted. `$HOME/code` is mounted at `/w`, so:
> ```
> -e SSL_CERT_FILE=/w/fleetops-bifrost-demo/certs/ca-bundle.crt
> ```
>
> **Verify rather than assume.** Ask the container itself, and resolve the variable inside it
> so the shell that expands `$SSL_CERT_FILE` is the container's:
> ```bash
> docker exec <container> printenv SSL_CERT_FILE
> docker exec <container> sh -c 'ls -l "$SSL_CERT_FILE"'
> ```
>
> A correctly configured container answers with a file that exists:
> ```
> /w/fleetops-bifrost-demo/certs/ca-bundle.crt
> -rw-r--r-- 1 root root 183327 ... /w/fleetops-bifrost-demo/certs/ca-bundle.crt
> ```
>
> And the same container will happily report `EXTRA_CA_BUNDLE=/certs/ca-bundle.crt` while
> having no `/certs` at all:
> ```bash
> docker exec <container> printenv EXTRA_CA_BUNDLE   # /certs/ca-bundle.crt
> docker exec <container> ls /certs                  # No such file or directory
> ```
> The Compose containers are the opposite case: there `/certs` **is** mounted, and
> `SSL_CERT_FILE` resolves to `/certs/ca-bundle.crt`.
>
> A variable set to a path that does not exist inside the container produces exactly the same
> unknown-authority error as having configured nothing at all, which is why this is worth
> checking directly rather than reasoning about.

Naming the variable `EXTRA_CA_BUNDLE` in `.env` rather than `SSL_CERT_FILE` is itself
deliberate: Compose gives the host shell precedence over `.env`, and `SSL_CERT_FILE` is
commonly already exported on a developer machine, where a stale host value would silently win
and point the container at a host path.

## The web app says the API is unreachable, or the gateway looks down when it is healthy

**Cause.** A containerised API defaults to the **container's own** localhost, which is not the
host's. A perfectly healthy Bifrost on the host is unreachable from inside the container, and
the bare transport error reads on a shared screen as "their gateway is down".

`SENTINEL_BIFROST_URL` defaults to `http://localhost:8080/mcp`, which is correct for an API
run natively next to the Compose stack and wrong for one in a container.

**Fix.** Two halves, and both are required:

```
-e SENTINEL_BIFROST_URL=http://host.docker.internal:8080/mcp
--add-host=host.docker.internal:host-gateway
```

The variable alone resolves to nothing without the `--add-host`. The API already detects this
case and prints the fix next to the symptom; see `loopbackHint` in `sentinel/api/gateway.go`.

**Confirm the gateway is genuinely up before chasing this**, from the host:

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/
```

Then confirm reachability from inside the container, which is the thing actually in question:

```bash
docker exec <container> wget -qO- http://host.docker.internal:8080/api/mcp/clients | head -c 80
```

> `SENTINEL_BIFROST_URL` is read by `sentinel/api/config.go` but is **not** listed in
> `sentinel/.env.example`. Do not conclude from its absence there that it is unsupported.

## Port 8000 answers, but not with what you expect

**Port 8000 is commonly occupied by an unrelated service** and, being a uvicorn app, answers
a cheerful HTTP 200. So "something is listening and returning 200" is not evidence that it is
yours.

`sentinel/README.md` still suggests 8000 for the static frontend. **Use 8800 instead:**

```bash
cd sentinel/web
python3 -m http.server 8800
```

**Identify the squatter rather than guessing.** The `Server` header is the giveaway:

```bash
lsof -nP -iTCP:8000 -sTCP:LISTEN
curl -sI http://localhost:8000/ | grep -i '^server:'
```

```
server: uvicorn
```

And confirm what you started is what you reached, by content rather than by status code:

```bash
curl -s http://localhost:8800/ | grep -o '<title>[^<]*</title>'
```

```
<title>Sentinel chain of custody</title>
```

Ports in use by this project: **8080** Bifrost, **8090** the sentinel API, **8800** the
suggested static server. The `fleetops` MCP server listens on 8080 inside its own container
and is not published to the host.

## A 200 from the Bifrost console proves nothing

The admin console is a single-page app. It returns **HTTP 200 on every path** and renders its
own 404 client-side. So a status code cannot tell you whether a route exists.

Demonstrate it:

```bash
for p in / /ui /workspace /workspace/plugins /this-route-does-not-exist-abc123; do
  printf '%-38s ' "$p"
  curl -s -o /dev/null -w '%{http_code}\n' "http://localhost:8080$p"
done
```

```
/                                      200
/ui                                    200
/workspace                             200
/workspace/plugins                     200
/this-route-does-not-exist-abc123      200
```

All 200, including the nonsense one. The bundle even ships a dedicated `not-found` chunk,
which is the client-side 404 doing the work a status code would normally do.

**The console lives under `/workspace/...`, not `/ui`.** Useful routes:

| Route | |
|---|---|
| `/workspace/plugins` | plugin list and status |
| `/workspace/mcp-registry` | registered MCP clients |
| `/workspace/mcp-sessions` | live sessions |
| `/workspace/logs/mcp-logs` | MCP request log |
| `/workspace/logs` | where the console's own 404 sends you |

**Never use a status code as a health check here.** Use `tools/list` against `/mcp`, at the
top of this page.

---

# Confirming it really works

Three tiers, each ruling out a different way of fooling yourself, are in
[../README.md](../README.md#how-to-verify-it-is-really-working). The independent accounts of
a single call, for showing a sceptic, are in [PROVING-IT.md](PROVING-IT.md).

The two commands worth having in muscle memory:

```bash
# the plugin loaded
docker compose logs bifrost | grep "plugin status" | grep okta-agent-identity

# the refused call never reached the resource server. Expect 0
docker logs fleetops-bifrost-demo-fleetops-1 | grep -c "tools/call.*dispatch_vehicle"
```

> **The count trap.** Grepping the bare tool name gives `1`, not `0`, because the server
> prints a startup banner listing the tools it serves. That line is not a call. Grep for
> `tools/call.*dispatch_vehicle` as above.

An accepted call looks like this, with the delegation chain the server read out of the token
itself:

```bash
docker compose logs fleetops | grep ACCEPTED | tail -1
```

```
tools/call list_routes ACCEPTED, scope task.read,
chain 0oa<your-service-id> (service) <- wlp<your-agent-id> (ai_agent), jti AT.<token-id>
```

> **`act` and `sub_profile` are not in Okta's published developer documentation.** They are
> verified empirically by decoding real tokens from a live tenant, and they behave
> consistently. Do not present them to anyone as documented behaviour, and if you script an
> assertion on them, treat their shape as observed behaviour that could change rather than as
> a contract.

---

# Quick reference

| Symptom | Go to |
|---|---|
| `tool not found` | [Tool not found](#tool-not-found-on-a-tool-you-know-exists) |
| Server rejects a token the plugin minted | [auth_type](#the-mcp-server-rejects-a-token-the-plugin-clearly-minted) |
| `wrong audience` | [wrong audience](#wrong-audience-from-the-mcp-server) |
| `no caller identity token` | [no caller identity token](#no-caller-identity-token) |
| Refusal naming an unrequested scope | [verdict cache](#a-refusal-naming-a-scope-the-call-never-requested) |
| `invalid_scope` | [invalid_scope](#invalid_scope-naming-a-scope) |
| `invalid_target` | [invalid_target](#invalid_target) |
| `access_denied` | [access_denied](#access_denied-policy-evaluation-failed) |
| `invalid_client` | [invalid_client](#invalid_client) |
| `'subject_token' is invalid` | [subject_token](#subject_token-is-invalid) |
| Plugin does not load | [three axes](#the-plugin-does-not-load-with-no-useful-error) |
| `Dynamic loading not supported` | [static linking](#pluginopen-dynamic-loading-not-supported) |
| Code change has no effect | [plugin.Open runs once](#a-code-change-appears-to-have-no-effect) |
| Build succeeds, behaviour unchanged | [PLATFORM](#platform-mismatch-including-the-case-where-the-build-succeeds-and-is-still-wrong) |
| `missing secrets/...jwk` | [make config](#make-config-stops-on-a-missing-key-file) |
| Unused variables demanded | [render-config.sh](#render-configsh-requires-two-variables-it-does-not-use) |
| `command not found` from `.env` | [not shell-sourceable](#env-cannot-be-shell-sourced) |
| `unknown authority` | [TLS](#certificate-signed-by-unknown-authority) |
| API or gateway unreachable | [container localhost](#the-web-app-says-the-api-is-unreachable-or-the-gateway-looks-down-when-it-is-healthy) |
| Wrong thing on port 8000 | [ports](#port-8000-answers-but-not-with-what-you-expect) |
| Console 200 on a bad route | [SPA](#a-200-from-the-bifrost-console-proves-nothing) |
