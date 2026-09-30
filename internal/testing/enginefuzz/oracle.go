package enginefuzz

import (
	"fmt"
	"math/bits"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

var catastrophicResults = map[ter.Result]struct{}{
	ter.TefINTERNAL:         {},
	ter.TefEXCEPTION:        {},
	ter.TefBAD_LEDGER:       {},
	ter.TefFAILURE:          {},
	ter.TecINTERNAL:         {},
	ter.TecINVARIANT_FAILED: {},
	ter.TefINVARIANT_FAILED: {},
}

func classifyOutcome(step traceStep, result jtx.TxResult, profile amendmentProfile, index int) error {
	if err := classifySafetyOutcome(step.String(), result, profile, index); err != nil {
		return err
	}
	if !allowedResult(step.Kind, result.Result) {
		return fmt.Errorf("step %d %s profile=%s: unexpected result %s: %s", index, step, profile, result.Code, result.Message)
	}
	return nil
}

func classifySafetyOutcome(description string, result jtx.TxResult, profile amendmentProfile, index int) error {
	if result.Code == "" || result.Code == "-" || result.Result.String() == "-" {
		return fmt.Errorf("step %d %s profile=%s: unknown or empty result code %q", index, description, profile, result.Code)
	}
	if _, catastrophic := catastrophicResults[result.Result]; catastrophic {
		return fmt.Errorf("step %d %s profile=%s: catastrophic result %s: %s", index, description, profile, result.Code, result.Message)
	}
	return nil
}

func allowedResult(kind txKind, result ter.Result) bool {
	if result == ter.TesSUCCESS {
		return true
	}
	switch kind {
	case kindPaymentXRP:
		return result == ter.TecDST_TAG_NEEDED || result == ter.TecUNFUNDED_PAYMENT || result == ter.TecNO_DST_INSUF_XRP
	case kindPaymentIOU:
		return result == ter.TecPATH_PARTIAL || result == ter.TecPATH_DRY || result == ter.TecFROZEN || result == ter.TecNO_AUTH
	case kindAccountSet:
		return result == ter.TecOWNERS || result == ter.TecNO_ALTERNATIVE_KEY
	case kindTrustSet:
		return result == ter.TecNO_LINE_REDUNDANT || result == ter.TecINSUF_RESERVE_LINE
	case kindOfferCreate:
		return result == ter.TecKILLED || result == ter.TecUNFUNDED_OFFER || result == ter.TecINSUF_RESERVE_OFFER
	case kindOfferCancel:
		return result == ter.TefPAST_SEQ
	default:
		return false
	}
}

func addXRP(total, amount uint64) (uint64, error) {
	sum, carry := bits.Add64(total, amount, 0)
	if carry != 0 {
		return 0, fmt.Errorf("XRP total overflow: %d + %d", total, amount)
	}
	return sum, nil
}
