#!/usr/bin/env python3
"""Inspect one previously approved release on a dedicated NibRunner inspection host."""

import argparse
import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path


def inspect(binary, digest, op, probe):
    if not re.fullmatch(r"[0-9a-f]{64}", digest):
        raise ValueError("Expected a lowercase release SHA-256.")
    with binary.open("rb") as release:
        if hashlib.file_digest(release, "sha256").hexdigest() != digest:
            raise ValueError("File differs from the approved release digest.")
    claim = op("beta.claim", {"sha256": digest})
    if claim.get("cached") is True:
        if claim.get("state") != "valid":
            raise ValueError("This release has a cached manifest rejection.")
        return "Manifest already cached."
    attempt = claim["attempt"]
    completion = {"sha256": digest, "attempt": attempt}
    try:
        result = probe(binary, digest)
        if result.returncode == 3:
            outcome = "invalid"
        elif result.returncode:
            outcome = "retry"
        else:
            if len(result.stdout) > 16 * 1024:
                raise ValueError("Oversized probe response.")
            manifest = json.loads(result.stdout)
            if not isinstance(manifest, dict):
                raise ValueError("Expected one manifest object.")
            completion["manifest"] = manifest
            outcome = "success"
    except (OSError, subprocess.TimeoutExpired, ValueError):
        outcome = "retry"
    op("beta.complete", {**completion, "outcome": outcome})
    if outcome != "success":
        raise ValueError(f"Release inspection ended with {outcome}.")
    return "Manifest cached."


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--sha256", required=True, help="Digest from the approved release")
    parser.add_argument("--config", required=True, type=Path, help="Inspection host NibRunner config")
    parser.add_argument("--drop", default="drop")
    parser.add_argument("--nibrunner", default="nibrunnerd")
    access = parser.add_mutually_exclusive_group(required=True)
    access.add_argument("--url", help="Authenticated Drop loopback connection")
    access.add_argument("--db", help="Project path for the local Drop server")
    args = parser.parse_args()
    destination = ["--url", args.url] if args.url else ["--db", args.db]

    def op(name, payload):
        result = subprocess.run(
            [args.drop, *destination, "op", name, "-"], input=json.dumps(payload).encode(),
            stdout=subprocess.PIPE, check=True, timeout=30,
        )
        return json.loads(result.stdout)

    def probe(binary, digest):
        return subprocess.run(
            [args.nibrunner, "probe-manifest", "--binary", str(binary),
             "--sha256", digest, "--config", str(args.config)],
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, timeout=120, check=False,
        )

    print(inspect(args.binary, args.sha256, op, probe))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        sys.exit(str(error))
