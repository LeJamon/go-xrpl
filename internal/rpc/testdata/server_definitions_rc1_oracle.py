#!/usr/bin/env python3
"""Derive rc1 server_definitions data from the pinned rippled source tree.

It parses the C++ protocol macros and combines them with construction rules
transcribed from the pinned rc1 sources, independently of go-xrpl. The input
must be a clean checkout of the pinned commit.

Regenerate the checked-in fixture from the pinned source checkout with:

    python3 server_definitions_rc1_oracle.py \
        /path/to/rippled-worktrees/v3.4.0-rc1 --output server_definitions_rc1_hashes.json

A built rc1 daemon can independently dump the runtime document with
`xrpld --definitions`; the source-derived checksums below are kept in the Go
suite so normal tests do not require a daemon or a C++ build.
"""

import argparse
import hashlib
import json
import re
import subprocess
from pathlib import Path


ORACLE_COMMIT = "2ad4def35fd8580da027462517ba3375cc005c94"


def verify_oracle(root):
    def git(*args):
        return subprocess.check_output(["git", "-C", str(root), *args], text=True).strip()

    if Path(git("rev-parse", "--show-toplevel")).resolve() != root:
        raise ValueError("oracle_root must be the checkout root")
    if git("rev-parse", "HEAD") != ORACLE_COMMIT:
        raise ValueError(f"oracle must be rippled 3.4.0-rc1 at {ORACLE_COMMIT}")
    if git("status", "--porcelain", "--untracked-files=normal"):
        raise ValueError("oracle checkout must be clean")


def without_comments(text):
    text = re.sub(r"/\*.*?\*/", "", text, flags=re.S)
    return re.sub(r"//[^\n]*", "", text)


def calls(text, macro):
    text = without_comments(text)
    pattern = re.compile(r"(?m)^\s*" + re.escape(macro) + r"\s*\(")
    for match in pattern.finditer(text):
        start = match.end() - 1
        depth = 0
        for i in range(start, len(text)):
            if text[i] == "(":
                depth += 1
            elif text[i] == ")":
                depth -= 1
                if depth == 0:
                    yield text[start + 1 : i]
                    break


def split_args(text):
    out, start, depth = [], 0, 0
    for i, char in enumerate(text):
        if char == "(":
            depth += 1
        elif char == ")":
            depth -= 1
        elif char == "," and depth == 0:
            out.append(text[start:i].strip())
            start = i + 1
    out.append(text[start:].strip())
    return out


def number(text):
    text = text.strip()
    return int(text, 0)


def translate(inp):
    if "UINT" in inp:
        if any(width in inp for width in ("512", "384", "256", "192", "160", "128")):
            return inp.replace("UINT", "Hash")
        return inp.replace("UINT", "UInt")
    replacements = {
        "OBJECT": "STObject",
        "ARRAY": "STArray",
        "ACCOUNT": "AccountID",
        "LEDGERENTRY": "LedgerEntry",
        "NOTPRESENT": "NotPresent",
        "PATHSET": "PathSet",
        "VL": "Blob",
        "XCHAIN_BRIDGE": "XChainBridge",
    }
    if inp in replacements:
        return replacements[inp]
    out = []
    for token in inp.split("_"):
        if len(token) > 1:
            token = token.lower().capitalize()
        out.append(token)
    return "".join(out)


def field_templates(blob):
    style = {"SoeRequired": 0, "SoeOptional": 1, "SoeDefault": 2}
    result = []
    for name, optional in re.findall(
        r"\{\s*(sf[A-Za-z0-9_]+)\s*,\s*(Soe(?:Required|Optional|Default))", blob
    ):
        result.append({"name": name[2:], "optionality": style[optional]})
    return result


