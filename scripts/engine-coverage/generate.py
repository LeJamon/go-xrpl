#!/usr/bin/env python3
"""Generate the checked-in transaction/engine coverage inventory.

The inventory is deliberately a small source audit, rather than a coverage
framework.  It reads the pinned rippled protocol registries and the Go
transaction registry, records the engine stage seams, and leaves execution
claims to an explicit evidence section.  Run from the go-xrpl repository root:

    python3 scripts/engine-coverage/generate.py
    python3 scripts/engine-coverage/generate.py --check

The default oracle is the sibling read-only v3.4.1 checkout used by issue
#2016.  ``--oracle`` is useful for a different local checkout, but the pinned
commit is still checked before an inventory is written.
"""

from __future__ import annotations

import argparse
from bisect import bisect_right
import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Any, Iterable


ORACLE_REPOSITORY = "XRPLF/xrpld-private"
ORACLE_TAG = "3.4.1"
ORACLE_COMMIT = "d147fccf54a500fce586522f28d6044c37fd8d29"
GO_BASE_COMMIT = "02f4f17c7d1c1b5676b112ddfa669655bcff4415"
SCHEMA = 1

TRANSACTION_MACRO = "include/xrpl/protocol/detail/transactions.macro"
TX_FORMATS = "src/libxrpl/protocol/TxFormats.cpp"
FEATURE_MACRO = "include/xrpl/protocol/detail/features.macro"

GO_TRANSACTION_TYPES = "protocol/transaction_type.go"
GO_TEMPLATE = "internal/tx/template.go"
GO_AMENDMENTS = "amendment/registry.go"

STYLE_NAMES = {
    "Required": "required",
    "Optional": "optional",
    "Default": "default",
}

METHODS = (
    "Validate",
    "PreflightRules",
    "PreflightWithRules",
    "CheckExtraFeatures",
    "PreflightSigValidated",
    "Preclaim",
    "PreclaimPseudo",
    "Apply",
    "ApplyInnerTransactions",
    "PreflightInnerTransactions",
    "ValidateBatchOuter",
    "GetFlagsMask",
    "RequiredAmendments",
)


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def oracle_head(oracle: Path) -> str:
    try:
        result = subprocess.run(
            ["git", "-C", str(oracle), "rev-parse", "HEAD"],
            check=True,
            capture_output=True,
            text=True,
        )
    except (OSError, subprocess.CalledProcessError) as exc:
        raise SystemExit(f"engine coverage: cannot resolve oracle commit: {exc}") from exc
    return result.stdout.strip()


def read(path: Path) -> str:
    try:
        return path.read_text()
    except OSError as exc:
        raise SystemExit(f"engine coverage: read {path}: {exc}") from exc


def balanced(text: str, start: int, opening: str, closing: str) -> tuple[str, int]:
    """Return the balanced expression beginning at ``start``."""

    if start >= len(text) or text[start] != opening:
        raise ValueError(f"expected {opening!r} at offset {start}")
    depth = 0
    quote = False
    escaped = False
    for index in range(start, len(text)):
        char = text[index]
        if quote:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                quote = False
            continue
        if char == '"':
            quote = True
            continue
        if char == opening:
            depth += 1
        elif char == closing:
            depth -= 1
            if depth == 0:
                return text[start + 1 : index], index + 1
    raise ValueError(f"unterminated {opening}{closing} expression")


def split_args(body: str) -> list[str]:
    result: list[str] = []
    begin = 0
    parens = braces = brackets = 0
    quote = False
    escaped = False
    for index, char in enumerate(body):
        if quote:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                quote = False
            continue
        if char == '"':
            quote = True
        elif char == '(':
            parens += 1
        elif char == ')':
            parens -= 1
        elif char == '{':
            braces += 1
        elif char == '}':
            braces -= 1
        elif char == '[':
            brackets += 1
        elif char == ']':
            brackets -= 1
        elif char == ',' and not (parens or braces or brackets):
            result.append(body[begin:index].strip())
            begin = index + 1
    result.append(body[begin:].strip())
    return result


def parse_field_entries(body: str) -> list[dict[str, Any]]:
    fields: list[dict[str, Any]] = []
    for match in re.finditer(
        r"\{\s*sf([A-Za-z0-9_]+)\s*,\s*Soe(Required|Optional|Default)", body
    ):
        fields.append({"name": match.group(1), "style": STYLE_NAMES[match.group(2)]})
    return fields


