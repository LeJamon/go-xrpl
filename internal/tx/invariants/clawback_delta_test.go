package invariants

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

func clawbackRules(enabled bool) *amendment.Rules {
	builder := amendment.NewRulesBuilder()
	if enabled {
		builder.Enable(amendment.FeatureMPTokensV2)
	}
	return builder.Build()
}

func clawbackLine(t *testing.T, holderBalance int64) []byte {
	t.Helper()
	holder, err := state.DecodeAccountID(addrHolderA)
	require.NoError(t, err)
	issuer, err := state.DecodeAccountID(addrIssuer)
	require.NoError(t, err)

	balance := state.NewIssuedAmountFromValue(holderBalance, 0, "USD", state.AccountOneAddress)
	if state.CompareAccountIDs(holder, issuer) > 0 {
		balance = balance.Negate()
	}
	lowLimit := state.NewIssuedAmountFromValue(0, 0, "USD", addrHolderA)
	highLimit := state.NewIssuedAmountFromValue(0, 0, "USD", addrIssuer)
	data, err := state.SerializeRippleState(&state.RippleState{
		Balance:   balance,
		LowLimit:  lowLimit,
		HighLimit: highLimit,
	})
	require.NoError(t, err)
	return data
}

func clawbackLineEntry(t *testing.T, before, after *int64) InvariantEntry {
	t.Helper()
	holder, err := state.DecodeAccountID(addrHolderA)
	require.NoError(t, err)
	issuer, err := state.DecodeAccountID(addrIssuer)
	require.NoError(t, err)
	result := InvariantEntry{Key: keylet.Line(holder, issuer, "USD").Key, EntryType: entry.TypeRippleState}
	if before != nil {
		result.Before = clawbackLine(t, *before)
	}
	if after != nil {
		result.After = clawbackLine(t, *after)
	} else if before != nil {
		result.IsDelete = true
	}
	return result
}

func clawbackMPTEntry(t *testing.T, holder string, issuanceID [24]byte, before, after *uint64) InvariantEntry {
	t.Helper()
	holderID, err := state.DecodeAccountID(holder)
	require.NoError(t, err)
	result := InvariantEntry{
		Key:       keylet.MPToken(keylet.MPTIssuance(issuanceID).Key, holderID).Key,
		EntryType: entry.TypeMPToken,
	}
	serialize := func(value uint64) []byte {
		data, err := state.SerializeMPToken(&state.MPTokenData{
			Account:           holderID,
			MPTokenIssuanceID: issuanceID,
			MPTAmount:         value,
		})
		require.NoError(t, err)
		return data
	}
	if before != nil {
		result.Before = serialize(*before)
	}
	if after != nil {
		result.After = serialize(*after)
	} else if before != nil {
		result.IsDelete = true
	}
	return result
}

