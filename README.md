# Fleet Ops

A runnable demonstration that **Okta can be the policy decision point** for AI agent tool
calls while **Bifrost remains the enforcement point**, without forking Bifrost and without
replacing the gateway.

Fleet Ops is a deliberately neutral stand-in for a real operational system. It has the
shape that makes agent identity matter: cheap reads on one side, and on the other a command
that moves a physical asset. Reading telemetry and dispatching a vehicle are not the same
risk, so they do not carry the same permission.

---

# Part 1. For readers who will never run this

Plain language, no jargon, no code. Skip to [Part 2](#part-2-for-developers) if you are
here to build it. A longer version of this section, with a diagram and the questions you
are likely to be asked, is in [docs/HOW-IT-WORKS.md](docs/HOW-IT-WORKS.md).

### The problem

An AI agent cannot do anything useful until it is allowed to reach a real system. Today
almost every deployment grants that access the same way: one shared account, used by
everything. Every call the agents make arrives looking identical.

Three consequences follow, and they compound.

- **You cannot tell which agent did what.** The log records the shared account. Twelve
  agents means twelve suspects.
- **You cannot stop one without stopping all of them.** So in practice nobody stops any of
  them, because the cost is everything going dark.
- **Permissions drift to the widest need.** The shared account has to cover everything
  anyone might do, so read-only agents carry write access they never use.

### What changes

Every call now carries two separate facts: **who asked**, and **who acted**. A service
starts the work. An agent carries it out. The receiving system is told both, and can log
both, and can decide on both. The credential that carries those facts is issued by Okta one
call at a time, and it expires in minutes rather than sitting in a config file.

### The division of labour

**Okta decides.** It holds the policy and answers one question: would I issue this agent a
credential for this thing, right now.

**Bifrost enforces.** It asks that question on every call and obeys the answer. It holds no
policy of its own.

### Why the gateway is load-bearing, not incidental

A credential that has been issued cannot be taken back. That is not a flaw in Okta, it is
how access tokens work everywhere. The credential is a signed statement that was true when
it was made, and nothing can reach out and un-say it.

So when you deactivate a misbehaving agent, two things are true at once. Okta will not
issue it anything new, instantly. And the credential it is already holding keeps working
until it expires on its own.

Closing that gap needs something that **keeps asking**, gating every call on a recent answer
rather than checking once per session. The only component that sees every call is the gateway.
That is why the gateway is not merely a convenient place to put this. It is the only place it
can go.

### What it does not do

Stated plainly, because someone who believes a wider claim and later finds the narrower
reality will trust none of it.

- **No per-object permissions.** It can say an agent may dispatch. It cannot say whether it
  may dispatch *this particular* vehicle. Object-level rules need a separate fine-grained
  authorization layer above this one.
- **Credentials are bearer credentials.** Anyone holding one can use it. They are protected
  by being short-lived and re-checked, not by being unstealable.
- **No sender-constrained tokens (DPoP).** Not implemented.
- **Narrowing permissions applies to the next session,** not the next call. Turning an agent
  off is caught on the next call. Reducing what it may do is picked up when the connection
  is next established.
- **This is a demonstration.** It proves the integration path works. It is not hardened,
  monitored, or supported.

---

# Part 2. For developers

## What is proven, live

The following ran against a real Okta tenant, through Bifrost with the plugin loaded, to
the MCP server in this repo.

| Tool call | Scope required | Agent granted it | Outcome |
|---|---|---|---|
| `get_telemetry` | `task.read` | yes | **Succeeded.** The server returned data and printed the delegation chain it read out of the token |
| `dispatch_vehicle` | `task.dispatch` | no, never granted | **Refused by Okta:** `invalid_scope: The following scopes are not allowed for this request: [task.dispatch].` |

**Nothing in the tenant changed between those two calls.** Repeatable in either order, as
many times as you like. That pairing is the demonstration: a success alone only proves the
plumbing works, whereas one agent allowed one thing and refused another with nothing
altered in between shows a decision being made.

The issued token carries an `act` claim naming both parties, with a `sub_profile` on each
level typing it as `service` or `ai_agent`. Two distinct principals on one credential: the
service that asked, and the agent that acted.

> **Sourcing, stated exactly.** `act` and `sub_profile` are **not** in Okta's published
> developer documentation. We verified them empirically by decoding real tokens from a live
> tenant. They behave consistently. Do not present them to a customer as documented
> behaviour.

## Repo layout

| Path | What it is |
|---|---|
| `server/` | The MCP server. Validates every token itself, so bypassing the gateway does not bypass authorization |
| `bifrost/` | Gateway config template, and `Dockerfile.dynamic`, which builds a plugin-capable Bifrost |
| `driver/` | Terminal runner for the exchange with no gateway in the way. Calls the plugin's own exchange code |
| `sentinel/` | Separate web demo of an agent-to-agent chain of custody. See `sentinel/README.md` |
| `scripts/` | Config rendering, `.env` parsing, and a one-line caller-token minter |
| `secrets/` | The agent's private key JWK. Gitignored |
| `docs/RUNBOOK.md` | The Okta setup, step by step |
| `docs/HOW-IT-WORKS.md` | The non-developer explainer |
| `docs/PROVING-IT.md` | How to show this is real, from Bifrost's log, the resource server's log, and Okta. Read this before demonstrating to anyone sceptical |
| `docs/TROUBLESHOOTING.md` | Organised by symptom, because that is all you have when you arrive. Every entry is a failure that was actually hit, and most of them point somewhere other than their cause |
| `CONTRIBUTING.md` | Layout, build and test, what must never be committed, and what support to expect |

## If you are adopting this rather than running it

This repo is a demonstration. The plugin is the reusable part and it lives in a sibling
repo, which is where the adoption documentation is:

| Document | For |
|---|---|
| [`okta-bifrost-plugin/docs/INTEGRATION.md`](https://github.com/oktaforai-okta/okta-bifrost-plugin/blob/main/docs/INTEGRATION.md) | Putting this into **your** Bifrost, in the order you will hit it, including the decisions rather than just the commands |
| [`okta-bifrost-plugin/docs/ARCHITECTURE.md`](https://github.com/oktaforai-okta/okta-bifrost-plugin/blob/main/docs/ARCHITECTURE.md) | Reviewing or extending it. Why there are exactly two places the plugin can act |
| [`okta-bifrost-plugin/SECURITY.md`](https://github.com/oktaforai-okta/okta-bifrost-plugin/blob/main/SECURITY.md) | Threat model, and what this does **not** protect against |
| [`okta-bifrost-plugin/docs/PRODUCTION.md`](https://github.com/oktaforai-okta/okta-bifrost-plugin/blob/main/docs/PRODUCTION.md) | The honest distance between this demo and something you would run |

The plugin lives in a **sibling repo**, because it is reusable and this demo is not. Clone
both next to each other.

```bash
git clone https://github.com/oktaforai-okta/okta-bifrost-plugin
git clone https://github.com/oktaforai-okta/fleetops-bifrost-demo
```

## Prerequisites

| | |
|---|---|
| Docker | Required. Nothing here needs a local Go toolchain |
| An Okta tenant | With the AI agent features enabled. See [docs/RUNBOOK.md](docs/RUNBOOK.md) |
| Platform | Linux or macOS. Go plugins do not work on Windows |

---

## Step 1. Build a plugin-capable Bifrost

**Do this first.** It is the step that surprises people.

**Bifrost's published images are statically linked and cannot load any Go plugin.** Not
this one, not any. Go's plugin system requires a dynamically-linked host binary. This is
[documented behaviour on Maxim's side](https://docs.getbifrost.ai/plugins/building-dynamic-binary),
not a defect and not something to file a bug about.

So running a plugin means building and maintaining your own Bifrost binary. That is a real
operational cost, and it is worth knowing before choosing the plugin route.

```bash
cd fleetops-bifrost-demo
docker build -f bifrost/Dockerfile.dynamic -t bifrost:dynamic-local .
```

Then point Compose at it, in `.env`:

```
BIFROST_IMAGE=bifrost:dynamic-local
```

> **`.env.example` ships the WRONG default here, and you must change it.** It carries
> `maximhq/bifrost:latest`, which is one of the published statically-linked images and
> therefore cannot load any Go plugin at all.
>
> **This is the most dangerous misconfiguration in the repo**, because it does not look like a
> failure. The gateway comes up healthy, tools register, and every tool call **succeeds with no
> authorization whatsoever.** A demo in that state appears to be working while proving nothing.
>
> After `make up`, confirm the plugin is actually deciding rather than merely absent: run the
> refusal. If `dispatch_vehicle` **succeeds**, the plugin is not loaded. A working system
> refuses it.

The only difference between that Dockerfile and Bifrost's own is dropping
`-extldflags '-static'` from the link step. Everything else about the build is theirs. The
build **fails deliberately** if the result comes out statically linked, because the runtime
symptom is `plugin.Open: Dynamic loading not supported`, which does not obviously point at
a linker flag.

Two things that Dockerfile also does, and why:

- **Builds Bifrost's real admin console.** A `ui-builder` stage on the same Node image their
  own Dockerfile uses runs `npm ci` and their build script, sharing one clone with the Go
  stage so the console and the server can never come from different commits. An earlier
  version of this file stubbed the directory with a placeholder instead, to keep Node out of
  the build; that is no longer what happens, and **you do get the console.** It is at
  `http://localhost:8080/workspace/dashboard`, with `/workspace/plugins` showing the gateway's
  own view of this plugin. Note the console has **no login**.

  The build asserts the console is genuinely built rather than trusting the copy, because
  `//go:embed all:ui` is satisfied by ANY non-empty directory: a failed UI build would
  otherwise yield a server that boots and serves a blank page. The check looks for the hashed
  asset reference a real build emits.
- **Optionally appends `certs/ca-bundle.crt` to the build trust store,** for networks that
  intercept TLS. Without it the `git clone` inside the build fails with an
  unknown-authority error.

## Step 2. Build the plugin, matched to that host

A `.so` must match the host on **three** axes. All three, not two.

| Must match | Why |
|---|---|
| Go version | Go refuses to load a plugin built by a different toolchain version |
| **Every** shared dependency version | `bifrost/core` v1.8.4 against a v1.8.3 host does not load. Patch versions count |
| Architecture | Docker on Apple Silicon defaults to arm64, which will not load into an amd64 host |

Do not guess any of them. Read them out of the image you just built.

```bash
cd ../okta-bifrost-plugin
make compat BIFROST_IMAGE=bifrost:dynamic-local
```

That prints the Go version and the `bifrost/core` version the host binary was actually
built with. Then pin and build:

```bash
make pin BIFROST_CORE=<the core version it printed>
make plugin PLATFORM=linux/arm64     # or linux/amd64
```

The artifact lands at `bin/okta-agent-identity-<arch>.so`, with the architecture in the
filename so two builds cannot be confused for each other. Compose mounts that whole `bin/`
directory read-only, so a rebuild needs no change here.

**From this repo you do not need to pass `PLATFORM` at all,** because everything runs on one
host. `make up` and `make plugin` derive it from the host, which is right *here* only because
`scripts/render-config.sh` independently derives the plugin filename from `uname -m`, so the two
must agree locally.

**Building from the plugin repo directly is different, and host-derivation would be wrong
there.** The `.so` must match the architecture of the **Bifrost image you are loading into**,
which is often not the machine you are building on: an arm64 Mac producing a plugin for amd64
Linux needs `linux/amd64`. So set `PLATFORM` to the **target's** architecture. The plugin repo's
`linux/amd64` default is deliberate and correct for that reason.

**Do not "fix" the plugin repo to derive `PLATFORM` from the host as well.** The two repos want
different answers and both are right. Here, the build and the rendered config both run on this
machine, so the host is the correct source. In the plugin repo the artifact has to match the
architecture of the **Bifrost image it will be loaded into**, which is frequently not the
machine it was built on: building on an Apple Silicon laptop for amd64 Linux is the normal
case, and host-derivation would hand you an arm64 `.so` that cannot load there.

> **The architecture mismatch has a failure mode worse than not loading.** On a clean checkout
> the two sides disagree and the plugin simply fails to load, which is at least loud. On a
> machine where an `.so` of the *other* architecture already exists, the build **succeeds while
> being wrong**: the artifact nobody loads is rebuilt, Bifrost loads the stale file already on
> disk, and a plugin source change looks applied when it is not. If a code change appears to
> have no effect, check which architecture is in `bin/` and which one the rendered config
> names. Running the driver under emulation also measured 39.6s against 9.3s native, so a
> silent fallback to the wrong platform costs about thirty seconds of dead air per run.

## Step 3. Set up Okta

Not optional, and not guessable. Work through [docs/RUNBOOK.md](docs/RUNBOOK.md) before
going further. The headlines, so you know what you are in for:

- **Two authorization servers, one per invoked agent. Not three.** The caller mints at the
  invoked agent's own server.
- Registering an agent as a resource, and assigning its owners, are **console-only**. The
  console calls it **Delegations > Non-human identity > Configure**, which is why it is hard
  to find.
- The resource URL is **immutable** once saved.
- Scopes are enforced on the managed **connection**, not only on the authorization server's
  policy.

## Step 4. Configure

```bash
cp .env.example .env      # then fill it in from your tenant
make config               # renders bifrost/config.json from the template
```

`bifrost/config.template.json` holds placeholders, `.env` holds the real values and is
gitignored, and the rendered `config.json` is gitignored too. The agent's private key never
passes through either.

If `make config` reports `missing secrets/sentinel-intake-key.jwk`, save the agent key under
that name. **There is one canonical filename**, and the gateway path, the `make demo-*` driver
targets and `scripts/render-config.sh` all now agree on it via the Makefile's
`AGENT_KEY_FILE`. Earlier revisions asked for it under two names; that is no longer needed,
and if you followed those instructions the second copy is harmless and unused.

### Configuration traps

Every one of these was hit for real. Each costs at least an hour if you meet it cold.

| Trap | What happens | What to do |
|---|---|---|
| **Hyphens in MCP client or tool names** | Bifrost **silently skips** the client. No error, no warning, and the tool simply is not there | Use underscores. `fleetops_read`, not `fleetops-read`. Same rule for tool names. No spaces, no leading digit |
| **`tools_to_execute` vs plugin `bindings.tools`** | `tools_to_execute` takes **bare** names (`get_telemetry`). The plugin's `bindings[].tools` needs **namespaced** ones (`fleetops_read-get_telemetry`). In the same file | Copy the pattern from the template. This is the nastiest inconsistency in the whole setup |
| **Missing `needs_session_stickiness: false`** | One shared connection with no caller context, so the plugin has nothing to delegate from | Set it to `false` on every MCP client |
| **Any `auth_type` other than `none`** | Bifrost resolves upstream headers of its own, which **overwrite the `Authorization` header the plugin set**. The upstream then rejects a credential the plugin never sent, and it looks like a minting bug | `"auth_type": "none"`. See below |
| **Config in the wrong place** | Bifrost starts on defaults, your plugin never loads, and the only hint is a single info line saying the config was not found | It must be at `$APP_DIR/config.json`, which is `/app/data/config.json`. Compose already mounts it there |
| **`allow_connect_without_caller` left false** | Every call fails as **"tool not found"**, not as an authorization error | Set it `true`. Read the next section before deciding it is a bypass |
| **`docker compose restart`** | Silently breaks everything. See below | Use `make clean && make up`, which does `down -v` |

### `auth_type` must be `none`

```json
"auth_type": "none",
"needs_session_stickiness": false
```

**This is not a placeholder, and not the absence of a choice. It is the only value that
works.** Two independent reasons, both verified live.

**1. `none` is the only option whose resolver returns no headers.** Every other auth type
resolves upstream headers of its own, and those **overwrite the `Authorization` header the
plugin set** on the connect request. The plugin mints correctly, the header is replaced, and
the MCP server rejects a credential the plugin never sent.

**2. `per_user_headers` additionally requires pre-stored per-caller credentials** and aborts
the connect when it finds none. There are none to store: the credential is minted per call
from Okta rather than kept anywhere.

> **Ignore any guidance that says caller context requires `per_user_headers`, `per_user_oauth`
> or `token_exchange`.** Those are Bifrost's per-call connection types, so the reasoning looks
> sound, and it sends you down a path that cannot work. `token_exchange` in particular makes
> Bifrost run its own single-hop exchange, which is exactly the thing the plugin replaces.

The plugin does not need Bifrost's credential brokering to find the caller. It reads the
caller's token off the inbound request headers directly, which needs no `auth_type`
configuration at all.

### Why `allow_connect_without_caller: true` is required, and is not a bypass

Bifrost registers MCP clients, and discovers their tools, **at startup**. There is no
inbound HTTP request at that moment, so there is no caller token to delegate from.

Leave this false and the connect is refused, registration fails, no tools are ever
discovered, and every later call fails with `tool not found`. The gateway is unusable for a
reason that looks nothing like its cause.

Turning it on does not weaken enforcement, for two independent reasons:

1. **No `Authorization` header is attached** to such a connection. The upstream MCP server
   validates tokens itself, so it rejects any call arriving over it.
2. **`PreMCPHook` still gates execution** on a caller token plus a successful live Okta
   mint, and is completely unaffected by this setting.

What it permits is exactly the unauthenticated handshake and `tools/list` that discovery
needs, which is the same thing any MCP server publishing a tool list already allows. It is
opt-in and defaults to false so that "connect without a credential" stays a deliberate,
visible choice in configuration. The plugin's test
`TestTokenlessConnectAllowedButExecuteDenied` pins both halves of that invariant.

### Never `docker compose restart`. Always `down -v`.

> **This is the trap most likely to cost you the demo.** The symptom points nowhere near the
> cause.
>
> **Tools are discovered only on FIRST registration.** On a restart, Bifrost reloads its MCP
> clients from its sqlite store **without re-running discovery**. The result is a gateway that
> lies to you:
>
> - `GET /api/mcp/clients` still reports all three tools, read from the database.
> - The live in-memory tool registry is **empty**.
> - Every call 404s with `tool not found`.
>
> There is no log line saying discovery was skipped. You will check the plugin, Okta, and your
> bindings, and all three will be fine.
>
> ```bash
> make clean && make up     # down -v, then rebuild and start
> ```
>
> Use `make clean && make up` after **every** config change, not just plugin changes.

## Step 5. Run it

```bash
make up          # builds the plugin, renders the config, starts everything
make logs        # follows the gateway and the server
```

Then point a client at it:

```bash
claude mcp add --transport http fleetops http://localhost:8080/mcp
```

The caller has to present its own token to the gateway, because that token is the subject
of the exchange. Mint one:

```bash
TOKEN=$(./scripts/get-caller-token.sh)
```

That does step 1 of three: the service client mints a token **for itself** via
`client_credentials` at the agent's own authorization server, with `resource` set to the
agent's resource URL. That `resource` parameter is the point. It makes the grant specific
to invoking this one agent rather than being ambient authority the service can spend
anywhere. The plugin never performs this step, because registered agents are not permitted
the `client_credentials` grant at all.

### The tools, and the two calls that make the point

| Tool | MCP client | Scope requested | Expect |
|---|---|---|---|
| `get_telemetry` | `fleetops_read` | `task.read` | **success**, with the delegation chain in the result |
| `list_routes` | `fleetops_read` | `task.read` | success |
| `dispatch_vehicle` | `fleetops_command` | `task.dispatch` | **`invalid_scope`**, naming the scope |

The MCP client name is the routing key. It selects the plugin binding, and the binding decides
which scopes are asked of Okta. Both bindings target the same authorization server; what
differs is what they request.

Listing is not gated. A listed tool still cannot run without the right scope, so hiding names
buys nothing and makes the server harder to work with. Each tool publishes the scope it
demands in its own description.

---

## How to verify it is really working

Three tiers. Each rules out a different way of fooling yourself.

### Tier 1. The plugin actually loaded

```bash
docker compose logs bifrost | grep "plugin status"
```

```
plugin status: okta-agent-identity - active
```

Anything else means the `.so` did not load, and Step 2 is where to look. A silent
non-failure here is the single most common outcome of a version or architecture mismatch.

### Tier 2. The MCP server received a token the caller never held

This is the one that proves delegation rather than pass-through.

```bash
docker compose logs fleetops | grep ACCEPTED
```

Compare that token against the caller's token from `get-caller-token.sh`. They must differ
in three ways:

| | Caller's token | Token the server received |
|---|---|---|
| `aud` | the agent's resource URL | the **target's** resource URL |
| `scp` | the caller's own scope | the **task** scope |
| `act` | absent | **present**, naming the agent |

If they are the same token, the gateway is forwarding the caller's credential rather than
exchanging it, and nothing has been delegated.

`act` and `sub_profile` are **not in Okta's published developer documentation.** They are
verified empirically against a live tenant. If you script an assertion on them, treat their
shape as observed behaviour that could change, not as a contract.

### Tier 3. Turn the plugin off and watch it break

```json
{ "enabled": false, "name": "okta-agent-identity", ... }
```

Then `make clean && make up` and call a tool. It must fail **completely**, with no
`Authorization` header reaching the server at all:

```
tools/call get_telemetry DENIED: no Authorization header reached this server
```

That failure is the proof the plugin is the only thing supplying credentials. If the call
still succeeds with the plugin disabled, something else is authenticating and your test
was never measuring what you thought.

Set it back to `true` and `make clean && make up`.

---

## Secrets and hygiene

**The agent key goes in `secrets/`, never in `.env`.**

A JWK pasted unquoted into an env file is destroyed the moment that file is sourced by a
shell: the double quotes are stripped and the commas are treated as brace expansion. The
result is a mangled key and a JSON parse error some distance from the cause. Keeping the key
in a file also means the rendered Bifrost config never contains key material, so the config
is not itself a secret.

**`.env` is not shell-sourceable in general.** An unquoted value containing a space parses
as an assignment followed by a command:

```
OKTA_SERVICE_CLIENT_SCOPE=agent.invoke task.read
```

sources as "run `task.read` with that variable set", failing with `task.read: command not
found`, which points nowhere near the problem. Docker's `--env-file` accepts the same line
happily, so the file looks fine everywhere else and breaks only in the shell. Use the
provided parser:

```sh
. ./scripts/load-env.sh
```

**For the Compose stack, set `EXTRA_CA_BUNDLE` rather than `SSL_CERT_FILE`.** Compose gives
the host shell precedence over `.env`, and `SSL_CERT_FILE` is commonly already exported on a
developer machine. Naming it `SSL_CERT_FILE` in `.env` would let a stale host value silently
win and point the container at a path that does not exist inside it. The symptom is an
unknown-authority TLS error identical to having configured nothing at all. Only needed on
networks that re-sign TLS. See `certs/`.

**What actually consumes it, since this is easy to get wrong.** `EXTRA_CA_BUNDLE` is not
special to anything: `docker-compose.yml` reads it and passes `SSL_CERT_FILE` into the
container, and **nothing outside Compose does that translation.** So a container you start by
hand, such as the Sentinel API in `sentinel/README.md`, must be given `SSL_CERT_FILE`
directly, pointing at a path that exists **inside that container**. In the running Sentinel
API, `EXTRA_CA_BUNDLE` is present and names `/certs/...`, which is not mounted there, while
`SSL_CERT_FILE` names the path under the `/w` mount and is the one doing the work. In the
Bifrost container the reverse holds and `/certs` is mounted. Do not chase `EXTRA_CA_BUNDLE`
when debugging a hand-started container; check `SSL_CERT_FILE` and check the path resolves
inside that container.

---

## Reading a failure

The common cases are below. **[docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) is the
fuller version**, organised by symptom, and it is the one to open when something is actually
broken. This table is a subset of it.

| Symptom | Cause |
|---|---|
| `invalid_scope`, naming a scope | A permission refusal. The scope is not on the agent's managed **connection**. Publishing it on the authorization server and its policy is not enough |
| `invalid_target` | No **ACTIVE** connection matches the `resource` sent. Byte-compare the URL and check the connection is not staged. Reads like permission, is almost always configuration |
| `access_denied`, policy evaluation failed | The caller is not a listed client of that authorization server, or the acting agent is deactivated |
| `'subject_token' is invalid` | The caller presented an ID token, or a token from the **org** authorization server. It must be an access token from a **custom** authorization server with a resource-scoped `aud` |
| `invalid_client` | Step 1: the service client's id or secret. Steps 2 and 3: the agent's key does not match the one registered on the agent |
| `tool not found` on every call | Either `allow_connect_without_caller` is false, or you restarted instead of `down -v` |
| The server rejects a token the plugin clearly minted | `auth_type` is not `none`, so Bifrost overwrote the plugin's `Authorization` header |
| A refusal naming a scope the call never asked for | This was a verdict-cache key collision, fixed in `Binding.verdictKey()`. If you see it, you are on a plugin build predating that fix |
| Plugin does not load, no obvious error | Go version, `bifrost/core` version, or architecture mismatch. Run `make compat` |
| `no caller identity token` | No `Authorization: Bearer` reached the gateway |
| `wrong audience` from the server | `FLEETOPS_AUDIENCES` does not match the resource URL sent as `resource`. The issued `aud` is observed to equal that value, so validate against the resource URL rather than the authorization server's `audiences` field |

**Okta does not down-scope.** A scope the connection does not grant fails the **whole**
request rather than returning the grantable subset. There is no partial success to
accidentally accept, and an over-broad ask is a clean, legible failure.

---

## The verdict cache, and what `agent_status_ttl` really controls

The plugin caches Okta's answer so repeated identical questions do not each cost a round trip.
`agent_status_ttl` bounds how long an answer is reused. It ships at `10s`.

### A fixed bug worth knowing about

The cache key was originally the authorization server id alone. Both bindings in this demo sit
on the **same** authorization server, so they shared a verdict: a denial recorded for
`fleetops_command` was reported for `fleetops_read`, telling a `get_telemetry` call it may not
have `task.dispatch`, a scope it never requested. Misleading rather than unsafe, but it sends
whoever is debugging it to the wrong place.

**Fixed at the root**, in `Binding.verdictKey()` in the plugin repo. The key is now the
authorization server id plus the target resource URL plus the **sorted** scope set, NUL-joined.
Two tests cover it in both directions and both were mutation-tested, so reverting the key makes
them fail. The caching path is verified at the live `10s` TTL, not only with caching disabled:
an interleaved six-call sequence across both bindings is order-independent and correct every
time.

### The honest caveat on `10s`

`10s` is a **demo default, not a tuned production value**, and the number does two jobs at
once. It is the cache lifetime, and therefore also the **revocation staleness bound**.

**Raising it widens the window in which a deactivated agent still passes.** That is the real
tradeoff. It is not a free performance dial, so choose it against how fast a deactivation needs
to bite in your environment.

### What actually costs a round trip

Easy to attribute to the wrong mechanism, so worth separating.

| | Frequency | Why |
|---|---|---|
| The **authorization check** | at most once per `10s` per distinct question | cached, keyed on the exact binding |
| The **mint** | two token requests per call | Bifrost connections are per-call, so the connect-time mint runs per call. Unrelated to the TTL |

The per-call cost comes from Bifrost's per-call connection model, not from the cache setting.

## Deliberately not here

- **No per-object authorization.** A scope can say an agent may dispatch. It cannot say
  whether it may dispatch *this particular* vehicle. That is a fine-grained authorization
  layer above this one.
- **No DPoP.** Tokens are bearer tokens.
- **No production claims.** Fleet state is in memory and resets with the container.
- **No revocation signals.** Revocation is caught by asking, within `agent_status_ttl`, so at
  the shipped `10s` a deactivation can take up to ten seconds to bite. The plugin exposes
  `InvalidateVerdicts()` as the seam for an event hook or shared-signals receiver, which would
  close that window. Nothing calls it yet.

## Two other ways to run this

Both exist, both are useful, both are secondary to the gateway path above.

**`driver/`, the exchange with no gateway in the way.** It calls the plugin's **own** exchange
code rather than reimplementing it, so a passing run is direct evidence about the code that
ships. It reads the same `FLEETOPS_SCOPE_*` variables as the server and targets the same
authorization server and resource as the gateway config, so it cannot drift from what Bifrost
does.

Start here when something is misconfigured. You see Okta's own answer rather than a gateway's
interpretation of it.

```bash
make demo          # both outcomes, and it completes rather than erroring out
```

```
lane read     ISSUED    chain 0oa135... <- wlp135...   scopes task.read agent.invoke
lane command  REFUSED   invalid_scope [task.dispatch]
```

`make demo-command` is a **refusal by design**, not a broken target. The connection does not
grant dispatch, so Okta declines, which is the same point the gateway path makes.
`make demo-deny` asks the read lane for an ungranted scope explicitly.

**`sentinel/`, the agent-to-agent chain of custody as a web page.** Decodes and displays
both the intermediate assertion and the final access token side by side, including
`sub_profile` at every level. Neither `act` nor `sub_profile` is documented by Okta; both are
verified empirically, and the app renders only what a token actually carries rather than
assuming a shape. See `sentinel/README.md`.

## Licence

Apache 2.0, matching Bifrost.
