package host

import "github.com/LeJamon/go-xrpl/internal/wasm"

// Field getters. A field is identified by code = (typeCode<<16)|fieldCode.

func (e *Env) GetTxField(code int32) ([]byte, wasm.HostFunctionError) {
	if e.view == nil {
		return nil, wasm.HfNoRuntime
	}
	return fieldReader(e.view.TxBytes(), code)
}

func (e *Env) GetCurrentLedgerObjField(code int32) ([]byte, wasm.HostFunctionError) {
	if e.view == nil {
		return nil, wasm.HfNoRuntime
	}
	return fieldReader(e.view.CurrentObjBytes(), code)
}

func (e *Env) GetLedgerObjField(cacheIdx, code int32) ([]byte, wasm.HostFunctionError) {
	sle, herr := e.slot(cacheIdx)
	if herr != wasm.HfSuccess {
		return nil, herr
	}
	return fieldReader(sle, code)
}

func (e *Env) GetTxArrayLen(code int32) (int32, wasm.HostFunctionError) {
	if e.view == nil {
		return 0, wasm.HfNoRuntime
	}
	return arrayLen(e.view.TxBytes(), code)
}

func (e *Env) GetCurrentLedgerObjArrayLen(code int32) (int32, wasm.HostFunctionError) {
	if e.view == nil {
		return 0, wasm.HfNoRuntime
	}
	return arrayLen(e.view.CurrentObjBytes(), code)
}

func (e *Env) GetLedgerObjArrayLen(cacheIdx, code int32) (int32, wasm.HostFunctionError) {
	sle, herr := e.slot(cacheIdx)
	if herr != wasm.HfSuccess {
		return 0, herr
	}
	return arrayLen(sle, code)
}

// CacheLedgerObj reads the ledger entry at objID into a cache slot and returns
// the 1-based slot index. A cacheIdx of 0 selects the first free slot.
func (e *Env) CacheLedgerObj(objID []byte, cacheIdx int32) (int32, wasm.HostFunctionError) {
	if e.view == nil {
		return 0, wasm.HfNoRuntime
	}
	if len(objID) != 32 {
		return 0, wasm.HfInvalidParams
	}
	if cacheIdx < 0 || cacheIdx > maxCache {
		return 0, wasm.HfSlotOutRange
	}
	idx := cacheIdx - 1
	if cacheIdx == 0 {
		for idx = 0; idx < maxCache; idx++ {
			if e.cache[idx] == nil {
				break
			}
		}
	}
	if idx >= maxCache {
		return 0, wasm.HfSlotsFull
	}
	var id [32]byte
	copy(id[:], objID)
	sle, ok := e.view.ReadSLE(id)
	if !ok {
		return 0, wasm.HfLedgerObjNotFound
	}
	e.cache[idx] = sle
	return idx + 1, wasm.HfSuccess
}

// GetNFT returns the URI of the NFToken with nftID owned by account.
func (e *Env) GetNFT(account, nftID []byte) ([]byte, wasm.HostFunctionError) {
	if e.view == nil {
		return nil, wasm.HfNoRuntime
	}
	if len(account) != 20 {
		return nil, wasm.HfInvalidAccount
	}
	if len(nftID) != 32 {
		return nil, wasm.HfInvalidParams
	}
	var a [20]byte
	copy(a[:], account)
	var id [32]byte
	copy(id[:], nftID)
	uri, ok := e.view.FindNFTURI(a, id)
	if !ok {
		return nil, wasm.HfLedgerObjNotFound
	}
	return uri, wasm.HfSuccess
}

// slot returns the cached ledger object at a 1-based index.
func (e *Env) slot(cacheIdx int32) ([]byte, wasm.HostFunctionError) {
	if cacheIdx < 1 || cacheIdx > maxCache {
		return nil, wasm.HfSlotOutRange
	}
	sle := e.cache[cacheIdx-1]
	if sle == nil {
		return nil, wasm.HfEmptySlot
	}
	return sle, wasm.HfSuccess
}
