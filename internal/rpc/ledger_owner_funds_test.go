package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ownerFundsView serves a single AccountRoot SLE so tx.AccountFunds can compute
// XRP liquidity; every other read is empty.
type ownerFundsView struct {
	entries    map[keylet.Keylet][]byte
	readErrors map[keylet.Keylet]error
}

func (v *ownerFundsView) Read(k keylet.Keylet) ([]byte, error) {
	if err := v.readErrors[k]; err != nil {
		return nil, err
	}
	return v.entries[k], nil
}
func (v *ownerFundsView) Exists(keylet.Keylet) (bool, error)                 { return false, nil }
func (v *ownerFundsView) Insert(keylet.Keylet, []byte) error                 { return nil }
func (v *ownerFundsView) Update(keylet.Keylet, []byte) error                 { return nil }
func (v *ownerFundsView) Erase(keylet.Keylet) error                          { return nil }
func (v *ownerFundsView) ForEach(func(key [32]byte, data []byte) bool) error { return nil }
func (v *ownerFundsView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v *ownerFundsView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }
func (v *ownerFundsView) TxExists([32]byte) (bool, error)            { return false, nil }
func (v *ownerFundsView) Rules() *amendment.Rules                    { return nil }
func (v *ownerFundsView) LedgerSeq() uint32                          { return 0 }

// ownerFundsLedgerMock adds LedgerViewSource + a JSON-stored OfferCreate to the
// ledger mock so the ledger method can annotate owner_funds.
type ownerFundsLedgerMock struct {
	*ledgerMock
	view     types.LedgerStateView
	hashView types.LedgerStateView
	reader   types.LedgerReader
}

func (m *ownerFundsLedgerMock) GetLedgerViewBySeq(seq uint32) (types.LedgerStateView, types.LedgerReader, error) {
	return m.view, m.reader, nil
}
func (m *ownerFundsLedgerMock) GetLedgerViewByHash(hash [32]byte) (types.LedgerStateView, types.LedgerReader, error) {
	if m.hashView != nil {
		return m.hashView, m.reader, nil
	}
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
		"LedgerEntryType":   "AccountRoot",
		"Account":           account,
		"Balance":           "1000000000",
		"Flags":             0,
		"OwnerCount":        0,
		"Sequence":          1,
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
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
		entries: map[keylet.Keylet][]byte{
			keylet.Account(accountID): accountBytes,
		},
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
		"Sequence":      1,
		"Fee":           "10",
		"SigningPubKey": "",
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

	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1, Services: types.NewTestServiceGraph(services)}
	method := &handlers.LedgerMethod{}
	for _, tc := range []struct {
		apiVersion int
		binary     bool
	}{
		{apiVersion: types.ApiVersion1},
		{apiVersion: types.ApiVersion1, binary: true},
		{apiVersion: types.ApiVersion2},
		{apiVersion: types.ApiVersion2, binary: true},
	} {
		t.Run(fmt.Sprintf("v%d binary=%t", tc.apiVersion, tc.binary), func(t *testing.T) {
			ctx.ApiVersion = tc.apiVersion
			paramsJSON, err := json.Marshal(map[string]any{
				"ledger_index": 2,
				"transactions": true,
				"expand":       true,
				"owner_funds":  true,
				"binary":       tc.binary,
			})
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr)

			resp := resultToMap(t, result)
			ledgerObj := resp["ledger"].(map[string]any)
			txns := ledgerObj["transactions"].([]any)
			require.Len(t, txns, 1)
			entry := txns[0].(map[string]any)
			assert.Equal(t, "990000000", entry["owner_funds"])
		})
	}

	hashAccountHex, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           account,
		"Balance":           "2000000000",
		"Flags":             0,
		"OwnerCount":        0,
		"Sequence":          1,
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	require.NoError(t, err)
	hashAccountData, err := hex.DecodeString(hashAccountHex)
	require.NoError(t, err)
	mock.hashView = &ownerFundsView{entries: map[keylet.Keylet][]byte{
		keylet.Account(accountID): hashAccountData,
	}}
	base.getLedgerByHashFn = func(hash [32]byte) (types.LedgerReader, error) {
		return reader, nil
	}
	ledgerHash := reader.Hash()
	paramsJSON, err := json.Marshal(map[string]any{
		"ledger_hash":  fmt.Sprintf("%X", ledgerHash[:]),
		"transactions": true,
		"expand":       true,
		"owner_funds":  true,
	})
	require.NoError(t, err)
	ctx.ApiVersion = types.ApiVersion1
	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	entry := resultToMap(t, result)["ledger"].(map[string]any)["transactions"].([]any)[0].(map[string]any)
	assert.Equal(t, "1990000000", entry["owner_funds"])

	services.QueueAllTxs = func() []types.QueuedTxInfo {
		return []types.QueuedTxInfo{{Account: accountID, TxID: [32]byte{0x02}, TxJSON: offerCreate}}
	}
	ctx.Services = types.NewTestServiceGraph(services)
	reader.closed = false
	base.currentLedgerIndex = 2
	for _, tc := range []struct {
		apiVersion int
		binary     bool
	}{
		{apiVersion: types.ApiVersion1},
		{apiVersion: types.ApiVersion1, binary: true},
		{apiVersion: types.ApiVersion2},
		{apiVersion: types.ApiVersion2, binary: true},
	} {
		t.Run(fmt.Sprintf("queue v%d binary=%t", tc.apiVersion, tc.binary), func(t *testing.T) {
			ctx.ApiVersion = tc.apiVersion
			paramsJSON, err := json.Marshal(map[string]any{
				"ledger_index": "current",
				"queue":        true,
				"expand":       true,
				"owner_funds":  true,
				"binary":       tc.binary,
			})
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr)
			entry := resultToMap(t, result)["queue_data"].([]any)[0].(map[string]any)
			txBody := entry
			if tc.apiVersion == types.ApiVersion1 {
				txBody = entry["tx"].(map[string]any)
			}
			assert.Equal(t, "990000000", txBody["owner_funds"])
			if tc.binary {
				assert.Contains(t, txBody, "tx_blob")
				if tc.apiVersion == types.ApiVersion1 {
					assert.NotContains(t, txBody, "hash")
				} else {
					assert.Contains(t, txBody, "hash")
				}
			} else if tc.apiVersion > types.ApiVersion1 {
				assert.Equal(t, false, txBody["validated"])
				assert.NotContains(t, txBody["tx_json"].(map[string]any), "validated")
			}
		})
	}
}

