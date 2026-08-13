package engine

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func confidentialPreflightRules(enabled bool) *amendment.Rules {
	builder := amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Enable(amendment.FeatureBatchV1_1).
		Enable(amendment.FeaturePermissionDelegationV1_1)
	if enabled {
		builder.Enable(amendment.FeatureConfidentialTransfer)
	}
	return builder.Build()
}

func malformedDelegatedConvert() *mpt.ConfidentialMPTConvert {
	transaction := &mpt.ConfidentialMPTConvert{
		BaseTx:                *txcore.NewBaseTx(txcore.TypeConfidentialMPTConvert, precedenceSourceAddr),
		MPTokenIssuanceID:     strings.Repeat("00", 24),
		HolderEncryptedAmount: strings.Repeat("01", 66),
		IssuerEncryptedAmount: strings.Repeat("02", 66),
		BlindingFactor:        strings.Repeat("03", 32),
	}
	transaction.Fee = "10"
	transaction.Sequence = u32(5)
	transaction.Delegate = precedenceGenesisAddr
	return transaction
}

func TestConfidentialMPTConvertPreflightPrecedence(t *testing.T) {
	if got := preflightEngine(confidentialPreflightRules(false)).preflight(malformedDelegatedConvert()); got != ter.TemDISABLED {
		t.Fatalf("disabled Convert preflight = %v, want temDISABLED", got)
	}
	if got := preflightEngine(confidentialPreflightRules(true)).preflight(malformedDelegatedConvert()); got != ter.TemINVALID {
		t.Fatalf("delegated Convert preflight = %v, want temINVALID", got)
	}

	inner := malformedDelegatedConvert()
	inner.Fee = "0"
	inner.SigningPubKey = ""
	inner.SetFlags(txcore.TfInnerBatchTxn)
	outer := batch.NewBatch(precedenceSourceAddr)
	outer.Fee = "130"
	outer.Sequence = u32(4)
	outer.SetFlags(batch.BatchFlagAllOrNothing)
	outer.AddInnerTransaction(inner)
	second := payment.NewPayment(precedenceSourceAddr, precedenceGenesisAddr, txcore.NewXRPAmount(1))
	second.Fee = "0"
	second.SigningPubKey = ""
	second.Sequence = u32(6)
	second.SetFlags(txcore.TfInnerBatchTxn)
	outer.AddInnerTransaction(second)
	outer.BatchSigners = []batch.BatchSigner{{BatchSigner: batch.BatchSignerData{
		Account: precedenceGenesisAddr, SigningPubKey: "ABC", BatchTxnSignature: "DEF",
	}}}
	if got := preflightEngine(confidentialPreflightRules(true)).preflight(outer); got != ter.TemINVALID_INNER_BATCH {
		t.Fatalf("Batch delegated Convert preflight = %v, want temINVALID_INNER_BATCH", got)
	}
}

func TestConfidentialMPTPreflightAmendmentGate(t *testing.T) {
	newBase := func(transactionType txcore.Type) txcore.BaseTx {
		base := *txcore.NewBaseTx(transactionType, precedenceSourceAddr)
		base.Fee = "100"
		base.Sequence = u32(5)
		return base
	}

	tests := []struct {
		name        string
		transaction txcore.Transaction
	}{
		{"Convert", &mpt.ConfidentialMPTConvert{BaseTx: newBase(txcore.TypeConfidentialMPTConvert)}},
		{"MergeInbox", &mpt.ConfidentialMPTMergeInbox{BaseTx: newBase(txcore.TypeConfidentialMPTMergeInbox)}},
		{"ConvertBack", &mpt.ConfidentialMPTConvertBack{BaseTx: newBase(txcore.TypeConfidentialMPTConvertBack)}},
		{"Send", &mpt.ConfidentialMPTSend{BaseTx: newBase(txcore.TypeConfidentialMPTSend)}},
		{"Clawback", &mpt.ConfidentialMPTClawback{BaseTx: newBase(txcore.TypeConfidentialMPTClawback)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := preflightEngine(confidentialPreflightRules(false)).preflight(test.transaction); got != ter.TemDISABLED {
				t.Fatalf("disabled preflight = %v, want temDISABLED", got)
			}
			if got := preflightEngine(confidentialPreflightRules(true)).preflight(test.transaction); got != ter.TemINVALID {
				t.Fatalf("enabled preflight = %v, want temINVALID", got)
			}
		})
	}
}
