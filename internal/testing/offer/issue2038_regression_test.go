package offer

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// Applies testnet ledger 21212185's transaction AC818C04... against a minimal
// book that crosses the same 527280507 drops as the validated transaction.
func TestOffer_Issue2038CrossingMetadata(t *testing.T) {
	const (
		currency      = "424F4F4B00000000000000000000000000000000"
		issuerAddress = "rET2LC3hkiofP4xDQbRUsu9g3ooYmQnaph"
		makerAddress  = "rp6c2j1FpPJx11NzCR2ApEqiXBgDBbDnp1"
		offerID       = "04B391C5D5ED07BE7224D23267C10AAA889300184917E46BA00C3C56779BCE13"
		incidentBlob  = "12000722000100002300001B592401424CAC2A3252061A201B0143AC2B64415FC6B3F9F6737065D4838D7EA4C68000424F4F4B000000000000000000000000000000009E9867E98668B4C16815B2A30329805112D849D168400000000000000C7321ED0EC3B88BEF4EB7626B3B7CD6C6789330D5475B39E60B9222F148E3394E250ADD74401293990A5C5FB5ECDB743BB425E201FF849405768D0A3E0C31A1B7E546FF80EA0FF90BAAF2BE1341FC9EBED04F1A15F10C3CA5EE1133078B90A25730B3568908811412A67764272B1B1F27B8FB53A168B672CDE15C4A"
		wantGets      = "1.158744133714571"
		wantPays      = "99016192593283573"
		wantGetsWire  = "65D4841DDF2479C28B424F4F4B000000000000000000000000000000009E9867E98668B4C16815B2A30329805112D849D1"
	)
	issuer := jtx.NewAccountWithAddress("issue2038-issuer", issuerAddress)
	maker := jtx.NewAccountWithAddress("issue2038-maker", makerAddress)
	counter := jtx.NewAccount("issue2038-counter")
	env := newEnvWithFeatures(t, nil)
	env.SetNumberContextOverride(state.NewNumberContext(state.MantissaScaleSmall, true))
	for _, account := range []*jtx.Account{issuer, maker} {
		env.FundAmountNoRipple(account, uint64(jtx.XRP(10000)))
		setIssue2038Account(t, env, account, func(root *state.AccountRoot) {
			root.Flags |= state.LsfDefaultRipple
		})
	}
	env.FundAmount(counter, uint64(jtx.XRP(10000)))
	book := func(mantissa int64, exponent int) tx.Amount {
		return tx.NewIssuedAmount(mantissa, exponent, currency, issuer.Address)
	}
	for _, account := range []*jtx.Account{maker, counter} {
		jtx.RequireTxSuccess(t, env.SubmitWithOptions(
			trustset.TrustSet(account, book(2, 0)).Build(), jtx.SubmitOptions{SkipSignature: true}))
		jtx.RequireTxSuccess(t, env.SubmitWithOptions(
			payment.PayIssued(issuer, account, book(2, 0)).Build(), jtx.SubmitOptions{SkipSignature: true}))
	}
	counterSeq := env.Seq(counter)
	jtx.RequireTxSuccess(t, env.Submit(OfferCreate(counter, book(5, -9), tx.NewXRPAmount(527280507)).Build()))
	require.NotNil(t, GetOffer(env, counter, counterSeq))
	setIssue2038Account(t, env, issuer, func(root *state.AccountRoot) {
		root.TickSize = 6
	})
	env.Close()
	setIssue2038Account(t, env, maker, func(root *state.AccountRoot) {
		root.Sequence = 21122220
	})

	blob, err := hex.DecodeString(incidentBlob)
	require.NoError(t, err)
	incident, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	require.Equal(t, makerAddress, incident.GetCommon().Account)
	require.Equal(t, uint32(21122220), *incident.GetCommon().Sequence)
	require.Equal(t, uint32(65536), incident.GetCommon().GetFlags())
	result := env.SubmitWithOptions(incident, jtx.SubmitOptions{SkipSignature: true})
	jtx.RequireTxSuccess(t, result)
	require.NotNil(t, result.Metadata)
	require.Nil(t, GetOffer(env, counter, counterSeq), "counter-offer must be consumed")

	var created *tx.AffectedNode
	for i := range result.Metadata.AffectedNodes {
		node := &result.Metadata.AffectedNodes[i]
		if node.NodeType == "CreatedNode" && node.LedgerIndex == offerID {
			created = node
			break
		}
	}
	require.NotNil(t, created)
	require.Equal(t, "Offer", created.LedgerEntryType)
	require.Equal(t, wantPays, created.NewFields["TakerPays"])
	require.Equal(t, map[string]any{
		"currency": currency,
		"issuer":   issuerAddress,
		"value":    wantGets,
	}, created.NewFields["TakerGets"])

	metaBlob, err := tx.SerializeMetadata(result.Metadata)
	require.NoError(t, err)
	require.Contains(t, strings.ToUpper(hex.EncodeToString(metaBlob)), wantGetsWire)
	remaining := GetOffer(env, maker, 21122220)
	require.NotNil(t, remaining)
	require.Equal(t, wantPays, remaining.TakerPays.Value())
	require.Equal(t, wantGets, remaining.TakerGets.Value())
}

func setIssue2038Account(t *testing.T, env *jtx.TestEnv, account *jtx.Account, mutate func(*state.AccountRoot)) {
	t.Helper()
	key := keylet.Account(account.ID)
	data, err := env.Ledger().Read(key)
	require.NoError(t, err)
	root, err := state.ParseAccountRoot(data)
	require.NoError(t, err)
	mutate(root)
	updated, err := state.SerializeAccountRoot(root)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(key, updated))
}