func TestLedgerOwnerFundsUsesTargetLedgerReservesIncludingZero(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	accountHex, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           account,
		"Balance":           "1000000000",
		"Flags":             0,
		"OwnerCount":        0,
		"Sequence":          1,
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	require.NoError(t, err)
	accountData, err := hex.DecodeString(accountHex)
	require.NoError(t, err)

	feeData, err := state.SerializeFeeSettings(&state.FeeSettings{
		XRPFeesMode:           true,
		BaseFeeDrops:          10,
		ReserveBaseDrops:      20_000_000,
		ReserveIncrementDrops: 3_000_000,
	})
	require.NoError(t, err)

	_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	require.NoError(t, err)
	var accountID [20]byte
	copy(accountID[:], idBytes)
	view := &ownerFundsView{entries: map[keylet.Keylet][]byte{
		keylet.Account(accountID): accountData,
		keylet.Fees():             feeData,
	}}

	storedJSON, err := json.Marshal(map[string]any{"tx_json": map[string]any{
		"TransactionType": "OfferCreate",
		"Account":         account,
		"TakerGets":       "100000000",
		"TakerPays": map[string]any{
			"currency": "USD",
			"issuer":   "rrrrrrrrrrrrrrrrrrrrBZbvji",
			"value":    "100",
		},
		"Sequence":      1,
		"Fee":           "10",
		"SigningPubKey": "",
	}})
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

	services := &types.ServiceContainer{Ledger: &ownerFundsLedgerMock{ledgerMock: base, view: view, reader: reader}}
	ctx := &types.RpcContext{Context: context.Background(), ApiVersion: types.ApiVersion1, Services: types.NewTestServiceGraph(services)}
	paramsJSON, err := json.Marshal(map[string]any{
		"ledger_index": 2,
		"transactions": true,
		"expand":       true,
		"owner_funds":  true,
	})
	require.NoError(t, err)

	result, rpcErr := (&handlers.LedgerMethod{}).Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	entry := resultToMap(t, result)["ledger"].(map[string]any)["transactions"].([]any)[0].(map[string]any)
	assert.Equal(t, "980000000", entry["owner_funds"])

	zeroFeeData, err := state.SerializeFeeSettings(&state.FeeSettings{
		XRPFeesMode: true,
	})
	require.NoError(t, err)
	view.entries[keylet.Fees()] = zeroFeeData
	result, rpcErr = (&handlers.LedgerMethod{}).Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	entry = resultToMap(t, result)["ledger"].(map[string]any)["transactions"].([]any)[0].(map[string]any)
	assert.Equal(t, "1000000000", entry["owner_funds"])

	partialFeeData, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType":       "FeeSettings",
		"Flags":                 uint32(0),
		"ReserveIncrementDrops": "3000000",
	})
	require.NoError(t, err)
	view.entries[keylet.Fees()] = partialFeeData
	result, rpcErr = (&handlers.LedgerMethod{}).Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	entry = resultToMap(t, result)["ledger"].(map[string]any)["transactions"].([]any)[0].(map[string]any)
	assert.Equal(t, "990000000", entry["owner_funds"])

	view.readErrors = map[keylet.Keylet]error{keylet.Fees(): errors.New("fee read failed")}
	paymentData, err := json.Marshal(map[string]any{"tx_json": map[string]any{
		"TransactionType": "Payment",
		"Account":         account,
		"Destination":     "rDsbeomae4FXwgQTJp9Rs64Qg9vDiTCdBv",
		"Amount":          "1",
		"Sequence":        1,
		"Fee":             "10",
		"SigningPubKey":   "",
	}})
	require.NoError(t, err)
	reader.transactions[0].data = paymentData
	result, rpcErr = (&handlers.LedgerMethod{}).Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	entry = resultToMap(t, result)["ledger"].(map[string]any)["transactions"].([]any)[0].(map[string]any)
	assert.NotContains(t, entry, "owner_funds")

	iouOfferData, err := json.Marshal(map[string]any{"tx_json": map[string]any{
		"TransactionType": "OfferCreate",
		"Account":         account,
		"TakerGets": map[string]any{
			"currency": "USD",
			"issuer":   "rDsbeomae4FXwgQTJp9Rs64Qg9vDiTCdBv",
			"value":    "1",
		},
		"TakerPays":     "1",
		"Sequence":      1,
		"Fee":           "10",
		"SigningPubKey": "",
	}})
	require.NoError(t, err)
	reader.transactions[0].data = iouOfferData
	result, rpcErr = (&handlers.LedgerMethod{}).Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	entry = resultToMap(t, result)["ledger"].(map[string]any)["transactions"].([]any)[0].(map[string]any)
	assert.Equal(t, "0", entry["owner_funds"])

	reader.transactions[0].data = storedJSON
	result, rpcErr = (&handlers.LedgerMethod{}).Handle(ctx, paramsJSON)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)

	view.readErrors = nil
	view.entries[keylet.Fees()] = []byte{0x11, 0xff}
	result, rpcErr = (&handlers.LedgerMethod{}).Handle(ctx, paramsJSON)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)

	view.entries[keylet.Fees()] = feeData
	view.readErrors = map[keylet.Keylet]error{keylet.Account(accountID): errors.New("account read failed")}
	result, rpcErr = (&handlers.LedgerMethod{}).Handle(ctx, paramsJSON)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
}

