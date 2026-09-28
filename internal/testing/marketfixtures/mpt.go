// Package marketfixtures supplies shared MPT book and amount vectors for flow and RPC tests.
package marketfixtures

type Asset struct {
	Currency [20]byte
	Issuer   [20]byte
	MPTID    [24]byte
}

type Book struct {
	Name       string
	Pays, Gets Asset
	Domain     *[32]byte
	Base       string
}

func Books() []Book {
	xrp := Asset{}
	usd := Asset{Currency: [20]byte{12: 'U', 13: 'S', 14: 'D'}, Issuer: [20]byte{2}}
	mpt1 := Asset{MPTID: [24]byte{3: 1, 4: 1}}
	mpt2 := Asset{MPTID: [24]byte{3: 2, 4: 2}}
	domain := [32]byte{0xab, 0xcd}
	return []Book{
		{Name: "xrp/mpt1", Pays: xrp, Gets: mpt1, Domain: nil, Base: "749788d1cdbd996235ad53856d8e828e5cd6a793856c345d0000000000000000"},
		{Name: "xrp/mpt1/domain", Pays: xrp, Gets: mpt1, Domain: &domain, Base: "387ba92bf15b7a0355f9953751803e5188cd3c6b6332602a0000000000000000"},
		{Name: "mpt1/xrp", Pays: mpt1, Gets: xrp, Domain: nil, Base: "831bee089db2b5f798a1f02be914a92478278ff077fa65810000000000000000"},
		{Name: "mpt1/xrp/domain", Pays: mpt1, Gets: xrp, Domain: &domain, Base: "872c92b19c5c280e09b3f9285a8ffb139775cfd75e08e1280000000000000000"},
		{Name: "usd/mpt1", Pays: usd, Gets: mpt1, Domain: nil, Base: "5d516693f2e78115ff7c2d6ee8e925f7a0847b327bef15e60000000000000000"},
		{Name: "usd/mpt1/domain", Pays: usd, Gets: mpt1, Domain: &domain, Base: "cdb31cfe0ad85f137d4768d333ed2757f234d4496f788dad0000000000000000"},
		{Name: "mpt1/usd", Pays: mpt1, Gets: usd, Domain: nil, Base: "6bd691bc90a1be5f078aa64f11e20fc87c938fa76ccd30c50000000000000000"},
		{Name: "mpt1/usd/domain", Pays: mpt1, Gets: usd, Domain: &domain, Base: "0d5a800ade46947dd23e64e4ecc2add9593c2c1163044deb0000000000000000"},
		{Name: "mpt1/mpt2", Pays: mpt1, Gets: mpt2, Domain: nil, Base: "2863347393b774b37f936c92e22475a59729d30ee2ce58eb0000000000000000"},
		{Name: "mpt1/mpt2/domain", Pays: mpt1, Gets: mpt2, Domain: &domain, Base: "691d1fa949d7a8412b21151d6e6eb011b4f4fbfe50cbd0aa0000000000000000"},
		{Name: "mpt2/mpt1", Pays: mpt2, Gets: mpt1, Domain: nil, Base: "e64fa403e859627ee150ba8b308152a93d66463be24c87c80000000000000000"},
		{Name: "mpt2/mpt1/domain", Pays: mpt2, Gets: mpt1, Domain: &domain, Base: "3e05a58ce6ac2bdf0570d794d3d65515d7fbcef5bb25379b0000000000000000"},
	}
}

type MPTFee struct {
	Name               string
	Funds              int64
	Fee                uint16
	Delivered, Debited int64
}

// MPTFees records floor-limited delivery and the corresponding ceiling debit.
func MPTFees() []MPTFee {
	return []MPTFee{
		{Name: "parity", Funds: 100, Fee: 0, Delivered: 100, Debited: 100},
		{Name: "fractional", Funds: 101, Fee: 10000, Delivered: 91, Debited: 101},
		{Name: "dust", Funds: 2, Fee: 10000, Delivered: 1, Debited: 2},
		{Name: "maximum", Funds: 9223372036854775807, Fee: 0, Delivered: 9223372036854775807, Debited: 9223372036854775807},
		{Name: "maximum-fee", Funds: 9223372036854775807, Fee: 10000, Delivered: 8384883669867978006, Debited: 9223372036854775807},
		{Name: "maximum-rate", Funds: 9223372036854775807, Fee: 50000, Delivered: 6148914691236517204, Debited: 9223372036854775806},
	}
}