def parse_oracle_transactions(oracle: Path) -> list[dict[str, Any]]:
    text = read(oracle / TRANSACTION_MACRO)
    rows: list[dict[str, Any]] = []
    for match in re.finditer(r"(?m)^TRANSACTION\s*\(", text):
        body, _ = balanced(text, match.end() - 1, "(", ")")
        args = split_args(body)
        if len(args) < 3:
            raise ValueError(f"malformed transaction macro near offset {match.start()}")
        tag, code_text, class_name = args[:3]
        try:
            code = int(code_text, 0)
        except ValueError as exc:
            raise ValueError(f"invalid transaction code {code_text!r}") from exc
        settings = args[3] if len(args) > 3 else ""
        fields = args[4] if len(args) > 4 else ""
        amendments = sorted(
            name.removeprefix("feature")
            for name in re.findall(r"\.amendment\s*=\s*(\w+)", settings)
        )
        rows.append(
            {
                "tag": tag,
                "code": code,
                "class": class_name,
                "amendments": amendments,
                "fields": parse_field_entries(fields),
            }
        )
    return rows


def parse_oracle_common_fields(oracle: Path) -> list[dict[str, str]]:
    text = read(oracle / TX_FORMATS)
    block = text.split("kCommonFields = std::vector<SOElement>{", 1)[1].split(
        "\n    };", 1
    )[0]
    fields = []
    for match in re.finditer(r"\{sf(\w+),\s*Soe(Required|Optional|Default)\}", block):
        fields.append({"name": match.group(1), "style": STYLE_NAMES[match.group(2)]})
    return fields


def parse_go_transaction_types(repo: Path) -> tuple[dict[int, dict[str, Any]], list[dict[str, Any]]]:
    text = read(repo / GO_TRANSACTION_TYPES)
    constants: dict[str, dict[str, Any]] = {}
    for match in re.finditer(
        r"(?m)^\s*TxType(\w+)\s+TxType\s*=\s*(0x[0-9A-Fa-f]+|\d+)", text
    ):
        constants[match.group(1)] = {
            "code": int(match.group(2), 0),
            "go_const": f"Type{match.group(1)}",
        }
    for match in re.finditer(
        r"(?m)^\s*case\s+TxType(\w+)\s*:\s*\n\s*return\s+\"([^\"]+)\"", text
    ):
        if match.group(1) in constants:
            constants[match.group(1)]["name"] = match.group(2)
    by_code: dict[int, dict[str, Any]] = {}
    for suffix, value in constants.items():
        if "name" in value:
            if value["code"] in by_code:
                raise ValueError(f"duplicate Go transaction type code {value['code']}")
            by_code[value["code"]] = {**value, "suffix": suffix}
    legacy_names = {"NickNameSet", "Contract", "SpinalTap", "HookSet"}
    legacy = [
        {**row, "name": suffix, "suffix": suffix}
        for suffix, row in constants.items()
        if suffix in legacy_names
    ]
    return by_code, legacy


def parse_go_registrations(repo: Path) -> dict[int, dict[str, Any]]:
    result: dict[int, dict[str, Any]] = {}
    constants, _ = parse_go_transaction_types(repo)
    by_const = {f"Type{row['suffix']}": row for row in constants.values()}
    for path in sorted((repo / "internal/tx").rglob("register.go")):
        text = read(path)
        for match in re.finditer(
            r"tx\.Register\(tx\.(Type\w+),\s*func\(\) tx\.Transaction\s*\{\s*return &([A-Za-z_]\w*)\{",
            text,
            re.S,
        ):
            constant, go_type = match.groups()
            if constant not in by_const:
                raise ValueError(f"registration uses unknown transaction constant {constant}")
            code = by_const[constant]["code"]
            if code in result:
                raise ValueError(f"duplicate Go transaction registration for code {code}")
            result[code] = {
                "go_type": go_type,
                "registration_path": str(path.relative_to(repo)),
            }
    return result


