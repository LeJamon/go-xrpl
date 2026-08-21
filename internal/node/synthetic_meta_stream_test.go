package node

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestProjectAcceptedTransactionInjectsSyntheticFields(t *testing.T) {
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

	projection, err := projectAcceptedTransaction(
		service.ParseAcceptedTransaction(data),
		handlers.SyntheticMetadataContext{LedgerSequence: 4_594_095},
	)
	require.NoError(t, err)
	decodedMeta := projection.metadata

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

func TestPrepareAcceptedPublicationsRejectsWholeLedger(t *testing.T) {
	ledgerHash := [32]byte{0xAB}
	goodAccepted := service.ParseAcceptedTransaction(validatedPaymentData(t, 0, 4))
	require.NoError(t, goodAccepted.ParseError())
	corruptAccepted := service.ParseAcceptedTransaction([]byte("corrupt"))
	require.Error(t, corruptAccepted.ParseError())

	event := &service.LedgerAcceptedEvent{
		LedgerInfo: &service.LedgerInfo{
			Sequence:  9,
			Hash:      ledgerHash,
			Validated: true,
		},
		TransactionResults: []service.TransactionResultEvent{
			{
				TxHash:      [32]byte{1},
				TxData:      []byte("must not be decoded again"),
				Accepted:    goodAccepted,
				Validated:   true,
				LedgerIndex: 9,
				LedgerHash:  ledgerHash,
			},
			{
				TxHash:      [32]byte{2},
				Accepted:    corruptAccepted,
				Validated:   true,
				LedgerIndex: 9,
				LedgerHash:  ledgerHash,
			},
		},
	}

	publications, bookTransactions, err := prepareAcceptedPublications(event, 0)
	require.Error(t, err)
	require.Nil(t, publications)
	require.Nil(t, bookTransactions)
}

func TestPrepareAcceptedPublicationsRejectsCorruptOwnerFundsState(t *testing.T) {
	svc, err := service.New(service.Config{
		Standalone:    true,
		Startup:       service.StartupConfig{Mode: service.StartupFresh},
		GenesisConfig: genesis.DefaultConfig(),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	view := svc.GetOpenLedger()
	require.NotNil(t, view)
	require.NoError(t, view.Update(keylet.Fees(), []byte{
		0x11, 0x00, 0x73, 0xff, 0, 0, 0, 0, 0, 0, 0, 0,
	}))

	accepted := service.ParseAcceptedTransaction(validatedOfferData(t, 0, 0))
	require.NoError(t, accepted.ParseError())
	event := &service.LedgerAcceptedEvent{
		Ledger: view,
		LedgerInfo: &service.LedgerInfo{
			Sequence:  view.Sequence(),
			Validated: true,
		},
		TransactionResults: []service.TransactionResultEvent{{
			TxHash:      [32]byte{1},
			Accepted:    accepted,
			Validated:   true,
			LedgerIndex: view.Sequence(),
		}},
	}

	publications, bookTransactions, err := prepareAcceptedPublications(event, 0)
	require.Error(t, err)
	require.Nil(t, publications)
	require.Nil(t, bookTransactions)
}

func TestPrepareAcceptedPublicationsReadsFeesOnlyForXRPOffers(t *testing.T) {
	svc, err := service.New(service.Config{
		Standalone:    true,
		Startup:       service.StartupConfig{Mode: service.StartupFresh},
		GenesisConfig: genesis.DefaultConfig(),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	view := svc.GetOpenLedger()
	require.NotNil(t, view)
	require.NoError(t, view.Update(keylet.Fees(), []byte{
		0x11, 0x00, 0x73, 0xff, 0, 0, 0, 0, 0, 0, 0, 0,
	}))

	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "Payment", data: validatedPaymentData(t, 0, 0)},
		{name: "IOU OfferCreate", data: validatedIOUOfferData(t, 0, 0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			accepted := service.ParseAcceptedTransaction(test.data)
			require.NoError(t, accepted.ParseError())
			event := &service.LedgerAcceptedEvent{
				Ledger: view,
				LedgerInfo: &service.LedgerInfo{
					Sequence:  view.Sequence(),
					Validated: true,
				},
				TransactionResults: []service.TransactionResultEvent{{
					TxHash:      [32]byte{1},
					Accepted:    accepted,
					Validated:   true,
					LedgerIndex: view.Sequence(),
				}},
			}

			publications, bookTransactions, err := prepareAcceptedPublications(event, 0)
			require.NoError(t, err)
			require.Len(t, publications, 1)
			require.Len(t, bookTransactions, 1)
		})
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
	return validatedTransactionData(t, txJSON, transactionIndex)
}

func validatedOfferData(t *testing.T, networkID, transactionIndex uint32) []byte {
	t.Helper()
	txJSON := map[string]any{
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Fee":             "10",
		"NetworkID":       networkID,
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"TakerGets":       "100",
		"TakerPays":       "200",
		"TransactionType": "OfferCreate",
	}
	return validatedTransactionData(t, txJSON, transactionIndex)
}

func validatedIOUOfferData(t *testing.T, networkID, transactionIndex uint32) []byte {
	t.Helper()
	txJSON := map[string]any{
		"Account":       "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Fee":           "10",
		"NetworkID":     networkID,
		"Sequence":      uint32(1),
		"SigningPubKey": "",
		"TakerGets": map[string]any{
			"currency": "USD",
			"issuer":   "rDsbeomae4FXwgQTJp9Rs64Qg9vDiTCdBv",
			"value":    "1",
		},
		"TakerPays":       "200",
		"TransactionType": "OfferCreate",
	}
	return validatedTransactionData(t, txJSON, transactionIndex)
}

func validatedTransactionData(t *testing.T, txJSON map[string]any, transactionIndex uint32) []byte {
	t.Helper()
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
