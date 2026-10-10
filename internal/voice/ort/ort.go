// Package ort is a minimal onnxruntime C API binding over purego (no cgo): enough to run
// float32/int64 models with named inputs and outputs, which is what openWakeWord (and silero
// VAD, if it is ever added) needs. It uses the same libonnxruntime that internal/voice/sherpa
// embeds (1.28.2 on every target), so a process can host both.
//
// The OrtApi function table is indexed by slot. Indices are for ORT_API_VERSION 28, computed
// from onnxruntime_c_api.h v1.28.2 (jarvis-host-agent spikes/pinode/tools/ortidx.py). The
// table is append-only, so the indices hold for any newer runtime too.
//
// Pointer rules (docs/agent/go-pi-node.md §3.5): input tensors wrap Go memory that holds no Go
// pointers, only for the duration of Run; the C strings and name arrays live in long-lived
// Session/Runtime fields. Go's GC doesn't move heap objects, and purego.SyscallN and this
// package's call helper are //go:uintptrescapes, so pointers passed as uintptr arguments stay
// valid for the call. Never add a helper between a uintptr(unsafe.Pointer(&x)) and the C
// call without that directive.
package ort

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// OrtApi slot indices (ORT_API_VERSION 28).
const (
	iGetErrorMessage                  = 2
	iCreateEnv                        = 3
	iCreateSession                    = 7
	iRun                              = 9
	iCreateSessionOptions             = 10
	iDisableCpuMemArena               = 19
	iSetSessionGraphOptimizationLevel = 23
	iSetIntraOpNumThreads             = 24
	iSetInterOpNumThreads             = 25
	iCreateTensorWithDataAsOrtValue   = 49
	iGetTensorMutableData             = 51
	iGetDimensionsCount               = 61
	iGetDimensions                    = 62
	iGetTensorTypeAndShape            = 65
	iCreateCpuMemoryInfo              = 69
	iReleaseEnv                       = 92
	iReleaseStatus                    = 93
	iReleaseMemoryInfo                = 94
	iReleaseSession                   = 95
	iReleaseValue                     = 96
	iReleaseTensorTypeAndShapeInfo    = 99
	iReleaseSessionOptions            = 100
)

// APIVersion is the OrtApi version this binding is written against.
const APIVersion = 28

// ONNX tensor element types used here.
const (
	typeFloat = 1
	typeInt64 = 7
)

const ptrSize = int(unsafe.Sizeof(uintptr(0)))

// Runtime is one loaded onnxruntime with a process-wide environment.
type Runtime struct {
	api     unsafe.Pointer // const OrtApi*
	env     uintptr        // OrtEnv* (opaque)
	memInfo uintptr        // OrtMemoryInfo* for CPU tensors wrapping Go memory
	errMsg  func(status uintptr) string
	// Version is the runtime's own version string, e.g. "1.28.2".
	Version string
}

// apiBase mirrors struct OrtApiBase { GetApi; GetVersionString; }.
type apiBase struct {
	getAPI, getVersionString uintptr
}

var (
	libMu   sync.Mutex
	libByAt = map[string]*Runtime{}
)

// Load opens libonnxruntime at path and creates the environment. Loading the same path again
// returns the same Runtime: ORT allows only one environment per process.
func Load(path string) (*Runtime, error) {
	libMu.Lock()
	defer libMu.Unlock()
	if r := libByAt[path]; r != nil {
		return r, nil
	}
	h, err := openLibrary(path)
	if err != nil {
		return nil, err
	}
	var getAPIBase func() unsafe.Pointer
	if err := register(&getAPIBase, h, "OrtGetApiBase"); err != nil {
		return nil, err
	}
	base := (*apiBase)(getAPIBase())
	if base == nil {
		return nil, errors.New("onnxruntime: OrtGetApiBase returned NULL")
	}
	var getAPI func(version uint32) unsafe.Pointer
	var getVersion func() string
	purego.RegisterFunc(&getAPI, base.getAPI)
	purego.RegisterFunc(&getVersion, base.getVersionString)

	r := &Runtime{Version: getVersion()}
	if r.api = getAPI(APIVersion); r.api == nil {
		return nil, fmt.Errorf("onnxruntime %s does not offer C API v%d", r.Version, APIVersion)
	}
	purego.RegisterFunc(&r.errMsg, r.fn(iGetErrorMessage))

	var cs cstrs
	if err := r.call(iCreateEnv, 3 /* ORT_LOGGING_LEVEL_ERROR */, cs.of("jarvis"), uintptr(unsafe.Pointer(&r.env))); err != nil {
		return nil, err
	}
	runtime.KeepAlive(&cs)
	// OrtArenaAllocator (1) would be wrong for caller-owned memory: OrtDeviceAllocator (0),
	// OrtMemTypeDefault (0).
	if err := r.call(iCreateCpuMemoryInfo, 0, 0, uintptr(unsafe.Pointer(&r.memInfo))); err != nil {
		purego.SyscallN(r.fn(iReleaseEnv), r.env)
		return nil, err
	}
	libByAt[path] = r
	return r, nil
}

