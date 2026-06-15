package payment

import (
	"fmt"

	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// creditTrustline credits `account` with `amount` of the issuer's IOU along
// their mutual trust line, auto-creating the line when absent (offer crossing
// creates trust lines on demand). The issuer is the sender. It delegates to the
// shared tx.RippleCredit, which records the credit through the sandbox's
// creditHook; PreviousTxn threading is applied by the apply-state table, so the
// txHash/ledgerSeq the book step threads through other helpers are unused here.
func (s *BookStep) creditTrustline(sb *PaymentSandbox, account, issuer [20]byte, amount tx.Amount, _ [32]byte, _ uint32) error {
	if r := tx.RippleCredit(sb, issuer, account, amount); r != ter.TesSUCCESS {
		return fmt.Errorf("creditTrustline: %s", r)
	}
	return nil
}

// debitTrustline debits `account` by `amount` of the issuer's IOU along their
// mutual trust line: the account is the sender and the issuer the receiver. It
// delegates to the shared tx.RippleCredit, which clears the sender's reserve and
// deletes the line once the holding empties on a fully default line.
func (s *BookStep) debitTrustline(sb *PaymentSandbox, account, issuer [20]byte, amount tx.Amount, _ [32]byte, _ uint32) error {
	if r := tx.RippleCredit(sb, account, issuer, amount); r != ter.TesSUCCESS {
		return fmt.Errorf("debitTrustline: %s", r)
	}
	return nil
}
