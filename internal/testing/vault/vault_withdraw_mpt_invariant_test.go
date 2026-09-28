package vault_test

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	credentialtest "github.com/LeJamon/go-xrpl/internal/testing/credential"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	permissioneddomaintest "github.com/LeJamon/go-xrpl/internal/testing/permissioneddomain"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type vaultWithdrawMPTFixture struct {
	env       *jtx.TestEnv
	depositor *jtx.Account
	vaultAcct [20]byte
	vaultKey  keylet.Keylet
	vaultID   string
	assetID   [24]byte
	shareID   [24]byte
	assetHold keylet.Keylet
	vaultHold keylet.Keylet
	shareHold keylet.Keylet
}

func newVaultWithdrawMPTFixture(
	t *testing.T,
	permissioned, cleanup, lending, lp11 bool,
) *vaultWithdrawMPTFixture {
	t.Helper()
	env := newVaultEnv(t)
	env.DisableFeature("MPTokensV2")
	if cleanup {
		env.EnableFeature("fixCleanup3_4_0")
	} else {
		env.DisableFeature("fixCleanup3_4_0")
	}
	if lending {
		env.EnableFeature("LendingProtocol")
	} else {
		env.DisableFeature("LendingProtocol")
	}
	if lp11 {
		env.EnableFeature("LendingProtocolV1_1")
	} else {
		env.DisableFeature("LendingProtocolV1_1")
	}
	env.Close()

	issuer := jtx.NewAccount("vault-withdraw-mpt-issuer")
	owner := jtx.NewAccount("vault-withdraw-mpt-owner")
	depositor := jtx.NewAccount("vault-withdraw-mpt-depositor")
	domainOwner := jtx.NewAccount("vault-withdraw-mpt-domain-owner")
	credentialIssuer := jtx.NewAccount("vault-withdraw-mpt-credential-issuer")
	env.Fund(owner)
	if permissioned {
		env.Fund(domainOwner, credentialIssuer)
	}

	token := mpttest.NewMPTTester(t, env, issuer, mpttest.MPTInit{
		Holders: []*jtx.Account{depositor},
	})
	token.Create(mpttest.CreateOpts{Flags: mpttest.TfMPTCanTransfer})
	token.Authorize(mpttest.AuthorizeOpts{Account: depositor})
	token.Pay(issuer, depositor, 1_000)

	asset := tx.Asset{MPTIssuanceID: token.IssuanceID()}
	vaultSequence := env.Seq(owner)
	if permissioned {
		const credentialType = "vault-withdraw-mpt"
		credentialTypeHex := strings.ToUpper(hex.EncodeToString([]byte(credentialType)))
		domainSequence := env.Seq(domainOwner)
		jtx.RequireTxSuccess(t, env.Submit(
			permissioneddomaintest.DomainSet(domainOwner).
				Credential(credentialIssuer, credentialTypeHex).
				Build(),
		))
		domainKey := keylet.PermissionedDomain(domainOwner.ID, domainSequence)
		domainID := strings.ToUpper(hex.EncodeToString(domainKey.Key[:]))

		create := vault.NewVaultCreate(owner.Address, asset)
		privateFlag := vault.VaultFlagPrivate
		create.Common.Flags = &privateFlag
		create.Common.Fee = createFee
		jtx.RequireTxSuccess(t, env.Submit(create))
		id := vaultID(owner, vaultSequence)
		set := vault.NewVaultSet(owner.Address, id)
		set.DomainID = domainID
		jtx.RequireTxSuccess(t, env.Submit(set))

		jtx.RequireTxSuccess(t, env.Submit(
			credentialtest.CredentialCreateText(credentialIssuer, depositor, credentialType).Build(),
		))
		jtx.RequireTxSuccess(t, env.Submit(
			credentialtest.CredentialAcceptText(depositor, credentialIssuer, credentialType).Build(),
		))
	} else {
		create := vault.NewVaultCreate(owner.Address, asset)
		create.Common.Fee = createFee
		jtx.RequireTxSuccess(t, env.Submit(create))
	}

	id := vaultID(owner, vaultSequence)
	deposit := vault.NewVaultDeposit(
		depositor.Address,
		id,
		state.NewMPTAmountWithIssuanceID(1_000, "", token.IssuanceID()),
	)
	jtx.RequireTxSuccess(t, env.Submit(deposit))

	// The final withdrawal must recreate this now-empty underlying holding while
	// deleting the holder's last vault-share holding.
	token.Authorize(mpttest.AuthorizeOpts{
		Account: depositor,
		Flags:   mpttest.TfMPTUnauthorize,
	})

	assetID, err := mptutil.DecodeID(token.IssuanceID())
	require.NoError(t, err)
	vaultKey := keylet.Vault(owner.AccountID(), vaultSequence)
	info, err := vault.ReadVaultInfo(env.Ledger(), vaultKey)
	require.NoError(t, err)
	require.NotNil(t, info)
	requireVaultWithdrawAmendments(t, env, cleanup, lending, lp11)
	shareID := info.ShareMPTID
	return &vaultWithdrawMPTFixture{
		env:       env,
		depositor: depositor,
		vaultKey:  vaultKey,
		vaultAcct: info.Account,
		vaultID:   id,
		assetID:   assetID,
		shareID:   shareID,
		assetHold: keylet.MPTokenByID(assetID, depositor.AccountID()),
		vaultHold: keylet.MPTokenByID(assetID, info.Account),
		shareHold: keylet.MPTokenByID(shareID, depositor.AccountID()),
	}
}

