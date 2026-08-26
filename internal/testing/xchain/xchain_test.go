package xchain_test

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/signerlist"
	xchaintx "github.com/LeJamon/go-xrpl/internal/tx/xchain"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

const dropsPerXRP = int64(1_000_000)

func nativeBridge(locking, issuing *jtx.Account) xchaintx.XChainBridge {
	return xchaintx.XChainBridge{
		LockingChainDoor:  locking.Address,
		LockingChainIssue: txcore.Asset{Currency: "XRP"},
		IssuingChainDoor:  issuing.Address,
		IssuingChainIssue: txcore.Asset{Currency: "XRP"},
	}
}

func bridgeJSON(bridge xchaintx.XChainBridge) map[string]any {
	return map[string]any{
		"LockingChainDoor":  bridge.LockingChainDoor,
		"LockingChainIssue": assetJSON(bridge.LockingChainIssue),
		"IssuingChainDoor":  bridge.IssuingChainDoor,
		"IssuingChainIssue": assetJSON(bridge.IssuingChainIssue),
	}
}

func assetJSON(asset txcore.Asset) map[string]any {
	result := map[string]any{"currency": asset.Currency}
	if asset.Issuer != "" {
		result["issuer"] = asset.Issuer
	}
	return result
}

func amountJSON(t *testing.T, amount txcore.Amount) any {
	t.Helper()
	encoded, err := json.Marshal(amount)
	require.NoError(t, err)
	var result any
	require.NoError(t, json.Unmarshal(encoded, &result))
	return result
}

func claimBridgeKey(t *testing.T, bridge xchaintx.XChainBridge) keylet.XChainBridge {
	t.Helper()
	currency, err := keylet.ParseCurrency("XRP")
	require.NoError(t, err)
	locking := jtx.NewAccountWithAddress("locking-key", bridge.LockingChainDoor)
	issuing := jtx.NewAccountWithAddress("issuing-key", bridge.IssuingChainDoor)
	return keylet.XChainBridge{
		LockingDoor: locking.ID, LockingCurrency: currency,
		IssuingDoor: issuing.ID, IssuingCurrency: currency,
	}
}

func signClaimAttestation(t *testing.T, att *xchaintx.XChainAddClaimAttestation, witness *jtx.Account) {
	t.Helper()
	fields := map[string]any{
		"XChainClaimID":            strconv.FormatUint(att.XChainClaimID, 16),
		"Amount":                   amountJSON(t, att.Amount),
		"OtherChainSource":         att.OtherChainSource,
		"AttestationRewardAccount": att.AttestationRewardAccount,
		"WasLockingChainSend":      uint8(0),
		"XChainBridge":             bridgeJSON(att.XChainBridge),
	}
	if att.WasLockingChainSend != 0 {
		fields["WasLockingChainSend"] = uint8(1)
	}
	if att.Destination != "" {
		fields["Destination"] = att.Destination
	}
	message, err := binarycodec.EncodeBytes(fields)
	require.NoError(t, err)
	signature, err := (secp256k1.Algorithm{}).SignBytes(message, witness.PrivateKey)
	require.NoError(t, err)
	att.PublicKey = witness.PublicKeyHex()
	att.Signature = strings.ToUpper(hex.EncodeToString(signature))
}

func signCreateAttestation(t *testing.T, att *xchaintx.XChainAddAccountCreateAttestation, witness *jtx.Account) {
	t.Helper()
	locking := uint8(0)
	if att.WasLockingChainSend != 0 {
		locking = 1
	}
	message, err := binarycodec.EncodeBytes(map[string]any{
		"XChainAccountCreateCount": strconv.FormatUint(att.XChainAccountCreateCount, 16),
		"Amount":                   amountJSON(t, att.Amount),
		"SignatureReward":          amountJSON(t, att.SignatureReward),
		"Destination":              att.Destination,
		"OtherChainSource":         att.OtherChainSource,
		"AttestationRewardAccount": att.AttestationRewardAccount,
		"WasLockingChainSend":      locking,
		"XChainBridge":             bridgeJSON(att.XChainBridge),
	})
	require.NoError(t, err)
	signature, err := (secp256k1.Algorithm{}).SignBytes(message, witness.PrivateKey)
	require.NoError(t, err)
	att.PublicKey = witness.PublicKeyHex()
	att.Signature = strings.ToUpper(hex.EncodeToString(signature))
}