func TestValidClawbackIOUBalanceDelta(t *testing.T) {
	amount := func(value int64) Amount {
		return state.NewIssuedAmountFromValue(value, 0, "USD", addrHolderA)
	}
	tx := func(value int64) clawbackTx {
		return clawbackTx{account: addrIssuer, amount: amount(value)}
	}
	ptr := func(value int64) *int64 { return &value }

	tests := []struct {
		name   string
		before *int64
		after  *int64
		amount int64
		mutate func(*InvariantEntry)
		want   string
	}{
		{name: "partial", before: ptr(100), after: ptr(90), amount: 10},
		{name: "full deletion", before: ptr(100), amount: 100},
		{name: "over clawback zeros balance", before: ptr(100), amount: 150},
		{name: "wrong delta", before: ptr(100), after: ptr(80), amount: 10, want: "trustline clawback balance change is invalid"},
		{name: "wrong direction", before: ptr(100), after: ptr(110), amount: 10, want: "trustline clawback balance change is invalid"},
		{name: "partial deletion", before: ptr(100), amount: 10, want: "trustline clawback balance change is invalid"},
		{name: "zero amount", before: ptr(100), after: ptr(90), amount: 0, want: "trustline clawback amount is invalid"},
		{name: "wrong trustline", before: ptr(100), after: ptr(90), amount: 10, mutate: func(e *InvariantEntry) { e.Key[0] ^= 1 }, want: "trustline clawback changed the wrong line"},
		{name: "wrong before balance currency", before: ptr(100), after: ptr(90), amount: 10, mutate: func(e *InvariantEntry) {
			line, err := state.ParseRippleState(e.Before)
			require.NoError(t, err)
			line.Balance.Currency = "EUR"
			e.Before, err = state.SerializeRippleState(line)
			require.NoError(t, err)
		}, want: "trustline clawback balance change is invalid"},
		{name: "wrong after balance currency", before: ptr(100), after: ptr(90), amount: 10, mutate: func(e *InvariantEntry) {
			line, err := state.ParseRippleState(e.After)
			require.NoError(t, err)
			line.Balance.Currency = "EUR"
			e.After, err = state.SerializeRippleState(line)
			require.NoError(t, err)
		}, want: "trustline clawback balance change is invalid"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := clawbackLineEntry(t, test.before, test.after)
			if test.mutate != nil {
				test.mutate(&changed)
			}
			violation := checkValidClawback(tx(test.amount), TesSUCCESS, []InvariantEntry{changed}, stubView{}, clawbackRules(true))
			if test.want == "" {
				require.Nil(t, violation)
				return
			}
			require.NotNil(t, violation)
			require.Equal(t, test.want, violation.Message)
		})
	}

	invalid := clawbackLineEntry(t, ptr(100), ptr(80))
	require.Nil(t, checkValidClawback(tx(10), TesSUCCESS, []InvariantEntry{invalid}, stubView{}, clawbackRules(false)))

	negative := clawbackLineEntry(t, ptr(100), ptr(-10))
	violation := checkValidClawback(tx(10), TesSUCCESS, []InvariantEntry{negative}, lineView{line: negative.After}, clawbackRules(true))
	require.NotNil(t, violation)
	require.Equal(t, "trustline or MPT balance is negative", violation.Message)
}

func TestValidClawbackMPTBalanceDelta(t *testing.T) {
	issuer, err := state.DecodeAccountID(addrIssuer)
	require.NoError(t, err)
	id := keylet.MakeMPTID(1, issuer)
	mptID := hex.EncodeToString(id[:])
	tx := func(value int64) clawbackTx {
		return clawbackTx{
			account: addrIssuer,
			holder:  addrHolderA,
			amount:  state.NewMPTAmountWithIssuanceID(value, addrIssuer, mptID),
		}
	}
	ptr := func(value uint64) *uint64 { return &value }

	tests := []struct {
		name   string
		before *uint64
		after  *uint64
		amount int64
		mutate func(*clawbackTx, *InvariantEntry)
		want   string
	}{
		{name: "partial", before: ptr(100), after: ptr(90), amount: 10},
		{name: "over clawback zeros balance", before: ptr(100), after: ptr(0), amount: 150},
		{name: "wrong delta", before: ptr(100), after: ptr(80), amount: 10, want: "MPT clawback balance change is invalid"},
		{name: "wrong direction", before: ptr(100), after: ptr(110), amount: 10, want: "MPT clawback balance change is invalid"},
		{name: "zero amount", before: ptr(100), after: ptr(90), amount: 0, want: "MPT clawback amount is invalid"},
		{name: "deleted token", before: ptr(100), amount: 100, want: "MPT clawback token is missing"},
		{name: "missing holder", before: ptr(100), after: ptr(90), amount: 10, mutate: func(tx *clawbackTx, _ *InvariantEntry) { tx.holder = "" }, want: "MPT clawback missing holder"},
		{name: "wrong token", before: ptr(100), after: ptr(90), amount: 10, mutate: func(_ *clawbackTx, e *InvariantEntry) { e.Key[0] ^= 1 }, want: "MPT clawback changed the wrong token"},
		{name: "wrong token contents", before: ptr(100), after: ptr(90), amount: 10, mutate: func(_ *clawbackTx, e *InvariantEntry) {
			other, err := state.DecodeAccountID(addrHolderB)
			require.NoError(t, err)
			token, err := state.ParseMPToken(e.After)
			require.NoError(t, err)
			token.Account = other
			e.After, err = state.SerializeMPToken(token)
			require.NoError(t, err)
		}, want: "MPT clawback changed the wrong token"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transaction := tx(test.amount)
			changed := clawbackMPTEntry(t, addrHolderA, id, test.before, test.after)
			if test.mutate != nil {
				test.mutate(&transaction, &changed)
			}
			violation := checkValidClawback(transaction, TesSUCCESS, []InvariantEntry{changed}, stubView{}, clawbackRules(true))
			if test.want == "" {
				require.Nil(t, violation)
				return
			}
			require.NotNil(t, violation)
			require.Equal(t, test.want, violation.Message)
		})
	}

	invalid := clawbackMPTEntry(t, addrHolderA, id, ptr(100), ptr(80))
	require.Nil(t, checkValidClawback(tx(10), TesSUCCESS, []InvariantEntry{invalid}, stubView{}, clawbackRules(false)))
}

