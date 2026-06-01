//go:build cgo && wasmi

package wasm

/*
#cgo CFLAGS: -I${SRCDIR}/wasmi/artifacts/include
#cgo LDFLAGS: -L${SRCDIR}/wasmi/artifacts/lib -lwasmi
#include <stdlib.h>
#include <stdint.h>
#include <string.h>
#include "wasmi.h"

// goHostCall is the Go dispatcher (//export) invoked for every WASM import. The
// forward declaration must match cgo's generated prototype (non-const).
extern wasm_trap_t* goHostCall(void* env, wasm_val_vec_t* params, wasm_val_vec_t* results);

static wasm_trap_t* host_trampoline(void* env, const wasm_val_vec_t* params, wasm_val_vec_t* results) {
    return goHostCall(env, (wasm_val_vec_t*)params, results);
}

// new_engine builds the engine with rippled's exact wasmi 1.0.9 configuration:
// fuel metering on, floats off, and every optional WASM proposal disabled.
// Matching this flag set is consensus-critical — a module using a proposal one
// side allows and the other rejects would diverge.
static wasm_engine_t* new_engine(void) {
    wasm_config_t* config = wasm_config_new();
    wasmi_config_consume_fuel_set(config, true);
    wasmi_config_ignore_custom_sections_set(config, true);
    wasmi_config_wasm_mutable_globals_set(config, false);
    wasmi_config_wasm_multi_value_set(config, false);
    wasmi_config_wasm_sign_extension_set(config, false);
    wasmi_config_wasm_saturating_float_to_int_set(config, false);
    wasmi_config_wasm_bulk_memory_set(config, false);
    wasmi_config_wasm_reference_types_set(config, false);
    wasmi_config_wasm_tail_call_set(config, false);
    wasmi_config_wasm_extended_const_set(config, false);
    wasmi_config_floats_set(config, false);
    wasmi_config_wasm_multi_memory_set(config, false);
    wasmi_config_wasm_custom_page_sizes_set(config, false);
    wasmi_config_wasm_memory64_set(config, false);
    wasmi_config_wasm_wide_arithmetic_set(config, false);
    return wasm_engine_new_with_config(config);
}

// make_functype builds a function type taking the given parameter kinds
// (0 = i32, 1 = i64) and returning a single i32 — the shape of every host
// import. Ownership of the returned type transfers to the caller.
static wasm_functype_t* make_functype(const int32_t* kinds, int n) {
    wasm_valtype_vec_t params;
    if (n == 0) {
        wasm_valtype_vec_new_empty(&params);
    } else {
        wasm_valtype_vec_new_uninitialized(&params, (size_t)n);
        for (int i = 0; i < n; ++i)
            params.data[i] = (kinds[i] == 1) ? wasm_valtype_new_i64() : wasm_valtype_new_i32();
    }
    wasm_valtype_t* r = wasm_valtype_new_i32();
    wasm_valtype_vec_t results;
    wasm_valtype_vec_new(&results, 1, &r);
    return wasm_functype_new(&params, &results);
}

// host_func creates an imported function of the given type bound to the Go
// trampoline with the environment handle. It consumes ft.
static wasm_func_t* host_func(wasm_store_t* store, wasm_functype_t* ft, uintptr_t env) {
    wasm_func_t* f = wasm_func_new_with_env(store, ft, host_trampoline, (void*)env, NULL);
    wasm_functype_delete(ft);
    return f;
}

static int32_t wval_i32(const wasm_val_vec_t* v, int i) { return v->data[i].of.i32; }
static int64_t wval_i64(const wasm_val_vec_t* v, int i) { return v->data[i].of.i64; }

static void results_set_i32(wasm_val_vec_t* results, int32_t x) {
    if (results && results->size > 0) { results->data[0].kind = WASM_I32; results->data[0].of.i32 = x; }
}

// instance_mem locates the instance's exported "memory" and returns its base
// pointer + byte length. Returns 0 if the instance exports no memory. The
// pointer is valid for the duration of the host call (WASM is suspended).
static int instance_mem(const wasm_instance_t* instance, uint8_t** out_ptr, size_t* out_size) {
    wasm_extern_vec_t exports;
    wasm_instance_exports(instance, &exports);
    int ok = 0;
    for (size_t i = 0; i < exports.size; ++i) {
        wasm_memory_t* m = wasm_extern_as_memory(exports.data[i]);
        if (m) {
            *out_ptr = (uint8_t*)wasm_memory_data(m);
            *out_size = wasm_memory_data_size(m);
            ok = 1;
            break;
        }
    }
    wasm_extern_vec_delete(&exports);
    return ok;
}

static char* module_import_name(const wasm_module_t* m, size_t i) {
    wasm_importtype_vec_t v;
    wasm_module_imports(m, &v);
    if (i >= v.size) { wasm_importtype_vec_delete(&v); return NULL; }
    const wasm_name_t* n = wasm_importtype_name(v.data[i]);
    char* s = (char*)malloc(n->size + 1);
    memcpy(s, n->data, n->size);
    s[n->size] = 0;
    wasm_importtype_vec_delete(&v);
    return s;
}

static size_t module_num_imports(const wasm_module_t* m) {
    wasm_importtype_vec_t v;
    wasm_module_imports(m, &v);
    size_t n = v.size;
    wasm_importtype_vec_delete(&v);
    return n;
}

static void extern_vec_new(wasm_extern_vec_t* out, size_t n) {
    if (n == 0) { wasm_extern_vec_new_empty(out); return; }
    wasm_extern_vec_new_uninitialized(out, n);
}

static void extern_vec_set_func(wasm_extern_vec_t* v, size_t i, wasm_func_t* f) {
    v->data[i] = wasm_func_as_extern(f);
}

static wasm_val_t* alloc_vals(int n) { return (wasm_val_t*)calloc(n > 0 ? n : 1, sizeof(wasm_val_t)); }
static void set_arg_i32(wasm_val_t* in, int i, int32_t x) { in[i].kind = WASM_I32; in[i].of.i32 = x; }
static void set_arg_i64(wasm_val_t* in, int i, int64_t x) { in[i].kind = WASM_I64; in[i].of.i64 = x; }

// call_export finds the exported function named fname and invokes it with nin
// args, writing the single i32 result. *found is set to 1 if the export exists.
// The call happens while the exports vector is alive; it is freed afterwards.
static wasm_trap_t* call_export(const wasm_module_t* module, const wasm_instance_t* instance,
                                const char* fname, wasm_val_t* in, int nin,
                                int32_t* out_i32, int* found) {
    *found = 0;
    wasm_exporttype_vec_t exporttypes;
    wasm_module_exports(module, &exporttypes);
    wasm_extern_vec_t exports;
    wasm_instance_exports(instance, &exports);

    wasm_trap_t* trap = NULL;
    int idx = -1;
    size_t flen = strlen(fname);
    for (size_t i = 0; i < exporttypes.size; ++i) {
        const wasm_name_t* n = wasm_exporttype_name(exporttypes.data[i]);
        if (n->size == flen && memcmp(n->data, fname, flen) == 0) { idx = (int)i; break; }
    }
    if (idx >= 0 && (size_t)idx < exports.size) {
        wasm_func_t* func = wasm_extern_as_func(exports.data[idx]);
        if (func) {
            *found = 1;
            wasm_val_t out[1] = { WASM_INIT_VAL };
            wasm_val_vec_t argsv = { (size_t)nin, in };
            wasm_val_vec_t resv = { 1, out };
            trap = wasm_func_call(func, &argsv, &resv);
            if (!trap) *out_i32 = out[0].of.i32;
        }
    }
    wasm_exporttype_vec_delete(&exporttypes);
    wasm_extern_vec_delete(&exports);
    return trap;
}

static wasm_trap_t* make_trap(wasm_store_t* store, const char* msg) {
    wasm_byte_vec_t m;
    wasm_byte_vec_new(&m, strlen(msg), msg);
    wasm_trap_t* t = wasm_trap_new(store, &m);
    wasm_byte_vec_delete(&m);
    return t;
}
*/
import "C"

