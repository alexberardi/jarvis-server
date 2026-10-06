package ocr

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// maxBody caps request bodies. Legacy had no cap; a batch of 100 phone photos in base64 is
// well under this.
const maxBody = 256 << 20

// readBody reads the request body, answering 413 when it is too large.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "Request body too large")
		} else {
			httpx.Error(w, http.StatusBadRequest, "Could not read request body")
		}
		return nil, false
	}
	return raw, true
}

// readObject parses a JSON object body with FastAPI's 422 shapes for the failures.
func readObject(w http.ResponseWriter, raw []byte) (*obj, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		httpx.ValidationError(w, httpx.FieldError{Type: "missing", Loc: []any{"body"}, Msg: "Field required"})
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		httpx.ValidationError(w, httpx.FieldError{Type: "json_invalid", Loc: []any{"body", dec.InputOffset()}, Msg: "JSON decode error", Input: map[string]any{}})
		return nil, false
	}
	m, ok := v.(map[string]any)
	if !ok {
		httpx.ValidationError(w, httpx.FieldError{Type: "model_attributes_type", Loc: []any{"body"},
			Msg: "Input should be a valid dictionary or object to extract fields from", Input: v})
		return nil, false
	}
	return &obj{m: m, loc: []any{"body"}, errs: &[]httpx.FieldError{}}, true
}

// obj validates one JSON object's fields, collecting errors like pydantic.
type obj struct {
	m    map[string]any
	loc  []any
	errs *[]httpx.FieldError
}

func (o *obj) fail(typ, msg string, input any, loc ...any) {
	*o.errs = append(*o.errs, httpx.FieldError{Type: typ, Loc: append(append([]any{}, o.loc...), loc...), Msg: msg, Input: input})
}

func (o *obj) done(w http.ResponseWriter) bool {
	if len(*o.errs) > 0 {
		httpx.ValidationError(w, *o.errs...)
		return false
	}
	return true
}

func (o *obj) child(name any, v any) (*obj, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		o.fail("model_type", "Input should be a valid dictionary or instance of the model", v, name)
		return nil, false
	}
	return &obj{m: m, loc: append(append([]any{}, o.loc...), name), errs: o.errs}, true
}

// str reads a string field; ok is false when absent, null or invalid.
func (o *obj) str(name string, required bool) (string, bool) {
	v, present := o.m[name]
	if !present || (v == nil && !required) {
		if required {
			o.fail("missing", "Field required", o.m, name)
		}
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		o.fail("string_type", "Input should be a valid string", v, name)
		return "", false
	}
	return s, true
}

// literal reads an optional string restricted to values, defaulting to def.
func (o *obj) literal(name, def string, values []string) string {
	v, present := o.m[name]
	if !present {
		return def
	}
	if s, ok := v.(string); ok {
		for _, want := range values {
			if s == want {
				return s
			}
		}
	}
	quoted := make([]string, len(values))
	for i, s := range values {
		quoted[i] = "'" + s + "'"
	}
	expected := strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
	o.fail("literal_error", "Input should be "+expected, v, name)
	return def
}

// laxBool is pydantic's lax bool.
func (o *obj) laxBool(name string, def bool) bool {
	v, present := o.m[name]
	if !present {
		return def
	}
	switch x := v.(type) {
	case bool:
		return x
	case json.Number:
		if x.String() == "0" {
			return false
		}
		if x.String() == "1" {
			return true
		}
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "on", "t", "true", "y", "yes":
			return true
		case "0", "off", "f", "false", "n", "no":
			return false
		}
	}
	o.fail("bool_parsing", "Input should be a valid boolean", v, name)
	return def
}

// optStrList reads an optional, nullable list of strings.
func (o *obj) optStrList(name string) []string {
	v, present := o.m[name]
	if !present || v == nil {
		return nil
	}
	l, ok := v.([]any)
	if !ok {
		o.fail("list_type", "Input should be a valid list", v, name)
		return nil
	}
	out := make([]string, 0, len(l))
	for i, e := range l {
		s, ok := e.(string)
		if !ok {
			o.fail("string_type", "Input should be a valid string", e, name, i)
			continue
		}
		out = append(out, s)
	}
	return out
}

// imageInput is the legacy ImageInput {content_type, base64}.
type imageInput struct {
	ContentType string
	Base64      string
}

func (o *obj) image(name any, v any) (imageInput, bool) {
	c, ok := o.child(name, v)
	if !ok {
		return imageInput{}, false
	}
	ct, ok1 := c.str("content_type", true)
	b, ok2 := c.str("base64", true)
	return imageInput{ContentType: ct, Base64: b}, ok1 && ok2
}

// options reads the legacy OCROptions; null or absent means the defaults.
func (o *obj) options() Options {
	opts := Options{ReturnBoxes: true, Mode: "document"}
	v, present := o.m["options"]
	if !present || v == nil {
		return opts
	}
	c, ok := o.child("options", v)
	if !ok {
		return opts
	}
	opts.LanguageHints = c.optStrList("language_hints")
	opts.ReturnBoxes = c.laxBool("return_boxes", true)
	opts.Mode = c.literal("mode", "document", []string{"document", "single_line", "word"})
	return opts
}

// ocrRequest is POST /v1/ocr's body (legacy OCRRequest).
type ocrRequest struct {
	DocumentID *string
	Provider   string
	Image      imageInput
	Options    Options
}

func parseOCRRequest(w http.ResponseWriter, raw []byte) (ocrRequest, bool) {
	o, ok := readObject(w, raw)
	if !ok {
		return ocrRequest{}, false
	}
	var req ocrRequest
	if id, ok := o.str("document_id", false); ok {
		req.DocumentID = &id
	}
	req.Provider = o.literal("provider", "auto", requestProviders)
	if v, present := o.m["image"]; !present {
		o.fail("missing", "Field required", o.m, "image")
	} else {
		req.Image, _ = o.image("image", v)
	}
	req.Options = o.options()
	return req, o.done(w)
}

// batchRequest is POST /v1/ocr/batch's body (legacy OCRBatchRequest).
type batchRequest struct {
	Provider string
	Images   []imageInput
	Options  Options
}

func parseBatchRequest(w http.ResponseWriter, raw []byte) (batchRequest, bool) {
	o, ok := readObject(w, raw)
	if !ok {
		return batchRequest{}, false
	}
	var req batchRequest
	o.str("document_id", false) // optional; validated, unused (as before)
	req.Provider = o.literal("provider", "auto", requestProviders)
	req.Images = o.imageList("images", 1, 100)
	req.Options = o.options()
	return req, o.done(w)
}

// imageList reads a required list of ImageInput with pydantic's length bounds.
func (o *obj) imageList(name string, minN, maxN int) []imageInput {
	v, present := o.m[name]
	if !present {
		o.fail("missing", "Field required", o.m, name)
		return nil
	}
	l, ok := v.([]any)
	if !ok {
		o.fail("list_type", "Input should be a valid list", v, name)
		return nil
	}
	if len(l) < minN {
		o.fail("too_short", "List should have at least 1 item after validation, not 0", v, name)
		return nil
	}
	if len(l) > maxN {
		o.fail("too_long", "List should have at most "+strconv.Itoa(maxN)+" items after validation, not "+strconv.Itoa(len(l)), nil, name)
		return nil
	}
	out := make([]imageInput, 0, len(l))
	lc := &obj{m: nil, loc: append(append([]any{}, o.loc...), name), errs: o.errs}
	for i, e := range l {
		img, ok := lc.image(i, e)
		if ok {
			out = append(out, img)
		}
	}
	return out
}
