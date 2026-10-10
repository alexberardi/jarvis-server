package servertools

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Turn images (docs/cc/chat-images.md §8). A mobile chat message may carry photos; a server
// tool that acts on one (save_recipe_from_image) receives it by reference: the model names
// "image 1", "image 2" (1-based, in the order the user attached them) and the tool resolves the
// number here. Image bytes never pass through the model's arguments, a tool result, a trace or
// a log line.

// Image is one attached photo: its sniffed type and decoded bytes.
type Image struct {
	MIME string
	Data []byte
}

// TurnImages resolves the photos the model currently sees: those of the latest user message
// that had photos (the current turn's, or an earlier turn's, whose bytes the conversation keeps
// for tools after their description replaced them for the model).
type TurnImages interface {
	// Count is the number of attached photos (0 when none or when they expired).
	Count() int
	// Image returns photo n (1-based). Errors: ErrNoImages, ErrImagesExpired, *ImageIndexError.
	Image(n int) (Image, error)
}

var (
	// ErrNoImages: the conversation has no attached photo the tool could use.
	ErrNoImages = errors.New("no photo is attached to this conversation")
	// ErrImagesExpired: only the photo's text description is left (the bytes went with an
	// expired conversation or a restart); the user has to send it again.
	ErrImagesExpired = errors.New("the photo is no longer available (only its description is kept)")
)

// ImageIndexError is a photo number outside 1..Count.
type ImageIndexError struct{ N, Count int }

func (e *ImageIndexError) Error() string {
	if e.Count == 1 {
		return fmt.Sprintf("there is no image %d: only image 1 is attached", e.N)
	}
	return fmt.Sprintf("there is no image %d: images 1 to %d are attached", e.N, e.Count)
}

