#!/usr/bin/env python3
"""Generate RSA 2048 key pairs for the Sentinel agents.

Writes each private JWK (with a kid) into secrets/ and prints the matching
public JWK to stdout so it can be registered with Okta. The kid is identical
on both halves, which the client assertion requires.
"""
import base64
import json
import sys
from pathlib import Path

from cryptography.hazmat.primitives.asymmetric import rsa

# Relative to this script, so a clone works anywhere rather than only on the machine
# this was first written on.
SECRETS = Path(__file__).resolve().parent.parent / "secrets"

AGENTS = {
    "sentinel-intake": "sentinel-intake-1",
    "sentinel-tasking": "sentinel-tasking-1",
}


def b64u(i: int) -> str:
    raw = i.to_bytes((i.bit_length() + 7) // 8, "big")
    return base64.urlsafe_b64encode(raw).decode().rstrip("=")


def main() -> None:
    out = {}
    for slug, kid in AGENTS.items():
        key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        n, pub = key.private_numbers(), key.public_key().public_numbers()

        public = {"kty": "RSA", "kid": kid, "e": b64u(pub.e), "n": b64u(pub.n)}
        private = dict(
            public,
            d=b64u(n.d),
            p=b64u(n.p),
            q=b64u(n.q),
            dp=b64u(n.dmp1),
            dq=b64u(n.dmq1),
            qi=b64u(n.iqmp),
        )

        path = SECRETS / f"{slug}-key.jwk"
        path.write_text(json.dumps(private, sort_keys=True) + "\n")
        path.chmod(0o600)

        out[slug] = {"kid": kid, "private_jwk": str(path), "public_jwk": public}
        print(f"wrote {path} (kid={kid})", file=sys.stderr)

    # Public halves only. Safe to print.
    print(json.dumps(out, indent=2))


if __name__ == "__main__":
    main()
