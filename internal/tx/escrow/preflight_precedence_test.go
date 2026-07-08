package escrow

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

// preflightCode extracts the TER code carried by a preflight/seam error.
func preflightCode(t *testing.T, err error) ter.Result {
	t.Helper()
	require.Error(t, err)
	var re *ter.ResultError
	require.ErrorAs(t, err, &re)
	return re.Code
}

// rulesDisabling returns the all-supported rule set with one amendment removed.
func rulesDisabling(name string) *amendment.Rules {
	return amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).DisableByName(name).Build()
}

// TestEscrowCreate_PreflightPrecedence pins the EscrowCreate mask-position finding
// and the non-XRP amount-vs-currency order that moving the preflight body into
// PreflightRules must preserve.
func TestEscrowCreate_PreflightPrecedence(t *testing.T) {
	on := amendment.AllSupportedRules()
	no1543 := rulesDisabling("fix1543")

	// fix1543 gates the flag mask: on → reject stray flags; off → allow any flags.
	require.Equal(t, tx.TfUniversalMask, (&EscrowCreate{}).GetFlagsMask(on))
	require.Equal(t, uint32(0), (&EscrowCreate{}).GetFlagsMask(no1543))

	// A zero non-XRP amount whose currency is also the reserved "XRP" code
	// surfaces temBAD_AMOUNT — rippled's Issue helper checks the amount before the
	// currency, so temBAD_AMOUNT wins over temBAD_CURRENCY.
	zeroBadCurrency := &EscrowCreate{
		BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
		Amount:      tx.NewIssuedAmountFromFloat64(0.0, "XRP", "rGw"),
		Destination: "rBob",
		FinishAfter: ptrUint32(700000000),
	}
	require.Equal(t, ter.TemBAD_AMOUNT, preflightCode(t, zeroBadCurrency.PreflightRules(on)))

	// A well-formed positive amount with the reserved currency surfaces
	// temBAD_CURRENCY (the amount check passes, the currency check fires).
	positiveBadCurrency := &EscrowCreate{
		BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
		Amount:      tx.NewIssuedAmountFromFloat64(100.0, "XRP", "rGw"),
		Destination: "rBob",
		FinishAfter: ptrUint32(700000000),
	}
	require.Equal(t, ter.TemBAD_CURRENCY, preflightCode(t, positiveBadCurrency.PreflightRules(on)))
}

// TestEscrowFinish_PreflightPrecedence pins the two EscrowFinish CredentialIDs
// findings and the mask-position finding.
func TestEscrowFinish_PreflightPrecedence(t *testing.T) {
	on := amendment.AllSupportedRules()
	noCreds := rulesDisabling("Credentials")

	require.Equal(t, tx.TfUniversalMask, (&EscrowFinish{}).GetFlagsMask(on))
	require.Equal(t, uint32(0), (&EscrowFinish{}).GetFlagsMask(rulesDisabling("fix1543")))

	// A CredentialIDs-bearing EscrowFinish that is ALSO shape-malformed (duplicate
	// IDs) and carries a Condition without a Fulfillment.
	dup := &EscrowFinish{
		BaseTx:        *tx.NewBaseTx(tx.TypeEscrowFinish, "rBob"),
		Owner:         "rAlice",
		OfferSequence: 1,
		Condition:     ptrString("A0258020E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855810100"),
		CredentialIDs: []string{"AB", "AB"},
	}

	// Finding #1: with Credentials disabled the field-presence gate (CheckExtraFeatures)
	// returns temDISABLED — ahead of the shape check and the Condition/Fulfillment XOR.
	require.Equal(t, ter.TemDISABLED, preflightCode(t, dup.CheckExtraFeatures(noCreds)))
	require.NoError(t, dup.CheckExtraFeatures(on))

	// Finding #2: the CredentialIDs shape check no longer lives in Validate — it
	// runs in PreflightSigValidated (after the signature). A duplicate therefore
	// does NOT fail Validate; only PreflightSigValidated reports temMALFORMED, so a
	// bad-signature temINVALID can win over it.
	valid := &EscrowFinish{
		BaseTx:        *tx.NewBaseTx(tx.TypeEscrowFinish, "rBob"),
		Owner:         "rAlice",
		OfferSequence: 1,
		CredentialIDs: []string{"AB", "AB"},
	}
	require.NoError(t, valid.Validate())
	require.Equal(t, ter.TemMALFORMED, preflightCode(t, valid.PreflightSigValidated()))
}

// TestEscrowCancel_PreflightPrecedence pins the EscrowCancel mask-position finding.
func TestEscrowCancel_PreflightPrecedence(t *testing.T) {
	require.Equal(t, tx.TfUniversalMask, (&EscrowCancel{}).GetFlagsMask(amendment.AllSupportedRules()))
	require.Equal(t, uint32(0), (&EscrowCancel{}).GetFlagsMask(rulesDisabling("fix1543")))
}
