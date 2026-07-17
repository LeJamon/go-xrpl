package mptutil

import (
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

type mptTestView struct {
	data         map[[32]byte][]byte
	readErrors   map[[32]byte]error
	existsErrors map[[32]byte]error
	adjustments  [][3]uint32
	rules        *amendment.Rules
}

func newMPTTestView() *mptTestView {
	return &mptTestView{
		data:         make(map[[32]byte][]byte),
		readErrors:   make(map[[32]byte]error),
		existsErrors: make(map[[32]byte]error),
	}
}

func (v *mptTestView) Read(k keylet.Keylet) ([]byte, error) {
	if err := v.readErrors[k.Key]; err != nil {
		return nil, err
	}
	return v.data[k.Key], nil
}
func (v *mptTestView) Exists(k keylet.Keylet) (bool, error) {
	if err := v.existsErrors[k.Key]; err != nil {
		return false, err
	}
	_, exists := v.data[k.Key]
	return exists, nil
}
func (v *mptTestView) Insert(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = data
	return nil
}
func (v *mptTestView) Update(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = data
	return nil
}
func (v *mptTestView) Erase(k keylet.Keylet) error {
	delete(v.data, k.Key)
	return nil
}
func (v *mptTestView) Rules() *amendment.Rules {
	if v.rules != nil {
		return v.rules
	}
	return amendment.AllSupportedRules()
}
func (v *mptTestView) AdjustOwnerCount(_ [20]byte, current, next uint32) {
	v.adjustments = append(v.adjustments, [3]uint32{current, next})
}

func TestMPTReadsPropagateStorageErrors(t *testing.T) {
	view := newMPTTestView()
	var id [24]byte
	id[23] = 1
	var holder [20]byte
	holder[19] = 2
	readErr := errors.New("storage read failed")

	view.readErrors[keylet.MPTIssuance(id).Key] = readErr
	_, _, result := ReadIssuance(view, id)
	require.Equal(t, ter.TefINTERNAL, result)

	delete(view.readErrors, keylet.MPTIssuance(id).Key)
	view.readErrors[keylet.MPTokenByID(id, holder).Key] = readErr
	_, _, result = ReadHolding(view, id, holder)
	require.Equal(t, ter.TefINTERNAL, result)
}

func TestMPTAuthorizationPropagatesAccountReadError(t *testing.T) {
	view := newMPTTestView()
	var issuer, holder [20]byte
	issuer[19] = 1
	holder[19] = 2
	id := keylet.MakeMPTID(1, issuer)
	putTestAccount(t, view, issuer, 0, [32]byte{})
	putTestIssuance(t, view, id, entry.LsfMPTRequireAuth, nil)
	view.readErrors[keylet.Account(holder).Key] = errors.New("storage read failed")

	require.Equal(t, ter.TefINTERNAL, RequireAuth(view, id, holder, false))
}

func TestPseudoAccountImplicitAuthorizationRequiresAmendment(t *testing.T) {
	view := newMPTTestView()
	var issuer, pseudo [20]byte
	issuer[19] = 1
	pseudo[19] = 2
	id := keylet.MakeMPTID(1, issuer)
	putTestIssuance(t, view, id, entry.LsfMPTRequireAuth, nil)
	putTestAccount(t, view, issuer, 0, [32]byte{})
	putTestAccount(t, view, pseudo, 0, [32]byte{1})

	view.rules = amendment.NewRules(nil)
	require.Equal(t, ter.TecNO_AUTH, RequireAuth(view, id, pseudo, false))

	view.rules = amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})
	require.Equal(t, ter.TesSUCCESS, RequireAuth(view, id, pseudo, false))

	view.rules = amendment.NewRules([][32]byte{amendment.FeatureMPTokensV2})
	require.Equal(t, ter.TesSUCCESS, RequireAuth(view, id, pseudo, false))
}

