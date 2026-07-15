package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"sort"
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
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
