#!/usr/bin/env python3
"""Export the node's public JWK and publish static OIDC discovery documents."""

import argparse
import base64
import hashlib
import json
from pathlib import Path

from cryptography.hazmat.primitives.serialization import load_pem_private_key


def b64(value):
    return base64.urlsafe_b64encode(value).rstrip(b"=").decode("ascii")


def documents(key_file, issuer):
    numbers = load_pem_private_key(Path(key_file).read_bytes(), password=None).public_key().public_numbers()
    jwk = {"kty": "RSA"}
    for field, integer in [("n", numbers.n), ("e", numbers.e)]:
        jwk[field] = b64(integer.to_bytes((integer.bit_length() + 7) // 8, "big"))
    thumbprint = hashlib.sha256(json.dumps(jwk, sort_keys=True, separators=(",", ":")).encode()).digest()
    jwk.update(kid=b64(thumbprint), alg="RS256", use="sig")
    discovery = {"issuer": issuer, "jwks_uri": issuer + "/keys.json",
                 "response_types_supported": ["id_token"], "subject_types_supported": ["public"],
                 "id_token_signing_alg_values_supported": ["RS256"]}
    return jwk, discovery


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Export only public documents to an operator directory.")
    parser.add_argument("issuer")
    parser.add_argument("output_dir", type=Path)
    parser.add_argument("key", nargs="+", help="all active and retiring root-owned PKCS8 RSA keys")
    args = parser.parse_args()
    keys = []
    for path in args.key:
        jwk, discovery = documents(path, args.issuer)
        if jwk not in keys:
            keys.append(jwk)
    args.output_dir.mkdir(parents=True, exist_ok=True)
    for name, body in [("keys.json", {"keys": keys}), ("openid-configuration", discovery)]:
        (args.output_dir / name).write_text(json.dumps(body) + "\n")