func TestValidClawbackRejectsAssetArmMismatch(t *testing.T) {
	issuer, err := state.DecodeAccountID(addrIssuer)
	require.NoError(t, err)
	id := keylet.MakeMPTID(1, issuer)
	mptID := hex.EncodeToString(id[:])
	mptBefore, mptAfter := uint64(100), uint64(90)
	mptEntry := clawbackMPTEntry(t, addrHolderA, id, &mptBefore, &mptAfter)
	iouTx := clawbackTx{
		account: addrIssuer,
		amount:  state.NewIssuedAmountFromValue(10, 0, "USD", addrHolderA),
	}
	violation := checkValidClawback(iouTx, TesSUCCESS, []InvariantEntry{mptEntry}, stubView{}, clawbackRules(true))
	require.NotNil(t, violation)
	require.Equal(t, "trustline clawback changed the wrong line", violation.Message)

	lineBefore, lineAfter := int64(100), int64(90)
	lineEntry := clawbackLineEntry(t, &lineBefore, &lineAfter)
	mptTx := clawbackTx{
		account: addrIssuer,
		holder:  addrHolderA,
		amount:  state.NewMPTAmountWithIssuanceID(10, addrIssuer, mptID),
	}
	violation = checkValidClawback(mptTx, TesSUCCESS, []InvariantEntry{lineEntry}, stubView{}, clawbackRules(true))
	require.NotNil(t, violation)
	require.Equal(t, "MPT clawback token is missing", violation.Message)
}

func TestValidClawbackRejectsMixedAssetMutation(t *testing.T) {
	issuer, err := state.DecodeAccountID(addrIssuer)
	require.NoError(t, err)
	id := keylet.MakeMPTID(1, issuer)
	mptID := hex.EncodeToString(id[:])
	lineBefore, lineAfter := int64(100), int64(90)
	mptBefore, mptAfter := uint64(100), uint64(90)
	entries := []InvariantEntry{
		clawbackLineEntry(t, &lineBefore, &lineAfter),
		clawbackMPTEntry(t, addrHolderA, id, &mptBefore, &mptAfter),
	}
	tx := clawbackTx{
		account: addrIssuer,
		holder:  addrHolderA,
		amount:  state.NewMPTAmountWithIssuanceID(10, addrIssuer, mptID),
	}
	violation := checkValidClawback(tx, TesSUCCESS, entries, stubView{}, clawbackRules(true))
	require.NotNil(t, violation)
	require.Equal(t, "trustline and MPToken both changed", violation.Message)
	require.Nil(t, checkValidClawback(tx, TesSUCCESS, entries, stubView{}, clawbackRules(false)))
}