def formats(root):
    tx_source = (root / "include/xrpl/protocol/detail/transactions.macro").read_text()
    tx_common = [
        ("TransactionType", 0),
        ("Flags", 1),
        ("SourceTag", 1),
        ("Account", 0),
        ("Sequence", 0),
        ("PreviousTxnID", 1),
        ("LastLedgerSequence", 1),
        ("AccountTxnID", 1),
        ("Fee", 0),
        ("OperationLimit", 1),
        ("Memos", 1),
        ("SigningPubKey", 0),
        ("TicketSequence", 1),
        ("TxnSignature", 1),
        ("Signers", 1),
        ("NetworkID", 1),
        ("Delegate", 1),
        ("Sponsor", 1),
        ("SponsorFlags", 1),
        ("SponsorSignature", 1),
    ]
    tx_common_names = {name for name, _ in tx_common}
    tx_formats = {"common": [{"name": n, "optionality": s} for n, s in tx_common]}
    for raw in calls(tx_source, "TRANSACTION"):
        args = split_args(raw)
        tx_formats[args[2]] = [
            field
            for field in field_templates(args[4])
            if field["name"] not in tx_common_names
        ]

    ledger_source = (root / "include/xrpl/protocol/detail/ledger_entries.macro").read_text()
    ledger_common = [("LedgerIndex", 1), ("LedgerEntryType", 0), ("Flags", 0), ("Sponsor", 1)]
    ledger_common_names = {name for name, _ in ledger_common}
    ledger_formats = {"common": [{"name": n, "optionality": s} for n, s in ledger_common]}
    for macro in ("LEDGER_ENTRY", "LEDGER_ENTRY_DUPLICATE"):
        for raw in calls(ledger_source, macro):
            args = split_args(raw)
            ledger_formats[args[2]] = [
                field
                for field in field_templates(args[4])
                if field["name"] not in ledger_common_names
            ]
    return tx_formats, ledger_formats


def flag_tables(root):
    ledger_source = without_comments((root / "include/xrpl/protocol/LedgerFormats.h").read_text())
    ledger_values = {
        name: number(value)
        for name, value in re.findall(
            r"LSF_FLAG(?:2)?\((\w+),\s*(0x[0-9A-Fa-f]+)\)", ledger_source
        )
    }
    ledger_flags = {}
    for raw in calls(ledger_source, "LEDGER_OBJECT"):
        args = split_args(raw)
        values = {}
        for blob in args[1:3]:
            for name, value in re.findall(
                r"LSF_FLAG(?:2)?\((\w+),\s*([^)]+)\)", blob
            ):
                values[name] = number(value) if value.strip().startswith("0x") else ledger_values[value.strip()]
        ledger_flags[args[0]] = values

    tx_source = without_comments((root / "include/xrpl/protocol/TxFlags.h").read_text())
    tx_flags = {
        "universal": {"tfFullyCanonicalSig": 0x80000000, "tfInnerBatchTxn": 0x40000000}
    }
    for raw in calls(tx_source, "TRANSACTION"):
        args = split_args(raw)
        values = {}
        for name, value in re.findall(r"TF_FLAG(?:2)?\((\w+),\s*([^)]+)\)", args[1]):
            value = value.strip()
            values[name] = number(value) if value.startswith("0x") else ledger_values[value]
        tx_flags[args[0]] = values

    account_flags = {
        name: int(value)
        for name, value in re.findall(r"ASF_FLAG\((\w+),\s*([0-9]+)\)", tx_source)
    }
    return tx_flags, ledger_flags, account_flags


def enum_values(text):
    values = {}
    clean = without_comments(text)
    for match in re.finditer(
        r"enum\s+(?:TELcodes|TEMcodes|TEFcodes|TERcodes|TEScodes|TECcodes)\s*:[^{]+\{(.*?)\}",
        clean,
        flags=re.S,
    ):
        previous = None
        for item in match.group(1).split(","):
            item = re.sub(r"\[\[.*?\]\]", "", item).strip()
            token = re.match(r"([A-Za-z_]\w*)\s*(?:=\s*(-?[0-9]+))?", item)
            if token is None:
                continue
            name, explicit = token.groups()
            value = int(explicit) if explicit is not None else previous + 1
            values[name] = value
            previous = value
    return values