func newClaimAttestation(
	t *testing.T,
	submitter, witness *jtx.Account,
	bridge xchaintx.XChainBridge,
	claimID uint64,
	source, destination string,
	amount txcore.Amount,
) *xchaintx.XChainAddClaimAttestation {
	t.Helper()
	att := xchaintx.NewXChainAddClaimAttestation(submitter.Address, bridge, claimID)
	att.OtherChainSource = source
	att.Amount = amount
	att.AttestationRewardAccount = witness.Address
	att.AttestationSignerAccount = witness.Address
	att.Destination = destination
	att.WasLockingChainSend = 0
	signClaimAttestation(t, att, witness)
	return att
}

func newCreateAttestation(
	t *testing.T,
	submitter, witness *jtx.Account,
	bridge xchaintx.XChainBridge,
	count uint64,
	source, destination string,
	amount, reward txcore.Amount,
) *xchaintx.XChainAddAccountCreateAttestation {
	t.Helper()
	att := xchaintx.NewXChainAddAccountCreateAttestation(submitter.Address, bridge)
	att.OtherChainSource = source
	att.Destination = destination
	att.Amount = amount
	att.SignatureReward = reward
	att.AttestationRewardAccount = witness.Address
	att.AttestationSignerAccount = witness.Address
	att.WasLockingChainSend = 0
	att.XChainAccountCreateCount = count
	signCreateAttestation(t, att, witness)
	return att
}

