package payment

// isPaymentFlowArithmeticPanic identifies the explicit protocol arithmetic
// exceptions used by Amount/Number. An allowlist is intentional: Go runtime
// panics, storage failures and arbitrary messages containing "overflow" must
// still reach the engine's non-applied tefEXCEPTION boundary.
func isPaymentFlowArithmeticPanic(value any) bool {
	message, ok := value.(string)
	if !ok {
		err, isError := value.(error)
		if !isError {
			return false
		}
		message = err.Error()
	}
	switch message {
	case "IOUAmount overflow",
		"MPT amount out of range",
		"MPT mulRatio overflow",
		"MPT value overflow",
		"MPT negate overflow",
		"MPT subtraction overflow",
		"Native currency amount out of range",
		"Native value overflow",
		"overflow in muldiv",
		"muldivRound overflow",
		"XRP mulRatio overflow",
		"XRPLNumber→IOUAmountValue overflow",
		"XRPLNumber::normalize overflow",
		"XRPLNumber::operator rep() overflow",
		"XRPLNumber::operator rep() rounding overflow",
		"XRPLNumber::shiftExponent overflow",
		"XRPLNumber::root2 nan",
		"XRPLNumber: divide by zero",
		"division by zero",
		"cannot take square root of negative amount":
		return true
	default:
		return false
	}
}
