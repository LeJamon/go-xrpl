package tx

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// WireNumbers rounds each Number field string into scale and renders it as the
// exact mantissa/exponent pair the binary codec serializes unchanged. "" and
// "0" render as "0".
func WireNumbers(scale state.MantissaScale, values ...string) ([]string, error) {
	numbers := make([]string, len(values))
	for i, value := range values {
		if value == "" || value == "0" {
			numbers[i] = "0"
			continue
		}
		number, err := state.ParseXRPLNumber(value, scale, state.RoundToNearest)
		if err != nil {
			return nil, err
		}
		numbers[i] = fmt.Sprintf("%de%d", number.Mantissa(), number.Exponent())
	}
	return numbers, nil
}