func TestIOUTransferCheckPropagatesTrustLineReadError(t *testing.T) {
	view := newMPTTestView()
	var issuer, from, to [20]byte
	issuer[19] = 1
	from[19] = 2
	to[19] = 3
	putTestAccount(t, view, issuer, 0, [32]byte{})
	view.readErrors[keylet.Line(from, issuer, "USD").Key] = errors.New("storage read failed")
	asset := tx.Asset{Currency: "USD", Issuer: state.EncodeAccountIDSafe(issuer)}

	require.Equal(t, ter.TefINTERNAL, canTransferAsset(view, asset, from, to, 0))
}

func TestMPTAuthorizationPropagatesMalformedTrustLine(t *testing.T) {
	view := newMPTTestView()
	var issuer, holder [20]byte
	issuer[19] = 1
	holder[19] = 2
	putTestAccount(t, view, issuer, state.LsfRequireAuth, [32]byte{})
	view.data[keylet.Line(holder, issuer, "USD").Key] = []byte{1}
	asset := tx.Asset{Currency: "USD", Issuer: state.EncodeAccountIDSafe(issuer)}

	require.Equal(t, ter.TefINTERNAL, requireAssetAuthAt(view, asset, holder, true, 0, 0))
}

func TestDomainAuthorizationPropagatesLedgerErrors(t *testing.T) {
	view := newMPTTestView()
	var owner, holder, credentialIssuer [20]byte
	owner[19] = 1
	holder[19] = 2
	credentialIssuer[19] = 3
	domainID := [32]byte{1}
	domainHex := strings.ToUpper(hex.EncodeToString(domainID[:]))
	credentialType := []byte("KYC")
	domainRaw, err := state.SerializePermissionedDomain(&state.PermissionedDomainData{
		Owner:    owner,
		Sequence: 1,
		AcceptedCredentials: []state.PermissionedDomainCredential{{
			Issuer: credentialIssuer, CredentialType: credentialType,
		}},
	}, state.EncodeAccountIDSafe(owner))
	require.NoError(t, err)
	view.data[keylet.PermissionedDomainByID(domainID).Key] = domainRaw

	domainKey := keylet.PermissionedDomainByID(domainID)
	view.readErrors[domainKey.Key] = errors.New("domain read failed")
	require.Equal(t, ter.TefINTERNAL, validDomain(view, domainHex, holder, 0))
	delete(view.readErrors, domainKey.Key)

	credentialKey := keylet.Credential(holder, credentialIssuer, credentialType)
	view.readErrors[credentialKey.Key] = errors.New("credential read failed")
	require.Equal(t, ter.TefINTERNAL, validDomain(view, domainHex, holder, 0))
	delete(view.readErrors, credentialKey.Key)
	view.data[credentialKey.Key] = []byte{1}
	require.Equal(t, ter.TefINTERNAL, validDomain(view, domainHex, holder, 0))
}

func TestMPTHoldingMutationsPropagateStorageErrors(t *testing.T) {
	view := newMPTTestView()
	var issuer, holder [20]byte
	issuer[19] = 1
	holder[19] = 2
	id := keylet.MakeMPTID(1, issuer)
	tokenKey := keylet.MPTokenByID(id, holder)
	storageErr := errors.New("storage failure")

	view.existsErrors[tokenKey.Key] = storageErr
	require.Equal(t, ter.TefINTERNAL, EnsureHolding(view, id, holder, 0, true))
	delete(view.existsErrors, tokenKey.Key)

	view.readErrors[keylet.Account(holder).Key] = storageErr
	require.Equal(t, ter.TefINTERNAL, EnsureHolding(view, id, holder, 0, true))
	delete(view.readErrors, keylet.Account(holder).Key)

	view.readErrors[tokenKey.Key] = storageErr
	require.Equal(t, ter.TefINTERNAL, RemoveHolding(view, id, holder, true))
}

