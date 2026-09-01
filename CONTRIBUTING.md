# Contributing

Short and practical. For the failure modes you will hit while working on this, see
[docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md).

---

## Support expectations

**This is unsupported sample code, provided as is. It is not an Okta product.**

- **No SLA, no support commitment, no security response process.** It is a demonstration that
  an integration path works, not something to run in production.
- **Validate in a non-production Okta tenant first.** Setup includes irreversible steps: an
  agent's resource URL is immutable once saved, and the private key is shown exactly once.
  Both are much cheaper to get wrong in a preview org.
- **It is not hardened or monitored.** Fleet state is in memory and resets with the container.
  The access policy rules in [docs/RUNBOOK.md](docs/RUNBOOK.md) use EVERYONE, which is
  deliberately permissive and would need tightening before any real use.
- Licensed Apache 2.0, matching Bifrost. Both repos.

Two things to be careful about when repeating claims from this repo:

**`act` and `sub_profile` are not in Okta's published developer documentation.** They are
verified empirically by decoding real tokens from a live tenant, and they behave consistently.
Do not present them as documented behaviour. If you assert on them in a test, treat their
shape as observed behaviour that could change rather than as a contract.

**Do not claim the `resource` parameter is what determines `aud`.** What is observed is that
the issued token's `aud` equals the `resource` sent on the exchange. That is not the same
statement, and the mechanism is an open question. Validating against the resource URL is
correct advice either way, and that is how it should be written.

---

## Two repos, cloned side by side

The plugin is reusable and this demo is not, so they are separate repos. **The sibling layout
is load-bearing, not a convention.** Three Go modules resolve the plugin through a relative
`replace` directive, so a plugin clone somewhere else will not build.

```
~/code/
├── okta-bifrost-plugin/       the reusable Bifrost plugin
└── fleetops-bifrost-demo/     this repo
```

```bash
git clone https://github.com/oktaforai-okta/okta-bifrost-plugin
git clone https://github.com/oktaforai-okta/fleetops-bifrost-demo
```

If you must put the plugin elsewhere, `make up` takes `PLUGIN_DIR`, but the `replace`
directives in `driver/go.mod` and `sentinel/api/go.mod` are relative and would also need
changing. Prefer the sibling layout.

## Where things belong

### `fleetops-bifrost-demo`

| Path | What it is, and what belongs in it |
|---|---|
| `server/` | The Fleet Ops MCP server. Validates every token itself, so bypassing the gateway does not bypass authorization. **No third-party dependencies**, deliberately |
| `bifrost/` | `config.template.json` and `Dockerfile.dynamic`. Gateway configuration and nothing else |
| `driver/` | Terminal runner for the exchange with no gateway in the way. Calls the plugin's own exchange code |
| `sentinel/` | A separate web demo of an agent-to-agent chain of custody. `api/` is Go, `web/` is static with no build step. See [sentinel/README.md](sentinel/README.md) |
| `scripts/` | Config rendering, `.env` parsing, the caller-token minter, the gateway log filter. POSIX `sh` unless there is a reason not to |
| `docs/` | Prose. One document per audience, listed below |
| `secrets/` | The agent's private key JWK. Gitignored apart from its README |
| `certs/` | Local CA bundles for TLS-intercepting networks. Gitignored apart from its README |

Documents and who each is for, so a change lands in one place rather than three:

| Document | Audience |
|---|---|
| `README.md` | Part 1 for non-developers, Part 2 for developers building it |
| `docs/HOW-IT-WORKS.md` | Readers who will never run it. No code, no jargon |
| `docs/RUNBOOK.md` | Okta tenant setup, step by step |
| `docs/PROVING-IT.md` | Showing a sceptic the demo is real, from sources that are not the demo |
| `docs/TROUBLESHOOTING.md` | Someone with a broken thing in front of them, organised by symptom |

**New failure modes go in `docs/TROUBLESHOOTING.md`, organised by symptom**, not appended to
whichever document you happened to be editing. That page exists because these were previously
scattered across five files.

### `okta-bifrost-plugin`

