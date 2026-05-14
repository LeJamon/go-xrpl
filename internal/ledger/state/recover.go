package state

import "fmt"

// RecoverArithmeticPanic converts a panicking XRPL arithmetic op (Add/Mul/
// Div/normalize/root2 on XRPLNumber, IOUAmountValue, and friends) into a
// returned error. Intended for code paths that may handle un-validated
// peer-supplied amounts — RPC simulation, exploratory path discovery,
// fuzz harnesses — so the node refuses the offending request rather than
// crashing.
//
// Production transaction processing already validates ranges at
// ParseIOUAmountBinary / ParseMPTAmountBinary time and should NOT need
// this wrapper; using it on the hot path silently masks programming
// bugs.
//
// Usage:
//
//	err := state.RecoverArithmeticPanic(func() {
//	    result = a.Mul(b)
//	})
//	if err != nil { return err }
func RecoverArithmeticPanic(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("xrpl arithmetic overflow: %v", r)
		}
	}()
	fn()
	return nil
}
