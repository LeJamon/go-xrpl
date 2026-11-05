# OfferCreate cancellation incident

`cancel_testnet21981990.json` records Testnet ledger 21981990 from validated
responses at `https://s.altnet.rippletest.net:51234/`, captured on 2026-11-05
for issue #2137.

- Parent: `5B81F1E72EA8F53DBD67953C251D4E21E0D3B875844833BF933F770FD04FB880`
- Target: `F68F1FAFCAE673252B130B3722C5940A5A47F0F3EB93F521F74DA3A67FC8A9A1`
- Target transaction root: `3F838CCF1AC00C0183C0BBA72FCBC03DBD7E311B48D5F23CEC041814621EF754`

The fixture contains 48 outer transactions and eight Batch inner records, sorted
by canonical metadata index. Transaction IDs were computed from the binary blobs
with the transaction hash prefix and SHA-512Half, then matched to the expanded
ledger response. Binary metadata is preserved without re-encoding.

The 226 parent entries were fetched with `ledger_entry`, pinned to the parent
hash. They include affected entries, unchanged account/book dependencies, and
additional entries encountered by replay, including the unfixed cancellation
path. FeeSettings and Amendments come from that same parent.

The regression executes the outer transactions through the block processor,
which also commits Batch inner transactions. It requires byte-identical blobs
and metadata for all 56 records, the canonical transaction root, and total XRP.
On the unfixed implementation, the first mismatch is transaction 32:
`BE3A0BB741C9C2AE30CC1674C050FF45B43F9A05F9D0BB7045CCC20530915AE5`.
Its sender retains 200,000 extra drops and creates a remainder offer. The fixed
metadata has nine affected nodes, sender balance 26,800,000 drops, and OwnerCount
129; the unfixed result has eleven affected nodes, balance 27,000,000, and
OwnerCount 130.

This is a sparse replay fixture. It does not contain the full parent state or
SHAMap proofs and cannot verify the parent/target account roots or complete
ledger hashes. It also does not establish recovery of a deployed validator.