func TestEnsureAndRemoveHolding(t *testing.T) {
	view := newMPTTestView()
	var id [24]byte
	copy(id[4:], []byte("issuer-account-id-123"))
	var holder [20]byte
	copy(holder[:], []byte("holder-account-id-123"))

	account := &state.AccountRoot{
		Account:    state.EncodeAccountIDSafe(holder),
		Balance:    100_000_000,
		OwnerCount: 2,
		Sequence:   1,
	}
	raw, err := state.SerializeAccountRoot(account)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(holder), raw))

	require.Equal(t, ter.TesSUCCESS, EnsureHolding(view, id, holder, 0, true))
	tokenKey := keylet.MPTokenByID(id, holder)
	tokenRaw, err := view.Read(tokenKey)
	require.NoError(t, err)
	token, err := state.ParseMPToken(tokenRaw)
	require.NoError(t, err)
	locked := uint64(1)
	token.LockedAmount = &locked
	tokenRaw, err = state.SerializeMPToken(token)
	require.NoError(t, err)
	require.NoError(t, view.Update(tokenKey, tokenRaw))

	require.Equal(t, ter.TecHAS_OBLIGATIONS, RemoveHolding(view, id, holder, true))
	token.LockedAmount = nil
	tokenRaw, err = state.SerializeMPToken(token)
	require.NoError(t, err)
	require.NoError(t, view.Update(tokenKey, tokenRaw))
	require.Equal(t, ter.TesSUCCESS, RemoveHolding(view, id, holder, true))

	exists, err := view.Exists(tokenKey)
	require.NoError(t, err)
	require.False(t, exists)
	accountRaw, err := view.Read(keylet.Account(holder))
	require.NoError(t, err)
	account, err = state.ParseAccountRoot(accountRaw)
	require.NoError(t, err)
	require.Equal(t, uint32(2), account.OwnerCount)
	require.Equal(t, [][3]uint32{{2, 3}, {3, 2}}, view.adjustments)
}

func TestRemoveHoldingOwnerDirectoryFailureTER(t *testing.T) {
	view := newMPTTestView()
	var issuer, holder [20]byte
	issuer[19] = 1
	holder[19] = 2
	id := keylet.MakeMPTID(1, issuer)
	putTestAccount(t, view, holder, 0, [32]byte{})
	putTestHolding(t, view, id, holder, 0)

	require.Equal(t, ter.TecINTERNAL, RemoveHolding(view, id, holder, false))
	view.readErrors[keylet.OwnerDir(holder).Key] = errors.New("storage read failed")
	require.Equal(t, ter.TefINTERNAL, RemoveHolding(view, id, holder, false))
}

func TestCreditOverflowStillCapsSingleIssueAtMaximum(t *testing.T) {
	view := newMPTTestView()
	var id [24]byte
	copy(id[4:], []byte("issuer-account-id-123"))
	issuer := Issuer(id)
	var holder [20]byte
	copy(holder[:], []byte("holder-account-id-123"))
	maximum := uint64(50)
	issuance := &state.MPTokenIssuanceData{Issuer: issuer, Sequence: 1, MaximumAmount: &maximum}
	raw, err := state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.MPTIssuance(id), raw))
	token := &state.MPTokenData{Account: holder, MPTokenIssuanceID: id}
	raw, err = state.SerializeMPToken(token)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.MPTokenByID(id, holder), raw))

	require.Equal(t, ter.TecPATH_DRY, Credit(view, id, issuer, holder, 51, true))
}

func TestVaultShareReferenceInheritsMPTChecks(t *testing.T) {
	view := newMPTTestView()
	var vaultPseudo, underlyingIssuer, from, to [20]byte
	vaultPseudo[19] = 0x20
	underlyingIssuer[19] = 0x10
	from[19] = 0x30
	to[19] = 0x40
	shareID := keylet.MakeMPTID(1, vaultPseudo)
	underlyingID := keylet.MakeMPTID(2, underlyingIssuer)

	referenceKey := keylet.MPTokenByID(underlyingID, vaultPseudo)
	referenceHex := strings.ToUpper(hex.EncodeToString(referenceKey.Key[:]))
	putTestIssuance(t, view, shareID, entry.LsfMPTCanTrade|entry.LsfMPTCanTransfer, &referenceHex)
	putTestIssuance(t, view, underlyingID, 0, nil)
	putTestHolding(t, view, underlyingID, vaultPseudo, 0)

	require.Equal(t, ter.TecNO_PERMISSION, CanTrade(view, shareID))
	require.Equal(t, ter.TecNO_AUTH, CanTransfer(view, shareID, from, to))
	require.False(t, IsFrozen(view, shareID, to))

	putTestIssuance(t, view, underlyingID,
		entry.LsfMPTCanTrade|entry.LsfMPTCanTransfer|entry.LsfMPTLocked, nil)
	require.Equal(t, ter.TesSUCCESS, CanTrade(view, shareID))
	require.Equal(t, ter.TesSUCCESS, CanTransfer(view, shareID, from, to))
	require.True(t, IsFrozen(view, shareID, to))

	view.rules = amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeatureMPTokensV2,
	})
	putTestIssuance(t, view, underlyingID, 0, nil)
	require.Equal(t, ter.TesSUCCESS, CanTrade(view, shareID))
	require.Equal(t, ter.TesSUCCESS, CanTransfer(view, shareID, from, to))
}

