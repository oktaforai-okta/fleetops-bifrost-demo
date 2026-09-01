# Agent key

Put the agent's **private** key JWK here as `sentinel-intake-key.jwk`, one JSON object.
This is the one canonical name. It is what `bifrost/config.template.json`,
`scripts/render-config.sh` and the Makefile's `AGENT_KEY_FILE` all expect. An older name,
`agent-key.jwk`, appears in some places and is no longer required.

Okta shows the private key exactly once, when the key pair is generated on the agent. If
you missed it, generate a new key pair rather than trying to recover the old one.

The plugin reads this file directly. It is deliberately not passed through `.env` or the
rendered Bifrost config, for two reasons:

- The config stays non-secret, so it does not have to be protected everywhere it is
  stored, rendered, or backed up.
- A JWK pasted unquoted into `.env` is corrupted the moment the file is sourced by a
  shell: the double quotes are stripped and the commas are treated as brace expansion.
  The resulting failure is a JSON parse error nowhere near the actual cause.

Everything here except this file is gitignored.
