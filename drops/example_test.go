package drops_test

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/drops"
)

func ExampleXRPAmount_DecimalXRP() {
	amount := drops.NewXRPAmount(2_500_000)
	fmt.Printf("%d drops = %g XRP\n", amount.Drops(), amount.DecimalXRP())
	// Output: 2500000 drops = 2.5 XRP
}

func ExampleFromDecimalXRP() {
	amount, err := drops.FromDecimalXRP(1.5)
	if err != nil {
		panic(err)
	}
	fmt.Println(amount.Drops())
	// Output: 1500000
}

func ExampleFees_AccountReserve() {
	fees := drops.Fees{
		Base:      drops.NewXRPAmount(10),
		Reserve:   drops.NewXRPAmount(10_000_000),
		Increment: drops.NewXRPAmount(2_000_000),
	}
	// Total reserve required for an account owning three objects:
	// the base reserve plus three increments.
	fmt.Println(fees.AccountReserve(3).Drops())
	// Output: 16000000
}