func TestVaultShareReferenceInheritsIOUChecks(t *testing.T) {
	view := newMPTTestView()
	var vaultPseudo, issuer, from, to [20]byte
	issuer[19] = 0x10
	vaultPseudo[19] = 0x20
	from[19] = 0x30
	to[19] = 0x40
	shareID := keylet.MakeMPTID(1, vaultPseudo)
	issuerAddress := state.EncodeAccountIDSafe(issuer)
	vaultAddress := state.EncodeAccountIDSafe(vaultPseudo)

	lineKey := keylet.Line(vaultPseudo, issuer, "USD")
	referenceHex := strings.ToUpper(hex.EncodeToString(lineKey.Key[:]))
	putTestIssuance(t, view, shareID, entry.LsfMPTCanTrade|entry.LsfMPTCanTransfer, &referenceHex)
	lineRaw, err := state.SerializeRippleState(&state.RippleState{
		Balance:   state.NewIssuedAmountFromValue(0, state.MinExponent, "USD", issuerAddress),
		LowLimit:  state.NewIssuedAmountFromValue(0, state.MinExponent, "USD", issuerAddress),
		HighLimit: state.NewIssuedAmountFromValue(0, state.MinExponent, "USD", vaultAddress),
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(lineKey, lineRaw))
	putTestAccount(t, view, issuer, state.LsfGlobalFreeze, [32]byte{})

	require.Equal(t, ter.TesSUCCESS, CanTrade(view, shareID))
	require.Equal(t, ter.TerNO_RIPPLE, CanTransfer(view, shareID, from, to))
	require.True(t, IsFrozen(view, shareID, to))
}

func TestVaultShareAuthorizationInheritsUnderlyingMPT(t *testing.T) {
	view := newMPTTestView()
	var vaultPseudo, underlyingIssuer, holder, owner [20]byte
	vaultPseudo[19] = 0x20
	underlyingIssuer[19] = 0x10
	holder[19] = 0x30
	owner[19] = 0x40
	shareID := keylet.MakeMPTID(1, vaultPseudo)
	underlyingID := keylet.MakeMPTID(2, underlyingIssuer)
	vaultID := [32]byte{1, 2, 3}

	putTestAccount(t, view, vaultPseudo, 0, vaultID)
	putTestAccount(t, view, underlyingIssuer, 0, [32]byte{})
	putTestIssuance(t, view, shareID, 0, nil)
	putTestIssuance(t, view, underlyingID, entry.LsfMPTRequireAuth, nil)
	putTestVault(t, view, vaultID, owner, vaultPseudo, shareID, underlyingID)

	require.Equal(t, ter.TecNO_AUTH, RequireAuthAt(view, shareID, holder, false, 10))
	putTestHolding(t, view, underlyingID, holder, entry.LsfMPTAuthorized)
	require.Equal(t, ter.TesSUCCESS, RequireAuthAt(view, shareID, holder, false, 10))
}

func TestRequireAuthAtRejectsExpiredDomainCredential(t *testing.T) {
	view := newMPTTestView()
	var issuer, holder, credentialIssuer [20]byte
	issuer[19] = 0x10
	holder[19] = 0x20
	credentialIssuer[19] = 0x30
	id := keylet.MakeMPTID(1, issuer)
	domainID := [32]byte{4, 5, 6}
	domainHex := strings.ToUpper(hex.EncodeToString(domainID[:]))
	credentialType := []byte("KYC")

	putTestAccount(t, view, issuer, 0, [32]byte{})
	putTestIssuance(t, view, id, entry.LsfMPTRequireAuth, nil)
	issuanceRaw := view.data[keylet.MPTIssuance(id).Key]
	issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
	require.NoError(t, err)
	issuance.DomainID = &domainHex
	issuanceRaw, err = state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	view.data[keylet.MPTIssuance(id).Key] = issuanceRaw

	domainRaw, err := state.SerializePermissionedDomain(&state.PermissionedDomainData{
		Owner:    issuer,
		Sequence: 1,
		AcceptedCredentials: []state.PermissionedDomainCredential{{
			Issuer: credentialIssuer, CredentialType: credentialType,
		}},
	}, state.EncodeAccountIDSafe(issuer))
	require.NoError(t, err)
	view.data[keylet.PermissionedDomainByID(domainID).Key] = domainRaw

	credentialHex, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType":   "Credential",
		"Subject":           state.EncodeAccountIDSafe(holder),
		"Issuer":            state.EncodeAccountIDSafe(credentialIssuer),
		"CredentialType":    hex.EncodeToString(credentialType),
		"Expiration":        uint32(100),
		"Flags":             entry.LsfAccepted,
		"IssuerNode":        "0",
		"SubjectNode":       "0",
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	require.NoError(t, err)
	credentialRaw, err := hex.DecodeString(credentialHex)
	require.NoError(t, err)
	view.data[keylet.Credential(holder, credentialIssuer, credentialType).Key] = credentialRaw

	require.Equal(t, ter.TesSUCCESS, RequireAuthAt(view, id, holder, false, 99))
	require.Equal(t, ter.TesSUCCESS, RequireAuthAt(view, id, holder, false, 100))
	require.Equal(t, ter.TecEXPIRED, RequireAuthAt(view, id, holder, false, 101))
}

func TestDecodeIDRequiresExactHex(t *testing.T) {
	id := keylet.MakeMPTID(7, [20]byte{1, 2, 3, 4})
	encoded := EncodeID(id)

	for _, value := range []string{encoded, strings.ToLower(encoded)} {
		decoded, err := DecodeID(value)
		require.NoError(t, err)
		require.Equal(t, id, decoded)
	}

	for _, value := range []string{
		"",
		"0",
		encoded[:len(encoded)-1],
		encoded + "0",
		" " + encoded,
		encoded + " ",
		"\t" + encoded,
		"G" + encoded[1:],
	} {
		_, err := DecodeID(value)
		require.ErrorIs(t, err, ErrInvalidID, "value %q", value)
	}
}

func TestTransferRateMultiplicationRoundsToNearestEven(t *testing.T) {
	tests := []struct {
		name   string
		amount int64
		rate   uint32
		want   int64
	}{
		{name: "below half", amount: 1, rate: 1_250_000_000, want: 1},
		{name: "tie to even", amount: 2, rate: 1_250_000_000, want: 2},
		{name: "tie to odd", amount: 1, rate: 1_500_000_000, want: 2},
		{name: "above half", amount: 1, rate: 1_500_000_001, want: 2},
		{name: "negative below half", amount: -1, rate: 1_250_000_000, want: -1},
		{name: "negative tie to even", amount: -2, rate: 1_250_000_000, want: -2},
		{name: "negative tie to odd", amount: -1, rate: 1_500_000_000, want: -2},
		{name: "negative above half", amount: -1, rate: 1_500_000_001, want: -2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := MultiplyRate(test.amount, test.rate)
			require.True(t, ok)
			require.Equal(t, test.want, got)
		})
	}
}

