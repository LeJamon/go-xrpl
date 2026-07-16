package payment

import (
	"math/big"

	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type mptEndpointCache struct {
	in       int64
	srcToDst int64
	out      int64
	dir      DebtDirection
}

// MPTEndpointStep moves an MPT between its issuer and a holder. MPT assets do
// not ripple, so the step is valid only at a strand endpoint.
type MPTEndpointStep struct {
	src                  [20]byte
	dst                  [20]byte
	issue                Issue
	prevStep             Step
	isFirst              bool
	isLast               bool
	offerCrossing        bool
	directBetweenHolders bool
	cache                *mptEndpointCache
}

func NewMPTEndpointStep(
	ctx *StrandContext,
	src, dst [20]byte,
	issue Issue,
	prevStep Step,
	isFirst, isLast bool,
) (*MPTEndpointStep, ter.Result) {
	step := &MPTEndpointStep{
		src:           src,
		dst:           dst,
		issue:         issue,
		prevStep:      prevStep,
		isFirst:       isFirst,
		isLast:        isLast,
		offerCrossing: ctx.OfferCrossing,
	}
	step.directBetweenHolders = issue.Equal(ctx.StrandDeliver) &&
		ctx.StrandSrc != issue.Issuer && ctx.StrandDst != issue.Issuer &&
		(isFirst || (prevStep != nil && prevStep.BookStepBook() == nil))
	if result := step.Check(ctx); result != ter.TesSUCCESS {
		return nil, result
	}
	return step, ter.TesSUCCESS
}

func (s *MPTEndpointStep) maxPaymentFlow(sb *PaymentSandbox) (int64, DebtDirection) {
	if s.src != s.issue.Issuer {
		funds, result := mptutil.Funds(sb, s.issue.MPTID, s.src, false)
		if result != ter.TesSUCCESS {
			return 0, DebtDirectionRedeems
		}
		return funds, DebtDirectionRedeems
	}

	issuance, _, result := mptutil.ReadIssuance(sb, s.issue.MPTID)
	if result != ter.TesSUCCESS {
		return 0, DebtDirectionIssues
	}
	if s.prevStep == nil {
		return int64(mptutil.AvailableAmount(issuance)), DebtDirectionIssues
	}
	return int64(mptutil.MaximumAmount(issuance)), DebtDirectionIssues
}

func (s *MPTEndpointStep) qualities(sb *PaymentSandbox, dir DebtDirection, strandDir StrandDirection) (uint32, uint32) {
	if Redeems(dir) {
		if s.prevStep == nil {
			return QualityOne, QualityOne
		}
		return max(s.prevStep.LineQualityIn(sb), QualityOne), QualityOne
	}

	prevDir := DebtDirectionIssues
	if s.prevStep != nil {
		prevDir = s.prevStep.DebtDirection(sb, strandDir)
	}
	if Redeems(prevDir) {
		return mptutil.TransferRate(sb, s.issue.MPTID), QualityOne
	}
	return QualityOne, QualityOne
}

func (s *MPTEndpointStep) ensureDestinationHolding(sb *PaymentSandbox) ter.Result {
	if !s.offerCrossing || !s.isLast || s.dst == s.issue.Issuer {
		return ter.TesSUCCESS
	}
	return mptutil.EnsureHolding(sb, s.issue.MPTID, s.dst, 0, true)
}

func (s *MPTEndpointStep) send(sb *PaymentSandbox, amount int64) ter.Result {
	return mptutil.Credit(sb, s.issue.MPTID, s.src, s.dst, amount, true)
}

func (s *MPTEndpointStep) resetCache(dir DebtDirection) {
	s.cache = &mptEndpointCache{dir: dir}
}

func (s *MPTEndpointStep) setCacheLimiting(in, srcToDst, out int64, dir DebtDirection) {
	if s.cache.in < in {
		difference := in - s.cache.in
		if difference > 1 && (s.cache.in == 0 || mptForwardDifferenceLarge(in, s.cache.in)) {
			s.cache = &mptEndpointCache{in: in, srcToDst: srcToDst, out: out, dir: dir}
			return
		}
	}
	s.cache.in = in
	s.cache.srcToDst = min(s.cache.srcToDst, srcToDst)
	s.cache.out = min(s.cache.out, out)
	s.cache.dir = dir
}

func mptForwardDifferenceLarge(forward, cached int64) bool {
	left := new(big.Int).Mul(big.NewInt(forward), big.NewInt(100))
	right := new(big.Int).Mul(big.NewInt(cached), big.NewInt(101))
	return left.Cmp(right) > 0
}

func (s *MPTEndpointStep) Rev(
	sb *PaymentSandbox,
	_ *PaymentSandbox,
	_ map[[32]byte]bool,
	out EitherAmount,
) (EitherAmount, EitherAmount) {
	if !out.IsMPT || out.MPTID != s.issue.MPTID {
		zero := ZeroMPTEitherAmount(s.issue.MPTID)
		return zero, zero
	}
	s.cache = nil
	maxSrcToDst, dir := s.maxPaymentFlow(sb)
	srcQOut, _ := s.qualities(sb, dir, StrandDirectionReverse)
	if maxSrcToDst <= 0 {
		s.resetCache(dir)
		zero := ZeroMPTEitherAmount(s.issue.MPTID)
		return zero, zero
	}
	if result := s.ensureDestinationHolding(sb); result != ter.TesSUCCESS {
		s.resetCache(dir)
		zero := ZeroMPTEitherAmount(s.issue.MPTID)
		return zero, zero
	}

	srcToDst := min(out.MPT, maxSrcToDst)
	in := mptMulRatio(srcToDst, srcQOut, QualityOne, true)
	s.cache = &mptEndpointCache{in: in, srcToDst: srcToDst, out: srcToDst, dir: dir}
	if result := s.send(sb, srcToDst); result != ter.TesSUCCESS {
		s.resetCache(dir)
		zero := ZeroMPTEitherAmount(s.issue.MPTID)
		return zero, zero
	}
	return NewMPTEitherAmount(in, s.issue.MPTID), NewMPTEitherAmount(srcToDst, s.issue.MPTID)
}

func (s *MPTEndpointStep) Fwd(
	sb *PaymentSandbox,
	_ *PaymentSandbox,
	_ map[[32]byte]bool,
	in EitherAmount,
) (EitherAmount, EitherAmount) {
	if !in.IsMPT || in.MPTID != s.issue.MPTID || s.cache == nil {
		zero := ZeroMPTEitherAmount(s.issue.MPTID)
		return zero, zero
	}
	maxSrcToDst, dir := s.maxPaymentFlow(sb)
	srcQOut, _ := s.qualities(sb, dir, StrandDirectionForward)
	if maxSrcToDst <= 0 {
		s.resetCache(dir)
		zero := ZeroMPTEitherAmount(s.issue.MPTID)
		return zero, zero
	}
	if result := s.ensureDestinationHolding(sb); result != ter.TesSUCCESS {
		s.resetCache(dir)
		zero := ZeroMPTEitherAmount(s.issue.MPTID)
		return zero, zero
	}

	srcToDst := mptMulRatio(in.MPT, QualityOne, srcQOut, false)
	actualIn := in.MPT
	if srcToDst > maxSrcToDst {
		srcToDst = maxSrcToDst
		actualIn = mptMulRatio(maxSrcToDst, srcQOut, QualityOne, true)
	}
	s.setCacheLimiting(actualIn, srcToDst, srcToDst, dir)
	if result := s.send(sb, s.cache.srcToDst); result != ter.TesSUCCESS {
		s.resetCache(dir)
		zero := ZeroMPTEitherAmount(s.issue.MPTID)
		return zero, zero
	}
	return NewMPTEitherAmount(s.cache.in, s.issue.MPTID), NewMPTEitherAmount(s.cache.out, s.issue.MPTID)
}

func (s *MPTEndpointStep) CachedIn() *EitherAmount {
	if s.cache == nil {
		return nil
	}
	value := NewMPTEitherAmount(s.cache.in, s.issue.MPTID)
	return &value
}

func (s *MPTEndpointStep) CachedOut() *EitherAmount {
	if s.cache == nil {
		return nil
	}
	value := NewMPTEitherAmount(s.cache.out, s.issue.MPTID)
	return &value
}

func (s *MPTEndpointStep) DebtDirection(_ *PaymentSandbox, dir StrandDirection) DebtDirection {
	if dir == StrandDirectionForward && s.cache != nil {
		return s.cache.dir
	}
	if s.src == s.issue.Issuer {
		return DebtDirectionIssues
	}
	return DebtDirectionRedeems
}

func (s *MPTEndpointStep) QualityUpperBound(v *PaymentSandbox, prevStepDir DebtDirection) (*Quality, DebtDirection) {
	dir := s.DebtDirection(v, StrandDirectionForward)
	srcQOut := QualityOne
	if Redeems(dir) {
		if s.prevStep != nil {
			srcQOut = max(s.prevStep.LineQualityIn(v), QualityOne)
		}
	} else if Redeems(prevStepDir) {
		srcQOut = mptutil.TransferRate(v, s.issue.MPTID)
	}
	out := NewMPTEitherAmount(int64(QualityOne), s.issue.MPTID)
	in := NewMPTEitherAmount(int64(srcQOut), s.issue.MPTID)
	quality := QualityFromAmounts(in, out)
	return &quality, dir
}

func (s *MPTEndpointStep) GetQualityFunc(v *PaymentSandbox, prevStepDir DebtDirection) (*QualityFunction, DebtDirection) {
	quality, dir := s.QualityUpperBound(v, prevStepDir)
	if quality == nil {
		return nil, dir
	}
	return newCLOBLikeQualityFunction(numberMath{ctx: v.NumberContext()}, *quality), dir
}

func (s *MPTEndpointStep) IsZero(amount EitherAmount) bool {
	return !amount.IsMPT || amount.MPTID != s.issue.MPTID || amount.MPT == 0
}

func (s *MPTEndpointStep) EqualIn(a, b EitherAmount) bool {
	return a.IsMPT && b.IsMPT && a.MPTID == b.MPTID && a.MPT == b.MPT
}

func (s *MPTEndpointStep) EqualOut(a, b EitherAmount) bool { return s.EqualIn(a, b) }
func (s *MPTEndpointStep) Inactive() bool                  { return false }
func (s *MPTEndpointStep) OffersUsed() uint32              { return 0 }

func (s *MPTEndpointStep) DirectStepAccts() *[2][20]byte {
	accounts := [2][20]byte{s.src, s.dst}
	return &accounts
}

func (s *MPTEndpointStep) BookStepBook() *Book { return nil }

func (s *MPTEndpointStep) LineQualityIn(_ *PaymentSandbox) uint32 { return QualityOne }

func (s *MPTEndpointStep) ValidFwd(sb *PaymentSandbox, afView *PaymentSandbox, in EitherAmount) (bool, EitherAmount) {
	if s.cache == nil || !in.IsMPT || in.MPTID != s.issue.MPTID {
		return false, ZeroMPTEitherAmount(s.issue.MPTID)
	}
	saved := *s.cache
	maxSrcToDst, _ := s.maxPaymentFlow(sb)
	_, out := s.Fwd(sb, afView, nil, in)
	if s.cache == nil || s.cache.srcToDst > maxSrcToDst {
		return false, out
	}
	if saved.in != s.cache.in || saved.out != s.cache.out {
		return false, out
	}
	return true, out
}

func (s *MPTEndpointStep) Check(ctx *StrandContext) ter.Result {
	if s.src == [20]byte{} || s.dst == [20]byte{} || s.src == s.dst || !s.issue.IsMPT {
		return ter.TemBAD_PATH
	}
	if exists, err := ctx.View.Exists(keylet.Account(s.src)); err != nil {
		return ter.TefINTERNAL
	} else if !exists {
		return ter.TerNO_ACCOUNT
	}

	if !(s.isFirst && s.isLast) {
		account := s.dst
		if s.isFirst {
			account = s.src
		}
		if (s.isFirst && mptutil.IsGlobalFrozen(ctx.View, s.issue.MPTID)) ||
			mptutil.IsIndividualFrozen(ctx.View, s.issue.MPTID, account) {
			return ter.TerLOCKED
		}
	}
	if ctx.SeenBookOuts[s.issue] {
		if s.prevStep == nil {
			return ter.TemBAD_PATH_LOOP
		}
		if book := s.prevStep.BookStepBook(); book != nil && !book.Out.Equal(s.issue) {
			return ter.TemBAD_PATH_LOOP
		}
	}
	if s.isFirst {
		if ctx.SeenDirectIssues[0][s.issue] {
			return ter.TemBAD_PATH_LOOP
		}
		ctx.SeenDirectIssues[0][s.issue] = true
	}
	if s.isLast {
		if ctx.SeenDirectIssues[1][s.issue] {
			return ter.TemBAD_PATH_LOOP
		}
		ctx.SeenDirectIssues[1][s.issue] = true
	}
	if !s.isFirst && !s.isLast {
		return ter.TemBAD_PATH
	}
	issuer := s.issue.Issuer
	if (s.src != issuer && s.dst != issuer) || (s.src == issuer && s.dst == issuer) {
		return ter.TemBAD_PATH
	}

	if s.offerCrossing {
		return ter.TesSUCCESS
	}
	if s.src != issuer {
		if result := mptutil.RequireAuthAt(ctx.View, s.issue.MPTID, s.src, true, ctx.ParentCloseTime); result != ter.TesSUCCESS {
			return result
		}
	}
	if s.dst != issuer {
		if result := mptutil.RequireAuthAt(ctx.View, s.issue.MPTID, s.dst, true, ctx.ParentCloseTime); result != ter.TesSUCCESS {
			return result
		}
	}
	if s.issue.Equal(ctx.StrandDeliver) &&
		(s.isFirst || (s.prevStep != nil && s.prevStep.BookStepBook() == nil)) {
		if s.directBetweenHolders {
			holder := s.dst
			if s.isFirst {
				holder = s.src
			}
			if mptutil.IsFrozen(ctx.View, s.issue.MPTID, holder) {
				return ter.TecLOCKED
			}
			if result := mptutil.CanTransfer(ctx.View, s.issue.MPTID, holder, ctx.StrandDst); result != ter.TesSUCCESS {
				return result
			}
		}
	} else if result := mptutil.CanTrade(ctx.View, s.issue.MPTID); result != ter.TesSUCCESS {
		return result
	}
	if s.prevStep == nil {
		funds, result := mptutil.Funds(ctx.View, s.issue.MPTID, s.src, false)
		if result != ter.TesSUCCESS || funds <= 0 {
			return ter.TecPATH_DRY
		}
	}
	return ter.TesSUCCESS
}