import (
	"math"
	"runtime/cgo"
	"unsafe"
)

// maxPages bounds a contract's linear memory to 8MB (64KB * 128), matching
// rippled's maxPages.
const maxPages = 128

// Engine compiles and runs WASM contracts. It owns a wasmi engine (engine
// configuration is immutable and shareable); each Run executes in a fresh store
// for isolation. An Engine is safe for concurrent use.
type Engine struct {
	engine *C.wasm_engine_t
}

// New creates a WASM engine configured identically to rippled.
func New() *Engine {
	return &Engine{engine: C.new_engine()}
}

// Close releases the underlying wasmi engine.
func (e *Engine) Close() {
	if e.engine != nil {
		C.wasm_engine_delete(e.engine)
		e.engine = nil
	}
}

// instanceRef is the runtime back-pointer host functions reach memory through.
// It is shared by all of a Run's import bindings and populated immediately
// after instantiation, mirroring rippled's HostFunctions::setRT.
type instanceRef struct {
	inst *C.wasm_instance_t
}

// importBinding is the per-import environment handed to the host trampoline.
type importBinding struct {
	hf    HostFunctions
	fn    hostFn
	store *C.wasm_store_t
	rt    *instanceRef
}

// Run executes funcName in code under gasLimit. params are the entry-function
// arguments; hf services the host imports the module declares (resolved against
// the built-in registry). It returns the i32 result and the fuel consumed,
// mirroring rippled's WasmEngine::run.
func (e *Engine) Run(code []byte, funcName string, params []Param, hf HostFunctions, gasLimit int64) (Result, error) {
	store := C.wasm_store_new_with_memory_max_pages(e.engine, C.uint32_t(maxPages))
	if store == nil {
		return Result{}, ErrExecution
	}
	defer C.wasm_store_delete(store)

	var initialFuel C.uint64_t
	if gasLimit < 0 {
		initialFuel = C.uint64_t(math.MaxUint64)
	} else {
		initialFuel = C.uint64_t(gasLimit)
	}
	if C.wasm_store_set_fuel(store, initialFuel) != nil {
		return Result{}, ErrExecution
	}

	module := compileModule(store, code)
	if module == nil {
		return Result{}, ErrExecution
	}
	defer C.wasm_module_delete(module)

	externs, handles, rt, err := buildImports(store, module, hf)
	for _, h := range handles {
		defer h.Delete()
	}
	if err != nil {
		return Result{}, err
	}
	defer C.wasm_extern_vec_delete(&externs)

	var trap *C.wasm_trap_t
	instance := C.wasm_instance_new(store, module, &externs, &trap)
	if trap != nil {
		C.wasm_trap_delete(trap)
	}
	if instance == nil {
		return Result{}, ErrExecution
	}
	defer C.wasm_instance_delete(instance)
	rt.inst = instance

	cname := C.CString(funcName)
	defer C.free(unsafe.Pointer(cname))

	result, found, callTrap := callExport(module, instance, cname, params)
	if callTrap != nil {
		C.wasm_trap_delete(callTrap)
		return Result{}, ErrExecution
	}
	if !found {
		return Result{}, ErrExecution
	}

	var remaining C.uint64_t
	C.wasm_store_get_fuel(store, &remaining)
	cost := int64(uint64(initialFuel) - uint64(remaining))

	return Result{Result: result, Cost: cost}, nil
}

