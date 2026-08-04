package did_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/internal/tx/did"
)

func TestDIDSetFieldPresenceRoundTrips(t *testing.T) {
	all.RegisterAll()
	alice := jtx.NewAccount("alice")

	for _, field := range []string{"URI", "DIDDocument", "Data"} {
		for _, tc := range []struct {
			name    string
			present bool
			value   string
		}{
			{name: "absent"},
			{name: "present empty", present: true},
			{name: "non-empty", present: true, value: "4142"},
		} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				original := did.NewDIDSet(alice.Address)
				original.Fee = "10"
				original.SetSequence(1)
				setDIDField(original, field, tc.value)
				if tc.present {
					original.SetPresentFields(map[string]bool{field: true})
				}

				assertDIDField(t, original, field, tc.present, tc.value)

				jsonBytes, err := tx.ToJSON(original)
				require.NoError(t, err)
				assertJSONField(t, jsonBytes, field, tc.present, tc.value)

				fromJSON, err := tx.FromJSON(jsonBytes)
				require.NoError(t, err)
				assertDIDField(t, fromJSON.(*did.DIDSet), field, tc.present, tc.value)

				binary, err := tx.SerializeTransaction(original)
				require.NoError(t, err)
				decoded, err := binarycodec.DecodeBytes(binary)
				require.NoError(t, err)
				value, ok := decoded[field]
				require.Equal(t, tc.present, ok)
				if tc.present {
					require.Equal(t, tc.value, value)
				}

				fromBinary, err := tx.ParseFromBinary(binary)
				require.NoError(t, err)
				assertDIDField(t, fromBinary.(*did.DIDSet), field, tc.present, tc.value)
			})
		}
	}
}

func setDIDField(transaction *did.DIDSet, field, value string) {
	switch field {
	case "URI":
		transaction.URI = value
	case "DIDDocument":
		transaction.DIDDocument = value
	case "Data":
		transaction.Data = value
	}
}

func didField(transaction *did.DIDSet, field string) string {
	switch field {
	case "URI":
		return transaction.URI
	case "DIDDocument":
		return transaction.DIDDocument
	case "Data":
		return transaction.Data
	default:
		return ""
	}
}

func assertDIDField(t *testing.T, transaction *did.DIDSet, field string, present bool, value string) {
	t.Helper()
	require.Equal(t, present, transaction.HasField(field))
	require.Equal(t, value, didField(transaction, field))
	flat, err := transaction.Flatten()
	require.NoError(t, err)
	flatValue, ok := flat[field]
	require.Equal(t, present, ok)
	if present {
		require.Equal(t, value, flatValue)
	}
}

func assertJSONField(t *testing.T, data []byte, field string, present bool, value string) {
	t.Helper()
	var object map[string]any
	require.NoError(t, json.Unmarshal(data, &object))
	jsonValue, ok := object[field]
	require.Equal(t, present, ok)
	if present {
		require.Equal(t, value, jsonValue)
	}
}
