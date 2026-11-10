package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"sort"
	"strings"
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ledgerEntryParserContext(mock *mockLedgerEntryService) (*handlers.LedgerEntryMethod, *types.RpcContext) {
	return &handlers.LedgerEntryMethod{}, &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   newLedgerEntryTestServices(mock),
	}
}

func ledgerEntryTestAccountID(t *testing.T, address string) [20]byte {
	t.Helper()
	_, decoded, err := addresscodec.DecodeClassicAddressToAccountID(address)
	require.NoError(t, err)
	var account [20]byte
	copy(account[:], decoded)
	return account
}

func TestLedgerEntryRawSequenceSelectors(t *testing.T) {
	mock := newMockLedgerEntryService()
	method, ctx := ledgerEntryParserContext(mock)
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	accountID := ledgerEntryTestAccountID(t, account)

	tests := []struct {
		selector      string
		accountField  string
		sequenceField string
		invalidToken  string
		expectedKey   [32]byte
	}{
		{"escrow", "owner", "seq", "malformedSeq", keylet.Escrow(accountID, 7).Key},
		{"offer", "account", "seq", "malformedRequest", keylet.Offer(accountID, 7).Key},
		{"oracle", "account", "oracle_document_id", "malformedDocumentID", keylet.Oracle(accountID, 7).Key},
		{"permissioned_domain", "account", "seq", "malformedRequest", keylet.PermissionedDomain(accountID, 7).Key},
		{"ticket", "account", "ticket_seq", "malformedRequest", keylet.Ticket(accountID, 7).Key},
		{"vault", "owner", "seq", "malformedRequest", keylet.Vault(accountID, 7).Key},
	}

	for _, tc := range tests {
		t.Run(tc.selector, func(t *testing.T) {
			valid := map[string]any{tc.accountField: account, tc.sequenceField: "7"}
			result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{tc.selector: valid})
			require.Nil(t, rpcErr)
			require.NotNil(t, result)
			assert.Equal(t, tc.expectedKey, mock.lastRequestedKey)

			invalid := map[string]any{tc.accountField: account, tc.sequenceField: "not-a-number"}
			result, rpcErr = handleLedgerEntry(t, method, ctx, map[string]any{tc.selector: invalid})
			require.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, tc.invalidToken, rpcErr.ErrorString)
			assert.Equal(t, "Invalid field '"+tc.sequenceField+"', not number.", rpcErr.Message)

			missing := map[string]any{tc.accountField: account}
			result, rpcErr = handleLedgerEntry(t, method, ctx, map[string]any{tc.selector: missing})
			require.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, "malformedRequest", rpcErr.ErrorString)
			assert.Equal(t, "Missing field '"+tc.sequenceField+"'.", rpcErr.Message)
		})
	}
}

