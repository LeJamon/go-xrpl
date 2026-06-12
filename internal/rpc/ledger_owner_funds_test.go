package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ownerFundsView serves a single AccountRoot SLE so tx.AccountFunds can compute
// XRP liquidity; every other read is empty.
type ownerFundsView struct {
	accountKey  keylet.Keylet
	accountData []byte
}

func (v *ownerFundsView) Read(k keylet.Keylet) ([]byte, error) {
	if k == v.accountKey {
		return v.accountData, nil
	}
	return nil, nil
}
func (v *ownerFundsView) Exists(keylet.Keylet) (bool, error)                 { return false, nil }
func (v *ownerFundsView) Insert(keylet.Keylet, []byte) error                 { return nil }
func (v *ownerFundsView) Update(keylet.Keylet, []byte) error                 { return nil }
func (v *ownerFundsView) Erase(keylet.Keylet) error                          { return nil }
func (v *ownerFundsView) ForEach(func(key [32]byte, data []byte) bool) error { return nil }
func (v *ownerFundsView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v *ownerFundsView) AdjustDropsDestroyed(drops.XRPAmount) {}
func (v *ownerFundsView) TxExists([32]byte) bool               { return false }
func (v *ownerFundsView) Rules() *amendment.Rules              { return nil }
func (v *ownerFundsView) LedgerSeq() uint32                    { return 0 }

// ownerFundsLedgerMock adds LedgerViewSource + a JSON-stored OfferCreate to the
// ledger mock so the ledger method can annotate owner_funds.
type ownerFundsLedgerMock struct {
	*ledgerMock
	view   types.LedgerStateView
	reader types.LedgerReader
}

func (m *ownerFundsLedgerMock) GetLedgerViewBySeq(seq uint32) (types.LedgerStateView, types.LedgerReader, error) {
	return m.view, m.reader, nil
}
func (m *ownerFundsLedgerMock) GetLedgerViewByHash(hash [32]byte) (types.LedgerStateView, types.LedgerReader, error) {
	return m.view, m.reader, nil
}

// TestLedgerOwnerFunds annotates an expanded OfferCreate selling XRP with the
// owner's spendable XRP, mirroring rippled fillJsonTx owner_funds
// (LedgerToJson.cpp:206-224). reserveBase=10000000, OwnerCount=0 ⇒
// owner_funds = balance - reserveBase = 990000000.
func TestLedgerOwnerFunds(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// Build an AccountRoot SLE (balance 1,000,000,000 drops, OwnerCount 0).
	accountRoot := map[string]any{
		"LedgerEntryType": "AccountRoot",
		"Account":         account,
		"Balance":         "1000000000",
		"Flags":           0,
		"OwnerCount":      0,
		"Sequence":        1,
	}
	accountHex, err := binarycodec.Encode(accountRoot)
	require.NoError(t, err)
	accountBytes, err := hex.DecodeString(accountHex)
	require.NoError(t, err)

	_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	require.NoError(t, err)
	var accountID [20]byte
	copy(accountID[:], idBytes)

	view := &ownerFundsView{
		accountKey:  keylet.Account(accountID),
		accountData: accountBytes,
	}

	// A JSON-stored OfferCreate selling XRP (TakerGets is XRP drops).
	offerCreate := map[string]any{
		"TransactionType": "OfferCreate",
		"Account":         account,
		"TakerGets":       "100000000",
		"TakerPays": map[string]any{
			"currency": "USD",
			"issuer":   "rrrrrrrrrrrrrrrrrrrrBZbvji",
			"value":    "100",
		},
		"Sequence": 1,
		"Fee":      "10",
	}
	stored := map[string]any{"tx_json": offerCreate}
	storedJSON, err := json.Marshal(stored)
	require.NoError(t, err)

	base := &ledgerMock{mockLedgerService: newMockLedgerService()}
	reader := newDefaultLedgerReader(2, true)
	reader.transactions = append(reader.transactions, struct {
		hash [32]byte
		data []byte
	}{hash: [32]byte{0x01}, data: storedJSON})
	base.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 2 {
			return reader, nil
		}
		return nil, errors.New("not found")
	}

	mock := &ownerFundsLedgerMock{ledgerMock: base, view: view, reader: reader}
	services := &types.ServiceContainer{Ledger: mock}

	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1, Services: services}
	method := &handlers.LedgerMethod{}
	paramsJSON, _ := json.Marshal(map[string]any{
		"ledger_index": 2,
		"transactions": true,
		"expand":       true,
		"owner_funds":  true,
	})

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)

	resp := resultToMap(t, result)
	ledgerObj := resp["ledger"].(map[string]any)
	txns := ledgerObj["transactions"].([]any)
	require.Len(t, txns, 1)
	entry := txns[0].(map[string]any)
	assert.Equal(t, "990000000", entry["owner_funds"])
}