func TestTransferRateDivisionRoundsToNearestEven(t *testing.T) {
	tests := []struct {
		name   string
		amount int64
		rate   uint32
		want   int64
	}{
		{name: "rippled transfer fee vector", amount: 90, rate: 1_100_000_000, want: 82},
		{name: "below half", amount: 3, rate: 1_200_000_001, want: 2},
		{name: "tie to even", amount: 3, rate: 1_200_000_000, want: 2},
		{name: "tie to odd", amount: 9, rate: 1_200_000_000, want: 8},
		{name: "above half", amount: 3, rate: 1_199_999_999, want: 3},
		{name: "negative below half", amount: -3, rate: 1_200_000_001, want: -2},
		{name: "negative tie to even", amount: -3, rate: 1_200_000_000, want: -2},
		{name: "negative tie to odd", amount: -9, rate: 1_200_000_000, want: -8},
		{name: "negative above half", amount: -3, rate: 1_199_999_999, want: -3},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := DivideRate(test.amount, test.rate)
			require.True(t, ok)
			require.Equal(t, test.want, got)
		})
	}
}

func TestTransferRateScalingReportsOverflow(t *testing.T) {
	const rate = uint32(1_250_000_000)

	value, ok := MultiplyRate(math.MaxInt64, RateOne)
	require.True(t, ok)
	require.Equal(t, int64(math.MaxInt64), value)
	value, ok = MultiplyRate(math.MinInt64, RateOne)
	require.True(t, ok)
	require.Equal(t, int64(math.MinInt64), value)

	value, ok = MultiplyRate(math.MaxInt64, rate)
	require.False(t, ok)
	require.Zero(t, value)
	value, ok = MultiplyRate(math.MinInt64, rate)
	require.False(t, ok)
	require.Zero(t, value)

	value, ok = DivideRate(math.MaxInt64, rate)
	require.True(t, ok)
	require.Equal(t, int64(7_378_697_629_483_820_646), value)
	value, ok = DivideRate(math.MinInt64, rate)
	require.True(t, ok)
	require.Equal(t, int64(-7_378_697_629_483_820_646), value)

	value, ok = DivideRate(1, 0)
	require.False(t, ok)
	require.Zero(t, value)
}

