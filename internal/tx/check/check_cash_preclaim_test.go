package check

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type checkCashPreclaimFixture struct {
	view     *checkMPTView
	cash     *CheckCash
	config   tx.EngineConfig
	check    *state.CheckData
	checkKey keylet.Keylet
	source   [20]byte
	dest     [20]byte
}

func newCheckCashPreclaimFixture(t *testing.T, expiration uint32) *checkCashPreclaimFixture {
	t.Helper()

	view := newCheckMPTView()
	source := checkMPTAccountID(0x41)
	dest := checkMPTAccountID(0x42)
	putCheckMPTAccount(t, view, source, 1)
	putCheckMPTAccount(t, view, dest, 0)

	checkKey := keylet.Check(source, 1)
	check := &state.CheckData{
		Account:         source,
		DestinationID:   dest,
		SendMax:         100,
		SendMaxAmount:   state.NewXRPAmountFromInt(100),
		IsNativeSendMax: true,
		Expiration:      expiration,
	}
	checkData, err := state.SerializeCheckFromData(check)
	if err != nil {
		t.Fatal(err)
	}
	if err := view.Insert(checkKey, checkData); err != nil {
		t.Fatal(err)
	}

	destAddress, err := state.EncodeAccountID(dest)
	if err != nil {
		t.Fatal(err)
	}
	cash := NewCheckCash(destAddress, hex.EncodeToString(checkKey.Key[:]))
	amount := state.NewXRPAmountFromInt(10)
	cash.Amount = &amount

	return &checkCashPreclaimFixture{
		view:     view,
		cash:     cash,
		config:   tx.EngineConfig{ReserveBase: 10_000_000, ReserveIncrement: 2_000_000, ParentCloseTime: 100, Rules: amendment.AllSupportedRules()},
		check:    check,
		checkKey: checkKey,
		source:   source,
		dest:     dest,
	}
}

func rewriteCheckData(t *testing.T, fixture *checkCashPreclaimFixture) {
	t.Helper()
	checkData, err := state.SerializeCheckFromData(fixture.check)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.view.Update(fixture.checkKey, checkData); err != nil {
		t.Fatal(err)
	}
}

func checkCashPreclaimResult(t *testing.T, fixture *checkCashPreclaimFixture) ter.Result {
	t.Helper()
	preclaimer, ok := any(fixture.cash).(tx.Preclaimer)
	if !ok {
		t.Fatal("CheckCash does not implement tx.Preclaimer")
	}
	return preclaimer.Preclaim(fixture.view, fixture.config)
}

func TestCheckCashPreclaimExpiryBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		expiration uint32
		want       ter.Result
	}{
		{name: "absent", expiration: 0, want: ter.TesSUCCESS},
		{name: "before close", expiration: 99, want: ter.TecEXPIRED},
		{name: "at close", expiration: 100, want: ter.TecEXPIRED},
		{name: "after close", expiration: 101, want: ter.TesSUCCESS},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkCashPreclaimResult(t, newCheckCashPreclaimFixture(t, tt.expiration))
			if got != tt.want {
				t.Fatalf("CheckCash preclaim result = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckCashPreclaimExpiryPrecedence(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *checkCashPreclaimFixture)
		want  ter.Result
	}{
		{
			name: "wrong destination precedes expiry",
			setup: func(t *testing.T, fixture *checkCashPreclaimFixture) {
				account, err := state.EncodeAccountID(fixture.source)
				if err != nil {
					t.Fatal(err)
				}
				fixture.cash.Account = account
			},
			want: ter.TecNO_PERMISSION,
		},
		{
			name: "missing check precedes expiry",
			setup: func(_ *testing.T, fixture *checkCashPreclaimFixture) {
				delete(fixture.view.data, fixture.checkKey.Key)
			},
			want: ter.TecNO_ENTRY,
		},
		{
			name: "self check precedes expiry",
			setup: func(t *testing.T, fixture *checkCashPreclaimFixture) {
				fixture.check.DestinationID = fixture.source
				fixture.cash.Account = state.EncodeAccountIDSafe(fixture.source)
				rewriteCheckData(t, fixture)
			},
			want: ter.TecINTERNAL,
		},
		{
			name: "missing source precedes expiry",
			setup: func(_ *testing.T, fixture *checkCashPreclaimFixture) {
				delete(fixture.view.data, keylet.Account(fixture.source).Key)
			},
			want: ter.TecNO_ENTRY,
		},
		{
			name: "destination tag precedes expiry",
			setup: func(t *testing.T, fixture *checkCashPreclaimFixture) {
				accountData, err := fixture.view.Read(keylet.Account(fixture.dest))
				if err != nil {
					t.Fatal(err)
				}
				account, err := state.ParseAccountRoot(accountData)
				if err != nil {
					t.Fatal(err)
				}
				account.Flags |= state.LsfRequireDestTag
				accountData, err = state.SerializeAccountRoot(account)
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.view.Update(keylet.Account(fixture.dest), accountData); err != nil {
					t.Fatal(err)
				}
			},
			want: ter.TecDST_TAG_NEEDED,
		},
		{
			name: "expiration precedes insufficient source funds",
			setup: func(t *testing.T, fixture *checkCashPreclaimFixture) {
				amount := tx.NewXRPAmount(3_000_000)
				fixture.cash.Amount = &amount
				fixture.check.SendMax = 3_000_000
				fixture.check.SendMaxAmount = amount
				rewriteCheckData(t, fixture)
				sourceData, err := fixture.view.Read(keylet.Account(fixture.source))
				if err != nil {
					t.Fatal(err)
				}
				sourceAccount, err := state.ParseAccountRoot(sourceData)
				if err != nil {
					t.Fatal(err)
				}
				sourceAccount.Balance = 0
				sourceData, err = state.SerializeAccountRoot(sourceAccount)
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.view.Update(keylet.Account(fixture.source), sourceData); err != nil {
					t.Fatal(err)
				}
			},
			want: ter.TecEXPIRED,
		},
		{
			name: "expiration precedes amount mismatch",
			setup: func(_ *testing.T, fixture *checkCashPreclaimFixture) {
				amount := tx.NewIssuedAmount(10, 0, "USD", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
				fixture.cash.Amount = &amount
			},
			want: ter.TecEXPIRED,
		},
		{
			name: "expiration precedes send max",
			setup: func(_ *testing.T, fixture *checkCashPreclaimFixture) {
				amount := state.NewXRPAmountFromInt(101)
				fixture.cash.Amount = &amount
			},
			want: ter.TecEXPIRED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCheckCashPreclaimFixture(t, 100)
			tt.setup(t, fixture)
			if got := checkCashPreclaimResult(t, fixture); got != tt.want {
				t.Fatalf("CheckCash preclaim result = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckCashPreclaimDestinationTags(t *testing.T) {
	tests := []struct {
		name       string
		hasDestTag bool
		want       ter.Result
	}{
		{name: "missing", want: ter.TecDST_TAG_NEEDED},
		{name: "present zero", hasDestTag: true, want: ter.TesSUCCESS},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCheckCashPreclaimFixture(t, 101)
			destinationData, err := fixture.view.Read(keylet.Account(fixture.dest))
			if err != nil {
				t.Fatal(err)
			}
			destination, err := state.ParseAccountRoot(destinationData)
			if err != nil {
				t.Fatal(err)
			}
			destination.Flags |= state.LsfRequireDestTag
			if tt.hasDestTag {
				fixture.check.HasDestTag = true
				fixture.check.DestinationTag = 0
			}
			destinationData, err = state.SerializeAccountRoot(destination)
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.view.Update(keylet.Account(fixture.dest), destinationData); err != nil {
				t.Fatal(err)
			}
			rewriteCheckData(t, fixture)
			if got := checkCashPreclaimResult(t, fixture); got != tt.want {
				t.Fatalf("CheckCash preclaim result = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckCashPreclaimPreservesStorageErrors(t *testing.T) {
	tests := []struct {
		name string
		key  func(*checkCashPreclaimFixture) keylet.Keylet
	}{
		{name: "check", key: func(f *checkCashPreclaimFixture) keylet.Keylet { return f.checkKey }},
		{name: "source account", key: func(f *checkCashPreclaimFixture) keylet.Keylet { return keylet.Account(f.source) }},
		{name: "destination account", key: func(f *checkCashPreclaimFixture) keylet.Keylet { return keylet.Account(f.dest) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCheckCashPreclaimFixture(t, 101)
			fixture.view.readErrors[tt.key(fixture).Key] = errors.New("storage failure")
			if got := checkCashPreclaimResult(t, fixture); got != ter.TefINTERNAL {
				t.Fatalf("CheckCash preclaim result = %v, want %v", got, ter.TefINTERNAL)
			}
		})
	}
}
