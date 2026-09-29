#!/usr/bin/env python3
"""Compare the pinned daemon's --definitions output with the source fixture."""

import hashlib
import json
import sys
from pathlib import Path


def verify(runtime_path, fixture_path):
    fixture = json.loads(Path(fixture_path).read_text())
    if fixture["oracle"] != {
        "repository": "XRPLF/xrpld-private",
        "tag": "3.4.1",
        "commit": "d147fccf54a500fce586522f28d6044c37fd8d29",
    }:
        raise ValueError("definitions fixture does not identify the private 3.4.1 oracle")
    document = json.loads(Path(runtime_path).read_text())
    advertised = document.pop("hash")
    payload = json.dumps(document, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
    digest = hashlib.sha512(payload).hexdigest()[:64].upper()
    if len(payload) != fixture["full_document_bytes"]:
        raise ValueError("runtime definitions length differs from the source fixture")
    if digest != fixture["full_document_sha512_half"] or advertised != digest:
        raise ValueError("runtime definitions hash differs from the source fixture")
    print(f"runtime_definitions_bytes={len(payload)}")
    print(f"runtime_definitions_sha512_half={digest}")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: runtime-definitions.py RUNTIME_JSON SOURCE_FIXTURE")
    try:
        verify(sys.argv[1], sys.argv[2])
    except (OSError, ValueError, KeyError, TypeError) as error:
        sys.exit(f"runtime-definitions: {error}")