func TestSendTransferRateOverflowReturnsTefException(t *testing.T) {
	view := newMPTTestView()
	var issuer, sender, receiver [20]byte
	issuer[19] = 1
	sender[19] = 2
	receiver[19] = 3
	id := keylet.MakeMPTID(1, issuer)
	maximum := uint64(math.MaxInt64)
	raw, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer:        issuer,
		Sequence:      1,
		TransferFee:   25_000,
		MaximumAmount: &maximum,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.MPTIssuance(id), raw))

	gross, result := Send(view, id, sender, receiver, math.MaxInt64, false, false)
	require.Equal(t, ter.TefEXCEPTION, result)
	require.Zero(t, gross)
}

func putTestAccount(t *testing.T, view *mptTestView, account [20]byte, flags uint32, vaultID [32]byte) {
	t.Helper()
	raw, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  state.EncodeAccountIDSafe(account),
		Balance:  1_000_000_000,
		Sequence: 1,
		Flags:    flags,
		VaultID:  vaultID,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(account), raw))
}

func putTestIssuance(t *testing.T, view *mptTestView, id [24]byte, flags uint32, reference *string) {
	t.Helper()
	raw, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer:           Issuer(id),
		Sequence:         1,
		Flags:            flags,
		ReferenceHolding: reference,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.MPTIssuance(id), raw))
}

func putTestHolding(t *testing.T, view *mptTestView, id [24]byte, account [20]byte, flags uint32) {
	t.Helper()
	raw, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           account,
		MPTokenIssuanceID: id,
		Flags:             flags,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.MPTokenByID(id, account), raw))
}

func putTestVault(t *testing.T, view *mptTestView, vaultID [32]byte, owner, pseudo [20]byte, shareID, assetID [24]byte) {
	t.Helper()
	hexRaw, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType":   "Vault",
		"Flags":             uint32(0),
		"Sequence":          uint32(1),
		"OwnerNode":         "0",
		"Owner":             state.EncodeAccountIDSafe(owner),
		"Account":           state.EncodeAccountIDSafe(pseudo),
		"Asset":             map[string]any{"mpt_issuance_id": EncodeID(assetID)},
		"ShareMPTID":        EncodeID(shareID),
		"WithdrawalPolicy":  uint8(0),
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	require.NoError(t, err)
	raw, err := hex.DecodeString(hexRaw)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.VaultByID(vaultID), raw))
}