func TestXChainNativeLifecycle(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("XChainBridge")
	env.Close()

	locking := jtx.NewAccount("xchain-locking-door")
	issuing := env.MasterAccount()
	owner := jtx.NewAccount("xchain-claim-owner")
	witness := jtx.NewAccount("xchain-witness")
	secondWitness := jtx.NewAccount("xchain-second-witness")
	destination := jtx.NewAccount("xchain-destination")
	newDestination := jtx.NewAccount("xchain-new-destination")
	secondNewDestination := jtx.NewAccount("xchain-second-new-destination")
	thirdNewDestination := jtx.NewAccount("xchain-third-new-destination")
	missingDestination := jtx.NewAccount("xchain-missing-destination")
	for _, account := range []*jtx.Account{locking, owner, witness, secondWitness, destination} {
		env.FundAmount(account, uint64(5_000*dropsPerXRP))
	}
	env.Close()

	bridge := nativeBridge(locking, issuing)
	reward := txcore.NewXRPAmount(0)
	minimum := txcore.NewXRPAmount(200 * dropsPerXRP)

	create := xchaintx.NewXChainCreateBridge(locking.Address, bridge, txcore.NewXRPAmount(1))
	create.MinAccountCreateAmount = &minimum
	jtx.RequireTxSuccess(t, env.Submit(create))

	bridgeKey := keylet.Bridge(locking.ID, [20]byte{})
	bridgeData, err := env.LedgerEntry(bridgeKey)
	require.NoError(t, err)
	var bridgeEntry entry.Bridge
	require.NoError(t, bridgeEntry.Decode(bridgeData))
	require.Equal(t, "1", bridgeEntry.SignatureReward)
	require.Equal(t, uint32(1), env.OwnerCount(locking))

	modify := xchaintx.NewXChainModifyBridge(locking.Address, bridge)
	modify.SignatureReward = &reward
	jtx.RequireTxSuccess(t, env.Submit(modify))
	bridgeData, err = env.LedgerEntry(bridgeKey)
	require.NoError(t, err)
	require.NoError(t, bridgeEntry.Decode(bridgeData))
	require.Equal(t, "0", bridgeEntry.SignatureReward)

	signers := signerlist.NewSignerListSet(locking.Address, 2)
	signers.AddSigner(witness.Address, 1)
	signers.AddSigner(secondWitness.Address, 1)
	jtx.RequireTxSuccess(t, env.Submit(signers))

	beforeDoor := env.Balance(locking)
	commit := xchaintx.NewXChainCommit(owner.Address, bridge, 0, txcore.NewXRPAmount(300*dropsPerXRP))
	jtx.RequireTxSuccess(t, env.Submit(commit))
	require.Equal(t, beforeDoor+uint64(300*dropsPerXRP), env.Balance(locking))

	accountCommit := xchaintx.NewXChainAccountCreateCommit(
		owner.Address, bridge, newDestination.Address, txcore.NewXRPAmount(210*dropsPerXRP), reward,
	)
	jtx.RequireTxSuccess(t, env.Submit(accountCommit))
	bridgeData, err = env.LedgerEntry(bridgeKey)
	require.NoError(t, err)
	require.NoError(t, bridgeEntry.Decode(bridgeData))
	require.Equal(t, "1", bridgeEntry.XChainAccountCreateCount)

	createAtt := newCreateAttestation(
		t, witness, witness, bridge, 1, owner.Address, newDestination.Address,
		txcore.NewXRPAmount(210*dropsPerXRP), reward,
	)
	jtx.RequireTxSuccess(t, env.Submit(createAtt))
	require.False(t, env.Exists(newDestination))
	createAtt = newCreateAttestation(
		t, secondWitness, secondWitness, bridge, 1, owner.Address, newDestination.Address,
		txcore.NewXRPAmount(210*dropsPerXRP), reward,
	)
	jtx.RequireTxSuccess(t, env.Submit(createAtt))
	jtx.RequireAccountExists(t, env, newDestination)
	bridgeData, err = env.LedgerEntry(bridgeKey)
	require.NoError(t, err)
	require.NoError(t, bridgeEntry.Decode(bridgeData))
	require.Equal(t, "1", bridgeEntry.XChainAccountClaimCount)

	for _, destination := range []*jtx.Account{secondNewDestination, thirdNewDestination} {
		commit := xchaintx.NewXChainAccountCreateCommit(
			owner.Address, bridge, destination.Address, txcore.NewXRPAmount(210*dropsPerXRP), reward,
		)
		jtx.RequireTxSuccess(t, env.Submit(commit))
	}
	thirdAtt := newCreateAttestation(
		t, witness, witness, bridge, 3, owner.Address, thirdNewDestination.Address,
		txcore.NewXRPAmount(210*dropsPerXRP), reward,
	)
	jtx.RequireTxSuccess(t, env.Submit(thirdAtt))
	thirdAtt = newCreateAttestation(
		t, secondWitness, secondWitness, bridge, 3, owner.Address, thirdNewDestination.Address,
		txcore.NewXRPAmount(210*dropsPerXRP), reward,
	)
	jtx.RequireTxSuccess(t, env.Submit(thirdAtt))
	require.False(t, env.Exists(thirdNewDestination))
	claimBridge := claimBridgeKey(t, bridge)
	thirdClaimKey := keylet.XChainCreateAccountClaimID(claimBridge, 3)
	require.True(t, env.LedgerEntryExists(thirdClaimKey))

	secondAtt := newCreateAttestation(
		t, witness, witness, bridge, 2, owner.Address, secondNewDestination.Address,
		txcore.NewXRPAmount(210*dropsPerXRP), reward,
	)
	jtx.RequireTxSuccess(t, env.Submit(secondAtt))
	require.False(t, env.Exists(secondNewDestination))
	secondAtt = newCreateAttestation(
		t, secondWitness, secondWitness, bridge, 2, owner.Address, secondNewDestination.Address,
		txcore.NewXRPAmount(210*dropsPerXRP), reward,
	)
	jtx.RequireTxSuccess(t, env.Submit(secondAtt))
	require.True(t, env.Exists(secondNewDestination))
	require.False(t, env.Exists(thirdNewDestination))
	thirdAtt = newCreateAttestation(
		t, witness, witness, bridge, 3, owner.Address, thirdNewDestination.Address,
		txcore.NewXRPAmount(210*dropsPerXRP), reward,
	)
	jtx.RequireTxSuccess(t, env.Submit(thirdAtt))
	require.True(t, env.Exists(thirdNewDestination))
	require.False(t, env.LedgerEntryExists(thirdClaimKey))
	bridgeData, err = env.LedgerEntry(bridgeKey)
	require.NoError(t, err)
	require.NoError(t, bridgeEntry.Decode(bridgeData))
	require.Equal(t, "3", bridgeEntry.XChainAccountClaimCount)

	createClaim := func(source string) uint64 {
		t.Helper()
		txn := xchaintx.NewXChainCreateClaimID(owner.Address, bridge, reward, source)
		jtx.RequireTxSuccess(t, env.Submit(txn))
		bridgeData, err := env.LedgerEntry(bridgeKey)
		require.NoError(t, err)
		require.NoError(t, bridgeEntry.Decode(bridgeData))
		id, err := strconv.ParseUint(bridgeEntry.XChainClaimID, 16, 64)
		require.NoError(t, err)
		require.True(t, env.LedgerEntryExists(keylet.XChainClaimID(claimBridge, id)))
		return id
	}

	explicitID := createClaim(issuing.Address)
	explicitAtt := newClaimAttestation(
		t, witness, witness, bridge, explicitID, issuing.Address, "", txcore.NewXRPAmount(10*dropsPerXRP),
	)
	jtx.RequireTxSuccess(t, env.Submit(explicitAtt))
	explicitAtt = newClaimAttestation(
		t, secondWitness, secondWitness, bridge, explicitID, issuing.Address, "", txcore.NewXRPAmount(10*dropsPerXRP),
	)
	jtx.RequireTxSuccess(t, env.Submit(explicitAtt))
	explicitClaim := xchaintx.NewXChainClaim(
		owner.Address, bridge, explicitID, destination.Address, txcore.NewXRPAmount(10*dropsPerXRP),
	)
	jtx.RequireTxSuccess(t, env.Submit(explicitClaim))
	require.False(t, env.LedgerEntryExists(keylet.XChainClaimID(claimBridge, explicitID)))

	autoID := createClaim(issuing.Address)
	autoAtt := newClaimAttestation(
		t, witness, witness, bridge, autoID, issuing.Address, destination.Address, txcore.NewXRPAmount(10*dropsPerXRP),
	)
	jtx.RequireTxSuccess(t, env.Submit(autoAtt))
	require.True(t, env.LedgerEntryExists(keylet.XChainClaimID(claimBridge, autoID)))
	autoAtt = newClaimAttestation(
		t, secondWitness, secondWitness, bridge, autoID, issuing.Address, destination.Address, txcore.NewXRPAmount(10*dropsPerXRP),
	)
	jtx.RequireTxSuccess(t, env.Submit(autoAtt))
	require.False(t, env.LedgerEntryExists(keylet.XChainClaimID(claimBridge, autoID)))

	failureID := createClaim(issuing.Address)
	failureAtt := newClaimAttestation(
		t, witness, witness, bridge, failureID, issuing.Address, missingDestination.Address, txcore.NewXRPAmount(1),
	)
	jtx.RequireTxSuccess(t, env.Submit(failureAtt))
	require.True(t, env.LedgerEntryExists(keylet.XChainClaimID(claimBridge, failureID)))
	failureAtt = newClaimAttestation(
		t, secondWitness, secondWitness, bridge, failureID, issuing.Address, missingDestination.Address, txcore.NewXRPAmount(1),
	)
	jtx.RequireTxSuccess(t, env.Submit(failureAtt))
	require.True(t, env.LedgerEntryExists(keylet.XChainClaimID(claimBridge, failureID)))
	replacement := newClaimAttestation(
		t, witness, witness, bridge, failureID, issuing.Address, missingDestination.Address, txcore.NewXRPAmount(1),
	)
	jtx.RequireTxSuccess(t, env.Submit(replacement))
	require.True(t, env.LedgerEntryExists(keylet.XChainClaimID(claimBridge, failureID)))

	negativeRewardAttestation := newCreateAttestation(
		t, witness, witness, bridge, 99, owner.Address, destination.Address,
		txcore.NewXRPAmount(210*dropsPerXRP), txcore.NewXRPAmount(-1),
	)
	jtx.RequireTxSuccess(t, env.Submit(negativeRewardAttestation))
	require.True(t, env.LedgerEntryExists(keylet.XChainCreateAccountClaimID(claimBridge, 99)))

	badKeyID := createClaim(issuing.Address)
	badKeyAttestation := newClaimAttestation(
		t, witness, secondWitness, bridge, badKeyID, issuing.Address, "", txcore.NewXRPAmount(1),
	)
	badKeyAttestation.AttestationSignerAccount = witness.Address
	require.Equal(t, "tecXCHAIN_BAD_PUBLIC_KEY_ACCOUNT_PAIR", env.Submit(badKeyAttestation).Code)

	prunedID := createClaim(issuing.Address)
	jtx.RequireTxSuccess(t, env.Submit(newClaimAttestation(
		t, witness, witness, bridge, prunedID, issuing.Address, "", txcore.NewXRPAmount(1),
	)))
	replacementSigners := signerlist.NewSignerListSet(locking.Address, 1)
	replacementSigners.AddSigner(secondWitness.Address, 1)
	jtx.RequireTxSuccess(t, env.Submit(replacementSigners))
	jtx.RequireTxSuccess(t, env.Submit(newClaimAttestation(
		t, secondWitness, secondWitness, bridge, prunedID, issuing.Address, "", txcore.NewXRPAmount(1),
	)))
	jtx.RequireTxSuccess(t, env.Submit(xchaintx.NewXChainClaim(
		owner.Address, bridge, prunedID, destination.Address, txcore.NewXRPAmount(1),
	)))
}

