// Behavioral vectors from rippled's AMM_test.cpp and AMMExtended_test.cpp.
package amm_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
)

func TestAMMBookStep_FixChangeSpotPriceQuality(t *testing.T) {
	type Status int
	const (
		SucceedShouldSucceedResize Status = iota
		FailShouldSucceed
		SucceedShouldFail
		Fail
		Succeed
	)

	type testCase struct {
		poolInStr  string
		poolOutStr string
		quality    paymenttx.Quality
		fee        uint16
		status     Status
	}

	// Quality from amounts helper — matches rippled's Quality{TAmounts{in, out}}
	xrpIouQ10_100 := paymenttx.QualityFromAmounts(
		paymenttx.NewXRPEitherAmount(10),
		paymenttx.NewIOUEitherAmount(tx.NewIssuedAmountFromFloat64(100, "", "")),
	)
	iouXrpQ10_100 := paymenttx.QualityFromAmounts(
		paymenttx.NewIOUEitherAmount(tx.NewIssuedAmountFromFloat64(10, "", "")),
		paymenttx.NewXRPEitherAmount(100),
	)

	tests := []testCase{
		// FailShouldSucceed (12 cases)
		{"0.001519763260828713", "1558701", paymenttx.Quality{Value: 5414253689393440221}, 1000, FailShouldSucceed},
		{"0.01099814367603737", "1892611", paymenttx.Quality{Value: 5482264816516900274}, 1000, FailShouldSucceed},
		{"0.78", "796599", paymenttx.Quality{Value: 5630392334958379008}, 1000, FailShouldSucceed},
		{"105439.2955578965", "49398693", paymenttx.Quality{Value: 5910869983721805038}, 400, FailShouldSucceed},
		{"12408293.23445213", "4340810521", paymenttx.Quality{Value: 5911611095910090752}, 997, FailShouldSucceed},
		{"1892611", "0.01099814367603737", paymenttx.Quality{Value: 6703103457950430139}, 1000, FailShouldSucceed},
		{"423028.8508101858", "3392804520", paymenttx.Quality{Value: 5837920340654162816}, 600, FailShouldSucceed},
		{"44565388.41001027", "73890647", paymenttx.Quality{Value: 6058976634606450001}, 1000, FailShouldSucceed},
		{"66831.68494832662", "16", paymenttx.Quality{Value: 6346111134641742975}, 0, FailShouldSucceed},
		{"675.9287302203422", "1242632304", paymenttx.Quality{Value: 5625960929244093294}, 300, FailShouldSucceed},
		{"7047.112186735699", "1649845866", paymenttx.Quality{Value: 5696855348026306945}, 504, FailShouldSucceed},
		{"840236.4402981238", "47419053", paymenttx.Quality{Value: 5982561601648018688}, 499, FailShouldSucceed},

		// SucceedShouldSucceedResize (6 cases)
		{"992715.618909774", "189445631733", paymenttx.Quality{Value: 5697835648288106944}, 815, SucceedShouldSucceedResize},
		{"504636667521", "185545883.9506651", paymenttx.Quality{Value: 6343802275337659280}, 503, SucceedShouldSucceedResize},
		{"992706.7218636649", "189447316000", paymenttx.Quality{Value: 5697835648288106944}, 797, SucceedShouldSucceedResize},
		{"1.068737911388205", "127860278877", paymenttx.Quality{Value: 5268604356368739396}, 293, SucceedShouldSucceedResize},
		{"17932506.56880419", "189308.6043676173", paymenttx.Quality{Value: 6206460598195440068}, 311, SucceedShouldSucceedResize},
		{"1.066379294658174", "128042251493", paymenttx.Quality{Value: 5268559341368739328}, 270, SucceedShouldSucceedResize},

		// Fail (14 cases)
		{"350131413924", "1576879.110907892", paymenttx.Quality{Value: 6487411636539049449}, 650, Fail},
		{"422093460", "2.731797662057464", paymenttx.Quality{Value: 6702911108534394924}, 1000, Fail},
		{"76128132223", "367172.7148422662", paymenttx.Quality{Value: 6487263463413514240}, 548, Fail},
		{"132701839250", "280703770.7695443", paymenttx.Quality{Value: 6273750681188885075}, 562, Fail},
		{"994165.7604612011", "189551302411", paymenttx.Quality{Value: 5697835592690668727}, 815, Fail},
		{"45053.33303227917", "86612695359", paymenttx.Quality{Value: 5625695218943638190}, 500, Fail},
		{"199649.077043865", "14017933007", paymenttx.Quality{Value: 5766034667318524880}, 324, Fail},
		{"27751824831.70903", "78896950", paymenttx.Quality{Value: 6272538159621630432}, 500, Fail},
		{"225.3731275781907", "156431793648", paymenttx.Quality{Value: 5477818047604078924}, 989, Fail},
		{"199649.077043865", "14017933007", paymenttx.Quality{Value: 5766036094462806309}, 324, Fail},
		{"3.590272027140361", "20677643641", paymenttx.Quality{Value: 5406056147042156356}, 808, Fail},
		{"1.070884664490231", "127604712776", paymenttx.Quality{Value: 5268620608623825741}, 293, Fail},
		{"3272.448829820197", "6275124076", paymenttx.Quality{Value: 5625710328924117902}, 81, Fail},
		{"0.009059512633902926", "7994028", paymenttx.Quality{Value: 5477511954775533172}, 1000, Fail},
		{"1", "1.0", paymenttx.Quality{Value: 0}, 100, Fail},
		{"1.0", "1", paymenttx.Quality{Value: 0}, 100, Fail},
		{"10", "10.0", xrpIouQ10_100, 100, Fail},
		{"10.0", "10", iouXrpQ10_100, 100, Fail},

		// Succeed (15 cases)
		{"69864389131", "287631.4543025075", paymenttx.Quality{Value: 6487623473313516078}, 451, Succeed},
		{"4328342973", "12453825.99247381", paymenttx.Quality{Value: 6272522264364865181}, 997, Succeed},
		{"32347017", "7003.93031579449", paymenttx.Quality{Value: 6347261126087916670}, 1000, Succeed},
		{"61697206161", "36631.4583206413", paymenttx.Quality{Value: 6558965195382476659}, 500, Succeed},
		{"1654524979", "7028.659825511603", paymenttx.Quality{Value: 6487551345110052981}, 504, Succeed},
		{"88621.22277293179", "5128418948", paymenttx.Quality{Value: 5766347291552869205}, 380, Succeed},
		{"1892611", "0.01099814367603737", paymenttx.Quality{Value: 6703102780512015436}, 1000, Succeed},
		{"4542.639373338766", "24554809", paymenttx.Quality{Value: 5838994982188783710}, 0, Succeed},
		{"5132932546", "88542.99750172683", paymenttx.Quality{Value: 6419203342950054537}, 380, Succeed},
		{"78929964.1549083", "1506494795", paymenttx.Quality{Value: 5986890029845558688}, 589, Succeed},
		{"10096561906", "44727.72453735605", paymenttx.Quality{Value: 6487455290284644551}, 250, Succeed},
		{"5092.219565514988", "8768257694", paymenttx.Quality{Value: 5626349534958379008}, 503, Succeed},
		{"1819778294", "8305.084302902864", paymenttx.Quality{Value: 6487429398998540860}, 415, Succeed},
		{"6970462.633911943", "57359281", paymenttx.Quality{Value: 6054087899185946624}, 850, Succeed},
		{"3983448845", "2347.543644281467", paymenttx.Quality{Value: 6558965195382476659}, 856, Succeed},

		// SucceedShouldFail (1 case)
		{"771493171", "1.243473020567508", paymenttx.Quality{Value: 6707566798038544272}, 100, SucceedShouldFail},
	}

	// Helper: determine if string represents XRP drops (all digits, no decimal point)
	isXRPStr := func(s string) bool {
		for _, c := range s {
			if c < '0' || c > '9' {
				return false
			}
		}
		return len(s) > 0
	}

	// Helper: parse pool amount from string
	parsePool := func(s string, isXRP bool) tx.Amount {
		if isXRP {
			var drops int64
			for _, c := range s {
				drops = drops*10 + int64(c-'0')
			}
			return tx.NewXRPAmount(drops)
		}
		f := 0.0
		fmt.Sscanf(s, "%f", &f)
		return tx.NewIssuedAmountFromFloat64(f, "", "")
	}

	// Run tests for both amendment states
	for _, fixAMMv1_1 := range []bool{false, true} {
		label := "PreFix"
		if fixAMMv1_1 {
			label = "PostFix"
		}
		t.Run(label, func(t *testing.T) {
			for i, tc := range tests {
				poolInIsXRP := isXRPStr(tc.poolInStr)
				poolOutIsXRP := isXRPStr(tc.poolOutStr)

				poolIn := parsePool(tc.poolInStr, poolInIsXRP)
				poolOut := parsePool(tc.poolOutStr, poolOutIsXRP)

				takerPays, takerGets, ok, _ := paymenttx.ChangeSpotPriceQuality(
					poolIn, poolOut, tc.quality, tc.fee, fixAMMv1_1, poolOutIsXRP,
				)

				if ok {
					offerQ := paymenttx.QualityFromAmounts(
						paymenttx.ToEitherAmt(takerPays),
						paymenttx.ToEitherAmt(takerGets),
					)

					switch tc.status {
					case SucceedShouldSucceedResize:
						if !fixAMMv1_1 {
							if !(offerQ.WorseThan(tc.quality)) {
								t.Errorf("[%d] PreFix SucceedShouldSucceedResize: expected quality < target, got q=%d target=%d", i, offerQ.Value, tc.quality.Value)
							}
						} else {
							if offerQ.WorseThan(tc.quality) {
								t.Errorf("[%d] PostFix SucceedShouldSucceedResize: expected quality >= target, got q=%d target=%d", i, offerQ.Value, tc.quality.Value)
							}
						}
					case Succeed:
						if !fixAMMv1_1 {
							if offerQ.WorseThan(tc.quality) && !paymenttx.WithinRelativeDistance(offerQ, tc.quality, 1e-7) {
								t.Errorf("[%d] PreFix Succeed: quality worse and not within tolerance, got q=%d target=%d", i, offerQ.Value, tc.quality.Value)
							}
						} else {
							if offerQ.WorseThan(tc.quality) {
								t.Errorf("[%d] PostFix Succeed: expected quality >= target, got q=%d target=%d", i, offerQ.Value, tc.quality.Value)
							}
						}
					case FailShouldSucceed:
						if !fixAMMv1_1 {
							t.Errorf("[%d] PreFix FailShouldSucceed: expected failure (no result), got success", i)
						} else {
							if offerQ.WorseThan(tc.quality) {
								t.Errorf("[%d] PostFix FailShouldSucceed: expected quality >= target, got q=%d target=%d", i, offerQ.Value, tc.quality.Value)
							}
						}
					case SucceedShouldFail:
						if fixAMMv1_1 {
							t.Errorf("[%d] PostFix SucceedShouldFail: expected failure (no result), got success", i)
						} else {
							if !(offerQ.WorseThan(tc.quality)) {
								t.Errorf("[%d] PreFix SucceedShouldFail: expected quality < target", i)
							}
							if !paymenttx.WithinRelativeDistance(offerQ, tc.quality, 1e-7) {
								t.Errorf("[%d] PreFix SucceedShouldFail: expected within tolerance", i)
							}
						}
					case Fail:
						t.Errorf("[%d] Fail: expected no offer, got quality q=%d for target=%d", i, offerQ.Value, tc.quality.Value)
					}
				} else {
					// No result
					switch tc.status {
					case Fail:
						// Expected failure — verify tiny offer quality < target if non-zero quality
						if tc.quality.Value != 0 {
							if poolInIsXRP {
								takerPays := tx.NewXRPAmount(1) // 1 drop
								takerGets := paymenttx.SwapAssetIn(poolIn, poolOut, takerPays, tc.fee, fixAMMv1_1)
								tinyQ := paymenttx.QualityFromAmounts(
									paymenttx.ToEitherAmt(takerPays),
									paymenttx.ToEitherAmt(takerGets),
								)
								if !(tinyQ.WorseThan(tc.quality)) {
									t.Errorf("[%d] Fail: tiny offer quality should be worse than target, got q=%d target=%d", i, tinyQ.Value, tc.quality.Value)
								}
							} else if poolOutIsXRP {
								takerGets := tx.NewXRPAmount(1) // 1 drop
								takerPays := paymenttx.SwapAssetOut(poolIn, poolOut, takerGets, tc.fee, fixAMMv1_1)
								tinyQ := paymenttx.QualityFromAmounts(
									paymenttx.ToEitherAmt(takerPays),
									paymenttx.ToEitherAmt(takerGets),
								)
								if !(tinyQ.WorseThan(tc.quality)) {
									t.Errorf("[%d] Fail: tiny offer quality should be worse than target, got q=%d target=%d", i, tinyQ.Value, tc.quality.Value)
								}
							}
						}
					case FailShouldSucceed:
						if fixAMMv1_1 {
							t.Errorf("[%d] PostFix FailShouldSucceed: expected success, got failure", i)
						}
						// Pre-fix failure is expected
					case SucceedShouldFail:
						if !fixAMMv1_1 {
							t.Errorf("[%d] PreFix SucceedShouldFail: expected success, got failure", i)
						}
						// Post-fix failure is expected
					case SucceedShouldSucceedResize:
						t.Errorf("[%d] %s SucceedShouldSucceedResize: expected success, got failure", i, label)
					case Succeed:
						t.Errorf("[%d] %s Succeed: expected success, got failure", i, label)
					}
				}
			}
		})
	}

	// Test negative discriminant
	t.Run("NegativeDiscriminant", func(t *testing.T) {
		one := tx.NewIssuedAmountFromFloat64(1, "", "")
		res := paymenttx.SolveQuadraticEqSmallest(one, one, one)
		if res != nil {
			t.Errorf("Expected nil for negative discriminant (1^2 - 4*1*1 = -3), got %v", res)
		}
	})
}

