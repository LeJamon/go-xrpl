package host

import "github.com/LeJamon/go-xrpl/internal/wasm"

// Nested field getters. A locator is a little-endian int32 sequence
// [fieldCode, (arrayIndex | fieldCode)...] selecting a field inside arrays and
// inner objects.

func (e *Env) GetTxNestedField(locator []byte) ([]byte, wasm.HostFunctionError) {
	if e.view == nil {
		return nil, wasm.HfNoRuntime
	}
	return nestedField(e.view.TxBytes(), locator)
}

func (e *Env) GetCurrentLedgerObjNestedField(locator []byte) ([]byte, wasm.HostFunctionError) {
	if e.view == nil {
		return nil, wasm.HfNoRuntime
	}
	obj, ok := e.view.CurrentObjBytes()
	if !ok {
		return nil, wasm.HfLedgerObjNotFound
	}
	return nestedField(obj, locator)
}

func (e *Env) GetLedgerObjNestedField(cacheIdx int32, locator []byte) ([]byte, wasm.HostFunctionError) {
	sle, herr := e.slot(cacheIdx)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return nestedField(sle, locator)
}

func (e *Env) GetTxNestedArrayLen(locator []byte) (int32, wasm.HostFunctionError) {
	if e.view == nil {
		return 0, wasm.HfNoRuntime
	}
	return nestedArrayLen(e.view.TxBytes(), locator)
}

func (e *Env) GetCurrentLedgerObjNestedArrayLen(locator []byte) (int32, wasm.HostFunctionError) {
	if e.view == nil {
		return 0, wasm.HfNoRuntime
	}
	obj, ok := e.view.CurrentObjBytes()
	if !ok {
		return 0, wasm.HfLedgerObjNotFound
	}
	return nestedArrayLen(obj, locator)
}

func (e *Env) GetLedgerObjNestedArrayLen(cacheIdx int32, locator []byte) (int32, wasm.HostFunctionError) {
	sle, herr := e.slot(cacheIdx)
	if herr != wasm.HfSuccess {
		return 0, herr
	}
	return nestedArrayLen(sle, locator)
}