func TestXChainIOUClaimDeliversExactAmountWithTransferRate(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("XChainBridge")
	env.Close()

	lockingDoor := jtx.NewAccount("xchain-iou-locking-door")
	lockingIssuer := jtx.NewAccount("xchain-iou-locking-issuer")
	issuingDoor := jtx.NewAccount("xchain-iou-issuing-door")
	owner := jtx.NewAccount("xchain-iou-owner")
	witness := jtx.NewAccount("xchain-iou-witness")
	destination := jtx.NewAccount("xchain-iou-destination")
	noLineDestination := jtx.NewAccount("xchain-iou-no-line-destination")
	for _, account := range []*jtx.Account{lockingDoor, lockingIssuer, issuingDoor, owner, witness, destination, noLineDestination} {
		env.FundAmount(account, uint64(5_000*dropsPerXRP))
	}
	env.Close()

	bridge := xchaintx.XChainBridge{
		LockingChainDoor: lockingDoor.Address,
		LockingChainIssue: txcore.Asset{
			Currency: "USD", Issuer: lockingIssuer.Address,
		},
		IssuingChainDoor: issuingDoor.Address,
		IssuingChainIssue: txcore.Asset{
			Currency: "USD", Issuer: issuingDoor.Address,
		},
	}
	reward := txcore.NewXRPAmount(0)
	jtx.RequireTxSuccess(t, env.Submit(xchaintx.NewXChainCreateBridge(lockingDoor.Address, bridge, reward)))

	signers := signerlist.NewSignerListSet(lockingDoor.Address, 1)
	signers.AddSigner(witness.Address, 1)
	jtx.RequireTxSuccess(t, env.Submit(signers))

	for _, holder := range []*jtx.Account{lockingDoor, owner, destination} {
		env.Trust(holder, txcore.NewIssuedAmountFromFloat64(1_000, "USD", lockingIssuer.Address))
	}
	env.PayIOU(lockingIssuer, owner, lockingIssuer, "USD", 200)
	env.SetTransferRate(lockingIssuer, 1_250_000_000)

	typedEquivalentBridge := bridge
	typedEquivalentBridge.LockingChainIssue.Currency = "0000000000000000000000005553440000000000"
	typedEquivalentBridge.IssuingChainIssue.Currency = "0000000000000000000000005553440000000000"
	commitAmount := txcore.NewIssuedAmountFromFloat64(100, "USD", lockingIssuer.Address)
	commit := xchaintx.NewXChainCommit(owner.Address, typedEquivalentBridge, 0, commitAmount)
	jtx.RequireTxSuccess(t, env.Submit(commit))
	require.InDelta(t, 100, env.BalanceIOU(lockingDoor, "USD", lockingIssuer), 0.000001)
	require.InDelta(t, 75, env.BalanceIOU(owner, "USD", lockingIssuer), 0.000001)

	createClaim := xchaintx.NewXChainCreateClaimID(owner.Address, bridge, reward, issuingDoor.Address)
	jtx.RequireTxSuccess(t, env.Submit(createClaim))
	currency, err := keylet.ParseCurrency("USD")
	require.NoError(t, err)
	bridgeData, err := env.LedgerEntry(keylet.Bridge(lockingDoor.ID, currency))
	require.NoError(t, err)
	var bridgeEntry entry.Bridge
	require.NoError(t, bridgeEntry.Decode(bridgeData))
	claimID, err := strconv.ParseUint(bridgeEntry.XChainClaimID, 16, 64)
	require.NoError(t, err)

	attestedAmount := txcore.NewIssuedAmountFromFloat64(10, "USD", issuingDoor.Address)
	attestation := newClaimAttestation(
		t, witness, witness, bridge, claimID, issuingDoor.Address, "", attestedAmount,
	)
	jtx.RequireTxSuccess(t, env.Submit(attestation))
	claimAmount := txcore.NewIssuedAmountFromFloat64(10, "USD", lockingIssuer.Address)
	claim := xchaintx.NewXChainClaim(owner.Address, bridge, claimID, destination.Address, claimAmount)
	jtx.RequireTxSuccess(t, env.Submit(claim))

	require.InDelta(t, 10, env.BalanceIOU(destination, "USD", lockingIssuer), 0.000001)
	require.InDelta(t, 87.5, env.BalanceIOU(lockingDoor, "USD", lockingIssuer), 0.000001)

	jtx.RequireTxSuccess(t, env.Submit(xchaintx.NewXChainCreateClaimID(
		owner.Address, bridge, reward, issuingDoor.Address,
	)))
	bridgeData, err = env.LedgerEntry(keylet.Bridge(lockingDoor.ID, currency))
	require.NoError(t, err)
	require.NoError(t, bridgeEntry.Decode(bridgeData))
	noLineClaimID, err := strconv.ParseUint(bridgeEntry.XChainClaimID, 16, 64)
	require.NoError(t, err)
	jtx.RequireTxSuccess(t, env.Submit(newClaimAttestation(
		t, witness, witness, bridge, noLineClaimID, issuingDoor.Address, "", attestedAmount,
	)))
	noLineClaim := func() *xchaintx.XChainClaim {
		return xchaintx.NewXChainClaim(owner.Address, bridge, noLineClaimID, noLineDestination.Address, claimAmount)
	}
	require.Equal(t, "terNO_LINE", env.Submit(noLineClaim()).Code)
	env.Trust(noLineDestination, txcore.NewIssuedAmountFromFloat64(1_000, "USD", lockingIssuer.Address))
	jtx.RequireTxSuccess(t, env.Submit(noLineClaim()))
}

