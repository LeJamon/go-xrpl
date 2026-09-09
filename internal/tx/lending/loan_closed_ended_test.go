package lending

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func lendingLifecycleRules(enabled bool) *amendment.Rules {
	builder := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported)
	if enabled {
		builder.Enable(amendment.FeatureLendingProtocolV1_1)
	} else {
		builder.Disable(amendment.FeatureLendingProtocolV1_1)
	}
	return builder.Build()
}

func setClosedEndedLoanVault(t *testing.T, fixture *coverClawbackFixture, kind uint8, subscription, redemption *uint32) {
	t.Helper()
	vaultID := fixture.brokerID
	broker, err := readLoanBroker(fixture.view, keylet.LoanBrokerByID(vaultID))
	if err != nil || broker == nil {
		t.Fatalf("read broker: broker=%v err=%v", broker, err)
	}
	issuerAddress := encodeTestAccount(t, fixture.issuerID)
	brokerAddress := encodeTestAccount(t, fixture.brokerAcct)
	assetID := strings.ToUpper(hex.EncodeToString(fixture.mptID[:]))
	fields := map[string]any{
		"LedgerEntryType":   "Vault",
		"Flags":             0,
		"Sequence":          uint32(1),
		"OwnerNode":         "0",
		"Owner":             issuerAddress,
		"Account":           brokerAddress,
		"Asset":             map[string]any{"mpt_issuance_id": assetID},
		"ShareMPTID":        strings.Repeat("0", 48),
		"WithdrawalPolicy":  uint8(1),
		"AssetsTotal":       "30000",
		"AssetsAvailable":   "30000",
		"PreviousTxnID":     strings.Repeat("43", 32),
		"PreviousTxnLgrSeq": uint32(1),
	}
	if kind != vault.VaultKindOpenEnded {
		fields["VaultKind"] = kind
	}
	if subscription != nil {
		fields["SubscriptionDate"] = *subscription
	}
	if redemption != nil {
		fields["RedemptionDate"] = *redemption
	}
	blob, err := binarycodec.Encode(fields)
	if err != nil {
		t.Fatalf("encode vault: %v", err)
	}
	vaultBytes, err := hex.DecodeString(blob)
	if err != nil {
		t.Fatalf("decode vault: %v", err)
	}
	fixture.view.data[keylet.VaultByID(broker.VaultID).Key] = vaultBytes
	ownerAddress := encodeTestAccount(t, fixture.issuerID)
	accountBytes, err := state.SerializeAccountRoot(&state.AccountRoot{Account: ownerAddress, Balance: 1_000_000_000, Sequence: 1})
	if err != nil {
		t.Fatalf("serialize owner account: %v", err)
	}
	fixture.view.data[keylet.Account(fixture.issuerID).Key] = accountBytes
}

func newLifecycleLoanSet(t *testing.T, fixture *coverClawbackFixture) *LoanSet {
	t.Helper()
	brokerID := strings.ToUpper(hex.EncodeToString(fixture.brokerID[:]))
	loan := NewLoanSet(encodeTestAccount(t, fixture.issuerID), brokerID, "1")
	interval, total := uint32(60), uint32(1)
	loan.PaymentInterval = &interval
	loan.PaymentTotal = &total
	return loan
}

func TestLoanBrokerSetClosedEndedGatePreservesLegacyOpenEndedBehavior(t *testing.T) {
	fixture := newCoverClawbackFixture(t, entry.LsfMPTCanTransfer)
	broker, err := readLoanBroker(fixture.view, keylet.LoanBrokerByID(fixture.brokerID))
	if err != nil || broker == nil {
		t.Fatalf("read broker: broker=%v err=%v", broker, err)
	}
	brokerSet := NewLoanBrokerSet(encodeTestAccount(t, fixture.issuerID), strings.ToUpper(hex.EncodeToString(broker.VaultID[:])))

	on := lendingLifecycleRules(true)
	fixture.view.rules = on
	fixture.config.Rules = on
	if got := brokerSet.Preclaim(fixture.view, fixture.config); got != ter.TecNO_PERMISSION {
		t.Fatalf("LP1.1 open-ended broker creation = %v, want tecNO_PERMISSION", got)
	}

	off := lendingLifecycleRules(false)
	fixture.view.rules = off
	fixture.config.Rules = off
	if got := brokerSet.Preclaim(fixture.view, fixture.config); got == ter.TecNO_PERMISSION {
		t.Fatal("LP1.1-disabled open-ended broker creation retained the closed-ended gate")
	}
}

func TestLoanSetClosedEndedPhaseAndRedemptionBuffer(t *testing.T) {
	const subscription uint32 = 100
	redemption := subscription + 180
	fixture := newCoverClawbackFixture(t, entry.LsfMPTCanTransfer)
	on := lendingLifecycleRules(true)
	fixture.view.rules = on
	fixture.config.Rules = on
	setClosedEndedLoanVault(t, fixture, vault.VaultKindClosedEnded, func() *uint32 { v := subscription; return &v }(), &redemption)
	broker, brokerErr := readLoanBroker(fixture.view, keylet.LoanBrokerByID(fixture.brokerID))
	if brokerErr != nil || broker == nil {
		t.Fatalf("read broker after setup: %v", brokerErr)
	}
	loan := newLifecycleLoanSet(t, fixture)

	cases := []struct {
		name string
		now  uint32
		want ter.Result
	}{
		{name: "subscription boundary", now: subscription, want: ter.TecTOO_SOON},
		{name: "redemption boundary", now: redemption, want: ter.TecEXPIRED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture.config.ParentCloseTime = tc.now
			if got := loan.Preclaim(fixture.view, fixture.config); got != tc.want {
				t.Fatalf("LoanSet.Preclaim() = %v, want %v", got, tc.want)
			}
		})
	}

	// At now=subscription+60 the final payment is exactly 60 seconds before
	// redemption, so the strict greater-than check accepts it.
	fixture.config.ParentCloseTime = subscription + 60
	if got := loan.Preclaim(fixture.view, fixture.config); got == ter.TecNO_PERMISSION {
		t.Fatalf("exact 60-second final-payment buffer was rejected: %v", got)
	}

	fixture.config.ParentCloseTime = subscription + 61
	if got := loan.Preclaim(fixture.view, fixture.config); got != ter.TecNO_PERMISSION {
		t.Fatalf("one-second-short final-payment buffer = %v, want tecNO_PERMISSION", got)
	}
}

func TestLoanSetOpenEndedLegacyBehaviorWhenLP11Disabled(t *testing.T) {
	fixture := newCoverClawbackFixture(t, entry.LsfMPTCanTransfer)
	off := lendingLifecycleRules(false)
	fixture.view.rules = off
	fixture.config.Rules = off
	setClosedEndedLoanVault(t, fixture, vault.VaultKindOpenEnded, nil, nil)
	fixture.config.ParentCloseTime = 101
	loan := newLifecycleLoanSet(t, fixture)
	if got := loan.Preclaim(fixture.view, fixture.config); got == ter.TecTOO_SOON || got == ter.TecEXPIRED || got == ter.TecNO_PERMISSION {
		t.Fatalf("LP1.1-disabled open-ended LoanSet used lifecycle gate: %v", got)
	}
}
