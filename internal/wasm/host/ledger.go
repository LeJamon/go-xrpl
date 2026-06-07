package host

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/wasm"
)

func (e *Env) GetParentLedgerTime() (uint32, wasm.HostFunctionError) {
	if e.view == nil {
		return 0, wasm.HfNoRuntime
	}
	return e.view.ParentCloseTime(), wasm.HfSuccess
}

func (e *Env) GetParentLedgerHash() ([]byte, wasm.HostFunctionError) {
	if e.view == nil {
		return nil, wasm.HfNoRuntime
	}
	h := e.view.ParentHash()
	b := make([]byte, 32)
	copy(b, h[:])
	return b, wasm.HfSuccess
}

func (e *Env) GetBaseFee() (uint32, wasm.HostFunctionError) {
	if e.view == nil {
		return 0, wasm.HfNoRuntime
	}
	return e.view.BaseFee(), wasm.HfSuccess
}

// IsAmendmentEnabled reports whether an amendment is active. A 32-byte argument
// is first tried as an amendment id; failing that (and for other sizes) it is
// treated as an amendment name, whose id is its SHA-512-half. This mirrors
// rippled's two-overload dispatch.
func (e *Env) IsAmendmentEnabled(data []byte) (int32, wasm.HostFunctionError) {
	if e.view == nil {
		return 0, wasm.HfNoRuntime
	}
	if len(data) == 32 {
		var id [32]byte
		copy(id[:], data)
		if e.view.AmendmentEnabled(id) {
			return 1, wasm.HfSuccess
		}
		// Fall through: the 32 bytes may instead be an amendment name.
	}
	if len(data) > 64 {
		return 0, wasm.HfDataFieldTooLarge
	}
	if e.view.AmendmentEnabled(amendment.FeatureID(string(data))) {
		return 1, wasm.HfSuccess
	}
	return 0, wasm.HfSuccess
}
