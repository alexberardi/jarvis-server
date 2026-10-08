//go:build darwin

package ocr

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// The native Apple Vision engine: VNRecognizeTextRequest through purego's Objective-C runtime.
// No cgo, no helper process, no entitlement: Vision and Foundation are Apple system frameworks
// (signed by Apple, in the shared cache), so the hardened runtime loads them whatever the
// binary's library-validation setting, and Vision's text recognition needs no TCC grant.
//
// Crash safety. An Objective-C exception (an unrecognised selector above all) cannot be caught
// from Go and aborts the process, so nothing is sent that has not been checked: every class is
// looked up and every selector is checked against its class once at load (instancesRespondTo
// Selector: / respondsToSelector:), and each object Vision hands back is checked with
// respondsToSelector: before it is messaged. A Go panic in the call path is recovered into an
// error. Anything missing at load leaves the engine unavailable with the reason.
//
// Memory. Each recognition runs on one locked OS thread inside its own NSAutoreleasePool, so
// the autoreleased NSData, arrays, results, candidates and strings go when the pool drains; the
// two objects this code owns (the alloc'd handler and request) are released explicitly.

const (
	foundationFramework = "/System/Library/Frameworks/Foundation.framework/Foundation"
	visionFramework     = "/System/Library/Frameworks/Vision.framework/Vision"

	// VNRequestTextRecognitionLevel: Accurate = 0, Fast = 1. Fast is unusable for documents
	// (one 8-character observation where Accurate returns 94 lines; jarvis-osx-api's notes).
	vnRecognitionLevelAccurate = 0
)

// cgRect is CGRect on 64-bit Apple platforms (four CGFloat = float64).
type cgRect struct {
	X, Y, W, H float64
}

type visionRuntime struct {
	pool, data, marray, dict, handler, request objc.Class

	alloc, init, release, drain, respondsTo, instancesRespondTo objc.SEL
	dataWithBytesLength, array, addObject, dictionary           objc.SEL
	stringWithUTF8, utf8String, count, objectAtIndex            objc.SEL
	initWithDataOptions, performRequestsError                   objc.SEL
	setRecognitionLevel, setUsesLanguageCorrection              objc.SEL
	setRecognitionLanguages, results, cancel                    objc.SEL
	topCandidates, boundingBox, str, confidence                 objc.SEL
	localizedDescription                                        objc.SEL

	nsString objc.Class
}

var (
	visionOnce sync.Once
	visionRT   *visionRuntime
	visionErr  error
)

// loadVision loads Foundation and Vision and resolves every class and selector, once.
func loadVision() (*visionRuntime, error) {
	visionOnce.Do(func() {
		defer func() {
			if p := recover(); p != nil {
				visionRT, visionErr = nil, fmt.Errorf("loading Vision panicked: %v", p)
			}
		}()
		visionRT, visionErr = resolveVision()
	})
	return visionRT, visionErr
}

