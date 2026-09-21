package consensus

import (
	"errors"
	"sync"
)

type signatureCheckState struct {
	mu     sync.Mutex
	key    [32]byte
	result bool
	set    bool
}

var signatureCheckStateInitMu sync.Mutex

func (v *Validation) signatureState() *signatureCheckState {
	signatureCheckStateInitMu.Lock()
	defer signatureCheckStateInitMu.Unlock()
	if v.signatureCheck == nil {
		v.signatureCheck = &signatureCheckState{}
	}
	return v.signatureCheck
}

// CheckSignature runs check once for the supplied signing input and caches
// completed true and false results. A callback error leaves the result unset,
// so a later call can retry serialization or verification.
func (v *Validation) CheckSignature(key [32]byte, check func() (bool, error)) (bool, error) {
	if v == nil {
		return false, errors.New("nil validation")
	}
	if check == nil {
		return false, errors.New("nil signature check")
	}

	state := v.signatureState()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.set && state.key == key {
		return state.result, nil
	}

	result, err := check()
	if err != nil {
		return false, err
	}
	state.key = key
	state.result = result
	state.set = true
	return result, nil
}

// ResetSignatureCheck clears a prior result after a caller changes the
// validation before signing it again.
func (v *Validation) ResetSignatureCheck() {
	if v == nil {
		return
	}
	state := v.signatureState()
	state.mu.Lock()
	state.set = false
	state.mu.Unlock()
}
