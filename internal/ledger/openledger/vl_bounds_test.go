package openledger_test

import (
	"bytes"
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

const maxVariableLength = 918744

const openLedgerTestAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

func encodeOpenLedgerVLBase(t *testing.T, field string) []byte {
	t.Helper()

	fields := map[string]any{
		"TransactionType": "PaymentChannelCreate",
		"Account":         openLedgerTestAccount,
		"Destination":     openLedgerTestAccount,
		"Amount":          "1",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SettleDelay":     uint32(1),
		"PublicKey":       "00",
		"SigningPubKey":   "00",
	}
	if field == "MemoData" {
		fields["TransactionType"] = "Payment"
		delete(fields, "PublicKey")
		delete(fields, "SettleDelay")
		fields["Memos"] = []map[string]any{{
			"Memo": map[string]any{"MemoData": "00"},
		}}
	}

	blob, err := binarycodec.EncodeBytes(fields)
	require.NoError(t, err)
	return blob
}

func replaceVLEncodedField(t *testing.T, blob []byte, field string, prefix, payload []byte) []byte {
	t.Helper()

	header, err := serdes.DefaultFieldIDCodec().Encode(field)
	require.NoError(t, err)
	marker := make([]byte, 0, len(header)+2)
	marker = append(marker, header...)
	marker = append(marker, 0x01, 0x00)
	fieldOffset := bytes.Index(blob, marker)
	require.NotEqual(t, -1, fieldOffset, "encoded %s field marker", field)

	fieldValue := make([]byte, 0, len(header)+len(prefix)+len(payload))
	fieldValue = append(fieldValue, header...)
	fieldValue = append(fieldValue, prefix...)
	fieldValue = append(fieldValue, payload...)

	result := make([]byte, 0, len(blob)-len(marker)+len(fieldValue))
	result = append(result, blob[:fieldOffset]...)
	result = append(result, fieldValue...)
	result = append(result, blob[fieldOffset+len(marker):]...)
	return result
}

func TestParsePendingTxRejectsCompleteOversizedVLEncodedFields(t *testing.T) {
	tests := []struct {
		name  string
		field string
	}{
		{name: "PublicKey", field: "PublicKey"},
		{name: "MemoData", field: "MemoData"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blob := replaceVLEncodedField(
				t,
				encodeOpenLedgerVLBase(t, test.field),
				test.field,
				[]byte{0xFE, 0xD4, 0x18},
				make([]byte, maxVariableLength+1),
			)

			_, err := openledger.ParsePendingTx(blob)
			require.ErrorIs(t, err, serdes.ErrVariableLengthTooLong)
		})
	}
}

func TestParsePendingTxPreservesMaxVLEncodedPreimageAndHash(t *testing.T) {
	payload := bytes.Repeat([]byte{0xA5}, maxVariableLength)
	tests := []struct {
		name   string
		field  string
		prefix []byte
	}{
		{name: "PublicKey", field: "PublicKey", prefix: []byte{0xFE, 0xD4, 0x17}},
		{name: "MemoData", field: "MemoData", prefix: []byte{0xFE, 0xD4, 0x17}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blob := replaceVLEncodedField(
				t,
				encodeOpenLedgerVLBase(t, test.field),
				test.field,
				test.prefix,
				payload,
			)

			pending, err := openledger.ParsePendingTx(blob)
			require.NoError(t, err)
			require.Equal(t, blob, pending.Blob)

			wantHash := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), blob)
			require.Equal(t, wantHash, pending.Hash)

			preimage, err := tx.SerializeTransaction(pending.Parsed)
			require.NoError(t, err)
			require.Equal(t, blob, preimage)
			computedHash, err := tx.ComputeTransactionHash(pending.Parsed)
			require.NoError(t, err)
			require.Equal(t, wantHash, computedHash)

			fields, err := pending.Parsed.Flatten()
			require.NoError(t, err)
			signingHex, err := binarycodec.EncodeForSigning(fields)
			require.NoError(t, err)
			signingPreimage, err := hex.DecodeString(signingHex)
			require.NoError(t, err)
			wantSigningPreimage := make([]byte, 0, len(protocol.HashPrefixTxSign().Bytes())+len(blob))
			wantSigningPreimage = append(wantSigningPreimage, protocol.HashPrefixTxSign().Bytes()...)
			wantSigningPreimage = append(wantSigningPreimage, blob...)
			require.Equal(t, wantSigningPreimage, signingPreimage)
		})
	}
}