func resolveVision() (*visionRuntime, error) {
	for _, fw := range []string{foundationFramework, visionFramework} {
		if _, err := purego.Dlopen(fw, purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
			return nil, fmt.Errorf("load %s: %w", fw, err)
		}
	}
	rt := &visionRuntime{}
	classes := []struct {
		dst  *objc.Class
		name string
	}{
		{&rt.pool, "NSAutoreleasePool"}, {&rt.data, "NSData"}, {&rt.marray, "NSMutableArray"},
		{&rt.dict, "NSDictionary"}, {&rt.nsString, "NSString"},
		{&rt.handler, "VNImageRequestHandler"}, {&rt.request, "VNRecognizeTextRequest"},
	}
	for _, c := range classes {
		if *c.dst = objc.GetClass(c.name); *c.dst == 0 {
			return nil, fmt.Errorf("Objective-C class %s not found", c.name)
		}
	}
	sels := []struct {
		dst  *objc.SEL
		name string
	}{
		{&rt.alloc, "alloc"}, {&rt.init, "init"}, {&rt.release, "release"}, {&rt.drain, "drain"},
		{&rt.respondsTo, "respondsToSelector:"}, {&rt.instancesRespondTo, "instancesRespondToSelector:"},
		{&rt.dataWithBytesLength, "dataWithBytes:length:"}, {&rt.array, "array"}, {&rt.addObject, "addObject:"},
		{&rt.dictionary, "dictionary"}, {&rt.stringWithUTF8, "stringWithUTF8String:"},
		{&rt.utf8String, "UTF8String"}, {&rt.count, "count"}, {&rt.objectAtIndex, "objectAtIndex:"},
		{&rt.initWithDataOptions, "initWithData:options:"}, {&rt.performRequestsError, "performRequests:error:"},
		{&rt.setRecognitionLevel, "setRecognitionLevel:"}, {&rt.setUsesLanguageCorrection, "setUsesLanguageCorrection:"},
		{&rt.setRecognitionLanguages, "setRecognitionLanguages:"}, {&rt.results, "results"}, {&rt.cancel, "cancel"},
		{&rt.topCandidates, "topCandidates:"}, {&rt.boundingBox, "boundingBox"}, {&rt.str, "string"},
		{&rt.confidence, "confidence"}, {&rt.localizedDescription, "localizedDescription"},
	}
	for _, s := range sels {
		if *s.dst = objc.RegisterName(s.name); *s.dst == 0 {
			return nil, fmt.Errorf("selector %s not registered", s.name)
		}
	}
	// respondsToSelector: and instancesRespondToSelector: are NSObject's; check them by hand
	// before using them to check the rest.
	nsobject := objc.GetClass("NSObject")
	if nsobject == 0 {
		return nil, errors.New("Objective-C class NSObject not found")
	}
	classMethods := []struct {
		cls  objc.Class
		name string
		sel  objc.SEL
	}{
		{rt.pool, "NSAutoreleasePool", rt.alloc}, {rt.data, "NSData", rt.dataWithBytesLength},
		{rt.marray, "NSMutableArray", rt.array}, {rt.dict, "NSDictionary", rt.dictionary},
		{rt.nsString, "NSString", rt.stringWithUTF8}, {rt.handler, "VNImageRequestHandler", rt.alloc},
		{rt.request, "VNRecognizeTextRequest", rt.alloc},
	}
	for _, m := range classMethods {
		if !objc.Send[bool](objc.ID(m.cls), rt.respondsTo, m.sel) {
			return nil, fmt.Errorf("%s does not respond to +%s", m.name, selName(rt, m.sel))
		}
	}
	instanceMethods := []struct {
		cls  objc.Class
		name string
		sels []objc.SEL
	}{
		{rt.pool, "NSAutoreleasePool", []objc.SEL{rt.init, rt.drain}},
		{rt.marray, "NSMutableArray", []objc.SEL{rt.addObject, rt.count, rt.objectAtIndex}},
		{rt.nsString, "NSString", []objc.SEL{rt.utf8String}},
		{rt.handler, "VNImageRequestHandler", []objc.SEL{rt.initWithDataOptions, rt.performRequestsError, rt.release}},
		{rt.request, "VNRecognizeTextRequest", []objc.SEL{rt.init, rt.setRecognitionLevel, rt.setUsesLanguageCorrection,
			rt.setRecognitionLanguages, rt.results, rt.cancel, rt.release}},
	}
	for _, m := range instanceMethods {
		for _, sel := range m.sels {
			if !objc.Send[bool](objc.ID(m.cls), rt.instancesRespondTo, sel) {
				return nil, fmt.Errorf("%s does not respond to -%s", m.name, selName(rt, sel))
			}
		}
	}
	return rt, nil
}