func TestAMMBookStep_LimitQuality(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(30000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(10000)))
	env.TestEnv.FundAmount(env.Bob, uint64(jtx.XRP(10000)))
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(10000)))
	env.Close()

	env.Trust(env.Alice, env.GW, "USD", 10000)
	env.Trust(env.Bob, env.GW, "USD", 10000)
	env.Trust(env.Carol, env.GW, "USD", 10000)
	env.Close()

	env.PayIOU(env.GW, env.Alice, "USD", 2000)
	env.PayIOU(env.GW, env.Bob, "USD", 2000)
	env.PayIOU(env.GW, env.Carol, "USD", 2000)
	env.Close()

	// Bob creates AMM: XRP(1000)/USD(1050)
	createTx := amm.AMMCreate(env.Bob,
		amm.XRPAmount(1000),
		amm.IOUAmount(env.GW, "USD", 1050)).Build()
	jtx.RequireTxSuccess(t, env.Submit(createTx))
	env.Close()

	ammAcc := amm.AMMAccount(t, env, amm.XRP(),
		tx.Asset{Currency: "USD", Issuer: env.GW.Address})

	// Bob creates CLOB offer: buy XRP(100), sell USD(50) — quality 0.5 (worse)
	offerTx := offerbuild.OfferCreate(env.Bob,
		amm.XRPAmount(100),
		amm.IOUAmount(env.GW, "USD", 50)).Build()
	jtx.RequireTxSuccess(t, env.Submit(offerTx))
	env.Close()

	// alice pays carol USD(100) with sendmax XRP(100), path(~USD),
	// tfNoRippleDirect | tfPartialPayment | tfLimitQuality
	payTx := payment.PayIssued(env.Alice, env.Carol,
		amm.IOUAmount(env.GW, "USD", 100)).
		SendMax(amm.XRPAmount(100)).
		PathsCurrency("USD", env.GW).
		PartialPayment().
		LimitQuality().
		NoDirectRipple().
		Build()
	jtx.RequireTxSuccess(t, env.Submit(payTx))

	// AMM: took 50 XRP, gave 50 USD → XRP(1050), USD(1000)
	env.ExpectAMMBalances(t, ammAcc,
		uint64(jtx.XRP(1050)), env.GW, "USD", 1000)

	// Carol: 2000 + 50 = 2050
	requireAMMIOUBalance(t, env.TestEnv, env.Carol, env.GW, "USD", 2050)

	// Bob's offer should still exist (quality too bad for limit)
	offerbuild.RequireOfferCount(t, env.TestEnv, env.Bob, 1)
}