def transaction_results(root):
    enum_map = enum_values((root / "include/xrpl/protocol/TER.h").read_text())
    source = without_comments((root / "src/libxrpl/protocol/TER.cpp").read_text())
    result = {}
    for name in re.findall(r"MAKE_ERROR\(((?:tec|tef|tel|tem|ter|tes)\w+),", source):
        if name not in enum_map:
            raise ValueError(f"missing TER enum value for {name}")
        result[name] = enum_map[name]
    return result


def types(root):
    source = (root / "include/xrpl/protocol/SField.h").read_text()
    result = {"Done": -1}
    for raw_name, value in re.findall(r"STYPE\((STI_\w+),\s*(-?[0-9]+)\)", source):
        result[translate(raw_name[4:])] = int(value)
    return result


def fields(root):
    source = (root / "include/xrpl/protocol/detail/sfields.macro").read_text()
    type_map = types(root)
    by_code = {}
    for macro in ("UNTYPED_SFIELD", "TYPED_SFIELD"):
        for raw in calls(source, macro):
            args = split_args(raw)
            name = args[0][2:]
            type_name = args[1]
            nth = number(args[2])
            type_code = type_map[translate(type_name)]
            by_code[(type_code << 16) + nth] = (
                name,
                {
                    "nth": nth,
                    "isVLEncoded": type_code in (7, 8, 19),
                    "isSerialized": type_code < 10000 and name not in ("hash", "index"),
                    "isSigningField": nth < 256 and "kNotSigning" not in ",".join(args[3:]),
                    "type": translate(type_name),
                },
            )

    # SField.cpp constructs these outside sfields.macro. Generic uses the
    # default constructor: STI_UNKNOWN, field value 0, signing enabled.
    manual = {
        "Generic": {"nth": 0, "isVLEncoded": False, "isSerialized": True, "isSigningField": True, "type": "Unknown"},
        "hash": {"nth": 257, "isVLEncoded": False, "isSerialized": False, "isSigningField": False, "type": "Hash256"},
        "index": {"nth": 258, "isVLEncoded": False, "isSerialized": False, "isSigningField": False, "type": "Hash256"},
    }
    by_code[0] = ("Generic", manual["Generic"])
    by_code[(5 << 16) + 257] = ("hash", manual["hash"])
    by_code[(5 << 16) + 258] = ("index", manual["index"])

    sentinel = [
        ["Invalid", {"nth": -1, "isVLEncoded": False, "isSerialized": False, "isSigningField": False, "type": "Unknown"}],
        ["ObjectEndMarker", {"nth": 1, "isVLEncoded": False, "isSerialized": True, "isSigningField": True, "type": "STObject"}],
        ["ArrayEndMarker", {"nth": 1, "isVLEncoded": False, "isSerialized": True, "isSigningField": True, "type": "STArray"}],
        ["taker_gets_funded", {"nth": 258, "isVLEncoded": False, "isSerialized": False, "isSigningField": False, "type": "Amount"}],
        ["taker_pays_funded", {"nth": 259, "isVLEncoded": False, "isSerialized": False, "isSigningField": False, "type": "Amount"}],
    ]
    rows = sentinel[:]
    rows.extend([[name, value] for _, (name, value) in sorted(by_code.items())])
    return rows


def transaction_types(root):
    source = (root / "include/xrpl/protocol/detail/transactions.macro").read_text()
    result = {"Invalid": -1}
    for raw in calls(source, "TRANSACTION"):
        args = split_args(raw)
        result[args[2]] = number(args[1])
    return result


