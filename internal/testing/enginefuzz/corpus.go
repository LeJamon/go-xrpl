package enginefuzz

import jtx "github.com/LeJamon/go-xrpl/internal/testing"

type namedSeed struct {
	Name  string
	Trace trace
}

func seedCorpus() []namedSeed {
	xrp := func(value int64) uint64 { return uint64(jtx.XRP(value)) }
	return []namedSeed{
		{
			Name: "all-core-kinds",
			Trace: trace{Profile: profileV320, Steps: []traceStep{
				{Kind: kindPaymentXRP, From: 0, To: 1, Amount: xrp(1), Limit: 1},
				{Kind: kindPaymentIOU, From: 1, To: 2, Currency: 0, Amount: 5_000_000, Limit: 1},
				{Kind: kindAccountSet, From: 2, To: 3, Option: 2, Amount: 1, Limit: 1},
				{Kind: kindTrustSet, From: 3, To: 0, Currency: 1, Option: 1, Amount: 1, Limit: 500_000},
				{Kind: kindOfferCreate, From: 0, To: 1, Currency: 0, Amount: xrp(2), Limit: 10_000},
				{Kind: kindOfferCancel, From: 0, To: 1, Amount: 1, Limit: 1},
			}},
		},
		{
			Name: "offer-create-cross-cancel",
			Trace: trace{Profile: profileV320, Steps: []traceStep{
				{Kind: kindOfferCreate, From: 0, To: 1, Currency: 0, Amount: xrp(100), Limit: 100_000},
				{Kind: kindOfferCreate, From: 1, To: 0, Currency: 0, Option: 1, Amount: xrp(100), Limit: 100_000},
				{Kind: kindOfferCancel, From: 0, To: 1, Amount: 1, Limit: 1},
			}},
		},
		{
			Name: "accountset-dependent-payment",
			Trace: trace{Profile: profileV320, Steps: []traceStep{
				{Kind: kindAccountSet, From: 1, To: 0, Option: 0, Amount: 1, Limit: 1},
				{Kind: kindPaymentXRP, From: 0, To: 1, Amount: xrp(1), Limit: 1},
				{Kind: kindPaymentXRP, From: 0, To: 1, Option: 1, Amount: xrp(1), Limit: 1},
				{Kind: kindAccountSet, From: 1, To: 0, Option: 1, Amount: 1, Limit: 1},
			}},
		},
		{
			Name: "trust-flag-payment-lifecycle",
			Trace: trace{Profile: profileV320, Steps: []traceStep{
				{Kind: kindTrustSet, From: 0, To: 1, Currency: 0, Option: 2, Amount: 1, Limit: 1_000_000},
				{Kind: kindPaymentIOU, From: 0, To: 1, Currency: 0, Amount: 1_000_000, Limit: 1},
				{Kind: kindTrustSet, From: 0, To: 1, Currency: 0, Option: 3, Amount: 1, Limit: 1_000_000},
				{Kind: kindPaymentIOU, From: 0, To: 1, Currency: 0, Amount: 1_000_000, Limit: 1},
			}},
		},
		{
			Name: "pre-post-close",
			Trace: trace{Profile: profileV320, Steps: []traceStep{
				{Kind: kindPaymentXRP, From: 2, To: 3, Amount: xrp(2), Limit: 1, CloseAfter: true},
				{Kind: kindOfferCreate, From: 3, To: 2, Currency: 1, Amount: xrp(3), Limit: 20_000, CloseAfter: true},
				{Kind: kindPaymentIOU, From: 3, To: 2, Currency: 1, Amount: 2_000_000, Limit: 1},
			}},
		},
	}
}
