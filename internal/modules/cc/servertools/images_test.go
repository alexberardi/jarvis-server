package servertools

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

func callWith(t *testing.T, args string) Call {
	t.Helper()
	v, err := pyjson.Loads(args)
	if err != nil {
		t.Fatal(err)
	}
	return Call{Args: v.(*pyjson.Object)}
}

func TestImageNumbers(t *testing.T) {
	for _, c := range []struct {
		args string
		want []int
		bad  bool
	}{
		{`{}`, nil, false},
		{`{"images": null}`, nil, false},
		{`{"images": []}`, nil, false},
		{`{"images": [2, 1, 2]}`, []int{2, 1}, false},
		{`{"images": [1.0, "3"]}`, []int{1, 3}, false},
		{`{"images": 2}`, []int{2}, false},
		{`{"images": ["one"]}`, nil, true},
		{`{"images": [1.5]}`, nil, true},
		{`{"images": [99999999999999999999]}`, nil, true},
	} {
		got, err := ImageNumbers(callWith(t, c.args), "images")
		if (err != nil) != c.bad || !slices.Equal(got, c.want) {
			t.Errorf("%s: %v %v", c.args, got, err)
		}
	}
	if got, err := ImageNumbers(Call{}, "images"); got != nil || err != nil {
		t.Errorf("no args: %v %v", got, err)
	}
}

type fixedImages []Image

func (f fixedImages) Count() int { return len(f) }
func (f fixedImages) Image(n int) (Image, error) {
	if n < 1 || n > len(f) {
		return Image{}, &ImageIndexError{N: n, Count: len(f)}
	}
	return f[n-1], nil
}

func TestResolveImages(t *testing.T) {
	src := fixedImages{{MIME: "image/png", Data: []byte("a")}, {MIME: "image/jpeg", Data: []byte("b")}}
	got, err := ResolveImages(src, []int{2})
	if err != nil || len(got) != 1 || string(got[0].Data) != "b" {
		t.Fatalf("[2]: %v %v", got, err)
	}
	var idx *ImageIndexError
	if _, err := ResolveImages(src, []int{1, 5}); !errors.As(err, &idx) || idx.Error() != "there is no image 5: images 1 to 2 are attached" {
		t.Fatalf("[1 5]: %v", err)
	}
	if _, err := ResolveImages(fixedImages{}, nil); err == nil {
		t.Fatal("empty source resolved")
	}
}

func TestImageParamsAndMarker(t *testing.T) {
	v, err := pyjson.Loads(`{"type":"function","function":{"name":"x","parameters":{"type":"object","properties":{` +
		`"a":{"type":"string"},"p":{"type":"array","x-jarvis-type":"image"},"q":{"type":"array","x-jarvis-type":"image"}},` +
		`"required":["q"]}}}`)
	if err != nil {
		t.Fatal(err)
	}
	tool := v.(*pyjson.Object)
	ps := ImageParams(tool)
	if len(ps) != 2 || ps[0] != (ImageParam{"p", false}) || ps[1] != (ImageParam{"q", true}) || !IsPhotoTool(tool) {
		t.Fatalf("params %+v", ps)
	}
	before := pyjson.Dumps(tool, true)
	stripped := StripImageMarkers(tool)
	if s := pyjson.Dumps(stripped, true); strings.Contains(s, ImageSchemaMarker) ||
		s != strings.ReplaceAll(before, `, "x-jarvis-type": "image"`, "") {
		t.Fatalf("stripped %s", s)
	}
	if pyjson.Dumps(tool, true) != before {
		t.Fatal("input changed")
	}
	plain := Obj("type", "function", "function", Obj("name", "y"))
	if StripImageMarkers(plain) != plain || IsPhotoTool(plain) || ImageParams(nil) != nil {
		t.Fatal("plain tool")
	}
	s := ImageSchema("Which photos.")
	if !IsImageProperty(s) || !strings.HasPrefix(pyjson.Str(mustGet(s, "description")), "Which photos. Photo numbers") {
		t.Fatalf("schema %s", pyjson.Dumps(s, true))
	}
}

func mustGet(o *pyjson.Object, k string) any { v, _ := o.Get(k); return v }