func TestLedgerEntryNumericStringSequenceSelectors(t *testing.T) {
	const (
		account     = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		brokerHash  = "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"
		lockingDoor = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		issuingDoor = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	)
	accountID := ledgerEntryTestAccountID(t, account)
	lockingDoorID := ledgerEntryTestAccountID(t, lockingDoor)
	issuingDoorID := ledgerEntryTestAccountID(t, issuingDoor)
	var brokerID [32]byte
	brokerBytes, err := hex.DecodeString(brokerHash)
	require.NoError(t, err)
	copy(brokerID[:], brokerBytes)
	bridge := func() map[string]any {
		return map[string]any{
			"LockingChainDoor":  lockingDoor,
			"LockingChainIssue": map[string]any{"currency": "XRP", "issuer": nil},
			"IssuingChainDoor":  issuingDoor,
			"IssuingChainIssue": map[string]any{"currency": "USD", "issuer": issuingDoor},
		}
	}
	bridgeKey := keylet.XChainBridge{
		LockingDoor:     lockingDoorID,
		LockingCurrency: [20]byte{},
		LockingIssuer:   [20]byte{},
		IssuingDoor:     issuingDoorID,
		IssuingCurrency: keylet.CurrencyBytes("USD"),
		IssuingIssuer:   issuingDoorID,
	}

	type sequenceSelector struct {
		selector string
		field    string
		base     map[string]any
		key      func(uint32) [32]byte
		token    string
	}
	selectors := []sequenceSelector{
		{
			selector: "offer", field: "seq", token: "malformedRequest",
			base: map[string]any{"account": account},
			key:  func(value uint32) [32]byte { return keylet.Offer(accountID, value).Key },
		},
		{
			selector: "escrow", field: "seq", token: "malformedSeq",
			base: map[string]any{"owner": account},
			key:  func(value uint32) [32]byte { return keylet.Escrow(accountID, value).Key },
		},
		{
			selector: "oracle", field: "oracle_document_id", token: "malformedDocumentID",
			base: map[string]any{"account": account},
			key:  func(value uint32) [32]byte { return keylet.Oracle(accountID, value).Key },
		},
		{
			selector: "permissioned_domain", field: "seq", token: "malformedRequest",
			base: map[string]any{"account": account},
			key:  func(value uint32) [32]byte { return keylet.PermissionedDomain(accountID, value).Key },
		},
		{
			selector: "ticket", field: "ticket_seq", token: "malformedRequest",
			base: map[string]any{"account": account},
			key:  func(value uint32) [32]byte { return keylet.Ticket(accountID, value).Key },
		},
		{
			selector: "vault", field: "seq", token: "malformedRequest",
			base: map[string]any{"owner": account},
			key:  func(value uint32) [32]byte { return keylet.Vault(accountID, value).Key },
		},
		{
			selector: "loan", field: "loan_seq", token: "malformedSeq",
			base: map[string]any{"loan_broker_id": brokerHash},
			key:  func(value uint32) [32]byte { return keylet.Loan(brokerID, value).Key },
		},
		{
			selector: "loan_broker", field: "seq", token: "malformedSeq",
			base: map[string]any{"owner": account},
			key:  func(value uint32) [32]byte { return keylet.LoanBroker(accountID, value).Key },
		},
		{
			selector: "xchain_owned_claim_id", field: "xchain_owned_claim_id", token: "malformedXChainOwnedClaimID",
			base: bridge(),
			key:  func(value uint32) [32]byte { return keylet.XChainClaimID(bridgeKey, uint64(value)).Key },
		},
		{
			selector: "xchain_owned_create_account_claim_id", field: "xchain_owned_create_account_claim_id", token: "malformedXChainOwnedCreateAccountClaimID",
			base: bridge(),
			key: func(value uint32) [32]byte {
				return keylet.XChainCreateAccountClaimID(bridgeKey, uint64(value)).Key
			},
		},
	}

	accepted := []struct {
		name  string
		value string
		seq   uint32
	}{
		{name: "plus zero", value: "+0", seq: 0},
		{name: "plus one", value: "+1", seq: 1},
		{name: "plus leading zeros", value: "+0001", seq: 1},
		{name: "plus maximum uint32", value: "+4294967295", seq: 4294967295},
	}
	invalid := []struct {
		name  string
		value string
	}{
		{name: "negative", value: "-1"},
		{name: "negative zero", value: "-0"},
		{name: "leading whitespace", value: " 1"},
		{name: "trailing whitespace", value: "1 "},
		{name: "lone plus", value: "+"},
		{name: "double plus", value: "++1"},
		{name: "plus negative", value: "+-1"},
		{name: "plus whitespace", value: "+ 1"},
		{name: "uint32 overflow", value: "4294967296"},
		{name: "plus uint32 overflow", value: "+4294967296"},
	}

	request := func(base map[string]any, field, value string) map[string]any {
		result := make(map[string]any, len(base)+1)
		for key, item := range base {
			result[key] = item
		}
		result[field] = value
		return result
	}
	for _, tc := range selectors {
		t.Run(tc.selector, func(t *testing.T) {
			for _, value := range accepted {
				t.Run(value.name, func(t *testing.T) {
					mock := newMockLedgerEntryService()
					method, ctx := ledgerEntryParserContext(mock)
					result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
						tc.selector: request(tc.base, tc.field, value.value),
					})
					require.Nil(t, rpcErr)
					require.NotNil(t, result)
					assert.Equal(t, tc.key(value.seq), mock.lastRequestedKey)
				})
			}
			invalidValues := invalid
			if tc.selector != "escrow" {
				invalidValues = invalid[:1]
			}
			for _, value := range invalidValues {
				t.Run("reject "+value.name, func(t *testing.T) {
					mock := newMockLedgerEntryService()
					method, ctx := ledgerEntryParserContext(mock)
					result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
						tc.selector: request(tc.base, tc.field, value.value),
					})
					require.Nil(t, result)
					require.NotNil(t, rpcErr)
					assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
					assert.Equal(t, tc.token, rpcErr.ErrorString)
					assert.Equal(t, "Invalid field '"+tc.field+"', not number.", rpcErr.Message)
					assert.Equal(t, [32]byte{}, mock.lastRequestedKey)
				})
			}
		})
	}
}

