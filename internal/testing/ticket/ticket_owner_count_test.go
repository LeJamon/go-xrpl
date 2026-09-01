package ticket_test

import (
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/ticket"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func seedTicketOwnerCount(t *testing.T, env *jtx.TestEnv, account *jtx.Account, ownerCount uint32) {
	t.Helper()

	accountKey := keylet.Account(account.ID)
	data, err := env.Ledger().Read(accountKey)
	require.NoError(t, err)
	accountRoot, err := state.ParseAccountRoot(data)
	require.NoError(t, err)
	accountRoot.OwnerCount = ownerCount
	data, err = state.SerializeAccountRoot(accountRoot)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(accountKey, data))
}

func TestTicketCreate_ConfinesOwnerCount(t *testing.T) {
	tests := []struct {
		name             string
		sponsorEnabled   bool
		ownerCount       uint32
		ticketCount      uint32
		balance          uint64
		wantResult       string
		wantOwnerCount   uint32
		wantTicketCount  uint32
		wantSequenceStep uint32
	}{
		{
			name:             "sponsor enabled max minus one count one exact max reserve",
			sponsorEnabled:   true,
			ownerCount:       math.MaxUint32 - 1,
			ticketCount:      1,
			balance:          math.MaxUint32,
			wantResult:       "tesSUCCESS",
			wantOwnerCount:   math.MaxUint32,
			wantTicketCount:  1,
			wantSequenceStep: 2,
		},
		{
			name:             "sponsor enabled max count one below max reserve",
			sponsorEnabled:   true,
			ownerCount:       math.MaxUint32,
			ticketCount:      1,
			balance:          math.MaxUint32 - 1,
			wantResult:       "tecINSUFFICIENT_RESERVE",
			wantOwnerCount:   math.MaxUint32,
			wantTicketCount:  0,
			wantSequenceStep: 1,
		},
		{
			name:             "sponsor enabled max count one exact max reserve",
			sponsorEnabled:   true,
			ownerCount:       math.MaxUint32,
			ticketCount:      1,
			balance:          math.MaxUint32,
			wantResult:       "tesSUCCESS",
			wantOwnerCount:   math.MaxUint32,
			wantTicketCount:  1,
			wantSequenceStep: 2,
		},
		{
			name:             "sponsor enabled max minus one count two exact max reserve",
			sponsorEnabled:   true,
			ownerCount:       math.MaxUint32 - 1,
			ticketCount:      2,
			balance:          math.MaxUint32,
			wantResult:       "tesSUCCESS",
			wantOwnerCount:   math.MaxUint32,
			wantTicketCount:  2,
			wantSequenceStep: 3,
		},
		{
			name:             "sponsor disabled max count one below max reserve",
			ownerCount:       math.MaxUint32,
			ticketCount:      1,
			balance:          math.MaxUint32 - 1,
			wantResult:       "tesSUCCESS",
			wantOwnerCount:   math.MaxUint32,
			wantTicketCount:  1,
			wantSequenceStep: 2,
		},
		{
			name:             "sponsor disabled max minus one count two below max reserve",
			ownerCount:       math.MaxUint32 - 1,
			ticketCount:      2,
			balance:          math.MaxUint32 - 1,
			wantResult:       "tesSUCCESS",
			wantOwnerCount:   math.MaxUint32,
			wantTicketCount:  2,
			wantSequenceStep: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.SetReserves(0, 1)
			alice := jtx.NewAccount("alice")
			env.FundAmount(alice, tt.balance)
			if tt.sponsorEnabled {
				env.EnableFeature("Sponsor")
			} else {
				env.DisableFeature("Sponsor")
			}
			env.Close()
			seedTicketOwnerCount(t, env, alice, tt.ownerCount)

			sequenceBefore := env.Seq(alice)
			balanceBefore := env.Balance(alice)
			result := env.Submit(ticket.TicketCreate(alice, tt.ticketCount).Build())
			if tt.wantResult == "tesSUCCESS" {
				jtx.RequireTxSuccess(t, result)
			} else {
				jtx.RequireTxFail(t, result, tt.wantResult)
			}

			require.Equal(t, tt.wantOwnerCount, env.OwnerCount(alice))
			require.Equal(t, tt.wantTicketCount, env.TicketCount(alice))
			require.Equal(t, sequenceBefore+tt.wantSequenceStep, env.Seq(alice))
			require.Equal(t, balanceBefore-env.BaseFee(), env.Balance(alice))

			for i := uint32(0); i < tt.ticketCount; i++ {
				ticketKey := keylet.Ticket(alice.ID, sequenceBefore+1+i)
				require.Equal(t, tt.wantResult == "tesSUCCESS", env.LedgerEntryExists(ticketKey))
			}
			if tt.wantResult != "tesSUCCESS" {
				require.False(t, env.LedgerEntryExists(keylet.OwnerDir(alice.ID)))
			}
		})
	}
}
