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
from collections import Counter
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
GO_INVENTORY_SOURCE_COMMIT = "b002da57fb415b40a1150f0e0dc82f764a340556"
SCHEMA = 1

TRANSACTION_MACRO = "include/xrpl/protocol/detail/transactions.macro"
TX_FORMATS = "src/libxrpl/protocol/TxFormats.cpp"
FEATURE_MACRO = "include/xrpl/protocol/detail/features.macro"
V4_MANIFEST = "internal/testing/conformance/testdata/rippled-3.4.1-v4/manifest.json"
V4_README = "internal/testing/conformance/testdata/rippled-3.4.1-v4/README.md"

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


def common_field_stage_mapping() -> list[dict[str, Any]]:
    """Describe where each common field can affect the transaction pipeline.

    These are source links, not execution claims.  The checked-in snapshot keeps
    this beside the registry rows so a field can be reviewed from wire/template
    handling through the engine stages without implying that every edge ran.
    """

    return [
        {
            "name": "TransactionType",
            "classification": "wire + dispatch/template",
            "stages": ["decode", "dispatch", "preflight"],
            "go": [
                {"path": "internal/tx/registry.go", "symbols": ["FromJSON", "NewFromType"]},
                {"path": "internal/tx/template.go", "symbols": ["commonFields", "FormatCommonFields"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/protocol/STTx.cpp", "symbols": ["STTx::STTx", "getTxFormat"]},
                {"path": TX_FORMATS, "symbols": ["TxFormats::getCommonFields"]},
            ],
            "behavior": "Selects the registered type and its merged serialization template.",
            "unexecuted_edges": "Unknown-type parsing, every format rejection, and every type dispatch branch require runtime evidence.",
        },
        {
            "name": "Flags",
            "classification": "preflight flag mask",
            "stages": ["preflight0", "type-specific preflight", "apply"],
            "go": [
                {"path": "internal/tx/engine/preflight.go", "symbols": ["Engine.preflight0", "checkFlagsMask"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["preflight0"]},
            ],
            "behavior": "The engine checks the universal/type flag mask before type-specific work; handlers may then interpret permitted bits.",
            "unexecuted_edges": "Invalid-mask precedence and each type's permitted flag behavior are not established by registry rows.",
        },
        {
            "name": "SourceTag",
            "classification": "wire + type-specific ledger effect",
            "stages": ["serialization", "apply"],
            "go": [
                {"path": "internal/tx/transaction.go", "symbols": ["Common.ToMap"]},
                {"path": "internal/tx/paychan/helpers.go", "symbols": ["newPayChannelData"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/tx/transactors/check/CheckCreate.cpp", "symbols": ["CheckCreate::doApply"]},
                {"path": "src/libxrpl/tx/transactors/escrow/EscrowCreate.cpp", "symbols": ["EscrowCreate::doApply"]},
                {"path": "src/libxrpl/tx/transactors/payment_channel/PaymentChannelCreate.cpp", "symbols": ["PaymentChannelCreate::doApply"]},
            ],
            "behavior": "The common template carries the value; selected handlers copy it to created ledger objects.",
            "unexecuted_edges": "Only the listed handler families consume it; registry/codec evidence does not cover absent/present apply outcomes.",
        },
        {
            "name": "Account",
            "classification": "identity + preflight/preclaim gate",
            "stages": ["preflight1", "preclaim", "signature", "apply"],
            "go": [
                {"path": "internal/tx/engine/preflight.go", "symbols": ["checkAccountPresent"]},
                {"path": "internal/tx/engine/preclaim.go", "symbols": ["Engine.preclaimLoadAccount", "Engine.checkSign"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["Transactor::preflight1", "Transactor::checkSign"]},
                {"path": "src/libxrpl/tx/applySteps.cpp", "symbols": ["invokePreclaim"]},
            ],
            "behavior": "Supplies the source identity used to load state, authorize the signature, charge fees, and apply changes.",
            "unexecuted_edges": "Zero-account, missing-account, delegated-account, and pseudo-account paths need explicit cases.",
        },
        {
            "name": "Sequence",
            "classification": "sequence proxy + account mutation",
            "stages": ["preflight1", "preclaim", "apply"],
            "go": [
                {"path": "internal/tx/engine/preflight.go", "symbols": ["Engine.preflightSequence"]},
                {"path": "internal/tx/engine/preclaim.go", "symbols": ["Engine.checkSeqProxy"]},
                {"path": "internal/tx/engine/do_apply.go", "symbols": ["Engine.applyPreApplyAccountChanges"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/protocol/STTx.cpp", "symbols": ["STTx::getSeqProxy"]},
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["Transactor::checkSeqProxy", "Transactor::consumeSeqProxy"]},
            ],
            "behavior": "A nonzero sequence selects the normal proxy and is consumed before the handler runs.",
            "unexecuted_edges": "Sequence mismatch, zero sequence, ticket conflict, retry, and sequence increment need execution evidence.",
        },
        {
            "name": "PreviousTxnID",
            "classification": "wire + ledger-state metadata",
            "stages": ["serialization", "ledger metadata"],
            "go": [
                {"path": "internal/tx/transaction.go", "symbols": ["Common.ToMap"]},
                {"path": "internal/tx/metadata.go", "symbols": ["buildAffectedNodeInner"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/protocol/STLedgerEntry.cpp", "symbols": ["STLedgerEntry::thread"]},
                {"path": "src/libxrpl/ledger/ApplyStateTable.cpp", "symbols": ["ApplyStateTable::apply"]},
            ],
            "behavior": "It is a common transaction field and also participates in ledger-entry metadata threading; it is not a common transaction engine gate.",
            "unexecuted_edges": "Transaction presence, ledger-object stamping, and zero-hash omission are separate cases and are not inferred from format parity.",
        },
        {
            "name": "LastLedgerSequence",
            "classification": "preclaim expiry gate",
            "stages": ["preclaim"],
            "go": [
                {"path": "internal/tx/engine/preclaim.go", "symbols": ["Engine.checkPriorTxAndLastLedger"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["Transactor::checkPriorTxAndLastLedger"]},
            ],
            "behavior": "Preclaim rejects a transaction once the current ledger is past the declared limit.",
            "unexecuted_edges": "Boundary equality, expired transactions, and interaction with AccountTxnID need runtime cases.",
        },
        {
            "name": "AccountTxnID",
            "classification": "preclaim guard + apply account tracking",
            "stages": ["preflight1", "preclaim", "apply"],
            "go": [
                {"path": "internal/tx/engine/preflight.go", "symbols": ["Engine.preflightSequence"]},
                {"path": "internal/tx/engine/preclaim.go", "symbols": ["Engine.checkPriorTxAndLastLedger"]},
                {"path": "internal/tx/engine/do_apply.go", "symbols": ["Engine.applyPreApplyAccountChanges"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["Transactor::preflight1", "Transactor::checkPriorTxAndLastLedger", "Transactor::apply"]},
            ],
            "behavior": "A supplied prior hash is checked in preclaim; enabled account tracking is updated during apply.",
            "unexecuted_edges": "Mismatch, ticket prohibition, enabled tracking, and successful hash update need separate execution evidence.",
        },
        {
            "name": "Fee",
            "classification": "fee validation + account mutation",
            "stages": ["preflight1", "preclaim", "apply", "invariants"],
            "go": [
                {"path": "internal/tx/engine/preflight.go", "symbols": ["Engine.validateFee"]},
                {"path": "internal/tx/engine/preclaim.go", "symbols": ["Engine.checkFee"]},
                {"path": "internal/tx/engine/do_apply.go", "symbols": ["Engine.applyPreApplyAccountChanges"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["Transactor::preflight1", "Transactor::checkFee", "Transactor::payFee", "Transactor::apply"]},
                {"path": "src/libxrpl/tx/applySteps.cpp", "symbols": ["calculateBaseFee", "doApply"]},
            ],
            "behavior": "The fee is structurally validated, compared with the base fee, charged to the selected payer, and checked by invariants.",
            "unexecuted_edges": "Malformed, below-floor, sponsored, retry, tec, and invariant fee outcomes are not implied by success fixtures.",
        },
        {
            "name": "OperationLimit",
            "classification": "wire/template only in this oracle",
            "stages": ["serialization", "template"],
            "go": [
                {"path": "internal/tx/template.go", "symbols": ["commonFields", "FormatCommonFields"]},
                {"path": "internal/tx/transaction.go", "symbols": ["Common.ToMap"]},
            ],
            "oracle": [
                {"path": TX_FORMATS, "symbols": ["TxFormats::getCommonFields"]},
            ],
            "behavior": "The field is accepted as a common wire/template field; no v3.4.1 common engine check or mutation was found.",
            "unexecuted_edges": "Presence, value bounds, signing, and any type-specific interpretation remain unexecuted; do not call it behaviorally covered.",
        },
        {
            "name": "Memos",
            "classification": "local submission check",
            "stages": ["serialization", "local checks"],
            "go": [
                {"path": "internal/tx/local_checks.go", "symbols": ["PassesTransactionLocalChecks", "LocalChecksFailureReason", "serializedMemosLength"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/protocol/STTx.cpp", "symbols": ["passesLocalChecks", "isMemoOkay"]},
                {"path": "src/libxrpl/tx/apply.cpp", "symbols": ["checkValidity"]},
            ],
            "behavior": "Memo shape, hex, URL-character, and serialized-size checks occur at local submission validation before the consensus engine.",
            "unexecuted_edges": "Oversize, malformed child fields, parser boundaries, and local-check cache behavior require direct submission tests.",
        },
        {
            "name": "SigningPubKey",
            "classification": "signature shape + verification",
            "stages": ["preflight1", "preflight2", "preclaim"],
            "go": [
                {"path": "internal/tx/engine/preflight.go", "symbols": ["checkSigningKeyShape", "Engine.verifySignatures"]},
                {"path": "internal/tx/sign/signature.go", "symbols": ["VerifySignature"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["preflightCheckSigningKey", "Transactor::preflight2", "Transactor::checkSign"]},
            ],
            "behavior": "The key shape is checked before cryptographic validity; account authorization is checked after ledger state is available.",
            "unexecuted_edges": "Empty key, malformed key, master/regular key, delegated signer, and dry-run cases need explicit evidence.",
        },
        {
            "name": "TicketSequence",
            "classification": "ticket sequence proxy + consumption",
            "stages": ["preflight1", "preclaim", "apply"],
            "go": [
                {"path": "internal/tx/engine/preflight.go", "symbols": ["Engine.preflightSequence"]},
                {"path": "internal/tx/engine/preclaim.go", "symbols": ["Engine.checkSeqProxy"]},
                {"path": "internal/tx/engine/do_apply.go", "symbols": ["Engine.consumeTicket"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/protocol/STTx.cpp", "symbols": ["STTx::getSeqProxy"]},
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["Transactor::checkSeqProxy", "Transactor::consumeSeqProxy"]},
            ],
            "behavior": "TicketSequence selects and consumes a ticket instead of incrementing the account sequence.",
            "unexecuted_edges": "Missing ticket, wrong owner, simultaneous Sequence, recovery, and retry paths need runtime cases.",
        },
        {
            "name": "TxnSignature",
            "classification": "single-signature verification",
            "stages": ["preflight2", "preclaim"],
            "go": [
                {"path": "internal/tx/engine/preflight.go", "symbols": ["Engine.verifySignatures"]},
                {"path": "internal/tx/sign/signature.go", "symbols": ["VerifySignature"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/protocol/STTx.cpp", "symbols": ["STTx::checkSign", "STTx::checkSingleSign"]},
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["Transactor::preflight2", "Transactor::checkSign"]},
            ],
            "behavior": "The signature is checked in the crypto preflight stage and account authorization is checked in preclaim.",
            "unexecuted_edges": "Bad signature, canonicality, missing signature, delegated role, and role-prefix amendment paths remain distinct.",
        },
        {
            "name": "Signers",
            "classification": "multi-signature verification",
            "stages": ["preflight2", "preclaim"],
            "go": [
                {"path": "internal/tx/engine/preclaim.go", "symbols": ["Engine.checkMultiSign", "Engine.checkMultiSignForAccount"]},
                {"path": "internal/tx/sign/signature.go", "symbols": ["VerifyMultiSignatureCrypto", "VerifyMultiSignature"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/protocol/STTx.cpp", "symbols": ["STTx::checkSign", "STTx::checkMultiSign"]},
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["Transactor::checkSign", "Transactor::checkMultiSign"]},
                {"path": "src/libxrpl/protocol/Sign.cpp", "symbols": ["buildMultiSigningData", "startMultiSigningData"]},
            ],
            "behavior": "Crypto checks run before the ledger signer-list/quorum checks; signer data also changes the signing payload.",
            "unexecuted_edges": "Ordering, quorum, unauthorized signer, duplicate signer, delegated signer, and sponsor multisign cases need execution evidence.",
        },
        {
            "name": "NetworkID",
            "classification": "network preflight gate",
            "stages": ["preflight0"],
            "go": [
                {"path": "internal/tx/engine/preflight.go", "symbols": ["Engine.validateNetworkID"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["preflight0"]},
            ],
            "behavior": "Legacy networks reject a supplied ID; nonlegacy networks require the matching ID.",
            "unexecuted_edges": "Legacy, missing, wrong, matching, and pseudo-transaction exceptions require separate engine runs.",
        },
        {
            "name": "Delegate",
            "classification": "delegated identity + authorization",
            "stages": ["preflight1", "preclaim", "signature", "apply"],
            "go": [
                {"path": "internal/tx/engine/preflight.go", "symbols": ["checkDelegate"]},
                {"path": "internal/tx/engine/preclaim.go", "symbols": ["Engine.checkPermission", "Engine.checkSign"]},
                {"path": "internal/tx/engine/sponsor_fee.go", "symbols": ["Engine.getFeePayer"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["Transactor::preflight1", "Transactor::checkSign", "Transactor::getFeePayer"]},
                {"path": "src/libxrpl/protocol/STTx.cpp", "symbols": ["STTx::getInitiator", "STTx::getFeePayerID"]},
            ],
            "behavior": "Delegate changes the signing identity and fee payer after preflight validates the relationship.",
            "unexecuted_edges": "Disabled amendment, self-delegate, missing permission, delegate signer, and fee-payer failures need runtime cases.",
        },
        {
            "name": "Sponsor",
            "classification": "sponsorship identity + fee/reserve paths",
            "stages": ["preflight1", "preclaim", "apply", "invariants"],
            "go": [
                {"path": "internal/tx/engine/preflight.go", "symbols": ["checkSponsorFields"]},
                {"path": "internal/tx/engine/preclaim.go", "symbols": ["Engine.checkSponsor"]},
                {"path": "internal/tx/engine/sponsor_fee.go", "symbols": ["Engine.getFeePayer"]},
                {"path": "internal/tx/sponsor_reserve.go", "symbols": ["readSponsorship"]},
                {"path": "internal/tx/invariants/sponsorship.go", "symbols": ["checkSponsorship"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["preflight1Sponsor", "Transactor::checkSponsor", "Transactor::getFeePayer"]},
                {"path": "src/libxrpl/tx/invariants/InvariantCheck.cpp", "symbols": ["InvariantCheck"]},
            ],
            "behavior": "Sponsor fields select preflight, sponsorship-object checks, fee payer selection, reserve mutation, and invariant accounting.",
            "unexecuted_edges": "Feature-disabled, malformed combinations, co-signed, pre-funded, reserve, and invariant failure cases need direct evidence.",
        },
        {
            "name": "SponsorFlags",
            "classification": "sponsorship mode gate",
            "stages": ["preflight1", "preclaim", "apply"],
            "go": [
                {"path": "internal/tx/engine/preflight.go", "symbols": ["checkSponsorFields"]},
                {"path": "internal/tx/engine/preclaim.go", "symbols": ["Engine.checkSponsor"]},
                {"path": "internal/tx/engine/sponsor_fee.go", "symbols": ["Engine.getFeePayer"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["preflight1Sponsor", "Transactor::checkSponsor", "Transactor::getFeePayer"]},
            ],
            "behavior": "The mode bits choose fee or reserve sponsorship and are validated with Sponsor presence and allow-lists.",
            "unexecuted_edges": "Zero, masked, unsupported-type, fee, reserve, and changing-sponsorship modes need runtime evidence.",
        },
        {
            "name": "SponsorSignature",
            "classification": "nested sponsorship signature",
            "stages": ["preflight2", "preclaim"],
            "go": [
                {"path": "internal/tx/engine/preclaim.go", "symbols": ["Engine.checkSign"]},
                {"path": "internal/tx/sign/signature.go", "symbols": ["VerifySponsorSignatureWithRules", "VerifyMultiSignatureCrypto"]},
            ],
            "oracle": [
                {"path": "src/libxrpl/protocol/STTx.cpp", "symbols": ["STTx::checkSign"]},
                {"path": "src/libxrpl/protocol/Sign.cpp", "symbols": ["signatureField", "signingPrefix"]},
                {"path": "src/libxrpl/tx/Transactor.cpp", "symbols": ["Transactor::checkSign"]},
            ],
            "behavior": "The nested sponsor signature has a role-specific signing prefix and is checked alongside the transaction signature.",
            "unexecuted_edges": "Missing sponsor, wrong role prefix, single/multisign, canonicality, and feature-era cases need direct comparisons.",
        },
    ]


def parse_v4_corpus(repo: Path) -> dict[str, Any]:
    manifest_path = repo / V4_MANIFEST
    readme_path = repo / V4_README
    if not manifest_path.is_file() or not readme_path.is_file():
        raise ValueError("checked-in v4 conformance manifest/README is missing")
    try:
        manifest = json.loads(read(manifest_path))
    except json.JSONDecodeError as exc:
        raise ValueError(f"invalid v4 conformance manifest: {exc}") from exc
    if manifest.get("schema") != 3 or manifest.get("fixture_version") != "v4":
        raise ValueError("unexpected v4 conformance manifest schema or fixture version")
    if manifest.get("oracle_repository") != ORACLE_REPOSITORY:
        raise ValueError("v4 conformance manifest repository differs from pinned oracle")
    if manifest.get("rippled_tag") != ORACLE_TAG or manifest.get("rippled_commit") != ORACLE_COMMIT:
        raise ValueError("v4 conformance manifest oracle differs from pinned oracle")

    fixtures = manifest.get("fixtures")
    if not isinstance(fixtures, dict) or manifest.get("fixture_count") != len(fixtures):
        raise ValueError("v4 conformance fixture count does not match manifest rows")
    family_counts = Counter()
    for fixture_name, fixture in fixtures.items():
        if not isinstance(fixture, dict) or not fixture.get("family"):
            raise ValueError(f"v4 conformance fixture {fixture_name!r} has no family")
        family_counts[fixture["family"]] += 1
    matrix = manifest.get("amendment_matrix", [])
    if not isinstance(matrix, list) or not matrix or any(row.get("supported") is not True for row in matrix):
        raise ValueError("v4 conformance amendment matrix is missing or has unsupported profiles")

    readme_text = read(readme_path)
    recording = re.search(
        r"\*\*(\d+) fixtures\*\* with \*\*([\d,]+) assertions and zero failures\*\*",
        readme_text,
    )
    if not recording:
        raise ValueError("v4 conformance README has no C++ recording summary")
    malformed_word = {"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5}
    observations = re.search(
        r"observations are (\d+) `tesSUCCESS` and ([a-z]+) `temMALFORMED`", readme_text
    )
    malformed = malformed_word.get(observations.group(2), -1) if observations else -1
    if int(recording.group(1)) != manifest["fixture_count"]:
        raise ValueError("v4 README fixture count differs from manifest")

    return {
        "manifest_path": V4_MANIFEST,
        "manifest_sha256": sha256(manifest_path),
        "readme_path": V4_README,
        "readme_sha256": sha256(readme_path),
        "manifest_schema": manifest["schema"],
        "fixture_version": manifest["fixture_version"],
        "fixture_count": manifest["fixture_count"],
        "family_counts": dict(sorted(family_counts.items())),
        "amendment_profile_count": len(matrix),
        "oracle_binary_sha256": manifest.get("binary_sha256"),
        "recorder_commit": manifest.get("recorder_commit"),
        "recorder_source_sha256": manifest.get("recorder_sources", {}),
        "config_identity": manifest.get("config_identity"),
        "oracle_recording": {
            "suite": "app/StrictOracleRecorder",
            "fixtures": int(recording.group(1)),
            "assertions": int(recording.group(2).replace(",", "")),
            "failures": 0,
            "tesSUCCESS": int(observations.group(1)) if observations else None,
            "temMALFORMED": malformed if observations else None,
            "comparison_side": "C++ oracle recorder only",
        },
        "go_replay_report": {
            "status": "separate runtime report required",
            "runner_path": "internal/testing/conformance/conformance_test.go",
            "report_environment": "GOXRPL_CONFORMANCE_REPORT",
            "executed_by_generator": False,
            "per_transaction_flags_written_here": False,
        },
        "coverage_limits": manifest.get("coverage_limits", []),
    }


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
    helper_bodies: dict[tuple[str, str], str] = {}
    for path in sorted((repo / "internal/tx").rglob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        text = read(path)
        package_path = str(path.parent.relative_to(repo))
        for match in re.finditer(r"(?m)^func\s+(\w+)\s*\([^)]*\)[^{]*\{", text):
            helper_bodies[(package_path, match.group(1))] = method_body(text, match.start())

    required: dict[str, list[str]] = {}
    for receiver, names in methods.items():
        entries = names.get("RequiredAmendments", [])
        if not entries:
            continue
        # Locate the source text again so one-line helper-return methods and
        # multiline methods use the same extraction path.
        values: set[str] = set()
        for entry in entries:
            source_path = repo / entry["path"]
            text = read(source_path)
            package_path = str(source_path.parent.relative_to(repo))
            match = re.search(
                rf"func\s*\(\s*\w+\s+\*?{re.escape(receiver)}\s*\)\s*RequiredAmendments\b",
                text,
            )
            if not match:
                continue
            body = method_body(text, match.start())
            feature_vars = set(re.findall(r"amendment\.(Feature\w+)", body))
            for helper in re.findall(r"\b(\w+)\(\)", body):
                helper_body = helper_bodies.get((package_path, helper))
                if helper_body is not None:
                    feature_vars.update(re.findall(r"amendment\.(Feature\w+)", helper_body))
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
        support_condition = None
        if helper == "registerRetired":
            supported = "yes"
        elif helper == "registerFeature" or helper == "registerFix":
            if "confidentialTransferSupport()" in tail:
                supported = "conditional"
                support_condition = "mptcrypto.Available()"
            else:
                supported = "no" if "SupportedNo" in tail else "yes"
        rows.append(
            {
                "name": name,
                "kind": {"registerFeature": "feature", "registerFix": "fix", "registerRetired": "retired"}[helper],
                "supported": supported,
                "support_condition": support_condition,
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
        test_defs = []
        for match in re.finditer(r"(?m)^func\s+(Test\w+)", text):
            opening = text.find("{", match.end() - 1)
            if opening < 0:
                continue
            try:
                _, end = balanced(text, opening, "{", "}")
            except ValueError:
                continue
            test_defs.append((match.start(), end, match.group(1)))
        test_starts = [start for start, _, _ in test_defs]
        for match in pattern.finditer(text):
            test_index = bisect_right(test_starts, match.start()) - 1
            if test_index >= 0 and match.start() < test_defs[test_index][1]:
                owner = test_defs[test_index][2]
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
        return "legacy out-of-scope list includes app/Batch; checked-in v4 StrictOracleRecorder covers Batch cases separately"
    if name == "DelegateSet":
        return "legacy out-of-scope list includes app/Delegate; this source inventory does not infer v4 execution"
    if name.startswith("Vault"):
        return "legacy out-of-scope list includes app/Vault; checked-in v4 StrictOracleRecorder covers VaultCreate cases separately"
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


def validate_engine_inventory(repo: Path, engine: dict[str, Any]) -> None:
    def validate_source(path: str, symbols: Iterable[str]) -> None:
        source = repo / path
        if not source.is_file():
            raise ValueError(f"engine inventory source does not exist: {path}")
        text = read(source)
        for symbol in symbols:
            identifier = symbol.rsplit(".", 1)[-1]
            if not re.search(rf"\b{re.escape(identifier)}\b", text):
                raise ValueError(f"engine inventory symbol {symbol} is absent from {path}")

    def visit(value: Any) -> None:
        if not isinstance(value, dict):
            return
        path = value.get("path")
        if path:
            validate_source(path, value.get("symbols", []))
        aggregator_path = value.get("aggregator_path")
        aggregator_symbol = value.get("aggregator_symbol")
        if aggregator_path and aggregator_symbol:
            validate_source(aggregator_path, [aggregator_symbol])
        checker_path = value.get("checker_path")
        checker_symbol = value.get("checker_symbol")
        if checker_path and checker_symbol:
            validate_source(checker_path, [checker_symbol])
        for child in value.values():
            visit(child)

    visit(engine)


def validate_common_field_mapping(
    repo: Path, oracle: Path, mapping: list[dict[str, Any]]
) -> None:
    seen: set[str] = set()

    def validate_source(root: Path, source: dict[str, Any]) -> None:
        path = source.get("path")
        if not isinstance(path, str) or not (root / path).is_file():
            raise ValueError(f"common-field mapping source does not exist: {path}")
        text = read(root / path)
        for symbol in source.get("symbols", []):
            identifier = symbol.rsplit(".", 1)[-1]
            if not re.search(rf"\b{re.escape(identifier)}\b", text):
                raise ValueError(f"common-field mapping symbol {symbol} is absent from {path}")

    for row in mapping:
        name = row.get("name")
        if not isinstance(name, str) or not name or name in seen:
            raise ValueError(f"duplicate or missing common-field mapping row: {name!r}")
        seen.add(name)
        if not row.get("stages") or not row.get("classification"):
            raise ValueError(f"common-field mapping row {name} has no stage classification")
        for source in row.get("go", []):
            validate_source(repo, source)
        for source in row.get("oracle", []):
            validate_source(oracle, source)

    oracle_names = {row["name"] for row in parse_oracle_common_fields(oracle)}
    if seen != oracle_names:
        raise ValueError("common-field stage mapping does not cover every oracle common field")


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
                "go_required_amendment_references": go_required,
                "oracle_fields": oracle_row["fields"],
                "go_fields": go_fields,
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
                    "legacy_conformance_excluded": excluded is not None,
                    "legacy_conformance_exclusion_reason": excluded,
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

    engine = engine_inventory(repo)
    validate_engine_inventory(repo, engine)
    common_stage_mapping = common_field_stage_mapping()
    validate_common_field_mapping(repo, oracle, common_stage_mapping)
    v4_corpus = parse_v4_corpus(repo)
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
        "go_base_commit": GO_INVENTORY_SOURCE_COMMIT,
        "go_base_commit_provenance": {
            "revision": GO_INVENTORY_SOURCE_COMMIT,
            "protocol_base": GO_BASE_COMMIT,
            "description": "Go source revision after the prerequisite #2021/#2022 merge; execution evidence remains separate.",
        },
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
        "engine": engine,
        "common_fields": {
            "oracle": common_oracle,
            "go": common_go,
            "stage_mapping": common_stage_mapping,
        },
        "legacy_go_transaction_enums": sorted(
            [{"name": row["name"], "code": row["code"], "go_const": row["go_const"]} for row in legacy_types],
            key=lambda row: row["code"],
        ),
        "amendments": amendments,
        "transactions": transactions,
        "evidence": {
            "execution_semantics": "Rows are source/registry coverage. A row is executed only when a checked-in or explicitly supplied fixture record says so; named tests alone never set executed=true.",
            "checked_in_v4_corpus": v4_corpus,
            "legacy_runner": {
                "runner_path": "internal/testing/conformance/corpus.go",
                "fixture_contract": "rippled 3.4.0 legacy external corpus",
                "out_of_scope_path": "scripts/conformance-out-of-scope.txt",
                "out_of_scope_suites": ["app/Batch", "app/Delegate", "app/Vault"],
                "status_is_execution_record": False,
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