func compileModule(store *C.wasm_store_t, code []byte) *C.wasm_module_t {
	if len(code) == 0 {
		return nil
	}
	var binary C.wasm_byte_vec_t
	C.wasm_byte_vec_new(&binary, C.size_t(len(code)), (*C.wasm_byte_t)(unsafe.Pointer(&code[0])))
	defer C.wasm_byte_vec_delete(&binary)
	return C.wasm_module_new(store, &binary)
}

// buildImports resolves the module's declared imports against the registry by
// name, binding each to the host trampoline. Functions are built up first and
// only materialized into the C extern vector once every import is satisfied; an
// unknown import (or a host import with no HostFunctions provided) cleans up and
// fails, matching rippled rejecting the contract.
func buildImports(store *C.wasm_store_t, module *C.wasm_module_t, hf HostFunctions) (C.wasm_extern_vec_t, []cgo.Handle, *instanceRef, error) {
	rt := &instanceRef{}
	n := int(C.module_num_imports(module))
	handles := make([]cgo.Handle, 0, n)
	funcs := make([]*C.wasm_func_t, 0, n)

	fail := func() {
		for _, f := range funcs {
			C.wasm_func_delete(f)
		}
		for _, h := range handles {
			h.Delete()
		}
	}

	for i := 0; i < n; i++ {
		cstr := C.module_import_name(module, C.size_t(i))
		name := C.GoString(cstr)
		C.free(unsafe.Pointer(cstr))

		fn, ok := registry[name]
		if !ok || hf == nil {
			fail()
			return C.wasm_extern_vec_t{}, nil, nil, ErrExecution
		}
		h := cgo.NewHandle(&importBinding{hf: hf, fn: fn, store: store, rt: rt})
		handles = append(handles, h)

		kinds := wasmParamKinds(fn.args)
		var ft *C.wasm_functype_t
		if len(kinds) == 0 {
			ft = C.make_functype(nil, 0)
		} else {
			ft = C.make_functype(&kinds[0], C.int(len(kinds)))
		}
		funcs = append(funcs, C.host_func(store, ft, C.uintptr_t(h)))
	}

	var externs C.wasm_extern_vec_t
	C.extern_vec_new(&externs, C.size_t(n))
	for i, f := range funcs {
		C.extern_vec_set_func(&externs, C.size_t(i), f)
	}
	return externs, handles, rt, nil
}