// register binds a library symbol, turning purego's panic on a missing symbol into an error.
func register(fptr any, h uintptr, name string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("onnxruntime: binding %s: %v", name, r)
		}
	}()
	purego.RegisterLibFunc(fptr, h, name)
	return nil
}

// fn is the function pointer in OrtApi slot i.
func (r *Runtime) fn(i int) uintptr {
	return *(*uintptr)(unsafe.Add(r.api, i*ptrSize))
}

// call invokes an OrtStatus-returning API function and converts a non-NULL status to an error.
//
// uintptrescapes is load-bearing: callers pass out-parameter addresses (&vals[i], &info)
// converted to uintptr, and without it the compiler may keep those variables on the goroutine
// stack, which a stack growth inside this call moves, so C writes into the old stack
// (TestProcessAtAnyStackDepth in internal/voice/oww). With it, every uintptr(unsafe.Pointer(x))
// argument is heap-allocated and kept alive for the call, exactly as for purego.SyscallN.
//
//go:uintptrescapes
func (r *Runtime) call(i int, args ...uintptr) error {
	st, _, _ := purego.SyscallN(r.fn(i), args...)
	if st == 0 {
		return nil
	}
	err := errors.New("onnxruntime: " + r.errMsg(st))
	purego.SyscallN(r.fn(iReleaseStatus), st)
	return err
}

// cstrs keeps NUL-terminated copies of Go strings alive for as long as the holder is.
type cstrs struct{ bufs [][]byte }

func (c *cstrs) of(s string) uintptr {
	b := append([]byte(s), 0)
	c.bufs = append(c.bufs, b)
	return uintptr(unsafe.Pointer(&b[0]))
}

// Options tunes a session.
type Options struct {
	Threads int  // intra- and inter-op threads; openWakeWord uses 1 (the default)
	NoArena bool // DisableCpuMemArena: trades a little speed for a smaller footprint
}

// Session is one loaded model with fixed input and output names. It is not safe for
// concurrent use.
type Session struct {
	r                 *Runtime
	h                 uintptr
	inNames, outNames []uintptr // const char* arrays passed to Run
	names             cstrs     // backing storage for inNames/outNames
}

// NewSession loads the model at path. in and out name the inputs Run takes (in this order) and
// the outputs it returns.
func (r *Runtime) NewSession(path string, in, out []string, o Options) (*Session, error) {
	if len(in) == 0 || len(out) == 0 {
		return nil, errors.New("onnxruntime: a session needs at least one input and one output")
	}
	var opts uintptr
	if err := r.call(iCreateSessionOptions, uintptr(unsafe.Pointer(&opts))); err != nil {
		return nil, err
	}
	defer purego.SyscallN(r.fn(iReleaseSessionOptions), opts)
	t := max(o.Threads, 1)
	if err := r.call(iSetIntraOpNumThreads, opts, uintptr(t)); err != nil {
		return nil, err
	}
	if err := r.call(iSetInterOpNumThreads, opts, uintptr(t)); err != nil {
		return nil, err
	}
	if err := r.call(iSetSessionGraphOptimizationLevel, opts, 99 /* ORT_ENABLE_ALL */); err != nil {
		return nil, err
	}
	if o.NoArena {
		if err := r.call(iDisableCpuMemArena, opts); err != nil {
			return nil, err
		}
	}
	s := &Session{r: r}
	mp, keep := modelPath(path)
	err := r.call(iCreateSession, r.env, mp, opts, uintptr(unsafe.Pointer(&s.h)))
	runtime.KeepAlive(keep)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, n := range in {
		s.inNames = append(s.inNames, s.names.of(n))
	}
	for _, n := range out {
		s.outNames = append(s.outNames, s.names.of(n))
	}
	return s, nil
}

