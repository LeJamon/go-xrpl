package sign

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/clawback"
)

type clawbackWithTopLevelMPTID struct {
	*clawback.Clawback
}

func (c *clawbackWithTopLevelMPTID) Flatten() (map[string]any, error) {
	values, err := c.Clawback.Flatten()
	if err != nil {
		return nil, err
	}
	values["MPTokenIssuanceID"] = "000000000000000000000001000000000000000000000001"
	return values, nil
}

func TestMPTokenClawbackSigningPayloadGolden(t *testing.T) {
	amount := state.NewMPTAmountWithIssuanceID(
		100,
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"000000000000000000000001000000000000000000000001",
	)
	transaction := clawback.NewMPTokenClawback(
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"r3kmLJN5D28dHuH8vZNUZpMC43pEHpaocV",
		amount,
	)
	seq := uint32(1)
	transaction.Sequence = &seq
	transaction.Fee = "10"

	payload, err := getSigningPayload(transaction)
	if err != nil {
		t.Fatalf("getSigningPayload: %v", err)
	}
	want := "5354580012001E24000000016160000000000000006400000000000000000000000100000000000000000000000168400000000000000A73008114B5F762798A53D543A014CAF8B297CFF8F2F937E88B14550FC62003E785DC231A1058A05E56E3F09CF4E6"
	if payload != want {
		t.Fatalf("signing payload = %s, want %s", payload, want)
	}

	flat, err := transaction.Flatten()
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	txcore.PopulateRequiredWireFields(flat, transaction.GetCommon())
	if err := txcore.ValidateTemplateFields(txcore.TypeClawback, flat); err != nil {
		t.Fatalf("ValidateTemplateFields: %v", err)
	}
}

func TestMPTokenClawbackSigningRejectsTopLevelIssuanceID(t *testing.T) {
	transaction := &clawbackWithTopLevelMPTID{Clawback: clawback.NewMPTokenClawback(
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"r3kmLJN5D28dHuH8vZNUZpMC43pEHpaocV",
		state.NewMPTAmountWithIssuanceID(
			100,
			"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"000000000000000000000001000000000000000000000001",
		),
	)}
	seq := uint32(1)
	transaction.Sequence = &seq
	transaction.Fee = "10"

	_, err := getSigningPayload(transaction)
	if err == nil || err.Error() != "Field 'MPTokenIssuanceID' found in disallowed location." {
		t.Fatalf("getSigningPayload error = %v", err)
	}
}
