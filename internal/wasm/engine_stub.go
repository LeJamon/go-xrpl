//go:build !cgo || !wasmi

package wasm

// Engine is the stub used unless built with `-tags wasmi` and cgo enabled. WASM
// execution requires the native wasmi library (internal/wasm/wasmi/build.sh),
// which is gated behind the `wasmi` build tag while the SmartEscrow feature is
// experimental, so the default build and lint never link it. Every Run reports
// ErrCGODisabled, mirroring the peertls !cgo stub.
type Engine struct{}

// New returns a stub engine.
func New() *Engine { return &Engine{} }

// Close is a no-op for the stub engine.
func (e *Engine) Close() {}

// Run always fails without cgo.
func (e *Engine) Run(code []byte, funcName string, params []Param, hf HostFunctions, gasLimit int64) (Result, error) {
	return Result{}, ErrCGODisabled
}

// Check always reports ErrCGODisabled without cgo; callers treat this as "WASM
// validation unavailable in this build" rather than a validation failure.
func (e *Engine) Check(code []byte, funcName string) error {
	return ErrCGODisabled
}
