package tx

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// WireNumbers rounds Number fields at the ledger's mantissa scale before encoding.
func WireNumbers(scale state.MantissaScale, values ...string) ([]string, error) {
	numbers := make([]string, len(values))
	for i, value := range values {
		numbers[i] = "0"
		if value == "" || value == "0" {
			continue
		}
		number, err := state.ParseXRPLNumber(value, scale, state.RoundToNearest)
		if err != nil {
			return nil, err
		}
		if !number.IsZero() {
			numbers[i] = fmt.Sprintf("%de%d", number.Mantissa(), number.Exponent())
		}
	}
	return numbers, nil
}