func TestLedgerEntryRippleStateSelectorParsing(t *testing.T) {
	mock := newMockLedgerEntryService()
	method, ctx := ledgerEntryParserContext(mock)
	const account1 = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const account2 = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	accountID1 := ledgerEntryTestAccountID(t, account1)
	accountID2 := ledgerEntryTestAccountID(t, account2)
	expected := keylet.Line(accountID1, accountID2, "USD").Key
	expectedHex := hex.EncodeToString(expected[:])

	for _, selector := range []string{"ripple_state", "state"} {
		t.Run(selector+" object", func(t *testing.T) {
			result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
				selector: map[string]any{
					"accounts": []string{account1, account2},
					"currency": "USD",
				},
			})
			require.Nil(t, rpcErr)
			require.NotNil(t, result)
			assert.Equal(t, expected, mock.lastRequestedKey)
		})

		t.Run(selector+" hex", func(t *testing.T) {
			result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{selector: expectedHex})
			require.Nil(t, rpcErr)
			require.NotNil(t, result)
			assert.Equal(t, expected, mock.lastRequestedKey)
		})
	}

	tests := []struct {
		name    string
		value   any
		token   string
		message string
	}{
		{
			"currency is checked before accounts",
			map[string]any{},
			"malformedRequest",
			"Missing field 'currency'.",
		},
		{
			"accounts must have length two",
			map[string]any{"currency": "USD", "accounts": []string{account1}},
			"malformedRequest",
			"Invalid field 'accounts', not length-2 array of Accounts.",
		},
		{
			"accounts must be AccountIDs",
			map[string]any{"currency": "USD", "accounts": []string{account1, "not-an-account"}},
			"malformedAddress",
			"Invalid field 'accounts', not array of Accounts.",
		},
		{
			"self trust line is rejected",
			map[string]any{"currency": "USD", "accounts": []string{account1, account1}},
			"malformedRequest",
			"Cannot have a trustline to self.",
		},
		{
			"currency must be nonempty and well formed",
			map[string]any{"currency": "", "accounts": []string{account1, account2}},
			"malformedCurrency",
			"Invalid field 'currency', not Currency.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{"ripple_state": tc.value})
			require.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, tc.token, rpcErr.ErrorString)
			assert.Equal(t, tc.message, rpcErr.Message)
		})
	}
}

func TestLedgerEntryDepositPreauthAuthorizedCredentials(t *testing.T) {
	mock := newMockLedgerEntryService()
	method, ctx := ledgerEntryParserContext(mock)
	const owner = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const issuer1 = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	const issuer2 = "rnuF96W4SZoCJmbHYBFoJZpR8eCaxNvekK"
	ownerID := ledgerEntryTestAccountID(t, owner)
	issuerID1 := ledgerEntryTestAccountID(t, issuer1)
	issuerID2 := ledgerEntryTestAccountID(t, issuer2)

	credentials := []keylet.CredentialPair{
		{Issuer: issuerID1, CredentialType: []byte{0x01}},
		{Issuer: issuerID2, CredentialType: []byte{0x02}},
	}
	sort.Slice(credentials, func(i, j int) bool {
		if cmp := bytes.Compare(credentials[i].Issuer[:], credentials[j].Issuer[:]); cmp != 0 {
			return cmp < 0
		}
		return bytes.Compare(credentials[i].CredentialType, credentials[j].CredentialType) < 0
	})
	addresses := map[[20]byte]string{issuerID1: issuer1, issuerID2: issuer2}
	requestCredentials := []map[string]any{
		{"issuer": addresses[credentials[1].Issuer], "credential_type": hex.EncodeToString(credentials[1].CredentialType)},
		{"issuer": addresses[credentials[0].Issuer], "credential_type": hex.EncodeToString(credentials[0].CredentialType)},
	}

	result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
		"deposit_preauth": map[string]any{
			"owner":                  owner,
			"authorized_credentials": requestCredentials,
		},
	})
	require.Nil(t, rpcErr)
	require.NotNil(t, result)
	assert.Equal(t, keylet.DepositPreauthCredentials(ownerID, credentials).Key, mock.lastRequestedKey)

	t.Run("exactly one authorization form is required", func(t *testing.T) {
		result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
			"deposit_preauth": map[string]any{
				"owner":                  owner,
				"authorized":             issuer1,
				"authorized_credentials": requestCredentials,
			},
		})
		require.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "malformedRequest", rpcErr.ErrorString)
		assert.Equal(t, "Must have exactly one of `authorized` and `authorized_credentials`.", rpcErr.Message)
	})

	t.Run("duplicate credentials are rejected", func(t *testing.T) {
		duplicate := requestCredentials[0]
		result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
			"deposit_preauth": map[string]any{
				"owner":                  owner,
				"authorized_credentials": []map[string]any{duplicate, duplicate},
			},
		})
		require.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "malformedAuthorizedCredentials", rpcErr.ErrorString)
		assert.Equal(t, "Invalid field 'authorized_credentials', not array.", rpcErr.Message)
	})

	t.Run("null credentials are not an array", func(t *testing.T) {
		result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
			"deposit_preauth": map[string]any{
				"owner":                  owner,
				"authorized_credentials": nil,
			},
		})
		require.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "malformedAuthorizedCredentials", rpcErr.ErrorString)
		assert.Equal(t, "Invalid field 'authorized_credentials', not array.", rpcErr.Message)
	})
}