func TestXChainRewardRoundingPreventsOverDistribution(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("XChainBridge")
	env.DisableFeature("fixXChainRewardRounding")
	env.Close()

	lockingDoor := jtx.NewAccount("xchain-rounding-locking")
	issuingDoor := env.MasterAccount()
	owner := jtx.NewAccount("xchain-rounding-owner")
	destination := jtx.NewAccount("xchain-rounding-destination")
	witnesses := []*jtx.Account{
		jtx.NewAccount("xchain-rounding-witness-1"),
		jtx.NewAccount("xchain-rounding-witness-2"),
		jtx.NewAccount("xchain-rounding-witness-3"),
	}
	accounts := make([]*jtx.Account, 0, 3+len(witnesses))
	accounts = append(accounts, lockingDoor, owner, destination)
	accounts = append(accounts, witnesses...)
	for _, account := range accounts {
		env.FundAmount(account, uint64(5_000*dropsPerXRP))
	}
	env.Close()

	bridge := nativeBridge(lockingDoor, issuingDoor)
	reward := txcore.NewXRPAmount(2)
	jtx.RequireTxSuccess(t, env.Submit(xchaintx.NewXChainCreateBridge(lockingDoor.Address, bridge, reward)))
	signers := signerlist.NewSignerListSet(lockingDoor.Address, 3)
	for _, witness := range witnesses {
		signers.AddSigner(witness.Address, 1)
	}
	jtx.RequireTxSuccess(t, env.Submit(signers))
	jtx.RequireTxSuccess(t, env.Submit(xchaintx.NewXChainCommit(
		owner.Address, bridge, 0, txcore.NewXRPAmount(100*dropsPerXRP),
	)))
	jtx.RequireTxSuccess(t, env.Submit(xchaintx.NewXChainCreateClaimID(
		owner.Address, bridge, reward, issuingDoor.Address,
	)))

	currency, err := keylet.ParseCurrency("XRP")
	require.NoError(t, err)
	bridgeData, err := env.LedgerEntry(keylet.Bridge(lockingDoor.ID, currency))
	require.NoError(t, err)
	var bridgeEntry entry.Bridge
	require.NoError(t, bridgeEntry.Decode(bridgeData))
	claimID, err := strconv.ParseUint(bridgeEntry.XChainClaimID, 16, 64)
	require.NoError(t, err)
	claimBridge := claimBridgeKey(t, bridge)
	for _, witness := range witnesses {
		attestation := newClaimAttestation(
			t, witness, witness, bridge, claimID, issuingDoor.Address, "", txcore.NewXRPAmount(10*dropsPerXRP),
		)
		jtx.RequireTxSuccess(t, env.Submit(attestation))
	}

	claim := func() *xchaintx.XChainClaim {
		return xchaintx.NewXChainClaim(
			owner.Address, bridge, claimID, destination.Address, txcore.NewXRPAmount(10*dropsPerXRP),
		)
	}
	result := env.Submit(claim())
	require.Equal(t, "tecINTERNAL", result.Code)
	require.True(t, env.LedgerEntryExists(keylet.XChainClaimID(claimBridge, claimID)))

	env.EnableFeatureNow("fixXChainRewardRounding")
	jtx.RequireTxSuccess(t, env.Submit(claim()))
	require.False(t, env.LedgerEntryExists(keylet.XChainClaimID(claimBridge, claimID)))
}