| Path | |
|---|---|
| `main.go` | A thin shim, and nothing else. Bifrost's loader resolves plugins as free functions by name rather than as a type satisfying an interface, so this file exports the symbols the loader looks up and forwards them to `./plugin`. It pins every exported signature in a `var` block on purpose, which turns a runtime load failure into a build failure here |
| `plugin/` | The real package. Ordinary, testable, no plugin machinery. **Put logic here** |
| `bin/` | Build output. Gitignored |

The only non-standard-library import anywhere in the plugin is
`github.com/maximhq/bifrost/core/schemas`. **Keep it that way.** Every library added is
another version that has to match the host binary exactly, and therefore another way for the
plugin to refuse to load inside someone else's Bifrost build. That is why the RS256 signing is
hand-rolled on `crypto/rsa` rather than pulled from a JWT package: RS256 is a SHA-256 hash and
a PKCS#1 v1.5 signature, both already in the standard library.

---

## Building and testing

**Docker is the only prerequisite. No local Go toolchain is needed for anything**, in either
repo. Every build and every test runs in a pinned container. That is not a convenience: the
single most reliable way to produce a plugin that will not load is to build it on a
developer's own toolchain.

Go plugins do not work on Windows. Linux or macOS.

### The plugin

```bash
cd okta-bifrost-plugin
make check          # fmt-check, vet, test. Run this before every commit
make race           # the verdict cache is read concurrently
make plugin PLATFORM=linux/arm64      # or linux/amd64
```

`make help` lists the rest. There is **no CI in either repo**, so `make check` is a gate you
run yourself rather than one that runs for you.

### The demo

```bash
cd fleetops-bifrost-demo
make check          # gofmt, vet and build for server/
make up             # builds the plugin, renders the config, starts everything
make clean && make up   # after every config change. See below
make demo           # the exchange with no gateway in the way, both outcomes
```

Note that `make check` here covers `server/` only. `driver/` and `sentinel/api/` have no
target yet; test them with a containerised `go test` from their own directory, matching the
`golang:1.27` image the Makefiles use.

### Use `make clean && make up`, never `docker compose restart`

Bifrost discovers an MCP client's tools only when that client is **first registered**, and
caches the result in sqlite. A restart reloads clients without re-running discovery, so the
live registry comes up empty while the admin API still reports tools and health. Every call
then 404s with `tool not found`, and there is no log line saying discovery was skipped.

`make clean` runs `docker compose down -v`, and the `-v` is the part that matters: it drops
the volume holding the sqlite store.

