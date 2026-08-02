package node

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestDecodeTxWithMetaToJSONInjectsSyntheticFields(t *testing.T) {
	const issuer = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const sequence = uint32(42)
	txJSON := map[string]any{
		"Account":         issuer,
		"Fee":             "10",
		"Sequence":        sequence,
		"SigningPubKey":   "",
		"TransactionType": "MPTokenIssuanceCreate",
	}
	meta := map[string]any{
		"AffectedNodes": []any{
			map[string]any{"CreatedNode": map[string]any{
				"LedgerEntryType": "MPTokenIssuance",
				"LedgerIndex":     strings.Repeat("A", 64),
				"NewFields": map[string]any{
					"Issuer":   issuer,
					"Sequence": sequence,
				},
			}},
		},
		"TransactionIndex":  uint32(0),
		"TransactionResult": "tesSUCCESS",
	}

	txHex, err := binarycodec.Encode(txJSON)
	require.NoError(t, err)
	txBlob, err := hex.DecodeString(txHex)
	require.NoError(t, err)
	metaHex, err := binarycodec.Encode(meta)
	require.NoError(t, err)
	metaBlob, err := hex.DecodeString(metaHex)
	require.NoError(t, err)
	require.Less(t, len(txBlob), 193)
	require.Less(t, len(metaBlob), 193)
	data := append([]byte{byte(len(txBlob))}, txBlob...)
	data = append(data, byte(len(metaBlob)))
	data = append(data, metaBlob...)

	_, metaRaw, err := decodeTxWithMetaToJSONAt(data, handlers.SyntheticMetadataContext{LedgerSequence: 4_594_095})
	require.NoError(t, err)
	var decodedMeta map[string]any
	require.NoError(t, json.Unmarshal(metaRaw, &decodedMeta))

	_, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(issuer)
	require.NoError(t, err)
	var accountID [20]byte
	copy(accountID[:], accountBytes)
	mptID := keylet.MakeMPTID(sequence, accountID)
	want := strings.ToUpper(hex.EncodeToString(mptID[:]))
	require.Equal(t, want, decodedMeta["mpt_issuance_id"])
}

func TestBuildValidatedTransactionEventProjection(t *testing.T) {
	const (
		ledgerSequence   = uint32(1)
		transactionIndex = uint32(7)
		closeTime        = int64(446_000_001)
	)
	ledgerCloseTime := time.Unix(protocol.RippleEpochUnix+closeTime, 0).UTC()
	ledgerHash := [32]byte{0xAB}
	txHash := [32]byte{0xCD}
	ledgerEvent := &service.LedgerAcceptedEvent{LedgerInfo: &service.LedgerInfo{
		Sequence:  ledgerSequence,
		Hash:      ledgerHash,
		CloseTime: ledgerCloseTime,
		Validated: true,
		Closed:    true,
	}}

	t.Run("validated failure projects result time and checked CTID", func(t *testing.T) {
		txData := validatedPaymentData(t, 2_048, transactionIndex)
		event, result, err := buildValidatedTransactionEvent(service.TransactionResultEvent{
			TxHash:      txHash,
			TxData:      txData,
			Validated:   true,
			LedgerIndex: ledgerSequence,
			LedgerHash:  ledgerHash,
		}, ledgerEvent, 9)
		require.NoError(t, err)

		require.Equal(t, ter.TecUNFUNDED_PAYMENT, result)
		require.Equal(t, "tecUNFUNDED_PAYMENT", event.EngineResult)
		require.Equal(t, 104, event.EngineResultCode)
		require.Equal(t, "Insufficient XRP balance to send.", event.EngineResultMessage)
		require.Equal(t, ledgerCloseTime.Format(time.RFC3339), event.CloseTimeISO)
		require.Equal(t, "C000000100070800", event.CTID)

		var transaction struct {
			Date      uint32 `json:"date"`
			NetworkID uint32 `json:"NetworkID"`
		}
		require.NoError(t, json.Unmarshal(event.Transaction, &transaction))
		require.Equal(t, uint32(closeTime), transaction.Date)
		require.Equal(t, uint32(2_048), transaction.NetworkID)
	})

	t.Run("network ID overflow omits CTID", func(t *testing.T) {
		txData := validatedPaymentData(t, 65_536, transactionIndex)
		event, _, err := buildValidatedTransactionEvent(service.TransactionResultEvent{
			TxData:      txData,
			Validated:   true,
			LedgerIndex: ledgerSequence,
		}, ledgerEvent, 9)
		require.NoError(t, err)
		require.Empty(t, event.CTID)
	})
}

func TestBuildValidatedTransactionEventRejectsCorruptLeaf(t *testing.T) {
	ledgerEvent := &service.LedgerAcceptedEvent{LedgerInfo: &service.LedgerInfo{
		Sequence:  1,
		Validated: true,
		Closed:    true,
	}}
	valid := validatedPaymentData(t, 0, 0)
	tests := [][]byte{
		nil,
		{0xff},
		valid[:len(valid)-1],
		append(append([]byte(nil), valid...), 0),
	}
	for _, data := range tests {
		event, _, err := buildValidatedTransactionEvent(service.TransactionResultEvent{
			TxData:      data,
			Validated:   true,
			LedgerIndex: 1,
		}, ledgerEvent, 0)
		require.Error(t, err)
		require.Nil(t, event)
	}
}

func validatedPaymentData(t *testing.T, networkID, transactionIndex uint32) []byte {
	t.Helper()
	txJSON := map[string]any{
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Amount":          "100",
		"Destination":     "r4bbzCamAis69rNoRdSaMSmPb1kDUHXcAL",
		"Fee":             "10",
		"NetworkID":       networkID,
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"TransactionType": "Payment",
	}
	meta := map[string]any{
		"AffectedNodes":     []any{},
		"TransactionIndex":  transactionIndex,
		"TransactionResult": "tecUNFUNDED_PAYMENT",
	}
	txHex, err := binarycodec.Encode(txJSON)
	require.NoError(t, err)
	txBlob, err := hex.DecodeString(txHex)
	require.NoError(t, err)
	metaHex, err := binarycodec.Encode(meta)
	require.NoError(t, err)
	metaBlob, err := hex.DecodeString(metaHex)
	require.NoError(t, err)
	require.Less(t, len(txBlob), 193)
	require.Less(t, len(metaBlob), 193)
	data := append([]byte{byte(len(txBlob))}, txBlob...)
	data = append(data, byte(len(metaBlob)))
	return append(data, metaBlob...)
}