func TestXChainClaimAttestationPersistsWhenRewardIsUnfunded(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("XChainBridge")
	env.Close()

	lockingDoor := jtx.NewAccount("xchain-unfunded-locking")
	issuingDoor := env.MasterAccount()
	owner := jtx.NewAccount("xchain-unfunded-owner")
	witness := jtx.NewAccount("xchain-unfunded-witness")
	destination := jtx.NewAccount("xchain-unfunded-destination")
	for _, account := range []*jtx.Account{lockingDoor, owner, witness, destination} {
		env.FundAmount(account, uint64(5_000*dropsPerXRP))
	}
	env.Close()

	bridge := nativeBridge(lockingDoor, issuingDoor)
	reward := txcore.NewXRPAmount(10_000 * dropsPerXRP)
	jtx.RequireTxSuccess(t, env.Submit(xchaintx.NewXChainCreateBridge(lockingDoor.Address, bridge, reward)))
	signers := signerlist.NewSignerListSet(lockingDoor.Address, 1)
	signers.AddSigner(witness.Address, 1)
	jtx.RequireTxSuccess(t, env.Submit(signers))

	jtx.RequireTxSuccess(t, env.Submit(xchaintx.NewXChainCreateClaimID(
		owner.Address, bridge, reward, issuingDoor.Address,
	)))
	bridgeData, err := env.LedgerEntry(keylet.Bridge(lockingDoor.ID, [20]byte{}))
	require.NoError(t, err)
	var bridgeEntry entry.Bridge
	require.NoError(t, bridgeEntry.Decode(bridgeData))
	claimID, err := strconv.ParseUint(bridgeEntry.XChainClaimID, 16, 64)
	require.NoError(t, err)

	claimBridge := claimBridgeKey(t, bridge)
	claimKey := keylet.XChainClaimID(claimBridge, claimID)
	destinationBalance := env.Balance(destination)
	attestation := newClaimAttestation(
		t, witness, witness, bridge, claimID, issuingDoor.Address, destination.Address,
		txcore.NewXRPAmount(dropsPerXRP),
	)
	jtx.RequireTxSuccess(t, env.Submit(attestation))
	require.Equal(t, destinationBalance, env.Balance(destination))
	require.True(t, env.LedgerEntryExists(claimKey))
	claimData, err := env.LedgerEntry(claimKey)
	require.NoError(t, err)
	var claimEntry entry.XChainOwnedClaimID
	require.NoError(t, claimEntry.Decode(claimData))
	require.Len(t, claimEntry.XChainClaimAttestations, 1)
}

