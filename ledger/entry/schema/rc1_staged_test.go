package schema

import (
	"sort"
	"testing"

	"github.com/LeJamon/go-xrpl/protocol"
)

// rc1StagedLedgerFields are the rippled 3.5.0-rc1 ledger-entry template fields
// staged on top of the pinned 3.4.1 oracle.
var rc1StagedLedgerFields = map[string]map[string]Style{
	"FeeSettings":     {"GasLimit": StyleOptional, "BytecodeSizeLimit": StyleOptional, "GasPrice": StyleOptional},
	"Escrow":          {"Bytecode": StyleOptional, "Data": StyleOptional},
	"MPTokenIssuance": {"IssuerKeyEpoch": StyleOptional, "AuditorKeyEpoch": StyleOptional, "InitialIssuerEncryptionKey": StyleOptional},
	"MPToken": {
		"IssuerKeyMirrorEpoch":      StyleOptional,
		"AuditorKeyMirrorEpoch":     StyleOptional,
		"IssuerMirrorEncryptionKey": StyleOptional,
		"RecoveryKey":               StyleOptional,
	},
	"Vault":      {"YieldUnrealized": StyleDefault, "AssetsDeployed": StyleDefault},
	"LoanBroker": {"DomainID": StyleOptional},
	"TransactionProposal": {
		"PreviousTxnID":       StyleRequired,
		"PreviousTxnLgrSeq":   StyleRequired,
		"Owner":               StyleRequired,
		"ProposedTransaction": StyleRequired,
		"Expiration":          StyleRequired,
		"OwnerNode":           StyleRequired,
	},
}

// rc1StagedLedgerEntries are the ledger-entry types rippled 3.5.0-rc1 adds.
var rc1StagedLedgerEntries = map[string]rippledEntry{
	"TransactionProposal": {Type: protocol.LedgerEntryTypeTransactionProposal, RPCName: "transaction_proposal"},
}

func addRC1StagedStyles(t *testing.T, oracle map[string]map[string]Style) {
	t.Helper()
	for entryName, fields := range rc1StagedLedgerFields {
		if _, known := oracle[entryName]; !known {
			if _, added := rc1StagedLedgerEntries[entryName]; !added {
				t.Fatalf("staged rc1 fields target unknown entry %s", entryName)
			}
			oracle[entryName] = make(map[string]Style)
		}
		for fieldName, style := range fields {
			if _, found := oracle[entryName][fieldName]; found {
				t.Fatalf("staged rc1 field %s.%s already exists in the pinned oracle", entryName, fieldName)
			}
			oracle[entryName][fieldName] = style
		}
	}
}

func addRC1StagedEntries(t *testing.T, oracle map[string]rippledEntry) {
	t.Helper()
	for entryName, entry := range rc1StagedLedgerEntries {
		if _, found := oracle[entryName]; found {
			t.Fatalf("staged rc1 entry %s already exists in the pinned oracle", entryName)
		}
		oracle[entryName] = entry
	}
	for entryName, fields := range rc1StagedLedgerFields {
		entry := oracle[entryName]
		names := make([]string, 0, len(fields))
		for fieldName := range fields {
			names = append(names, fieldName)
		}
		sort.Strings(names)
		entry.Fields = append(entry.Fields, names...)
		oracle[entryName] = entry
	}
}