// ImageNumbers reads an optional list-of-integers argument (e.g. "images": [1, 2]) into photo
// numbers. Absent or empty means every attached photo (nil). Numbers may come as JSON ints,
// floats with no fraction, or digit strings; repeats are dropped, order is kept.
func ImageNumbers(c Call, name string) ([]int, error) {
	v, ok := c.Arg(name)
	if !ok || v == nil {
		return nil, nil
	}
	var raw []any
	switch x := v.(type) {
	case []any:
		raw = x
	default:
		raw = []any{x} // a lone number: be lenient
	}
	seen := map[int]bool{}
	var out []int
	for _, e := range raw {
		n, ok := toInt(e)
		if !ok {
			return nil, fmt.Errorf("%s must be a list of image numbers (1, 2, ...)", name)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		if x == float64(int(x)) {
			return int(x), true
		}
	case string:
		n := 0
		if x == "" {
			return 0, false
		}
		for _, r := range x {
			if r < '0' || r > '9' {
				return 0, false
			}
			n = n*10 + int(r-'0')
			if n > 1<<20 {
				return 0, false
			}
		}
		return n, true
	}
	if b, ok := v.(*big.Int); ok && b.IsInt64() { // pyjson's Python int
		if n := b.Int64(); n >= -1<<20 && n <= 1<<20 {
			return int(n), true
		}
	}
	return 0, false
}

// ResolveImages returns the photos numbered ns (nil or empty = all attached), in that order.
func ResolveImages(ti TurnImages, ns []int) ([]Image, error) {
	if ti == nil {
		return nil, ErrNoImages
	}
	if len(ns) == 0 {
		count := ti.Count()
		if count == 0 {
			// Let the source say whether they expired or never existed.
			_, err := ti.Image(1)
			if err == nil {
				err = ErrNoImages
			}
			return nil, err
		}
		for i := 1; i <= count; i++ {
			ns = append(ns, i)
		}
	}
	out := make([]Image, 0, len(ns))
	for _, n := range ns {
		im, err := ti.Image(n)
		if err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	return out, nil
}

// --- Image parameters (docs/cc/chat-images.md §9, CI7) ---
//
// Any tool, server or client (node), declares a photo parameter the same way: a JSON-schema
// property marked "x-jarvis-type": "image" (jarvis-command-sdk's IMAGE_SCHEMA_MARKER). The
// model fills it with 1-based photo numbers; jarvisd swaps them for the photos before a client
// call reaches the node, and server tools resolve them through Turn.Images. The marker drives
// that and the photo-tool gate; it is stripped before the schema reaches the LLM.

const (
	// ImageSchemaMarker / ImageSchemaMarkerValue mark an image parameter in a tool schema.
	ImageSchemaMarker      = "x-jarvis-type"
	ImageSchemaMarkerValue = "image"
	// MaxImages is how many photos one image argument (and one chat message) holds.
	MaxImages = 4
)

// ImageSchema is the property an image parameter declares: an array of photo numbers, marked.
// It matches jarvis-command-sdk's image_tool_schema, so server and node tools look alike.
func ImageSchema(description string) *pyjson.Object {
	hint := fmt.Sprintf("Photo numbers: a list of integers, 1 = the first photo the user attached, 2 = the second, "+
		"and so on (at most %d).", MaxImages)
	if description != "" {
		hint = description + " " + hint
	}
	return Obj("type", "array", "items", Obj("type", "integer", "minimum", 1), "maxItems", MaxImages,
		"description", hint, ImageSchemaMarker, ImageSchemaMarkerValue)
}

// ImageParam is one image parameter of a tool.
type ImageParam struct {
	Name     string
	Required bool
}

func toolFunction(tool *pyjson.Object) *pyjson.Object {
	if tool == nil {
		return nil
	}
	fv, _ := tool.Get("function")
	fn, _ := fv.(*pyjson.Object)
	return fn
}

// ImageParams lists a tool's image parameters in schema order (nil for none). A tool with any
// is a photo tool.
func ImageParams(tool *pyjson.Object) []ImageParam {
	fn := toolFunction(tool)
	if fn == nil {
		return nil
	}
	pv, _ := fn.Get("parameters")
	params, _ := pv.(*pyjson.Object)
	if params == nil {
		return nil
	}
	propsV, _ := params.Get("properties")
	props, _ := propsV.(*pyjson.Object)
	if props == nil {
		return nil
	}
	required := map[string]bool{}
	if rv, ok := params.Get("required"); ok {
		if l, ok := rv.([]any); ok {
			for _, r := range l {
				if s, ok := r.(string); ok {
					required[s] = true
				}
			}
		}
	}
	var out []ImageParam
	for _, k := range props.Keys() {
		v, _ := props.Get(k)
		if p, ok := v.(*pyjson.Object); ok && IsImageProperty(p) {
			out = append(out, ImageParam{Name: k, Required: required[k]})
		}
	}
	return out
}

// IsImageProperty reports whether a schema property is marked as an image parameter.
func IsImageProperty(p *pyjson.Object) bool {
	v, _ := p.Get(ImageSchemaMarker)
	return v == ImageSchemaMarkerValue
}

// IsPhotoTool reports whether a tool definition declares an image parameter.
func IsPhotoTool(tool *pyjson.Object) bool { return len(ImageParams(tool)) > 0 }

// StripImageMarkers returns tool without its image markers, for the LLM: the marker is ours,
// and a strict backend (or a grammar built from the schema) has no use for an unknown key.
// The schema is otherwise unchanged (an array of integers); a tool without markers is
// returned as is.
func StripImageMarkers(tool *pyjson.Object) *pyjson.Object {
	if !IsPhotoTool(tool) {
		return tool
	}
	return stripMarker(tool).(*pyjson.Object)
}

func stripMarker(v any) any {
	switch x := v.(type) {
	case *pyjson.Object:
		o := pyjson.NewObject()
		for _, k := range x.Keys() {
			val, _ := x.Get(k)
			if k == ImageSchemaMarker && val == ImageSchemaMarkerValue {
				continue
			}
			o.Set(k, stripMarker(val))
		}
		return o
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = stripMarker(e)
		}
		return out
	}
	return v
}
