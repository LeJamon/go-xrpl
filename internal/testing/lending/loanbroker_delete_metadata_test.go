package lending_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

// The metadata golden covers the complete serialized blob after applying
// RippleStateHelpers::removeEmptyHolding's v3.2.0 update-before-delete order.
// The state root was captured from this fixture before the metadata-only fix.
const (
	loanBrokerDeleteIOUMetadataSHA256 = "52AEA2C45DFCB21A917B6A8EE7F75B803DC34F98961AFFE1DABE7343CC789F7D"
	loanBrokerDeleteIOUStateRoot      = "B042E3865CD1895BBCD90C63D30B258D28AE13C0DCC2B8D7119B605325A3B802"
)

func TestLoanBrokerDeleteIOUMetadataTransitions(t *testing.T) {
	env := newLendingEnv(t)
	issuer := jtx.NewAccount("issuer")
	owner := jtx.NewAccount("owner")
	env.Fund(issuer, owner)

	vaultSeq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "USD", Issuer: issuer.Address})
	create.Common.Fee = reserveIncrement
	jtx.RequireTxSuccess(t, env.Submit(create))

	brokerSeq := env.Seq(owner)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID(owner, vaultSeq))))
	brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSeq)
	pseudoID := loanBrokerPseudoID(t, env, brokerKey)

	lineKey := keylet.Line(pseudoID, issuer.ID, "USD")
	lineData, err := env.LedgerEntry(lineKey)
	if err != nil {
		t.Fatalf("read broker trust line: %v", err)
	}
	line, err := state.ParseRippleState(lineData)
	if err != nil {
		t.Fatalf("parse broker trust line: %v", err)
	}
	if keylet.IsLowAccount(pseudoID, issuer.ID) {
		t.Fatal("fixture must cover the high-account reserve transition")
	}
	reserveFlag := state.LsfHighReserve
	if line.Flags&reserveFlag == 0 {
		t.Fatalf("broker trust line flags = %#x, missing reserve flag %#x", line.Flags, reserveFlag)
	}

	result := env.Submit(lending.NewLoanBrokerDelete(owner.Address, strings.ToUpper(hex.EncodeToString(brokerKey.Key[:]))))
	jtx.RequireTxSuccess(t, result)
	assertDeletedTransition(t, result.Metadata, keylet.Account(pseudoID), "AccountRoot", "OwnerCount", uint32(0), uint32(1))
	assertDeletedTransition(t, result.Metadata, lineKey, "RippleState", "Flags", line.Flags&^reserveFlag, line.Flags)

	blob, err := tx.SerializeMetadata(result.Metadata)
	if err != nil {
		t.Fatalf("serialize metadata: %v", err)
	}
	root, err := env.Ledger().StateMapHash()
	if err != nil {
		t.Fatalf("state map hash: %v", err)
	}
	metadataHash := sha256.Sum256(blob)
	if got := strings.ToUpper(hex.EncodeToString(metadataHash[:])); got != loanBrokerDeleteIOUMetadataSHA256 {
		t.Errorf("full metadata blob SHA-256 = %s, want %s", got, loanBrokerDeleteIOUMetadataSHA256)
	}
	if got := strings.ToUpper(hex.EncodeToString(root[:])); got != loanBrokerDeleteIOUStateRoot {
		t.Errorf("state root = %s, want %s", got, loanBrokerDeleteIOUStateRoot)
	}
}

func TestLoanBrokerDeleteMPTMetadataOwnerCount(t *testing.T) {
	env := newLendingEnv(t)
	issuer := jtx.NewAccount("issuer")
	owner := jtx.NewAccount("owner")
	token := mpttest.NewMPTTester(t, env, issuer, mpttest.MPTInit{Holders: []*jtx.Account{owner}})
	token.Create(mpttest.CreateOpts{Flags: mpttest.TfMPTCanTransfer})

	vaultSeq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{MPTIssuanceID: token.IssuanceID()})
	create.Common.Fee = reserveIncrement
	jtx.RequireTxSuccess(t, env.Submit(create))

	brokerSeq := env.Seq(owner)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID(owner, vaultSeq))))
	brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSeq)
	pseudoID := loanBrokerPseudoID(t, env, brokerKey)

	result := env.Submit(lending.NewLoanBrokerDelete(owner.Address, strings.ToUpper(hex.EncodeToString(brokerKey.Key[:]))))
	jtx.RequireTxSuccess(t, result)
	assertDeletedTransition(t, result.Metadata, keylet.Account(pseudoID), "AccountRoot", "OwnerCount", uint32(0), uint32(1))
}

func loanBrokerPseudoID(t *testing.T, env *jtx.TestEnv, brokerKey keylet.Keylet) [20]byte {
	t.Helper()
	brokerData, err := env.LedgerEntry(brokerKey)
	if err != nil {
		t.Fatalf("read LoanBroker: %v", err)
	}
	brokerFields, err := binarycodec.DecodeBytes(brokerData)
	if err != nil {
		t.Fatalf("decode LoanBroker: %v", err)
	}
	pseudoAddress, ok := brokerFields["Account"].(string)
	if !ok {
		t.Fatalf("LoanBroker Account = %#v, want address", brokerFields["Account"])
	}
	pseudoID, err := state.DecodeAccountID(pseudoAddress)
	if err != nil {
		t.Fatalf("decode broker pseudo-account: %v", err)
	}
	return pseudoID
}

func assertDeletedTransition(
	t *testing.T,
	meta *tx.Metadata,
	key keylet.Keylet,
	entryType, field string,
	wantFinal, wantPrevious uint32,
) {
	t.Helper()
	wantIndex := strings.ToUpper(hex.EncodeToString(key.Key[:]))
	for _, node := range meta.AffectedNodes {
		if node.NodeType != "DeletedNode" || node.LedgerEntryType != entryType || node.LedgerIndex != wantIndex {
			continue
		}
		if got, ok := node.FinalFields[field].(uint32); !ok || got != wantFinal {
			t.Errorf("%s FinalFields.%s = %#v, want %d", entryType, field, node.FinalFields[field], wantFinal)
		}
		if got, ok := node.PreviousFields[field].(uint32); !ok || got != wantPrevious {
			t.Errorf("%s PreviousFields.%s = %#v, want %d", entryType, field, node.PreviousFields[field], wantPrevious)
		}
		return
	}
	t.Errorf("deleted %s %s not found in metadata", entryType, wantIndex)
}
