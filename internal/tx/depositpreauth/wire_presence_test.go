package depositpreauth

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

var registerWireTests sync.Once

func TestCredentialArrayPresenceRoundTrips(t *testing.T) {
	registerWireTests.Do(Register)
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	privateKey, publicKey, err := ed25519.Algorithm{}.DeriveKeypair([]byte("depositpreauth-field-presence"), false)
	require.NoError(t, err)

	for _, tc := range []struct {
		name  string
		field string
		set   func(*DepositPreauth)
		get   func(*DepositPreauth) []CredentialWrapper
	}{
		{
			name:  "authorize",
			field: "AuthorizeCredentials",
			set:   func(txn *DepositPreauth) { txn.AuthorizeCredentials = []CredentialWrapper{} },
			get:   func(txn *DepositPreauth) []CredentialWrapper { return txn.AuthorizeCredentials },
		},
		{
			name:  "unauthorize",
			field: "UnauthorizeCredentials",
			set:   func(txn *DepositPreauth) { txn.UnauthorizeCredentials = []CredentialWrapper{} },
			get:   func(txn *DepositPreauth) []CredentialWrapper { return txn.UnauthorizeCredentials },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			txn := NewDepositPreauth(account)
			txn.Fee = "10"
			txn.SetSequence(1)
			txn.SigningPubKey = publicKey
			tc.set(txn)

			flat, err := txn.Flatten()
			require.NoError(t, err)
			value, ok := flat[tc.field]
			require.True(t, ok)
			require.Empty(t, value)

			jsonBytes, err := txcore.ToJSON(txn)
			require.NoError(t, err)
			var jsonFields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(jsonBytes, &jsonFields))
			require.JSONEq(t, "[]", string(jsonFields[tc.field]))

			fromJSON, err := txcore.ParseJSON(jsonBytes)
			require.NoError(t, err)
			jsonTxn := fromJSON.(*DepositPreauth)
			require.NotNil(t, tc.get(jsonTxn))
			require.Empty(t, tc.get(jsonTxn))

			jsonFields[tc.field] = json.RawMessage("null")
			nullJSON, err := json.Marshal(jsonFields)
			require.NoError(t, err)
			fromNullJSON, err := txcore.ParseJSON(nullJSON)
			require.NoError(t, err)
			nullTxn := fromNullJSON.(*DepositPreauth)
			require.True(t, nullTxn.GetCommon().HasField(tc.field))
			nullFlat, err := nullTxn.Flatten()
			require.NoError(t, err)
			require.Equal(t, []map[string]any{}, nullFlat[tc.field])
			featureErr := nullTxn.CheckExtraFeatures(amendment.EmptyRules())
			resultErr, ok := ter.AsResultError(featureErr)
			require.True(t, ok)
			require.Equal(t, ter.TemDISABLED, resultErr.Code)
			require.NoError(t, nullTxn.CheckExtraFeatures(amendment.AllSupportedRules()))
			validationErr := nullTxn.Validate()
			resultErr, ok = ter.AsResultError(validationErr)
			require.True(t, ok)
			require.Equal(t, ter.TemARRAY_EMPTY, resultErr.Code)

			blob, err := binarycodec.EncodeBytes(flat)
			require.NoError(t, err)
			fromBinary, err := txcore.ParseFromBinary(blob)
			require.NoError(t, err)
			binaryTxn := fromBinary.(*DepositPreauth)
			require.NotNil(t, tc.get(binaryTxn))
			require.Empty(t, tc.get(binaryTxn))
			require.True(t, binaryTxn.GetCommon().HasField(tc.field))
			binaryFlat, err := binaryTxn.Flatten()
			require.NoError(t, err)
			reencoded, err := binarycodec.EncodeBytes(binaryFlat)
			require.NoError(t, err)
			require.Equal(t, blob, reencoded)

			_, err = sign.SignTransaction(txn, privateKey)
			require.NoError(t, err)

			featureErr = binaryTxn.CheckExtraFeatures(amendment.EmptyRules())
			resultErr, ok = ter.AsResultError(featureErr)
			require.True(t, ok)
			require.Equal(t, ter.TemDISABLED, resultErr.Code)
			require.NoError(t, binaryTxn.CheckExtraFeatures(amendment.AllSupportedRules()))

			validationErr = binaryTxn.Validate()
			resultErr, ok = ter.AsResultError(validationErr)
			require.True(t, ok)
			require.Equal(t, ter.TemARRAY_EMPTY, resultErr.Code)
		})
	}
}

