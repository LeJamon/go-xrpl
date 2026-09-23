package escrow_test

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	credentialtest "github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	permissioneddomaintest "github.com/LeJamon/go-xrpl/internal/testing/permissioneddomain"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

const domainCredentialType = "mpt-escrow-access"

type mptDomainEscrowFixture struct {
	env              *jtx.TestEnv
	issuer           *jtx.Account
	sender           *jtx.Account
	destination      *jtx.Account
	domainOwner      *jtx.Account
	credentialIssuer *jtx.Account
	token            *mpt.MPTTester
	domainID         string
}

func newMPTDomainEscrowFixture(t *testing.T, destinationHasToken bool) *mptDomainEscrowFixture {
	t.Helper()

	env := jtx.NewTestEnv(t)
	env.EnableFeature("TokenEscrow")

	f := &mptDomainEscrowFixture{
		env:              env,
		issuer:           jtx.NewAccount("mpt-issuer"),
		sender:           jtx.NewAccount("escrow-sender"),
		destination:      jtx.NewAccount("escrow-destination"),
		domainOwner:      jtx.NewAccount("domain-owner"),
		credentialIssuer: jtx.NewAccount("credential-issuer"),
	}
	env.Fund(f.domainOwner, f.credentialIssuer)
	env.Close()

	domainSequence := env.Seq(f.domainOwner)
	domainCredentialTypeHex := hex.EncodeToString([]byte(domainCredentialType))
	jtx.RequireTxSuccess(t, env.Submit(
		permissioneddomaintest.DomainSet(f.domainOwner).
			Credential(f.credentialIssuer, domainCredentialTypeHex).
			Build(),
	))
	env.Close()

	domainKey := keylet.PermissionedDomain(f.domainOwner.ID, domainSequence)
	f.domainID = hex.EncodeToString(domainKey.Key[:])

	f.token = mpt.NewMPTTester(t, env, f.issuer, mpt.MPTInit{
		Holders: []*jtx.Account{f.sender, f.destination},
	})
	f.token.Create(mpt.CreateOpts{
		Flags: mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer | mpt.TfMPTRequireAuth,
	})
	f.token.Authorize(mpt.AuthorizeOpts{Account: f.sender})
	f.token.Authorize(mpt.AuthorizeOpts{Account: f.issuer, Holder: f.sender})
	if destinationHasToken {
		f.token.Authorize(mpt.AuthorizeOpts{Account: f.destination})
		f.token.Authorize(mpt.AuthorizeOpts{Account: f.issuer, Holder: f.destination})
	}
	f.token.Pay(f.issuer, f.sender, 100)
	env.Close()

	bindMPTIssuanceToDomain(t, env, f.token.IssuanceID(), f.domainID)
	return f
}

func bindMPTIssuanceToDomain(t *testing.T, env *jtx.TestEnv, issuanceID, domainID string) {
	t.Helper()

	decodedID, err := hex.DecodeString(issuanceID)
	require.NoError(t, err)
	require.Len(t, decodedID, 24)
	var mptID [24]byte
	copy(mptID[:], decodedID)

	issuanceKey := keylet.MPTIssuance(mptID)
	raw, err := env.LedgerEntry(issuanceKey)
	require.NoError(t, err)
	require.NotNil(t, raw)
	issuance, err := state.ParseMPTokenIssuance(raw)
	require.NoError(t, err)
	issuance.DomainID = &domainID
	raw, err = state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(issuanceKey, raw))
}

func (f *mptDomainEscrowFixture) addCredential(t *testing.T, subject *jtx.Account, expiration *uint32, accept bool) keylet.Keylet {
	t.Helper()

	create := credentialtest.CredentialCreateText(f.credentialIssuer, subject, domainCredentialType)
	if expiration != nil {
		create.Expiration(*expiration)
	}
	jtx.RequireTxSuccess(t, f.env.Submit(create.Build()))
	f.env.Close()

	key := jtx.CredentialKeylet(subject, f.credentialIssuer, domainCredentialType)
	if accept {
		jtx.RequireTxSuccess(t, f.env.Submit(
			credentialtest.CredentialAcceptText(subject, f.credentialIssuer, domainCredentialType).Build(),
		))
		f.env.Close()
	}
	return key
}

func (f *mptDomainEscrowFixture) createEscrow(cancelAfter *uint32) (uint32, jtx.TxResult) {
	sequence := f.env.Seq(f.sender)
	builder := escrow.EscrowCreate(f.sender, f.destination, 0).
		MPTAmount(f.token.MPTAmount(10)).
		Condition(escrow.TestCondition1()).
		FinishTime(f.env.Now().Add(time.Hour)).
		Fee(f.env.BaseFee() * 150)
	if cancelAfter != nil {
		builder.CancelAfter(*cancelAfter)
	}
	return sequence, f.env.Submit(builder.Build())
}