// Close releases the session. The Runtime stays loaded.
func (s *Session) Close() {
	if s.h != 0 {
		purego.SyscallN(s.r.fn(iReleaseSession), s.h)
		s.h = 0
	}
}

// Tensor is an input: exactly one of F32 and I64 is set, and its length matches Shape.
type Tensor struct {
	Shape []int64
	F32   []float32
	I64   []int64
}

// Output is a float32 output tensor, copied into Go memory.
type Output struct {
	Shape []int64
	Data  []float32
}

// Run feeds inputs (in the session's input-name order) and returns its float32 outputs.
func (s *Session) Run(inputs ...Tensor) ([]Output, error) {
	if s.h == 0 {
		return nil, errors.New("onnxruntime: session is closed")
	}
	if len(inputs) != len(s.inNames) {
		return nil, fmt.Errorf("onnxruntime: %d inputs for %d input names", len(inputs), len(s.inNames))
	}
	r := s.r
	vals := make([]uintptr, len(inputs))
	defer func() {
		for _, v := range vals {
			if v != 0 {
				purego.SyscallN(r.fn(iReleaseValue), v)
			}
		}
	}()
	for i, t := range inputs {
		var p unsafe.Pointer
		var n, typ uintptr
		var elems int
		switch {
		case len(t.I64) > 0 && len(t.F32) == 0:
			p, n, typ, elems = unsafe.Pointer(&t.I64[0]), uintptr(len(t.I64)*8), typeInt64, len(t.I64)
		case len(t.F32) > 0 && len(t.I64) == 0:
			p, n, typ, elems = unsafe.Pointer(&t.F32[0]), uintptr(len(t.F32)*4), typeFloat, len(t.F32)
		default:
			return nil, fmt.Errorf("onnxruntime: input %d must set exactly one of F32 and I64", i)
		}
		want := int64(1)
		for _, d := range t.Shape {
			want *= d
		}
		if want != int64(elems) {
			return nil, fmt.Errorf("onnxruntime: input %d has %d elements, shape %v needs %d", i, elems, t.Shape, want)
		}
		var shapeP uintptr
		if len(t.Shape) > 0 {
			shapeP = uintptr(unsafe.Pointer(&t.Shape[0]))
		}
		if err := r.call(iCreateTensorWithDataAsOrtValue, r.memInfo, uintptr(p), n, shapeP, uintptr(len(t.Shape)), typ, uintptr(unsafe.Pointer(&vals[i]))); err != nil {
			return nil, err
		}
	}
	outs := make([]uintptr, len(s.outNames))
	err := r.call(iRun, s.h, 0,
		uintptr(unsafe.Pointer(&s.inNames[0])), uintptr(unsafe.Pointer(&vals[0])), uintptr(len(vals)),
		uintptr(unsafe.Pointer(&s.outNames[0])), uintptr(len(outs)), uintptr(unsafe.Pointer(&outs[0])))
	// The tensors wrap the inputs' memory; keep it reachable until Run has returned.
	runtime.KeepAlive(inputs)
	runtime.KeepAlive(s)
	if err != nil {
		return nil, err
	}
	res := make([]Output, len(outs))
	for i, v := range outs {
		if err == nil {
			res[i], err = r.read(v)
		}
		purego.SyscallN(r.fn(iReleaseValue), v)
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (r *Runtime) read(v uintptr) (Output, error) {
	var info uintptr
	if err := r.call(iGetTensorTypeAndShape, v, uintptr(unsafe.Pointer(&info))); err != nil {
		return Output{}, err
	}
	defer purego.SyscallN(r.fn(iReleaseTensorTypeAndShapeInfo), info)
	var nd uintptr
	if err := r.call(iGetDimensionsCount, info, uintptr(unsafe.Pointer(&nd))); err != nil {
		return Output{}, err
	}
	shape := make([]int64, nd)
	if nd > 0 {
		if err := r.call(iGetDimensions, info, uintptr(unsafe.Pointer(&shape[0])), nd); err != nil {
			return Output{}, err
		}
	}
	n := int64(1)
	for _, d := range shape {
		n *= d
	}
	var p unsafe.Pointer
	if err := r.call(iGetTensorMutableData, v, uintptr(unsafe.Pointer(&p))); err != nil {
		return Output{}, err
	}
	data := make([]float32, n)
	if n > 0 {
		copy(data, unsafe.Slice((*float32)(p), n))
	}
	return Output{Shape: shape, Data: data}, nil
}