func TestXChainClaimDestinationTagAndDepositAuth(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("XChainBridge")
	env.Close()

	lockingDoor := jtx.NewAccount("xchain-auth-locking")
	issuingDoor := env.MasterAccount()
	owner := jtx.NewAccount("xchain-auth-owner")
	witness := jtx.NewAccount("xchain-auth-witness")
	destination := jtx.NewAccount("xchain-auth-destination")
	for _, account := range []*jtx.Account{lockingDoor, owner, witness, destination} {
		env.FundAmount(account, uint64(5_000*dropsPerXRP))
	}
	env.Close()

	bridge := nativeBridge(lockingDoor, issuingDoor)
	reward := txcore.NewXRPAmount(0)
	jtx.RequireTxSuccess(t, env.Submit(xchaintx.NewXChainCreateBridge(lockingDoor.Address, bridge, reward)))
	signers := signerlist.NewSignerListSet(lockingDoor.Address, 1)
	signers.AddSigner(witness.Address, 1)
	jtx.RequireTxSuccess(t, env.Submit(signers))
	jtx.RequireTxSuccess(t, env.Submit(xchaintx.NewXChainCommit(
		owner.Address, bridge, 0, txcore.NewXRPAmount(100*dropsPerXRP),
	)))
	jtx.RequireTxSuccess(t, env.Submit(xchaintx.NewXChainCreateClaimID(
		owner.Address, bridge, reward, issuingDoor.Address,
	)))

	currency, err := keylet.ParseCurrency("XRP")
	require.NoError(t, err)
	bridgeData, err := env.LedgerEntry(keylet.Bridge(lockingDoor.ID, currency))
	require.NoError(t, err)
	var bridgeEntry entry.Bridge
	require.NoError(t, bridgeEntry.Decode(bridgeData))
	claimID, err := strconv.ParseUint(bridgeEntry.XChainClaimID, 16, 64)
	require.NoError(t, err)
	jtx.RequireTxSuccess(t, env.Submit(newClaimAttestation(
		t, witness, witness, bridge, claimID, issuingDoor.Address, "", txcore.NewXRPAmount(10*dropsPerXRP),
	)))

	env.EnableRequireDest(destination)
	env.EnableDepositAuth(destination)
	claim := func(tag *uint32) *xchaintx.XChainClaim {
		txn := xchaintx.NewXChainClaim(
			owner.Address, bridge, claimID, destination.Address, txcore.NewXRPAmount(10*dropsPerXRP),
		)
		txn.DestinationTag = tag
		return txn
	}
	require.Equal(t, "tecDST_TAG_NEEDED", env.Submit(claim(nil)).Code)
	tag := uint32(7)
	require.Equal(t, "tecNO_PERMISSION", env.Submit(claim(&tag)).Code)
	env.Preauthorize(destination, lockingDoor)
	jtx.RequireTxSuccess(t, env.Submit(claim(&tag)))
}