def parse_go_templates(repo: Path) -> dict[str, list[dict[str, str]]]:
    text = read(repo / GO_TEMPLATE)
    marker = "var txTemplates = map[Type][]templateField{"
    map_start = text.index("{", text.index(marker))
    body, _ = balanced(text, map_start, "{", "}")
    templates: dict[str, list[dict[str, str]]] = {}
    for match in re.finditer(r"(?m)^\s*(Type\w+)\s*:\s*\{", body):
        fields_body, _ = balanced(body, match.end() - 1, "{", "}")
        fields = []
        for field in re.finditer(
            r'\{name:\s*"([^"]+)",\s*style:\s*soe(\w+)\}', fields_body
        ):
            fields.append({"name": field.group(1), "style": STYLE_NAMES[field.group(2).title()]})
        templates[match.group(1)] = fields
    return templates


def method_body(text: str, start: int) -> str:
    opening = text.find("{", start)
    if opening < 0:
        return ""
    body, _ = balanced(text, opening, "{", "}")
    return body


def parse_go_methods(repo: Path) -> dict[str, dict[str, list[dict[str, Any]]]]:
    methods: dict[str, dict[str, list[dict[str, Any]]]] = {}
    for path in sorted((repo / "internal/tx").rglob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        text = read(path)
        for match in re.finditer(
            r"(?m)^func\s*\(\s*\w+\s+\*?(\w+)\s*\)\s*(\w+)\s*\([^)]*\)[^{]*\{",
            text,
        ):
            receiver, name = match.groups()
            if name not in METHODS:
                continue
            line = text.count("\n", 0, match.start()) + 1
            methods.setdefault(receiver, {}).setdefault(name, []).append(
                {"path": str(path.relative_to(repo)), "line": line, "symbol": f"{receiver}.{name}"}
            )
    return methods


def parse_go_required_amendments(repo: Path, methods: dict[str, dict[str, list[dict[str, Any]]]]) -> dict[str, list[str]]:
    registry = read(repo / GO_AMENDMENTS)
    feature_names = {
        f"Feature{match.group(1)}": match.group(2)
        for match in re.finditer(
            r'\bFeature(\w+)\s*=\s*(?:registerFeature|registerFix|registerRetired)\("([^"]+)"',
            registry,
        )
    }
    helper_bodies: dict[str, str] = {}
    for path in sorted((repo / "internal/tx").rglob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        text = read(path)
        for match in re.finditer(r"(?m)^func\s+(\w+)\s*\([^)]*\)[^{]*\{", text):
            helper_bodies[match.group(1)] = method_body(text, match.start())

    required: dict[str, list[str]] = {}
    for receiver, names in methods.items():
        entries = names.get("RequiredAmendments", [])
        if not entries:
            continue
        # Locate the source text again so one-line helper-return methods and
        # multiline methods use the same extraction path.
        values: set[str] = set()
        for entry in entries:
            text = read(repo / entry["path"])
            match = re.search(
                rf"func\s*\(\s*\w+\s+\*?{re.escape(receiver)}\s*\)\s*RequiredAmendments\b",
                text,
            )
            if not match:
                continue
            body = method_body(text, match.start())
            feature_vars = set(re.findall(r"amendment\.(Feature\w+)", body))
            for helper in re.findall(r"\b(\w+)\(\)", body):
                if helper in helper_bodies:
                    feature_vars.update(re.findall(r"amendment\.(Feature\w+)", helper_bodies[helper]))
            values.update(feature_names.get(var, var.removeprefix("Feature")) for var in feature_vars)
        required[receiver] = sorted(values)
    return required


def parse_go_amendments(repo: Path) -> list[dict[str, Any]]:
    text = read(repo / GO_AMENDMENTS)
    rows: list[dict[str, Any]] = []
    for match in re.finditer(
        r'\b(registerFeature|registerFix|registerRetired)\("([^"]+)"([^\n]*)', text
    ):
        helper, name, tail = match.groups()
        supported = None
        if helper == "registerRetired":
            supported = "yes"
        elif helper == "registerFeature" or helper == "registerFix":
            supported = (
                "conditional"
                if "confidentialTransferSupport" in tail
                else ("no" if "SupportedNo" in tail else "yes")
            )
        rows.append(
            {
                "name": name,
                "kind": {"registerFeature": "feature", "registerFix": "fix", "registerRetired": "retired"}[helper],
                "supported": supported,
            }
        )
    return rows


def parse_oracle_amendments(oracle: Path) -> list[dict[str, Any]]:
    text = read(oracle / FEATURE_MACRO)
    rows: list[dict[str, Any]] = []
    pattern = re.compile(
        r"(?m)^\s*XRPL_(FEATURE|FIX|RETIRE_FEATURE|RETIRE_FIX)\s*\(\s*([A-Za-z0-9_]+)(?:\s*,\s*([^\n]*))?"
    )
    for match in pattern.finditer(text):
        kind, raw_name, tail = match.groups()
        name = raw_name
        if kind in {"FIX", "RETIRE_FIX"}:
            name = "fix" + name
        supported = None
        if kind in {"FEATURE", "FIX"}:
            supported = "no" if "Supported::No" in (tail or "") else "yes"
        rows.append(
            {
                "name": name,
                "kind": {
                    "FEATURE": "feature",
                    "FIX": "fix",
                    "RETIRE_FEATURE": "retired",
                    "RETIRE_FIX": "retired",
                }[kind],
                "supported": supported,
            }
        )
    return rows


def source_path_for_oracle_class(oracle: Path, class_name: str) -> list[str]:
    transactors = oracle / "src/libxrpl/tx/transactors"
    direct = sorted(transactors.rglob(f"{class_name}.cpp"))
    if direct:
        return [str(path.relative_to(oracle)) for path in direct]
    hits = []
    for path in sorted(transactors.rglob("*.cpp")):
        if class_name in read(path):
            hits.append(str(path.relative_to(oracle)))
    return hits


def build_test_reference_index(
    repo: Path, constants: Iterable[str]
) -> dict[str, list[dict[str, Any]]]:
    constants = sorted(set(constants))
    if not constants:
        return {}
    pattern = re.compile(rf"\b(?P<constant>{'|'.join(map(re.escape, constants))})\b")
    reference_index: dict[str, dict[str, dict[str, int]]] = {}
    test_paths = sorted(
        path
        for root in (repo / "internal/tx", repo / "internal/testing")
        for path in root.rglob("*_test.go")
    )
    for path in test_paths:
        text = read(path)
        test_defs = [
            (match.start(), match.group(1))
            for match in re.finditer(r"(?m)^func\s+(Test\w+)", text)
        ]
        test_starts = [start for start, _ in test_defs]
        for match in pattern.finditer(text):
            test_index = bisect_right(test_starts, match.start()) - 1
            if test_index >= 0:
                owner = test_defs[test_index][1]
                alias = match.group("constant")
                path_key = str(path.relative_to(repo))
                reference_index.setdefault(alias, {}).setdefault(path_key, {}).setdefault(
                    owner, text.count("\n", 0, match.start()) + 1
                )
    references: dict[str, list[dict[str, Any]]] = {}
    for alias, paths in reference_index.items():
        references[alias] = [
            {
                "path": path,
                "tests": [
                    {"name": test_name, "line": line}
                    for test_name, line in sorted(test_lines.items())
                ],
                "kind": "Go source reference; execution is tracked separately",
            }
            for path, test_lines in sorted(paths.items())
        ]
    return references


def family_exclusion(name: str) -> str | None:
    if name == "Batch":
        return "current legacy conformance suite app/Batch is out of scope"
    if name == "DelegateSet":
        return "current legacy conformance suite app/Delegate is out of scope"
    if name.startswith("Vault"):
        return "current legacy conformance suite app/Vault is out of scope"
    return None


def engine_inventory(repo: Path) -> dict[str, Any]:
    return {
        "registry": {
            "path": "internal/tx/registry.go",
            "symbols": ["Register", "NewFromType", "SupportedTypes"],
            "aggregator_path": "internal/tx/all/all.go",
            "aggregator_symbol": "RegisterAll",
        },
        "common_template": {
            "path": "internal/tx/template.go",
            "symbols": ["commonFields", "txTemplates", "ValidateTemplateFields", "ValidateTransactionTemplateAllowlist"],
        },
        "normal": {
            "entry": {"path": "internal/tx/engine/apply.go", "symbols": ["Engine.Apply", "Engine.ApplyWithContext", "Engine.applyWithContext"]},
            "validate": {"path": "internal/tx/engine/preflight.go", "symbols": ["Engine.preflightStructure", "runTypePreflight", "Transaction.Validate"]},
            "preflight": {"path": "internal/tx/engine/preflight.go", "symbols": ["Engine.preflight", "Engine.preflight1", "Engine.preflight0", "checkExtraFeatures", "runTypePreflight", "Engine.verifySignatures"]},
            "preclaim": {"path": "internal/tx/engine/preclaim.go", "symbols": ["Engine.preclaim", "Preclaimer.Preclaim"]},
            "apply": {"path": "internal/tx/engine/do_apply.go", "symbols": ["Engine.doApply", "Engine.invokeApply", "Engine.invokeApplyInner", "Appliable.Apply"]},
            "invariants": {"path": "internal/tx/engine/do_apply.go", "symbols": ["Engine.runInvariants", "Engine.runInvariantsOnTable", "Engine.CheckInnerInvariants"], "checker_path": "internal/tx/invariants/invariants.go", "checker_symbol": "CheckInvariants"},
        },
        "pseudo": {
            "entry": {"path": "internal/tx/engine/apply.go", "symbols": ["Engine.ApplyPseudo", "Engine.ApplyPseudoWithContext", "Engine.applyPseudoTransaction"]},
            "preflight": {"path": "internal/tx/engine/pseudo_gates.go", "symbols": ["Engine.pseudoPreflight"]},
            "preclaim": {"path": "internal/tx/engine/pseudo_gates.go", "symbols": ["Engine.pseudoPreclaim", "PseudoPreclaim.PreclaimPseudo"]},
            "apply": {"path": "internal/tx/engine/apply.go", "symbols": ["Engine.applyPseudoTransaction", "Appliable.Apply"]},
            "invariants": {"status": "not run by ApplyPseudo path"},
        },
        "batch_inner": {
            "preflight": {"path": "internal/tx/engine/preflight.go", "symbols": ["Engine.preflightInner", "BatchInnerPreflightRunner.PreflightInnerTransactions"]},
            "preclaim": {"path": "internal/tx/engine/preclaim.go", "symbols": ["Engine.preclaimInner"]},
            "apply": {"path": "internal/tx/engine/apply.go", "symbols": ["Engine.ApplyInnerTransaction", "BatchInnerApplier.ApplyInnerTransactions"]},
            "invariants": {"path": "internal/tx/engine/do_apply.go", "symbols": ["Engine.CheckInnerInvariants"]},
        },
    }


def build_inventory(repo: Path, oracle: Path) -> dict[str, Any]:
    oracle_transactions = parse_oracle_transactions(oracle)
    go_types, legacy_types = parse_go_transaction_types(repo)
    go_registrations = parse_go_registrations(repo)
    go_templates = parse_go_templates(repo)
    test_reference_index = build_test_reference_index(
        repo,
        [
            go_types[row["code"]]["go_const"]
            for row in oracle_transactions
            if row["code"] in go_types
        ],
    )
    common_go = []
    template_text = read(repo / GO_TEMPLATE)
    common_marker = template_text.index("var commonFields")
    common_body, _ = balanced(
        template_text,
        template_text.index("{", common_marker),
        "{",
        "}",
    )
    for match in re.finditer(r'\{name:\s*"([^"]+)",\s*style:\s*soe(\w+)\}', common_body):
        common_go.append({"name": match.group(1), "style": STYLE_NAMES[match.group(2).title()]})
    common_oracle = parse_oracle_common_fields(oracle)
    methods = parse_go_methods(repo)
    required_amendments = parse_go_required_amendments(repo, methods)
    go_amendments = parse_go_amendments(repo)
    oracle_amendments = parse_oracle_amendments(oracle)
    if len({row["name"] for row in go_amendments}) != len(go_amendments):
        raise ValueError("duplicate Go amendment registry row")
    if len({row["name"] for row in oracle_amendments}) != len(oracle_amendments):
        raise ValueError("duplicate oracle amendment registry row")
    oracle_amendment_by_name = {row["name"]: row for row in oracle_amendments}
    go_amendment_by_name = {row["name"]: row for row in go_amendments}

    transactions: list[dict[str, Any]] = []
    for oracle_row in sorted(oracle_transactions, key=lambda row: row["code"]):
        code = oracle_row["code"]
        go_row = go_types.get(code)
        if go_row is None:
            raise ValueError(f"oracle transaction {oracle_row['class']}({code}) is absent from Go type registry")
        registration = go_registrations.get(code)
        if registration is None:
            raise ValueError(f"oracle transaction {oracle_row['class']}({code}) is absent from Go runtime registration")
        if go_row["name"] != oracle_row["class"]:
            # The Go enum constants for pseudo transactions have shorter names;
            # their wire names remain the protocol class names.
            name_match = {
                "EnableAmendment": "EnableAmendment",
                "SetFee": "SetFee",
            }
            if name_match.get(oracle_row["class"]) != go_row["name"]:
                raise ValueError(f"transaction name mismatch at code {code}: Go={go_row['name']} oracle={oracle_row['class']}")
        go_type = registration["go_type"]
        row_methods = methods.get(go_type, {})
        method_names = sorted(row_methods)
        unique_paths = {registration["registration_path"]}
        for entries in row_methods.values():
            unique_paths.update(entry["path"] for entry in entries)
        pseudo = code in {100, 101, 102}
        excluded = family_exclusion(oracle_row["class"])
        go_fields = go_templates.get(go_row["go_const"], [])
        go_required = required_amendments.get(go_type, [])
        transactions.append(
            {
                "name": oracle_row["class"],
                "code": code,
                "oracle_tag": oracle_row["tag"],
                "go_const": go_row["go_const"],
                "go_type": go_type,
                "oracle_source": source_path_for_oracle_class(oracle, oracle_row["class"]),
                "go_source": sorted(unique_paths),
                "oracle_amendments": oracle_row["amendments"],
                "go_required_amendments": go_required,
                "oracle_fields": oracle_row["fields"],
                "go_fields": go_fields,
                "field_templates_match": oracle_row["fields"] == go_fields,
                "methods": method_names,
                "method_locations": row_methods,
                "stages": {
                    "validate": "typed Validate method" if "Validate" in row_methods else "embedded BaseTx.Validate",
                    "preflight": {
                        "common": True,
                        "typed": [name for name in ["PreflightRules", "PreflightWithRules", "CheckExtraFeatures", "PreflightSigValidated"] if name in row_methods],
                    },
                    "preclaim": "pseudo gate" if pseudo else ("typed Preclaim" if "Preclaim" in row_methods else "engine common checks only"),
                    "apply": "pseudo ApplyPseudo dispatch" if pseudo else "Appliable.Apply",
                    "invariants": "not run by pseudo path" if pseudo else ("outer + inner invariant seam" if oracle_row["class"] == "Batch" else "outer invariant seam"),
                },
                "status": {
                    "oracle_registered": True,
                    "go_registered": True,
                    "go_supported": "Apply" in row_methods,
                    "pseudo": pseudo,
                    "conformance_excluded": excluded is not None,
                    "conformance_exclusion_reason": excluded,
                    "executed_in_checked_in_corpus": False,
                    "oracle_comparison_executed": False,
                },
                "test_references": test_reference_index.get(oracle_row["class"], [])
                + test_reference_index.get(go_row["go_const"], []),
            }
        )

    amendment_names = sorted(set(go_amendment_by_name) | set(oracle_amendment_by_name))
    amendments = []
    for name in amendment_names:
        go_row = go_amendment_by_name.get(name)
        oracle_row = oracle_amendment_by_name.get(name)
        amendments.append(
            {
                "name": name,
                "go": go_row,
                "oracle": oracle_row,
                "status": "matched" if go_row and oracle_row else ("go_only" if go_row else "oracle_only"),
            }
        )

    transaction_codes = {row["code"] for row in transactions}
    transaction_names = {row["name"] for row in transactions}
    if (
        len(transactions) != len(oracle_transactions)
        or len(transaction_codes) != len(transactions)
        or len(transaction_names) != len(transactions)
    ):
        raise ValueError("duplicate or missing oracle transaction rows")
    if len(common_go) != len(common_oracle) or common_go != common_oracle:
        raise ValueError("Go and oracle common transaction fields differ")

    return {
        "schema": SCHEMA,
        "oracle": {
            "repository": ORACLE_REPOSITORY,
            "tag": ORACLE_TAG,
            "commit": ORACLE_COMMIT,
            "source_sha256": {
                TRANSACTION_MACRO: sha256(oracle / TRANSACTION_MACRO),
                TX_FORMATS: sha256(oracle / TX_FORMATS),
                FEATURE_MACRO: sha256(oracle / FEATURE_MACRO),
            },
        },
        "go_base_commit": GO_BASE_COMMIT,
        "counts": {
            "oracle_transactions": len(oracle_transactions),
            "go_runtime_transaction_rows": len(go_registrations),
            "go_legacy_enum_rows": len(legacy_types),
            "oracle_amendments": len(oracle_amendments),
            "go_amendments": len(go_amendments),
            "go_only_amendments": sum(row["status"] == "go_only" for row in amendments),
            "common_fields": len(common_go),
        },
        "source_sha256": {
            GO_TRANSACTION_TYPES: sha256(repo / GO_TRANSACTION_TYPES),
            GO_TEMPLATE: sha256(repo / GO_TEMPLATE),
            GO_AMENDMENTS: sha256(repo / GO_AMENDMENTS),
        },
        "engine": engine_inventory(repo),
        "common_fields": {"oracle": common_oracle, "go": common_go, "match": common_go == common_oracle},
        "legacy_go_transaction_enums": sorted(
            [{"name": row["name"], "code": row["code"], "go_const": row["go_const"]} for row in legacy_types],
            key=lambda row: row["code"],
        ),
        "amendments": amendments,
        "transactions": transactions,
        "evidence": {
            "execution_semantics": "Rows are source/registry coverage. A row is executed only when a checked-in or explicitly supplied fixture record says so; named tests alone never set executed=true.",
            "conformance_runner": {
                "runner_path": "internal/testing/conformance",
                "manifest_path": "internal/testing/conformance/testdata/manifest.json",
                "manifest_present_at_base": (repo / "internal/testing/conformance/testdata/manifest.json").exists(),
                "oracle_comparison_executed_at_base": False,
                "reason": "The base tree has no checked-in fixture corpus; the v3 runner requires an external GOXRPL_FIXTURES_DIR corpus pinned to rippled 3.4.0.",
                "status_snapshot_path": "docs/conformance-status.md",
                "status_snapshot_is_execution_record": False,
                "out_of_scope_path": "scripts/conformance-out-of-scope.txt",
                "out_of_scope_suites": ["app/Batch", "app/Delegate", "app/Vault"],
            },
            "go_only_contracts": [
                {"path": "internal/tx/common_wire_coverage_test.go", "test": "TestCommonTemplateFieldsSurviveRegisteredTypedProjection", "scope": "all registered types × common fields; Go codec round-trip only"},
                {"path": "internal/tx/template_registry_test.go", "test": "TestParseFromBinary_EveryRegisteredTypeAcceptsCommonFields", "scope": "registered type parsing; Go-only"},
                {"path": "codec/binarycodec/definitions/final_inventory_test.go", "test": "TestFinalDefinitionsMatchRippled", "scope": "definitions registry against v3.4.0 oracle; not engine execution"},
            ],
            "oracle_source_contracts": [
                {"path": TRANSACTION_MACRO, "scope": "transaction tags, codes, settings, unique fields"},
                {"path": TX_FORMATS, "scope": "common field order and styles"},
                {"path": FEATURE_MACRO, "scope": "amendment registry"},
            ],
        },
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--oracle", type=Path, help="local rippled v3.4.1 oracle checkout")
    parser.add_argument("--output", type=Path, help="inventory JSON output path")
    parser.add_argument("--check", action="store_true", help="fail if the checked-in snapshot is stale")
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[2]
    oracle = args.oracle or repo.parents[1] / "rippled-worktrees/v3.4.1-oracle"
    output = args.output or repo / "scripts/engine-coverage/inventory.json"
    if not oracle.is_dir():
        raise SystemExit(f"engine coverage: oracle checkout not found: {oracle}")
    head = oracle_head(oracle)
    if head != ORACLE_COMMIT:
        raise SystemExit(
            f"engine coverage: oracle HEAD {head} does not match pinned {ORACLE_COMMIT}"
        )
    inventory = build_inventory(repo, oracle)
    rendered = json.dumps(inventory, indent=2, sort_keys=False) + "\n"
    if args.check:
        if not output.exists() or output.read_text() != rendered:
            print(f"engine coverage: stale inventory: {output}", file=sys.stderr)
            return 1
        return 0
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(rendered)
    print(f"wrote {output} ({len(inventory['transactions'])} transactions, {len(inventory['amendments'])} amendments)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