func metadataKinds(meta *tx.Metadata) []string {
	result := make([]string, 0, len(meta.AffectedNodes))
	for _, node := range meta.AffectedNodes {
		result = append(result, node.NodeType+":"+node.LedgerEntryType)
	}
	sort.Strings(result)
	return result
}

func metadataNode(t *testing.T, meta *tx.Metadata, nodeType, entryType, index string) *tx.AffectedNode {
	t.Helper()
	for i := range meta.AffectedNodes {
		node := &meta.AffectedNodes[i]
		if node.NodeType == nodeType && node.LedgerEntryType == entryType && node.LedgerIndex == index {
			return node
		}
	}
	return nil
}

func requireVaultWithdrawAmendments(t *testing.T, env *jtx.TestEnv, cleanup, lending, lp11 bool) {
	t.Helper()
	rules := env.Rules()
	require.Equal(t, lending, rules.Enabled(amendment.FeatureLendingProtocol))
	require.Equal(t, cleanup, rules.Enabled(amendment.FeatureFixCleanup3_4_0))
	require.Equal(t, lp11, rules.Enabled(amendment.FeatureLendingProtocolV1_1))
	require.False(t, rules.Enabled(amendment.FeatureMPTokensV2))
}

func requireAccountMetadata(t *testing.T, result jtx.TxResult, beforeBalance uint64, beforeSequence, beforeOwnerCount uint32, env *jtx.TestEnv, account *jtx.Account) {
	t.Helper()
	require.NotNil(t, result.Metadata)
	require.Equal(t, result.Code, result.Metadata.TransactionResult.String())
	var accountNode *tx.AffectedNode
	for i := range result.Metadata.AffectedNodes {
		node := &result.Metadata.AffectedNodes[i]
		if node.NodeType == "ModifiedNode" && node.LedgerEntryType == "AccountRoot" {
			accountNode = node
		}
	}
	require.NotNil(t, accountNode)
	require.Equal(t, account.Address, accountNode.FinalFields["Account"])
	require.Equal(t, beforeBalance, env.Balance(account)+env.BaseFee())
	require.Equal(t, beforeSequence+1, env.Seq(account))
	require.Equal(t, beforeOwnerCount, env.OwnerCount(account))
	require.Equal(t, fmt.Sprintf("%d", beforeBalance), accountNode.PreviousFields["Balance"])
	require.Equal(t, beforeSequence, accountNode.PreviousFields["Sequence"])
	require.Equal(t, fmt.Sprintf("%d", env.Balance(account)), accountNode.FinalFields["Balance"])
	require.Equal(t, beforeSequence+1, accountNode.FinalFields["Sequence"])
	require.Equal(t, beforeOwnerCount, accountNode.FinalFields["OwnerCount"])
}

