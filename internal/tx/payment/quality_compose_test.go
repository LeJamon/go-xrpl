package payment

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Compose rounds the muldiv quotient up, then truncates while scaling the
// mantissa back into range and rounds up only on the final divide by 10.
func TestQualityCompose_Rounding(t *testing.T) {
	tests := []struct {
		name  string
		lhsM  uint64
		lhsE  int
		rhsM  uint64
		rhsE  int
		wantM uint64
		wantE int
	}{
		{
			name: "exact product",
			lhsM: 2000000000000000, lhsE: -15,
			rhsM: 3000000000000000, rhsE: -15,
			wantM: 6000000000000000, wantE: -15,
		},
		{
			name: "intermediate divide truncates",
			lhsM: 1500000000000000, lhsE: -21,
			rhsM: 6666666666666667, rhsE: -9,
			wantM: 1000000000000000, wantE: -14,
		},
		{
			name: "final divide rounds up",
			lhsM: 1000000000000001, lhsE: -15,
			rhsM: 1000000000000001, rhsE: -15,
			wantM: 1000000000000003, wantE: -15,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lhs := QualityFromMantissaExp(tc.lhsM, tc.lhsE)
			rhs := QualityFromMantissaExp(tc.rhsM, tc.rhsE)
			want := QualityFromMantissaExp(tc.wantM, tc.wantE)
			require.Equal(t, want, lhs.Compose(rhs))
			require.Equal(t, want, rhs.Compose(lhs))
		})
	}
}