func TestLedgerEntryCredentialHexParityAndBounds(t *testing.T) {
	const (
		subject = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		issuer  = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	)
	subjectID := ledgerEntryTestAccountID(t, subject)
	issuerID := ledgerEntryTestAccountID(t, issuer)
	owner := subject
	ownerID := subjectID

	for _, credentialType := range []string{"F", "0F"} {
		t.Run("credential odd/even "+credentialType, func(t *testing.T) {
			mock := newMockLedgerEntryService()
			method, ctx := ledgerEntryParserContext(mock)
			result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
				"credential": map[string]any{
					"subject":         subject,
					"issuer":          issuer,
					"credential_type": credentialType,
				},
			})
			require.Nil(t, rpcErr)
			require.NotNil(t, result)
			assert.Equal(t, keylet.Credential(subjectID, issuerID, []byte{0x0F}).Key, mock.lastRequestedKey)
		})
	}

	credentialBounds := []struct {
		name        string
		credential  string
		wantSuccess bool
	}{
		{name: "64 bytes", credential: strings.Repeat("AB", 64), wantSuccess: true},
		{name: "65 bytes", credential: strings.Repeat("AB", 65), wantSuccess: false},
	}
	for _, tc := range credentialBounds {
		t.Run("credential "+tc.name, func(t *testing.T) {
			mock := newMockLedgerEntryService()
			method, ctx := ledgerEntryParserContext(mock)
			result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
				"credential": map[string]any{
					"subject":         subject,
					"issuer":          issuer,
					"credential_type": tc.credential,
				},
			})
			if tc.wantSuccess {
				require.Nil(t, rpcErr)
				require.NotNil(t, result)
				credentialBytes, err := hex.DecodeString(tc.credential)
				require.NoError(t, err)
				assert.Equal(t, keylet.Credential(subjectID, issuerID, credentialBytes).Key, mock.lastRequestedKey)
				return
			}
			require.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
			assert.Equal(t, "malformedRequest", rpcErr.ErrorString)
			assert.Equal(t, "Invalid field 'credential_type', not hex string.", rpcErr.Message)
		})
	}

	depositKey := func(credentialType string) ([32]byte, *rpcerrors.RpcError) {
		mock := newMockLedgerEntryService()
		method, ctx := ledgerEntryParserContext(mock)
		result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
			"deposit_preauth": map[string]any{
				"owner": owner,
				"authorized_credentials": []map[string]any{
					{"issuer": issuer, "credential_type": credentialType},
				},
			},
		})
		if rpcErr == nil {
			require.NotNil(t, result)
		}
		return mock.lastRequestedKey, rpcErr
	}

	t.Run("authorized credential odd/even have the same key", func(t *testing.T) {
		oddKey, oddErr := depositKey("F")
		evenKey, evenErr := depositKey("0F")
		require.Nil(t, oddErr)
		require.Nil(t, evenErr)
		assert.Equal(t, evenKey, oddKey)
		want := keylet.DepositPreauthCredentials(ownerID, []keylet.CredentialPair{{
			Issuer:         issuerID,
			CredentialType: []byte{0x0F},
		}}).Key
		assert.Equal(t, want, oddKey)
	})

	for _, tc := range credentialBounds {
		t.Run("authorized credential "+tc.name, func(t *testing.T) {
			mock := newMockLedgerEntryService()
			method, ctx := ledgerEntryParserContext(mock)
			result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
				"deposit_preauth": map[string]any{
					"owner": owner,
					"authorized_credentials": []map[string]any{
						{"issuer": issuer, "credential_type": tc.credential},
					},
				},
			})
			if tc.wantSuccess {
				require.Nil(t, rpcErr)
				require.NotNil(t, result)
				credentialBytes, err := hex.DecodeString(tc.credential)
				require.NoError(t, err)
				want := keylet.DepositPreauthCredentials(ownerID, []keylet.CredentialPair{{
					Issuer:         issuerID,
					CredentialType: credentialBytes,
				}}).Key
				assert.Equal(t, want, mock.lastRequestedKey)
				return
			}
			require.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
			assert.Equal(t, "malformedAuthorizedCredentials", rpcErr.ErrorString)
			assert.Equal(t, "Invalid field 'credential_type', not hex string.", rpcErr.Message)
		})
	}

	t.Run("authorized credential odd/even forms are duplicate", func(t *testing.T) {
		mock := newMockLedgerEntryService()
		method, ctx := ledgerEntryParserContext(mock)
		result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
			"deposit_preauth": map[string]any{
				"owner": owner,
				"authorized_credentials": []map[string]any{
					{"issuer": issuer, "credential_type": "F"},
					{"issuer": issuer, "credential_type": "0F"},
				},
			},
		})
		require.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "malformedAuthorizedCredentials", rpcErr.ErrorString)
		assert.Equal(t, "Invalid field 'authorized_credentials', not array.", rpcErr.Message)
	})
}

