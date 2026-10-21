package relationaldb

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	binarytypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
)

func TestMatchesAccountTxDelegateAllowsLegacyOverCapTransaction(t *testing.T) {
	const overCap = uint64(binarytypes.MaxDrops) + 1
	ownerAddress := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	delegateAddress := "r4bbzCamAis69rNoRdSaMSmPb1kDUHXcAL"
	transaction := map[string]any{
		"Account":         ownerAddress,
		"Amount":          binarytypes.RawXRPAmount{Drops: overCap},
		"Delegate":        delegateAddress,
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"TransactionType": "AccountSet",
	}
	raw, err := binarycodec.EncodeBytes(transaction)
	if err != nil {
		t.Fatal(err)
	}
	_, ownerBytes, err := addresscodec.DecodeClassicAddressToAccountID(ownerAddress)
	if err != nil {
		t.Fatal(err)
	}
	_, delegateBytes, err := addresscodec.DecodeClassicAddressToAccountID(delegateAddress)
	if err != nil {
		t.Fatal(err)
	}
	var owner, delegate AccountID
	copy(owner[:], ownerBytes)
	copy(delegate[:], delegateBytes)

	matched, err := matchesAccountTxDelegate(raw, owner, AccountTxDelegateFilter{
		Role:         AccountTxDelegateActor,
		Counterparty: &delegate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("legacy over-cap delegated transaction did not match actor filter")
	}
}