def ledger_entry_types(root):
    source = (root / "include/xrpl/protocol/detail/ledger_entries.macro").read_text()
    result = {"Invalid": -1}
    for macro in ("LEDGER_ENTRY", "LEDGER_ENTRY_DUPLICATE"):
        for raw in calls(source, macro):
            args = split_args(raw)
            result[args[2]] = number(args[1])
    return result


def digest(value):
    payload = json.dumps(value, separators=(",", ":"), sort_keys=True).encode()
    return hashlib.sha512(payload).hexdigest()[:64].upper(), payload


def build(root):
    tx_formats, ledger_formats = formats(root)
    tx_flags, ledger_flags, account_flags = flag_tables(root)
    return {
        "TYPES": types(root),
        "FIELDS": fields(root),
        "LEDGER_ENTRY_TYPES": ledger_entry_types(root),
        "TRANSACTION_TYPES": transaction_types(root),
        "TRANSACTION_RESULTS": transaction_results(root),
        "TRANSACTION_FORMATS": tx_formats,
        "LEDGER_ENTRY_FORMATS": ledger_formats,
        "TRANSACTION_FLAGS": tx_flags,
        "LEDGER_ENTRY_FLAGS": ledger_flags,
        "ACCOUNT_SET_FLAGS": account_flags,
    }


def section_stats(value):
    if isinstance(value, dict):
        entries = 0
        for child in value.values():
            entries += len(child) if isinstance(child, (dict, list)) else 1
        return {"groups": len(value), "entries": entries}
    return {"groups": 0, "entries": len(value) if isinstance(value, list) else 1}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("oracle_root", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    root = args.oracle_root.resolve()
    try:
        verify_oracle(root)
    except (OSError, subprocess.CalledProcessError, ValueError) as error:
        parser.error(str(error))
    document = build(root)
    full_hash, full_payload = digest(document)
    output = {
        "oracle": {
            "tag": "3.4.0-rc1",
            "commit": ORACLE_COMMIT,
        },
        "serialization": "compact UTF-8 JSON with lexicographically sorted object keys; no trailing newline",
        "regeneration": {
            "source": "python3 server_definitions_rc1_oracle.py <rippled-3.4.0-rc1> --output server_definitions_rc1_hashes.json",
            "runtime": "xrpld --definitions",
        },
        "transaction_results": {
            "source": "src/libxrpl/protocol/TER.cpp::transResults()",
            "enum_only_excluded": ["tecHOOK_REJECTED", "tecNO_DELEGATE_PERMISSION"],
        },
        "full_document_sha512_half": full_hash,
        "full_document_bytes": len(full_payload),
        "sections": {},
        "source_files": [
            "include/xrpl/protocol/SField.h",
            "include/xrpl/protocol/detail/sfields.macro",
            "include/xrpl/protocol/TER.h",
            "src/libxrpl/protocol/TER.cpp",
            "include/xrpl/protocol/detail/transactions.macro",
            "include/xrpl/protocol/detail/ledger_entries.macro",
            "include/xrpl/protocol/TxFlags.h",
            "include/xrpl/protocol/LedgerFormats.h",
        ],
        "transcribed_rule_sources": [
            "include/xrpl/protocol/TxFlags.h",
            "src/libxrpl/protocol/TxFormats.cpp",
            "src/libxrpl/protocol/LedgerFormats.cpp",
            "src/libxrpl/protocol/SField.cpp",
            "src/xrpld/rpc/handlers/server_info/ServerDefinitions.cpp",
        ],
    }
    for name, value in document.items():
        section_hash, payload = digest(value)
        stats = section_stats(value)
        output["sections"][name] = {
            "sha512_half": section_hash,
            "bytes": len(payload),
            **stats,
        }
        print(f"{name} {len(payload)} {section_hash} {stats}")
    print(f"FULL_DOCUMENT {len(full_payload)} {full_hash}")
    if args.output:
        args.output.write_text(json.dumps(output, indent=2) + "\n")


if __name__ == "__main__":
    main()