func TestLedgerEntryBridgeAndXChainIssueKeys(t *testing.T) {
	mock := newMockLedgerEntryService()
	method, ctx := ledgerEntryParserContext(mock)
	const lockingDoor = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const issuingDoor = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	lockingDoorID := ledgerEntryTestAccountID(t, lockingDoor)
	issuingDoorID := ledgerEntryTestAccountID(t, issuingDoor)

	bridge := func() map[string]any {
		return map[string]any{
			"LockingChainDoor":  lockingDoor,
			"LockingChainIssue": map[string]any{"currency": "XRP", "issuer": nil},
			"IssuingChainDoor":  issuingDoor,
			"IssuingChainIssue": map[string]any{"currency": "USD", "issuer": issuingDoor},
		}
	}

	for _, tc := range []struct {
		name    string
		account string
		key     [32]byte
	}{
		{"locking chain", lockingDoor, keylet.Bridge(lockingDoorID, [20]byte{}).Key},
		{"issuing chain", issuingDoor, keylet.Bridge(issuingDoorID, keylet.CurrencyBytes("USD")).Key},
	} {
		t.Run("bridge "+tc.name, func(t *testing.T) {
			result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{
				"bridge":         bridge(),
				"bridge_account": tc.account,
			})
			require.Nil(t, rpcErr)
			require.NotNil(t, result)
			assert.Equal(t, tc.key, mock.lastRequestedKey)
		})
	}

	bridgeKey := keylet.XChainBridge{
		LockingDoor:     lockingDoorID,
		LockingCurrency: [20]byte{},
		LockingIssuer:   [20]byte{},
		IssuingDoor:     issuingDoorID,
		IssuingCurrency: keylet.CurrencyBytes("USD"),
		IssuingIssuer:   issuingDoorID,
	}
	for _, tc := range []struct {
		selector string
		key      [32]byte
	}{
		{"xchain_owned_claim_id", keylet.XChainClaimID(bridgeKey, 9).Key},
		{"xchain_owned_create_account_claim_id", keylet.XChainCreateAccountClaimID(bridgeKey, 9).Key},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			value := bridge()
			value[tc.selector] = "9"
			result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{tc.selector: value})
			require.Nil(t, rpcErr)
			require.NotNil(t, result)
			assert.Equal(t, tc.key, mock.lastRequestedKey)
		})
	}

	t.Run("MPT issue members are rejected", func(t *testing.T) {
		value := bridge()
		value["xchain_owned_claim_id"] = 9
		value["LockingChainIssue"].(map[string]any)["mpt_issuance_id"] = "00"
		result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{"xchain_owned_claim_id": value})
		require.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "malformedIssue", rpcErr.ErrorString)
		assert.Equal(t, "Invalid field 'LockingChainIssue', not Issue.", rpcErr.Message)
	})

	t.Run("XRP issue rejects non-null issuer", func(t *testing.T) {
		value := bridge()
		value["xchain_owned_claim_id"] = 9
		value["LockingChainIssue"].(map[string]any)["issuer"] = lockingDoor
		result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{"xchain_owned_claim_id": value})
		require.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "malformedIssue", rpcErr.ErrorString)
		assert.Equal(t, "Invalid field 'LockingChainIssue', not Issue.", rpcErr.Message)
	})

	t.Run("non-string bridge scalar parses as bridge fields", func(t *testing.T) {
		result, rpcErr := handleLedgerEntry(t, method, ctx, map[string]any{"bridge": 7})
		require.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "malformedRequest", rpcErr.ErrorString)
		assert.Equal(t, "Missing field 'LockingChainDoor'.", rpcErr.Message)
	})
}