// wasmParamKinds flattens an argument shape into the WASM parameter kinds
// (0 = i32, 1 = i64) the functype is built from.
func wasmParamKinds(args []argKind) []C.int32_t {
	var k []C.int32_t
	for _, a := range args {
		switch a {
		case argSliceIn, argBufferOut:
			k = append(k, 0, 0)
		case argScalarI64:
			k = append(k, 1)
		default: // argScalarI32
			k = append(k, 0)
		}
	}
	return k
}

func callExport(module *C.wasm_module_t, instance *C.wasm_instance_t, cname *C.char, params []Param) (int32, bool, *C.wasm_trap_t) {
	in := C.alloc_vals(C.int(len(params)))
	defer C.free(unsafe.Pointer(in))
	for i, p := range params {
		switch p.kind {
		case kindI64:
			C.set_arg_i64(in, C.int(i), C.int64_t(p.i64))
		default:
			C.set_arg_i32(in, C.int(i), C.int32_t(p.i32))
		}
	}
	var result C.int32_t
	var found C.int
	trap := C.call_export(module, instance, cname, in, C.int(len(params)), &result, &found)
	return int32(result), found != 0, trap
}

//export goHostCall
func goHostCall(env unsafe.Pointer, params *C.wasm_val_vec_t, results *C.wasm_val_vec_t) *C.wasm_trap_t {
	b := cgo.Handle(uintptr(env)).Value().(*importBinding)

	// Charge gas before dispatch, mirroring rippled's checkGas: deduct the
	// import's cost from remaining fuel, trapping if it would go negative.
	// Fuel is unsigned and can approach MaxUint64 (the unlimited budget), so the
	// comparison must not run through int64.
	var remaining C.uint64_t
	C.wasm_store_get_fuel(b.store, &remaining)
	rem, gas := uint64(remaining), uint64(b.fn.gas)
	if rem < gas {
		C.wasm_store_set_fuel(b.store, 0)
		return trap(b.store, "hf out of gas")
	}
	C.wasm_store_set_fuel(b.store, C.uint64_t(rem-gas))

	mem, ok := instanceMemory(b.rt.inst)
	if !ok {
		return hfReturn(results, int32(HfNoMemExported))
	}

	var (
		in       hostInputs
		outPtr   int32
		outSize  int32
		haveOut  bool
		paramIdx C.int
	)
	readI32 := func() int32 { v := int32(C.wval_i32(params, paramIdx)); paramIdx++; return v }
	readI64 := func() int64 { v := int64(C.wval_i64(params, paramIdx)); paramIdx++; return v }

	for _, a := range b.fn.args {
		switch a {
		case argSliceIn:
			ptr, size := readI32(), readI32()
			s, ok := readMem(mem, ptr, size)
			if !ok {
				return hfReturn(results, int32(HfPointerOutOfBounds))
			}
			in.slices = append(in.slices, s)
		case argBufferOut:
			outPtr, outSize = readI32(), readI32()
			haveOut = true
		case argScalarI64:
			in.i64s = append(in.i64s, readI64())
		default: // argScalarI32
			in.i32s = append(in.i32s, uint32(readI32()))
		}
	}

	res := b.fn.invoke(b.hf, in)
	if res.err != HfSuccess {
		return hfReturn(results, int32(res.err))
	}
	if haveOut {
		if len(res.data) > int(outSize) {
			return hfReturn(results, int32(HfBufferTooSmall))
		}
		if !writeMem(mem, outPtr, res.data) {
			return hfReturn(results, int32(HfPointerOutOfBounds))
		}
		return hfReturn(results, int32(len(res.data)))
	}
	return hfReturn(results, res.val)
}

func hfReturn(results *C.wasm_val_vec_t, v int32) *C.wasm_trap_t {
	C.results_set_i32(results, C.int32_t(v))
	return nil
}

// instanceMemory returns a []byte view over the instance's linear memory. The
// view is valid only for the current host call.
func instanceMemory(inst *C.wasm_instance_t) ([]byte, bool) {
	var ptr *C.uint8_t
	var size C.size_t
	if C.instance_mem(inst, &ptr, &size) == 0 {
		return nil, false
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(ptr)), int(size)), true
}

func readMem(mem []byte, ptr, size int32) ([]byte, bool) {
	if ptr < 0 || size < 0 || int(ptr)+int(size) > len(mem) {
		return nil, false
	}
	out := make([]byte, size)
	copy(out, mem[ptr:int(ptr)+int(size)])
	return out, true
}

func writeMem(mem []byte, ptr int32, data []byte) bool {
	if ptr < 0 || int(ptr)+len(data) > len(mem) {
		return false
	}
	copy(mem[ptr:], data)
	return true
}

func trap(store *C.wasm_store_t, msg string) *C.wasm_trap_t {
	cs := C.CString(msg)
	defer C.free(unsafe.Pointer(cs))
	return C.make_trap(store, cs)
}