func TestVaultWithdrawMPTHoldingCreateAndShareDelete(t *testing.T) {
	for _, permissioned := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			for _, lending := range []bool{false, true} {
				for _, lp11 := range []bool{false, true} {
					name := fmt.Sprintf("permissioned=%t/cleanup=%t/lending=%t/lp11=%t", permissioned, cleanup, lending, lp11)
					t.Run(name, func(t *testing.T) {
						f := newVaultWithdrawMPTFixture(t, permissioned, cleanup, lending, lp11)
						beforeBalance := f.env.Balance(f.depositor)
						beforeSequence := f.env.Seq(f.depositor)
						beforeOwnerCount := f.env.OwnerCount(f.depositor)
						beforeVault, err := f.env.LedgerEntry(f.vaultKey)
						require.NoError(t, err)
						beforeShare, err := f.env.LedgerEntry(f.shareHold)
						require.NoError(t, err)
						require.NotNil(t, beforeShare)
						beforeOwnerDir, err := f.env.LedgerEntry(keylet.OwnerDir(f.depositor.ID))
						require.NoError(t, err)
						require.NotNil(t, beforeOwnerDir)
						beforeVaultHolding, err := f.env.LedgerEntry(f.vaultHold)
						require.NoError(t, err)
						require.NotNil(t, beforeVaultHolding)
						beforeIssuance, err := f.env.LedgerEntry(keylet.MPTIssuance(f.shareID))
						require.NoError(t, err)
						require.NotNil(t, beforeIssuance)
						beforeAssetIssuance, err := f.env.LedgerEntry(keylet.MPTIssuance(f.assetID))
						require.NoError(t, err)
						require.NotNil(t, beforeAssetIssuance)
						require.False(t, f.env.LedgerEntryExists(f.assetHold))

						withdraw := vault.NewVaultWithdraw(
							f.depositor.Address,
							f.vaultID,
							state.NewMPTAmountWithIssuanceID(1_000, "", mptutil.EncodeID(f.assetID)),
						)
						requireVaultWithdrawAmendments(t, f.env, cleanup, lending, lp11)
						result := f.env.Submit(withdraw)
						wantSuccess := cleanup || !lending
						if wantSuccess {
							jtx.RequireTxSuccess(t, result)
							require.True(t, result.Applied)
							require.Equal(t, f.env.BaseFee(), result.Fee)
							requireAccountMetadata(t, result, beforeBalance, beforeSequence, beforeOwnerCount, f.env, f.depositor)
							require.Equal(t, []string{
								"CreatedNode:MPToken",
								"DeletedNode:MPToken",
								"ModifiedNode:AccountRoot",
								"ModifiedNode:DirectoryNode",
								"ModifiedNode:MPToken",
								"ModifiedNode:MPTokenIssuance",
								"ModifiedNode:Vault",
							}, metadataKinds(result.Metadata))

							created := metadataNode(t, result.Metadata, "CreatedNode", "MPToken", keyIndex(f.assetHold))
							require.NotNil(t, created)
							require.Equal(t, map[string]any{
								"Account":           f.depositor.Address,
								"MPTAmount":         "1000",
								"MPTokenIssuanceID": mptutil.EncodeID(f.assetID),
							}, created.NewFields)

							deleted := metadataNode(t, result.Metadata, "DeletedNode", "MPToken", keyIndex(f.shareHold))
							require.NotNil(t, deleted)
							require.Equal(t, f.depositor.Address, deleted.FinalFields["Account"])
							require.Equal(t, mptutil.EncodeID(f.shareID), deleted.FinalFields["MPTokenIssuanceID"])
							require.Equal(t, "1000", deleted.PreviousFields["MPTAmount"])

							pseudoHold := keylet.MPTokenByID(f.assetID, f.vaultAcct)
							modifiedHold := metadataNode(t, result.Metadata, "ModifiedNode", "MPToken", keyIndex(pseudoHold))
							require.NotNil(t, modifiedHold)
							require.Equal(t, "1000", modifiedHold.PreviousFields["MPTAmount"])
							require.NotContains(t, modifiedHold.FinalFields, "MPTAmount")

							shareIssuance := metadataNode(t, result.Metadata, "ModifiedNode", "MPTokenIssuance", keyIndex(keylet.MPTIssuance(f.shareID)))
							require.NotNil(t, shareIssuance)
							require.Equal(t, "1000", shareIssuance.PreviousFields["OutstandingAmount"])
							require.Equal(t, "0", shareIssuance.FinalFields["OutstandingAmount"])

							vaultNode := metadataNode(t, result.Metadata, "ModifiedNode", "Vault", keyIndex(f.vaultKey))
							require.NotNil(t, vaultNode)
							require.Equal(t, "1000", vaultNode.PreviousFields["AssetsTotal"])
							require.Equal(t, "1000", vaultNode.PreviousFields["AssetsAvailable"])
							require.NotContains(t, vaultNode.FinalFields, "AssetsTotal")
							require.NotContains(t, vaultNode.FinalFields, "AssetsAvailable")

							var directoryNode *tx.AffectedNode
							for i := range result.Metadata.AffectedNodes {
								node := &result.Metadata.AffectedNodes[i]
								if node.NodeType == "ModifiedNode" && node.LedgerEntryType == "DirectoryNode" {
									directoryNode = node
								}
							}
							require.NotNil(t, directoryNode)
							require.Equal(t, f.depositor.Address, directoryNode.FinalFields["Owner"])
							require.Equal(t, directoryNode.LedgerIndex, directoryNode.FinalFields["RootIndex"])
							require.EqualValues(t, 0, directoryNode.FinalFields["Flags"])

							require.Equal(t, beforeBalance-f.env.BaseFee(), f.env.Balance(f.depositor))
							require.Equal(t, beforeSequence+1, f.env.Seq(f.depositor))
							require.Equal(t, beforeOwnerCount, f.env.OwnerCount(f.depositor))
							require.True(t, f.env.LedgerEntryExists(f.assetHold))
							assetRaw, err := f.env.LedgerEntry(f.assetHold)
							require.NoError(t, err)
							assetToken, err := state.ParseMPToken(assetRaw)
							require.NoError(t, err)
							require.Equal(t, f.assetID, assetToken.MPTokenIssuanceID)
							require.Equal(t, uint64(1_000), assetToken.MPTAmount)
							vaultHoldingRaw, err := f.env.LedgerEntry(f.vaultHold)
							require.NoError(t, err)
							vaultHolding, err := state.ParseMPToken(vaultHoldingRaw)
							require.NoError(t, err)
							require.Zero(t, vaultHolding.MPTAmount)
							require.False(t, f.env.LedgerEntryExists(f.shareHold))
							issuanceRaw, err := f.env.LedgerEntry(keylet.MPTIssuance(f.shareID))
							require.NoError(t, err)
							issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
							require.NoError(t, err)
							require.Zero(t, issuance.OutstandingAmount)
							require.Equal(t, beforeAssetIssuance, mustLedgerEntry(t, f.env, keylet.MPTIssuance(f.assetID)))
							updatedVault, err := vault.ReadVaultLending(f.env.Ledger(), f.vaultKey)
							require.NoError(t, err)
							require.Empty(t, updatedVault.AssetsTotal)
							require.Empty(t, updatedVault.AssetsAvailable)
						} else {
							jtx.RequireTxClaimed(t, result, jtx.TecINVARIANT_FAILED)
							require.True(t, result.Applied)
							require.Equal(t, f.env.BaseFee(), result.Fee)
							requireAccountMetadata(t, result, beforeBalance, beforeSequence, beforeOwnerCount, f.env, f.depositor)
							require.Equal(t, beforeBalance-f.env.BaseFee(), f.env.Balance(f.depositor))
							require.Equal(t, beforeSequence+1, f.env.Seq(f.depositor))
							require.Equal(t, beforeOwnerCount, f.env.OwnerCount(f.depositor))
							require.Equal(t, beforeVault, mustLedgerEntry(t, f.env, f.vaultKey))
							require.Equal(t, beforeShare, mustLedgerEntry(t, f.env, f.shareHold))
							require.Equal(t, beforeOwnerDir, mustLedgerEntry(t, f.env, keylet.OwnerDir(f.depositor.ID)))
							require.Equal(t, beforeVaultHolding, mustLedgerEntry(t, f.env, f.vaultHold))
							require.Equal(t, beforeIssuance, mustLedgerEntry(t, f.env, keylet.MPTIssuance(f.shareID)))
							require.Equal(t, beforeAssetIssuance, mustLedgerEntry(t, f.env, keylet.MPTIssuance(f.assetID)))
							require.False(t, f.env.LedgerEntryExists(f.assetHold))
							require.Equal(t, []string{"ModifiedNode:AccountRoot"}, metadataKinds(result.Metadata))
						}
					})
				}
			}
		}
	}
}

func mustLedgerEntry(t *testing.T, env *jtx.TestEnv, key keylet.Keylet) []byte {
	t.Helper()
	data, err := env.LedgerEntry(key)
	require.NoError(t, err)
	return data
}

func keyIndex(key keylet.Keylet) string {
	return strings.ToUpper(hex.EncodeToString(key.Key[:]))
}