func TestMPTEscrow_DomainAuthorization(t *testing.T) {
	t.Run("CreateRequiresMissingDestinationCredential", func(t *testing.T) {
		f := newMPTDomainEscrowFixture(t, false)

		sequence, result := f.createEscrow(nil)
		jtx.RequireTxClaimed(t, result, jtx.TecNO_AUTH)
		f.env.Close()

		jtx.RequireLedgerEntryNotExists(t, f.env, keylet.Escrow(f.sender.ID, sequence))
		f.token.RequireMPTokenAmount(f.sender, 100)
	})

	t.Run("CreateAllowsAcceptedDomainCredentialWithoutDestinationHolding", func(t *testing.T) {
		f := newMPTDomainEscrowFixture(t, false)
		f.addCredential(t, f.destination, nil, true)

		sequence, result := f.createEscrow(nil)
		jtx.RequireTxSuccess(t, result)
		f.env.Close()

		jtx.RequireLedgerEntryExists(t, f.env, keylet.Escrow(f.sender.ID, sequence))
		jtx.RequireLedgerEntryNotExists(t, f.env, keylet.MPTokenByID(mptIDFromHex(t, f.token.IssuanceID()), f.destination.ID))
	})

	t.Run("CreateRejectsUnacceptedDomainCredential", func(t *testing.T) {
		f := newMPTDomainEscrowFixture(t, false)
		f.addCredential(t, f.destination, nil, false)

		sequence, result := f.createEscrow(nil)
		jtx.RequireTxClaimed(t, result, jtx.TecNO_AUTH)
		f.env.Close()

		jtx.RequireLedgerEntryNotExists(t, f.env, keylet.Escrow(f.sender.ID, sequence))
		f.token.RequireMPTokenAmount(f.sender, 100)
	})

	t.Run("CreateRejectsExpiredDomainCredential", func(t *testing.T) {
		f := newMPTDomainEscrowFixture(t, false)
		expiration := f.env.NowRipple() + 10
		f.addCredential(t, f.destination, &expiration, true)
		f.env.CloseToParentCloseTime(expiration + 1)

		sequence, result := f.createEscrow(nil)
		jtx.RequireTxClaimed(t, result, jtx.TecEXPIRED)
		f.env.Close()

		jtx.RequireLedgerEntryNotExists(t, f.env, keylet.Escrow(f.sender.ID, sequence))
		f.token.RequireMPTokenAmount(f.sender, 100)
	})

	t.Run("CreateExplicitAuthorizationOverridesInvalidDomain", func(t *testing.T) {
		f := newMPTDomainEscrowFixture(t, true)

		sequence, result := f.createEscrow(nil)
		jtx.RequireTxSuccess(t, result)
		f.env.Close()

		jtx.RequireLedgerEntryExists(t, f.env, keylet.Escrow(f.sender.ID, sequence))
		jtx.RequireLedgerEntryExists(t, f.env, keylet.MPTokenByID(mptIDFromHex(t, f.token.IssuanceID()), f.destination.ID))
	})
}

func TestMPTEscrow_FinishRejectsExpiredDomainCredentialAtParentCloseTime(t *testing.T) {
	f := newMPTDomainEscrowFixture(t, false)
	expiration := f.env.NowRipple() + 100
	credentialKey := f.addCredential(t, f.destination, &expiration, true)

	sequence, result := f.createEscrow(nil)
	jtx.RequireTxSuccess(t, result)
	f.env.Close()
	escrowKey := keylet.Escrow(f.sender.ID, sequence)
	jtx.RequireLedgerEntryExists(t, f.env, escrowKey)
	f.token.RequireMPTokenAmount(f.sender, 90)
	require.Equal(t, uint64(10), f.token.HolderLockedAmount(f.sender))

	f.env.CloseToParentCloseTime(expiration + 1)
	result = f.env.Submit(
		escrow.EscrowFinish(f.destination, f.sender, sequence).
			Condition(escrow.TestCondition1()).
			Fulfillment(escrow.TestFulfillment1()).
			Fee(f.env.BaseFee() * 150).
			Build(),
	)
	jtx.RequireTxClaimed(t, result, jtx.TecEXPIRED)

	jtx.RequireLedgerEntryExists(t, f.env, escrowKey)
	jtx.RequireLedgerEntryExists(t, f.env, credentialKey)
	f.token.RequireMPTokenAmount(f.sender, 90)
	require.Equal(t, uint64(10), f.token.HolderLockedAmount(f.sender))
}

func TestMPTEscrow_CancelUsesDomainAuthorizationAfterTokenUnauthorization(t *testing.T) {
	f := newMPTDomainEscrowFixture(t, false)
	f.addCredential(t, f.sender, nil, true)
	f.addCredential(t, f.destination, nil, true)
	cancelAfter := f.env.NowRipple() + 2*60*60

	sequence, result := f.createEscrow(&cancelAfter)
	jtx.RequireTxSuccess(t, result)
	f.env.Close()
	escrowKey := keylet.Escrow(f.sender.ID, sequence)

	f.token.Authorize(mpt.AuthorizeOpts{
		Account: f.issuer,
		Holder:  f.sender,
		Flags:   mpt.TfMPTUnauthorize,
	})
	f.env.Close()
	f.env.CloseToParentCloseTime(cancelAfter + 1)

	result = f.env.Submit(
		escrow.EscrowCancel(f.destination, f.sender, sequence).
			Fee(f.env.BaseFee() * 150).
			Build(),
	)
	jtx.RequireTxSuccess(t, result)
	f.env.Close()
	jtx.RequireLedgerEntryNotExists(t, f.env, escrowKey)
}

func mptIDFromHex(t *testing.T, value string) [24]byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	require.NoError(t, err)
	require.Len(t, decoded, 24)
	var id [24]byte
	copy(id[:], decoded)
	return id
}
