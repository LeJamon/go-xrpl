package escrow

import (
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestBuildersPreserveFeeRange(t *testing.T) {
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	want := "18446744073709551615"

	require.Equal(t, want, EscrowCreate(alice, bob, 1).Fee(math.MaxUint64).Build().Fee)
	require.Equal(t, want, EscrowFinish(bob, alice, 1).Fee(math.MaxUint64).Build().Fee)
	require.Equal(t, want, EscrowCancel(bob, alice, 1).Fee(math.MaxUint64).Build().Fee)
}

func TestBuildersPreserveRawHexIntent(t *testing.T) {
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	create := EscrowCreate(alice, bob, 1).ConditionHex("not-hex").Build()
	require.NotNil(t, create.Condition)
	require.Equal(t, "not-hex", *create.Condition)

	empty := EscrowCreate(alice, bob, 1).Condition([]byte{}).Build()
	require.NotNil(t, empty.Condition)
	require.Empty(t, *empty.Condition)

	finish := EscrowFinish(bob, alice, 1).
		ConditionHex("condition-not-hex").
		FulfillmentHex("fulfillment-not-hex").
		Build()
	require.NotNil(t, finish.Condition)
	require.Equal(t, "condition-not-hex", *finish.Condition)
	require.NotNil(t, finish.Fulfillment)
	require.Equal(t, "fulfillment-not-hex", *finish.Fulfillment)
}

func TestEscrowFinishBuilderPreservesEmptyCredentialIDs(t *testing.T) {
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	finish := EscrowFinish(bob, alice, 1).CredentialIDs([]string{}).Sequence(1).Build()
	require.NotNil(t, finish.CredentialIDs)
	require.Empty(t, finish.CredentialIDs)
	require.True(t, finish.HasField("CredentialIDs"))

	flat, err := finish.Flatten()
	require.NoError(t, err)
	credentialIDs, ok := flat["CredentialIDs"]
	require.True(t, ok)
	require.Equal(t, []string{}, credentialIDs)

	blob, err := tx.SerializeTransaction(finish)
	require.NoError(t, err)
	decoded, err := binarycodec.DecodeBytes(blob)
	require.NoError(t, err)
	credentialIDs, ok = decoded["CredentialIDs"]
	require.True(t, ok)
	require.Equal(t, []string{}, credentialIDs)
}

func TestEscrowCreateBuilderUsesSingleAmountState(t *testing.T) {
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	iou := tx.NewIssuedAmount(100, 0, "USD", alice.Address)
	mpt := state.NewMPTAmountWithIssuanceID(50, alice.Address, "00000001AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

	require.True(t, EscrowCreate(alice, bob, 1).IOUAmount(iou).MPTAmount(mpt).Build().Amount.IsMPT())
	finalAmount := EscrowCreate(alice, bob, 1).MPTAmount(mpt).IOUAmount(iou).Build().Amount
	require.False(t, finalAmount.IsNative())
	require.False(t, finalAmount.IsMPT())
}
