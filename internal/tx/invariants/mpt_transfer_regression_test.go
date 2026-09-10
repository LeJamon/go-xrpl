package invariants

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func TestMPTTransferDeletedHoldingAndFailureMatrix(t *testing.T) {
	issuer, sender, receiver := [20]byte{1}, [20]byte{2}, [20]byte{3}
	id := keylet.MakeMPTID(1, issuer)
	for _, tc := range []struct {
		name                                                     string
		cleanup, failed, orphan, deleted, changed, wantViolation bool
	}{
		{name: "empty orphan deletion", orphan: true, deleted: true},
		{name: "failed orphan deletion before cleanup", failed: true, orphan: true, deleted: true},
		{name: "failed orphan deletion after cleanup", cleanup: true, failed: true, orphan: true, deleted: true, wantViolation: true},
		{name: "orphan increase", orphan: true, changed: true, wantViolation: true},
		{name: "failed orphan increase before cleanup", failed: true, orphan: true, changed: true, wantViolation: true},
		{name: "single holder mint", changed: true},
		{name: "failed single holder mint before cleanup", failed: true, changed: true},
		{name: "failed single holder mint after cleanup", cleanup: true, failed: true, changed: true, wantViolation: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := mapView{data: make(map[[32]byte][]byte)}
			if !tc.orphan {
				view.data[keylet.MPTIssuance(id).Key] = mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{Issuer: issuer, Sequence: 1, Flags: entry.LsfMPTCanTransfer})
			}
			token := state.MPTokenData{Account: sender, MPTokenIssuanceID: id}
			before := mustSerializeMPT(t, &token)
			if tc.changed {
				token.MPTAmount = 1
			}
			after := mustSerializeMPT(t, &token)
			change := InvariantEntry{EntryType: entry.TypeMPToken, Key: keylet.MPTokenByID(id, sender).Key, Before: before, After: after}
			if tc.deleted {
				change.IsDelete = true
				change.DeleteFinal = after
				change.After = nil
			} else {
				view.data[change.Key] = after
			}
			rules := mptInvariantRules()
			if tc.cleanup {
				rules = mptInvariantRulesWithCleanup()
			}
			result := TesSUCCESS
			if tc.failed {
				result = Result(ter.TecPATH_PARTIAL)
			}
			violation := checkValidMPTTransfer(stubTx{txType: TypePayment}, result, []InvariantEntry{change}, view, rules)
			require.Equal(t, tc.wantViolation, violation != nil, "%v", violation)
			require.Nil(t, checkValidMPTTransfer(stubTx{txType: TypePayment}, result, []InvariantEntry{change}, view, amendment.EmptyRules()))
		})
	}

	t.Run("failed lock movement", func(t *testing.T) {
		locked := uint64(5)
		before := mustSerializeMPT(t, &state.MPTokenData{Account: receiver, MPTokenIssuanceID: id, MPTAmount: 10})
		after := mustSerializeMPT(t, &state.MPTokenData{Account: receiver, MPTokenIssuanceID: id, MPTAmount: 5, LockedAmount: &locked})
		change := InvariantEntry{EntryType: entry.TypeMPToken, Before: before, After: after}
		view := mapView{data: map[[32]byte][]byte{
			keylet.MPTIssuance(id).Key:           mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{Issuer: issuer, Sequence: 1}),
			keylet.MPTokenByID(id, receiver).Key: after,
		}}
		require.Nil(t, checkValidMPTBalanceChanges(stubTx{txType: TypePayment}, Result(ter.TecPATH_PARTIAL), []InvariantEntry{change}, view, mptInvariantRulesWithCleanup()))
		require.NotNil(t, checkValidMPTTransfer(stubTx{txType: TypePayment}, Result(ter.TecPATH_PARTIAL), []InvariantEntry{change}, view, mptInvariantRulesWithCleanup()))
	})
}

func TestMPTTransferUsesPretransactionPseudoClassification(t *testing.T) {
	issuer, sender, receiver := [20]byte{4}, [20]byte{5}, [20]byte{6}
	id := keylet.MakeMPTID(1, issuer)
	for _, tc := range []struct {
		name                                                           string
		beforePseudo, afterPseudo, deleted, holdingDeleted, authorized bool
	}{
		{name: "deleted pseudo", beforePseudo: true, deleted: true},
		{name: "deleted ordinary", deleted: true},
		{name: "deleted authorized ordinary", deleted: true, authorized: true},
		{name: "pseudo marker removed", beforePseudo: true},
		{name: "ordinary account gains marker while deleting holding", afterPseudo: true, holdingDeleted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := state.AccountRoot{Account: state.EncodeAccountIDSafe(sender)}
			if tc.beforePseudo {
				root.AMMID = [32]byte{1}
			}
			beforeRoot, err := state.SerializeAccountRoot(&root)
			require.NoError(t, err)
			root.AMMID = [32]byte{}
			if tc.afterPseudo {
				root.AMMID = [32]byte{1}
			}
			afterRoot, err := state.SerializeAccountRoot(&root)
			require.NoError(t, err)
			sourceFlags := uint32(0)
			if tc.authorized {
				sourceFlags = entry.LsfMPTAuthorized
			}
			srcBefore := mustSerializeMPT(t, &state.MPTokenData{Account: sender, MPTokenIssuanceID: id, MPTAmount: 10, Flags: sourceFlags})
			srcAfter := mustSerializeMPT(t, &state.MPTokenData{Account: sender, MPTokenIssuanceID: id})
			dstBefore := mustSerializeMPT(t, &state.MPTokenData{Account: receiver, MPTokenIssuanceID: id, Flags: entry.LsfMPTAuthorized})
			dstAfter := mustSerializeMPT(t, &state.MPTokenData{Account: receiver, MPTokenIssuanceID: id, MPTAmount: 10, Flags: entry.LsfMPTAuthorized})
			changes := []InvariantEntry{
				{EntryType: entry.TypeAccountRoot, Key: keylet.Account(sender).Key, Before: beforeRoot, After: afterRoot},
				{EntryType: entry.TypeMPToken, Key: keylet.MPTokenByID(id, sender).Key, Before: srcBefore, After: srcAfter},
				{EntryType: entry.TypeMPToken, Key: keylet.MPTokenByID(id, receiver).Key, Before: dstBefore, After: dstAfter},
			}
			view := mapView{data: map[[32]byte][]byte{
				keylet.Account(sender).Key:           afterRoot,
				keylet.MPTokenByID(id, sender).Key:   srcAfter,
				keylet.MPTokenByID(id, receiver).Key: dstAfter,
				keylet.MPTIssuance(id).Key:           mustSerializeMPTIssuance(t, &state.MPTokenIssuanceData{Issuer: issuer, Sequence: 1, Flags: entry.LsfMPTRequireAuth | entry.LsfMPTCanTransfer}),
			}}
			if tc.deleted {
				changes[0].IsDelete = true
				changes[0].DeleteFinal = changes[0].After
				changes[0].After = nil
				delete(view.data, keylet.Account(sender).Key)
			}
			if tc.deleted || tc.holdingDeleted {
				changes[1].IsDelete = true
				changes[1].DeleteFinal = changes[1].After
				changes[1].After = nil
				delete(view.data, keylet.MPTokenByID(id, sender).Key)
			}
			violation := checkValidMPTTransfer(stubTx{txType: TypePayment}, TesSUCCESS, changes, view, mptInvariantRulesWithCleanup())
			require.Equal(t, !tc.beforePseudo && !tc.authorized, violation != nil, "%v", violation)
		})
	}
}