// selName names a selector for an error message.
func selName(rt *visionRuntime, sel objc.SEL) string {
	names := map[objc.SEL]string{rt.alloc: "alloc", rt.init: "init", rt.drain: "drain", rt.release: "release",
		rt.dataWithBytesLength: "dataWithBytes:length:", rt.array: "array", rt.dictionary: "dictionary",
		rt.stringWithUTF8: "stringWithUTF8String:", rt.addObject: "addObject:", rt.count: "count",
		rt.objectAtIndex: "objectAtIndex:", rt.utf8String: "UTF8String", rt.initWithDataOptions: "initWithData:options:",
		rt.performRequestsError: "performRequests:error:", rt.setRecognitionLevel: "setRecognitionLevel:",
		rt.setUsesLanguageCorrection: "setUsesLanguageCorrection:", rt.setRecognitionLanguages: "setRecognitionLanguages:",
		rt.results: "results", rt.cancel: "cancel"}
	if n, ok := names[sel]; ok {
		return n
	}
	return "?"
}

// responds reports whether obj is non-nil and answers sel.
func (rt *visionRuntime) responds(obj objc.ID, sel objc.SEL) bool {
	return obj != 0 && objc.Send[bool](obj, rt.respondsTo, sel)
}

// goString copies an NSString's UTF-8 contents ("" for nil or a non-string).
func (rt *visionRuntime) goString(s objc.ID) string {
	if !rt.responds(s, rt.utf8String) {
		return ""
	}
	return objc.Send[string](s, rt.utf8String)
}

func (rt *visionRuntime) errorText(e objc.ID) string {
	if !rt.responds(e, rt.localizedDescription) {
		return "unknown error"
	}
	if s := rt.goString(e.Send(rt.localizedDescription)); s != "" {
		return s
	}
	return "unknown error"
}

// NativeAppleVision is the in-process Apple Vision engine (darwin).
type NativeAppleVision struct {
	rt *visionRuntime
	mu sync.Mutex // one recognition at a time: Vision is heavy and the callers are sequential anyway
}

// newNativeAppleVision loads Vision; the error says why it can't be used.
func newNativeAppleVision() (Engine, error) {
	rt, err := loadVision()
	if err != nil {
		return nil, err
	}
	return &NativeAppleVision{rt: rt}, nil
}

func (n *NativeAppleVision) Name() string                   { return EngineAppleVision }
func (n *NativeAppleVision) Available(context.Context) bool { return n != nil && n.rt != nil }

func (n *NativeAppleVision) Diagnose(ctx context.Context) Diagnostic {
	if !n.Available(ctx) {
		return Diagnostic{Provider: EngineAppleVision, Reason: "unavailable", Detail: strp("Vision framework not loaded")}
	}
	return Diagnostic{Provider: EngineAppleVision, Available: true, Reason: "ok", Detail: strp("native (Vision.framework)")}
}

