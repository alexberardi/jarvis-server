package servertools

import (
	"errors"
	"fmt"
	"math/big"
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