func TestNonEmptyCredentialArrayKeepsCanonicalSTArrayShape(t *testing.T) {
	for _, field := range []string{"AuthorizeCredentials", "UnauthorizeCredentials"} {
		t.Run(field, func(t *testing.T) {
			txn := NewDepositPreauth("rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
			credentials := []CredentialWrapper{{Credential: CredentialSpec{
				Issuer:         "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
				CredentialType: "61",
			}}}
			if field == "AuthorizeCredentials" {
				txn.AuthorizeCredentials = credentials
			} else {
				txn.UnauthorizeCredentials = credentials
			}
			flat, err := txn.Flatten()
			require.NoError(t, err)
			require.IsType(t, []map[string]any{}, flat[field])
			_, err = binarycodec.EncodeBytes(flat)
			require.NoError(t, err)
		})
	}
}

func TestCredentialArrayAbsenceRoundTrips(t *testing.T) {
	registerWireTests.Do(Register)
	_, publicKey, err := ed25519.Algorithm{}.DeriveKeypair([]byte("depositpreauth-field-absence"), false)
	require.NoError(t, err)
	txn := NewDepositPreauth("rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
	txn.Fee = "10"
	txn.SetSequence(1)
	txn.SigningPubKey = publicKey
	flat, err := txn.Flatten()
	require.NoError(t, err)
	require.NotContains(t, flat, "AuthorizeCredentials")
	require.NotContains(t, flat, "UnauthorizeCredentials")

	jsonBytes, err := txcore.ToJSON(txn)
	require.NoError(t, err)
	fromJSON, err := txcore.ParseJSON(jsonBytes)
	require.NoError(t, err)
	jsonTxn := fromJSON.(*DepositPreauth)
	require.False(t, jsonTxn.GetCommon().HasField("AuthorizeCredentials"))
	require.False(t, jsonTxn.GetCommon().HasField("UnauthorizeCredentials"))

	blob, err := binarycodec.EncodeBytes(flat)
	require.NoError(t, err)
	fromBinary, err := txcore.ParseFromBinary(blob)
	require.NoError(t, err)
	binaryTxn := fromBinary.(*DepositPreauth)
	require.False(t, binaryTxn.GetCommon().HasField("AuthorizeCredentials"))
	require.False(t, binaryTxn.GetCommon().HasField("UnauthorizeCredentials"))

	require.NoError(t, txn.CheckExtraFeatures(amendment.EmptyRules()))
	validationErr := txn.Validate()
	resultErr, ok := ter.AsResultError(validationErr)
	require.True(t, ok)
	require.Equal(t, ter.TemMALFORMED, resultErr.Code)
}

func TestAccountSettersReplaceParsedFieldPresence(t *testing.T) {
	const (
		authorize   = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		unauthorize = "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn"
	)

	txn := NewDepositPreauth(authorize)
	txn.Unauthorize = unauthorize
	txn.SetPresentFields(map[string]bool{"Unauthorize": true})
	txn.SetAuthorize(authorize)

	require.True(t, txn.hasAuthorize())
	require.False(t, txn.hasUnauthorize())
	require.True(t, txn.HasField("Authorize"))
	require.False(t, txn.HasField("Unauthorize"))

	txn.SetUnauthorize(unauthorize)

	require.False(t, txn.hasAuthorize())
	require.True(t, txn.hasUnauthorize())
	require.False(t, txn.HasField("Authorize"))
	require.True(t, txn.HasField("Unauthorize"))

	txn = NewDepositPreauth(authorize)
	txn.SetAuthorize("")
	require.True(t, txn.hasAuthorize())
	require.False(t, txn.hasUnauthorize())

	txn.SetUnauthorize("")
	require.False(t, txn.hasAuthorize())
	require.True(t, txn.hasUnauthorize())
}