Before believing a stack is ready, check the live registry rather than a status code. Full
explanation and the exact command are in
[docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md#the-one-check-to-run-first).

---

## The pinned-version rule

**A plugin `.so` must match the Bifrost binary it loads into on three axes, exactly.** This is
the rule most likely to be broken accidentally, and its usual symptom is a plugin that silently
does not load.

| Must match | Note |
|---|---|
| Go version | Currently `1.27.0`, declared in every `go.mod` |
| **Every** shared dependency version | `bifrost/core` v1.8.4 against a v1.8.3 host does not load. Patch versions count |
| Architecture | An arm64 `.so` will not load into an amd64 host |

**Do not guess, and do not bump a version because a newer one exists.** Read the versions out
of the image you are actually loading into:

```bash
cd okta-bifrost-plugin
make compat BIFROST_IMAGE=bifrost:dynamic-local
make pin BIFROST_CORE=<the version it printed>
```

Three consequences for how changes get made:

1. **`bifrost/core` in `go.mod` is pinned to the host, not to latest.** Currently `v1.8.3`.
   Changing it means rebuilding the host image too, and re-running `make compat` to confirm
   they still agree. A routine dependency bump is not routine here.
2. **`BIFROST_REF` in `bifrost/Dockerfile.dynamic` is pinned** to a Bifrost tag. Moving it
   changes which `core` version the host wants, so the plugin's pin moves with it. Move both
   together or neither.
3. **`BIFROST_IMAGE` in `.env` should name a specific image, not `latest`.** `latest` will
   drift and the plugin will stop loading with a message that does not point at the cause.

`PLATFORM` is a deliberate inconsistency between the two repos, and **both defaults are
correct**. The demo derives it from the host, because `scripts/render-config.sh` independently
derives the plugin filename from `uname -m` and the two must agree locally. The plugin repo
defaults to `linux/amd64`, because there the artifact must match the architecture of the
Bifrost image it will be loaded into, which is frequently not the build machine. **Do not
"fix" either one to match the other.** The trap this creates, where a build succeeds while
loading a stale `.so` of the other architecture, is in
[docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md#platform-mismatch-including-the-case-where-the-build-succeeds-and-is-still-wrong).

---

## Never commit

All four are already gitignored. Check before you commit anyway, because the cost of getting
this wrong is a live credential in a public repo.

| Never commit | Why |
|---|---|
| `.env`, `.env.local` | Every value is specific to one Okta org, including a client secret |
| `secrets/` | The agent's private key. `secrets/README.md` is the only tracked file |
| `certs/` | A CA bundle identifies your employer's network and is specific to your machine. `certs/README.md` is the only tracked file |
| `bifrost/config.json` | **Rendered output.** Holds every tenant identifier. Edit `bifrost/config.template.json` and re-run `make config` |
| `*.so`, `bin/` | Build artifacts, and architecture-specific |
| `server/server`, `driver/driver`, `sentinel/api/api` | Binaries written into the mount by containerised builds |

```bash
git status --short
git check-ignore -v .env bifrost/config.json \
  secrets/sentinel-intake-key.jwk certs/ca-bundle.crt
```

```
.gitignore:6:.env               .env
.gitignore:3:bifrost/config.json    bifrost/config.json
.gitignore:26:secrets/*         secrets/sentinel-intake-key.jwk
.gitignore:22:certs/*           certs/ca-bundle.crt
```

Name **files** rather than directories there. The patterns are `secrets/*` and `certs/*`, so
`git check-ignore -v secrets/` matches nothing and reads as "not ignored", which is the wrong
conclusion from the right command.

**Nothing real belongs in `.env.example` or `sentinel/.env.example`.** If you find yourself
pasting a `wlp` id, an `aus` id, a `0oa` id or a secret into either, stop.

**The agent key never goes in `.env`, in any form.** It lives in `secrets/` and is read from
disk. A JWK pasted unquoted into an env file is destroyed the moment that file is sourced: the
quotes are stripped and the commas become brace expansion. Keeping it in a file also means the
rendered config never contains key material.

**Do not quote values in `.env` to make it shell-sourceable.** It cannot be sourced by design,
and quoting breaks the containers instead. Use `scripts/load-env.sh`, which parses rather than
sources. The reasoning is in
[docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md#env-cannot-be-shell-sourced).

---

## Documentation conventions

The prose in this repo is load-bearing. Most of it exists because someone lost hours to
something the error message pointed away from, and it is written to save the next person those
hours.

**No em-dashes anywhere in this project.** Use a comma, a colon, or a full stop. This applies
to Markdown, code comments, log messages, and error strings alike.

Beyond that:

- **State the symptom before the cause.** A reader arriving at a document has the symptom and
  nothing else. That is why `docs/TROUBLESHOOTING.md` is organised the way it is.
- **Say where knowledge came from.** "Verified empirically" and "documented by Okta" are
  different claims. Do not upgrade one into the other, and do not leave a reader unable to
  tell which they are reading.
- **Give the command that distinguishes cause from symptom**, not just the fix. Verify it runs
  before you commit it. A command that does not work in a troubleshooting document is worse
  than no command.
- **Be precise about what is not covered.** No per-object authorization, tokens are bearer
  tokens, no DPoP, scope narrowing applies to the next session rather than the next call.
  Someone who believes a wider claim and later finds the narrower reality will trust none of
  the rest of it.
- **Cross-reference rather than restate.** The same passage in three files becomes three
  slightly different passages after one edit.
- **Explain why a surprising choice is correct**, in a comment next to it. `auth_type: none`,
  `allow_connect_without_caller: true` and the two different `PLATFORM` defaults all look like
  mistakes and are not. Each is annotated where it lives, so the next person does not "fix" it.

Keep code comments in the same spirit. Several comments in this repo record what was tried and
why it failed, which is more useful than a description of what the line does.