func TestTransactionOwnerFundsMPT(t *testing.T) {
	const (
		holder = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		issuer = "rDsbeomae4FXwgQTJp9Rs64Qg9vDiTCdBv"
	)

	_, holderBytes, err := addresscodec.DecodeClassicAddressToAccountID(holder)
	require.NoError(t, err)
	var holderID [20]byte
	copy(holderID[:], holderBytes)
	_, issuerBytes, err := addresscodec.DecodeClassicAddressToAccountID(issuer)
	require.NoError(t, err)
	var issuerID [20]byte
	copy(issuerID[:], issuerBytes)

	var issuanceID [24]byte
	issuanceID[3] = 1
	copy(issuanceID[4:], issuerID[:])
	issuanceData, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer:            issuerID,
		Sequence:          1,
		OutstandingAmount: 75,
	})
	require.NoError(t, err)
	holdingData, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           holderID,
		MPTokenIssuanceID: issuanceID,
		MPTAmount:         75,
	})
	require.NoError(t, err)

	view := &ownerFundsView{entries: map[keylet.Keylet][]byte{
		keylet.MPTIssuance(issuanceID):           issuanceData,
		keylet.MPTokenByID(issuanceID, holderID): holdingData,
	}}
	takerGets := map[string]any{
		"value":           "10",
		"mpt_issuance_id": hex.EncodeToString(issuanceID[:]),
	}

	funds, ok, fundsErr := handlers.TransactionOwnerFunds(map[string]any{
		"TransactionType": "OfferCreate",
		"Account":         holder,
		"TakerGets":       takerGets,
	}, view, 0, 0)
	require.NoError(t, fundsErr)
	require.True(t, ok)
	assert.Equal(t, "75", funds)

	view.readErrors = map[keylet.Keylet]error{keylet.MPTIssuance(issuanceID): errors.New("issuance read failed")}
	_, ok, fundsErr = handlers.TransactionOwnerFunds(map[string]any{
		"TransactionType": "OfferCreate",
		"Account":         holder,
		"TakerGets":       takerGets,
	}, view, 0, 0)
	require.True(t, ok)
	require.Error(t, fundsErr)

	view.readErrors = nil
	delete(view.entries, keylet.MPTIssuance(issuanceID))
	funds, ok, fundsErr = handlers.TransactionOwnerFunds(map[string]any{
		"TransactionType": "OfferCreate",
		"Account":         holder,
		"TakerGets":       takerGets,
	}, view, 0, 0)
	require.NoError(t, fundsErr)
	require.True(t, ok)
	assert.Equal(t, "0", funds)
	view.entries[keylet.MPTIssuance(issuanceID)] = issuanceData

	_, ok, fundsErr = handlers.TransactionOwnerFunds(map[string]any{
		"TransactionType": "OfferCreate",
		"Account":         issuer,
		"TakerGets":       takerGets,
	}, view, 0, 0)
	require.NoError(t, fundsErr)
	assert.False(t, ok)

	mptOffer := map[string]any{
		"TransactionType": "OfferCreate",
		"Account":         holder,
		"TakerGets":       takerGets,
		"TakerPays":       "100",
		"Sequence":        1,
		"Fee":             "10",
		"SigningPubKey":   "",
	}
	storedMPT, err := json.Marshal(map[string]any{"tx_json": mptOffer})
	require.NoError(t, err)
	storedLater, err := json.Marshal(map[string]any{"tx_json": map[string]any{
		"TransactionType": "AccountSet",
		"Account":         holder,
		"Sequence":        2,
		"Fee":             "10",
		"SigningPubKey":   "",
	}})
	require.NoError(t, err)

	base := &ledgerMock{mockLedgerService: newMockLedgerService()}
	reader := newDefaultLedgerReader(2, true)
	reader.transactions = append(reader.transactions,
		struct {
			hash [32]byte
			data []byte
		}{hash: [32]byte{0x01}, data: storedMPT},
		struct {
			hash [32]byte
			data []byte
		}{hash: [32]byte{0x02}, data: storedLater},
	)
	base.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 2 {
			return reader, nil
		}
		return nil, errors.New("not found")
	}
	services := &types.ServiceContainer{Ledger: &ownerFundsLedgerMock{ledgerMock: base, view: view, reader: reader}}
	paramsJSON, err := json.Marshal(map[string]any{
		"ledger_index": 2,
		"transactions": true,
		"expand":       true,
		"owner_funds":  true,
	})
	require.NoError(t, err)
	result, rpcErr := (&handlers.LedgerMethod{}).Handle(&types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(services),
	}, paramsJSON)
	require.Nil(t, rpcErr)
	txns := resultToMap(t, result)["ledger"].(map[string]any)["transactions"].([]any)
	assert.Empty(t, txns)

	services.QueueAllTxs = func() []types.QueuedTxInfo {
		return []types.QueuedTxInfo{
			{Account: holderID, TxID: [32]byte{0x02}, TxJSON: map[string]any{"TransactionType": "Payment"}},
			{Account: holderID, TxID: [32]byte{0x03}, TxJSON: mptOffer},
			{Account: holderID, TxID: [32]byte{0x04}, TxJSON: map[string]any{"TransactionType": "AccountSet"}},
		}
	}
	reader.closed = false
	base.currentLedgerIndex = 2
	queueParams, err := json.Marshal(map[string]any{
		"ledger_index": "current",
		"queue":        true,
		"expand":       true,
		"owner_funds":  true,
	})
	require.NoError(t, err)
	result, rpcErr = (&handlers.LedgerMethod{}).Handle(&types.RpcContext{
		Context:    context.Background(),
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(services),
	}, queueParams)
	require.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	queueData := rpcErr.Extra["queue_data"].([]any)
	require.Len(t, queueData, 2)
	assert.Contains(t, queueData[0].(map[string]any), "tx")
	failingEntry := queueData[1].(map[string]any)
	assert.Equal(t, holder, failingEntry["account"])
	assert.NotContains(t, failingEntry, "tx")
}
