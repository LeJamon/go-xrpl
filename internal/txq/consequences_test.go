package txq

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/escrow"
	"github.com/LeJamon/go-xrpl/internal/tx/offer"
	"github.com/LeJamon/go-xrpl/internal/tx/paychan"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/sponsor"
	"github.com/LeJamon/go-xrpl/internal/tx/ticket"
	"github.com/LeJamon/go-xrpl/internal/tx/xchain"
)

const consequencesAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

// TestConsequencesEveryRegisteredType pins the queue consequences of a bare
// instance of every registered transaction type.
func TestConsequencesEveryRegisteredType(t *testing.T) {
	blockers := map[tx.Type]bool{
		tx.TypeRegularKeySet:                true,
		tx.TypeSignerListSet:                true,
		tx.TypeAccountDelete:                true,
		tx.TypeXChainClaim:                  true,
		tx.TypeXChainAddClaimAttestation:    true,
		tx.TypeXChainAddAccountCreateAttest: true,
	}
	types := tx.SupportedTypes()
	if len(types) == 0 {
		t.Fatal("no transaction types registered")
	}
	for _, txType := range types {
		txn, err := tx.NewFromType(txType)
		if err != nil {
			t.Fatal(err)
		}
		txn.GetCommon().Fee = "12"
		got := computeConsequences(txn, NewSeqProxySequence(7))
		want := txConsequences{Fee: 12, FollowingSeq: NewSeqProxySequence(8), IsBlocker: blockers[txType]}
		if got != want {
			t.Errorf("%s: consequences = %+v, want %+v", txType, got, want)
		}
		if ticketed := computeConsequences(txn, NewSeqProxyTicket(7)); ticketed.FollowingSeq != NewSeqProxyTicket(7) {
			t.Errorf("%s: ticketed following seq = %+v", txType, ticketed.FollowingSeq)
		}
	}
}

func TestConsequencesCustomTypes(t *testing.T) {
	xrp := tx.NewXRPAmount
	iou := tx.NewIssuedAmount(1, 0, "USD", consequencesAccount)
	withSendMax := func(amount, sendMax tx.Amount) tx.Transaction {
		p := payment.NewPayment(consequencesAccount, consequencesAccount, amount)
		p.SendMax = &sendMax
		return p
	}
	feeDelta := func(delta tx.Amount) tx.Transaction {
		s := sponsor.NewSponsorshipSet(consequencesAccount)
		s.FeeAmountDelta = &delta
		return s
	}
	accountSet := func(flags, setFlag, clearFlag *uint32) tx.Transaction {
		a := account.NewAccountSet(consequencesAccount)
		if flags != nil {
			a.SetFlags(*flags)
		}
		a.SetFlag = setFlag
		a.ClearFlag = clearFlag
		return a
	}
	u32 := func(v uint32) *uint32 { return &v }

	cases := []struct {
		name      string
		txn       tx.Transaction
		spend     uint64
		blocker   bool
		following uint32
	}{
		{"payment xrp", payment.NewPayment(consequencesAccount, consequencesAccount, xrp(100)), 100, false, 2},
		{"payment iou", payment.NewPayment(consequencesAccount, consequencesAccount, iou), 0, false, 2},
		{"payment xrp sendmax", withSendMax(xrp(100), xrp(150)), 150, false, 2},
		{"payment iou sendmax", withSendMax(xrp(100), iou), 0, false, 2},
		{"offer xrp gets", offer.NewOfferCreate(consequencesAccount, xrp(50), iou), 50, false, 2},
		{"offer iou gets", offer.NewOfferCreate(consequencesAccount, iou, xrp(50)), 0, false, 2},
		{"escrow xrp", escrow.NewEscrowCreate(consequencesAccount, consequencesAccount, xrp(70)), 70, false, 2},
		{"escrow iou", escrow.NewEscrowCreate(consequencesAccount, consequencesAccount, iou), 0, false, 2},
		{"paychan create", paychan.NewPaymentChannelCreate(consequencesAccount, consequencesAccount, xrp(80), 1, ""), 80, false, 2},
		{"paychan fund", paychan.NewPaymentChannelFund(consequencesAccount, "", xrp(90)), 90, false, 2},
		{"xchain commit xrp", xchain.NewXChainCommit(consequencesAccount, xchain.XChainBridge{}, 1, xrp(60)), 60, false, 2},
		{"xchain commit iou", xchain.NewXChainCommit(consequencesAccount, xchain.XChainBridge{}, 1, iou), 0, false, 2},
		{"sponsorship positive", feeDelta(xrp(40)), 40, false, 2},
		{"sponsorship negative", feeDelta(xrp(-40)), 0, false, 2},
		{"sponsorship iou", feeDelta(iou), 0, false, 2},
		{"ticket create", ticket.NewTicketCreate(consequencesAccount, 5), 0, false, 6},
		{"accountset plain", accountSet(nil, u32(account.AccountSetFlagDefaultRipple), nil), 0, false, 2},
		{"accountset require auth flag", accountSet(u32(account.AccountSetTxFlagRequireAuth), nil, nil), 0, true, 2},
		{"accountset optional auth flag", accountSet(u32(account.AccountSetTxFlagOptionalAuth), nil, nil), 0, true, 2},
		{"accountset set disable master", accountSet(nil, u32(account.AccountSetFlagDisableMaster), nil), 0, true, 2},
		{"accountset clear account txn id", accountSet(nil, nil, u32(account.AccountSetFlagAccountTxnID)), 0, true, 2},
		{"accountset set require auth", accountSet(nil, u32(account.AccountSetFlagRequireAuth), nil), 0, true, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.txn.GetCommon().Fee = "10"
			got := computeConsequences(c.txn, NewSeqProxySequence(1))
			want := txConsequences{Fee: 10, PotentialSpend: c.spend, IsBlocker: c.blocker, FollowingSeq: NewSeqProxySequence(c.following)}
			if got != want {
				t.Errorf("consequences = %+v, want %+v", got, want)
			}
		})
	}
}