func (n *NativeAppleVision) Recognize(ctx context.Context, img Image, o Options) (Result, error) {
	if !n.Available(ctx) {
		return Result{}, errors.New("Apple Vision is not available")
	}
	if len(img.Data) == 0 {
		return Result{}, errors.New("apple_vision: empty image")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	langs := visionLanguages(o.LanguageHints)
	obs, err := n.run(ctx, img.Data, langs)
	if err != nil && len(langs) > 0 && ctx.Err() == nil {
		// An unsupported language fails the request; read with Vision's defaults instead.
		obs, err = n.run(ctx, img.Data, nil)
	}
	if err != nil {
		return Result{}, err
	}
	w, h, _ := imageSize(img.Data)
	return mapVision(obs, w, h, o.ReturnBoxes), nil
}

// run performs one recognition on its own locked thread (the autorelease pool is per thread),
// cancelling the request if ctx ends.
func (n *NativeAppleVision) run(ctx context.Context, data []byte, langs []string) ([]visionObservation, error) {
	type outcome struct {
		obs []visionObservation
		err error
	}
	done := make(chan outcome, 1)
	var (
		reqMu sync.Mutex
		req   objc.ID // the live request, 0 once released
	)
	go func() {
		var out outcome
		defer func() {
			if p := recover(); p != nil {
				out = outcome{err: fmt.Errorf("apple_vision: recognition panicked: %v", p)}
			}
			done <- out
		}()
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		out.obs, out.err = n.recognize(data, langs, func(r objc.ID) {
			reqMu.Lock()
			req = r
			reqMu.Unlock()
		})
	}()
	select {
	case out := <-done:
		return out.obs, out.err
	case <-ctx.Done():
		reqMu.Lock()
		if req != 0 {
			req.Send(n.rt.cancel)
		}
		reqMu.Unlock()
		<-done // a cancelled request returns promptly; the next recognition must not overlap it
		return nil, ctx.Err()
	}
}

// recognize is the Vision call. setReq publishes the request (and 0 before it is released) so
// a cancellation can reach it.
func (n *NativeAppleVision) recognize(data []byte, langs []string, setReq func(objc.ID)) ([]visionObservation, error) {
	rt := n.rt
	pool := objc.ID(rt.pool).Send(rt.alloc).Send(rt.init)
	if pool == 0 {
		return nil, errors.New("apple_vision: could not create an autorelease pool")
	}
	defer pool.Send(rt.drain)

	nsData := objc.ID(rt.data).Send(rt.dataWithBytesLength, &data[0], uint(len(data)))
	runtime.KeepAlive(data)
	if nsData == 0 {
		return nil, errors.New("apple_vision: could not wrap the image bytes")
	}
	options := objc.ID(rt.dict).Send(rt.dictionary)
	// Vision decodes JPEG/PNG/HEIC/… itself and honours EXIF orientation: the bytes go straight
	// in, as in jarvis-osx-api.
	handler := objc.ID(rt.handler).Send(rt.alloc).Send(rt.initWithDataOptions, nsData, options)
	if handler == 0 {
		return nil, errors.New("apple_vision: could not create the image request handler")
	}
	defer handler.Send(rt.release)

	req := objc.ID(rt.request).Send(rt.alloc).Send(rt.init)
	if req == 0 {
		return nil, errors.New("apple_vision: could not create the text request")
	}
	defer func() {
		setReq(0)
		req.Send(rt.release)
	}()
	req.Send(rt.setRecognitionLevel, int(vnRecognitionLevelAccurate))
	req.Send(rt.setUsesLanguageCorrection, true)
	if len(langs) > 0 {
		arr := objc.ID(rt.marray).Send(rt.array)
		for _, l := range langs {
			if s := objc.ID(rt.nsString).Send(rt.stringWithUTF8, l); s != 0 {
				arr.Send(rt.addObject, s)
			}
		}
		req.Send(rt.setRecognitionLanguages, arr)
	}
	requests := objc.ID(rt.marray).Send(rt.array)
	requests.Send(rt.addObject, req)
	setReq(req)

	var nsErr objc.ID
	if ok := objc.Send[bool](handler, rt.performRequestsError, requests, &nsErr); !ok || nsErr != 0 {
		return nil, fmt.Errorf("apple_vision recognition failed: %s", rt.errorText(nsErr))
	}
	results := req.Send(rt.results)
	if !rt.responds(results, rt.count) || !rt.responds(results, rt.objectAtIndex) {
		return []visionObservation{}, nil
	}
	cnt := objc.Send[uint](results, rt.count)
	obs := make([]visionObservation, 0, cnt)
	for i := uint(0); i < cnt; i++ {
		o := results.Send(rt.objectAtIndex, i)
		if !rt.responds(o, rt.topCandidates) || !rt.responds(o, rt.boundingBox) {
			continue
		}
		cands := o.Send(rt.topCandidates, uint(1))
		if !rt.responds(cands, rt.count) || objc.Send[uint](cands, rt.count) == 0 {
			continue
		}
		c := cands.Send(rt.objectAtIndex, uint(0))
		if !rt.responds(c, rt.str) {
			continue
		}
		v := visionObservation{Text: rt.goString(c.Send(rt.str))}
		if rt.responds(c, rt.confidence) {
			v.Confidence = float64(objc.Send[float32](c, rt.confidence))
		}
		box := objc.Send[cgRect](o, rt.boundingBox)
		v.X, v.Y, v.W, v.H = box.X, box.Y, box.W, box.H
		obs = append(obs, v)
	}
	return obs, nil
}
