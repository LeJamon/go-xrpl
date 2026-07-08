package nft_test

// precedence_test.go - end-to-end pins for the NFToken preflight TER-precedence
// alignment. Each test drives the full engine pipeline and asserts the exact
// code a malformed transaction surfaces, proving the tem* verdict wins over the
// later tec/tef it previously forked to.

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
)

// fakeOfferID / fakeOfferID2 are well-formed but never-created NFTokenOffer IDs.
const (
	fakeOfferID  = "00000000000000000000000000000000000000000000000000000000DEADBEEF"
	fakeOfferID2 = "00000000000000000000000000000000000000000000000000000000FEEDFACE"
)

// TestPrecedence_CancelOfferUniversalFlag pins finding 1: a NFTokenCancelOffer
// carrying tfFullyCanonicalSig (0x80000000, set by default by many client
// libraries) must pass the flag mask. Previously the mask was 0xFFFFFFFF, so the
// transaction was rejected temINVALID_FLAG and never reached the ledger — a
// direct ledger-content fork versus rippled, whose mask is ~tfUniversal.
func TestPrecedence_CancelOfferUniversalFlag(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	// Cancelling a non-existent offer is a no-op success in rippled.
	cancel := nft.NFTokenCancelOffer(alice, fakeOfferID).BuildNFTokenCancelOffer()
	cancel.SetFlags(tx.TfFullyCanonicalSig)

	result := env.Submit(cancel)
	jtx.RequireTxSuccess(t, result)
}

// TestPrecedence_MintNegativeAmountBeforeIssuer pins finding 3: a NFTokenMint
// with a negative Amount and a non-existent Issuer must fail temBAD_AMOUNT in
// preflight, before the preclaim Issuer read that previously returned
// tecNO_ISSUER (a tec that IS written to the ledger with the fee burned). tem vs
// tec = ledger fork.
func TestPrecedence_MintNegativeAmountBeforeIssuer(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	gw := jtx.NewAccount("gw")
	nonExistent := jtx.NewAccount("ghost")
	env.Fund(alice, gw)
	env.Close()

	mint := nft.NFTokenMint(alice, 0).
		Issuer(nonExistent).
		Amount(gw.IOU("USD", -5)).
		Build()

	result := env.Submit(mint)
	jtx.RequireTxFail(t, result, "temBAD_AMOUNT")
}

// TestPrecedence_CreateOfferNegativeAmountBeforeFindToken pins finding 6: a
// NFTokenCreateOffer with a negative Amount must fail temBAD_AMOUNT in preflight,
// before the Apply-time findToken that previously returned tecNO_ENTRY.
func TestPrecedence_CreateOfferNegativeAmountBeforeFindToken(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	gw := jtx.NewAccount("gw")
	env.Fund(alice, gw)
	env.Close()

	// A valid-looking NFTokenID that was never minted.
	tokenID := nft.GetNextNFTokenID(env, alice, 0, 0, 0)
	offer := nft.NFTokenCreateSellOffer(alice, tokenID, gw.IOU("USD", -5)).Build()

	result := env.Submit(offer)
	jtx.RequireTxFail(t, result, "temBAD_AMOUNT")
}

// TestPrecedence_AcceptOfferNegativeBrokerFee pins finding 2: a brokered
// NFTokenAcceptOffer with a negative NFTokenBrokerFee must fail temMALFORMED in
// preflight. Previously only a zero fee was rejected, so a negative fee reached
// the broker-payment logic and mutated state (tes/tec) — a cross-class fork.
func TestPrecedence_AcceptOfferNegativeBrokerFee(t *testing.T) {
	env := jtx.NewTestEnv(t)
	broker := jtx.NewAccount("broker")
	gw := jtx.NewAccount("gw")
	env.Fund(broker, gw)
	env.Close()

	accept := nftoken.NewNFTokenAcceptOffer(broker.Address)
	accept.Fee = "10"
	accept.NFTokenBuyOffer = fakeOfferID
	accept.NFTokenSellOffer = fakeOfferID2
	brokerFee := gw.IOU("USD", -10)
	accept.NFTokenBrokerFee = &brokerFee

	result := env.Submit(accept)
	jtx.RequireTxFail(t, result, "temMALFORMED")
}
