package tx

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// TrustCreateParams describes a trust line (RippleState) to create. The field
// names and semantics mirror rippled's trustCreate so every call site (escrow
// unlock, check cash, and eventually the payment engine) can share one
// implementation.
//
// Reference: rippled View.cpp trustCreate (lines 1329-1445).
type TrustCreateParams struct {
	// SrcHigh is rippled's bSrcHigh: true when Src is the high account of the line.
	SrcHigh bool
	// Src and Dst are rippled's uSrcAccountID / uDstAccountID.
	Src, Dst [20]byte
	// LineKey is the RippleState keylet (rippled's uIndex).
	LineKey keylet.Keylet
	// LimitIssuer is the issuer recorded on Limit. bSetDst (and hence which side
	// is the "account being set") is derived as LimitIssuer == Dst.
	LimitIssuer [20]byte
	// Auth, NoRipple, Freeze, DeepFreeze set the matching flag on the
	// account-being-set's side (rippled's bAuth/bNoRipple/bFreeze/bDeepFreeze).
	Auth, NoRipple, Freeze, DeepFreeze bool
	// Balance is stored on the line (negated for the high side, per rippled). Its
	// issuer should be the AccountOne sentinel.
	Balance state.Amount
	// Limit is the account-being-set's limit (issuer == LimitIssuer).
	Limit state.Amount
	// QualityIn and QualityOut are stored on the account-being-set's side when
	// non-zero.
	QualityIn, QualityOut uint32
}

// TrustCreate creates a RippleState (trust line) ledger entry: it inserts the
// line into both the low and high accounts' owner directories, records the
// deletion hints (LowNode/HighNode), stores the balance/limits/qualities, and
// computes the flags exactly as rippled's trustCreate does — including setting
// the peer side's noRipple flag when the peer lacks lsfDefaultRipple.
//
// It does NOT adjust the owner count of the account being set; the caller owns
// that bump so it can route through whichever account object the engine writes
// back (the in-memory ctx.Account for the submitter, or the view for a third
// party). This is the one part of rippled's trustCreate left to the caller.
//
// Reference: rippled View.cpp trustCreate (lines 1329-1445).
func TrustCreate(view LedgerView, p TrustCreateParams) ter.Result {
	var lowAccountID, highAccountID [20]byte
	if p.SrcHigh {
		lowAccountID, highAccountID = p.Dst, p.Src
	} else {
		lowAccountID, highAccountID = p.Src, p.Dst
	}

	// bSetDst: the destination owns the limit (is the account being set).
	// bSetHigh: that account is the high account of the line.
	bSetDst := p.LimitIssuer == p.Dst
	bSetHigh := p.SrcHigh != bSetDst

	currency := p.Balance.Currency

	rs := &state.RippleState{}

	// The peer side carries a zero limit issued by the peer account (rippled
	// stores an Issue-only amount). bSetDst ? Src : Dst is the peer of the
	// account being set.
	peerLimitIssuer := highAccountID
	if bSetHigh {
		peerLimitIssuer = lowAccountID
	}
	peerLimitStr, err := state.EncodeAccountID(peerLimitIssuer)
	if err != nil {
		return ter.TefINTERNAL
	}
	peerLimit := NewIssuedAmount(0, state.MinExponent, currency, peerLimitStr)

	if bSetHigh {
		rs.HighLimit = p.Limit
		rs.LowLimit = peerLimit
		rs.Balance = p.Balance.Negate()
	} else {
		rs.LowLimit = p.Limit
		rs.HighLimit = peerLimit
		rs.Balance = p.Balance
	}

	if p.QualityIn != 0 {
		if bSetHigh {
			rs.HighQualityIn = p.QualityIn
		} else {
			rs.LowQualityIn = p.QualityIn
		}
	}
	if p.QualityOut != 0 {
		if bSetHigh {
			rs.HighQualityOut = p.QualityOut
		} else {
			rs.LowQualityOut = p.QualityOut
		}
	}

	var flags uint32
	flags |= sideFlag(bSetHigh, state.LsfHighReserve, state.LsfLowReserve)
	if p.Auth {
		flags |= sideFlag(bSetHigh, state.LsfHighAuth, state.LsfLowAuth)
	}
	if p.NoRipple {
		flags |= sideFlag(bSetHigh, state.LsfHighNoRipple, state.LsfLowNoRipple)
	}
	if p.Freeze {
		flags |= sideFlag(bSetHigh, state.LsfHighFreeze, state.LsfLowFreeze)
	}
	if p.DeepFreeze {
		flags |= sideFlag(bSetHigh, state.LsfHighDeepFreeze, state.LsfLowDeepFreeze)
	}

	// The peer (other side) gets noRipple when it lacks lsfDefaultRipple.
	peerID := lowAccountID
	if !bSetHigh {
		peerID = highAccountID
	}
	peerData, err := view.Read(keylet.Account(peerID))
	if err != nil {
		return ter.TefINTERNAL
	}
	if peerData == nil {
		// Matches rippled trustCreate: a missing peer account is tecNO_TARGET,
		// not an internal error (View.cpp slePeer null branch).
		return ter.TecNO_TARGET
	}
	peerAcct, err := state.ParseAccountRoot(peerData)
	if err != nil {
		return ter.TefINTERNAL
	}
	if peerAcct.Flags&state.LsfDefaultRipple == 0 {
		flags |= sideFlag(bSetHigh, state.LsfLowNoRipple, state.LsfHighNoRipple)
	}

	rs.Flags = flags

	lowDirKey := keylet.OwnerDir(lowAccountID)
	lowDir, err := state.DirInsert(view, lowDirKey, p.LineKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = lowAccountID
	})
	if err != nil {
		return ter.TecDIR_FULL
	}
	rs.LowNode = lowDir.Page

	highDirKey := keylet.OwnerDir(highAccountID)
	highDir, err := state.DirInsert(view, highDirKey, p.LineKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = highAccountID
	})
	if err != nil {
		return ter.TecDIR_FULL
	}
	rs.HighNode = highDir.Page

	data, err := state.SerializeRippleState(rs)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := view.Insert(p.LineKey, data); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// sideFlag returns highFlag when bSetHigh is true, otherwise lowFlag.
func sideFlag(bSetHigh bool, highFlag, lowFlag uint32) uint32 {
	if bSetHigh {
		return highFlag
	}
	return lowFlag
}
