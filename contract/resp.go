//go:build contract

package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime"
	"strings"
	"testing"
)

// Resp is a fully-read HTTP response. Its Expect* helpers fail the test with the request line,
// status and body, so a contract failure is diagnosable from the log alone.
type Resp struct {
	t      testing.TB
	Method string
	URL    string
	Status int
	Header map[string][]string
	Body   []byte
}

func (r *Resp) describe() string {
	body := string(r.Body)
	if len(body) > 4000 {
		body = body[:4000] + "…(truncated)"
	}
	return fmt.Sprintf("%s %s → %d\nbody: %s", r.Method, r.URL, r.Status, body)
}

// Fatalf fails the test, appending the response.
func (r *Resp) Fatalf(format string, args ...any) {
	r.t.Helper()
	r.t.Fatalf("%s\n%s", fmt.Sprintf(format, args...), r.describe())
}

// ExpectStatus fails unless the status code is want.
func (r *Resp) ExpectStatus(want int) *Resp {
	r.t.Helper()
	if r.Status != want {
		r.Fatalf("status: want %d, got %d", want, r.Status)
	}
	return r
}

// ExpectJSONContentType fails unless Content-Type is application/json.
func (r *Resp) ExpectJSONContentType() *Resp {
	r.t.Helper()
	ct := ""
	if v := r.Header["Content-Type"]; len(v) > 0 {
		ct = v[0]
	}
	mt, _, _ := mime.ParseMediaType(ct)
	if mt != "application/json" {
		r.Fatalf("content-type: want application/json, got %q", ct)
	}
	return r
}

// ExpectEmpty fails unless the body is empty (e.g. 204 responses).
func (r *Resp) ExpectEmpty() *Resp {
	r.t.Helper()
	if len(bytes.TrimSpace(r.Body)) != 0 {
		r.Fatalf("want empty body")
	}
	return r
}

// JSON decodes the body. Numbers decode as json.Number so Int and Num can tell them apart.
func (r *Resp) JSON() any {
	r.t.Helper()
	dec := json.NewDecoder(bytes.NewReader(r.Body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		r.Fatalf("body is not JSON: %v", err)
	}
	if dec.More() {
		r.Fatalf("trailing data after JSON value")
	}
	return v
}

// Object decodes the body as a JSON object.
func (r *Resp) Object() map[string]any {
	r.t.Helper()
	m, ok := r.JSON().(map[string]any)
	if !ok {
		r.Fatalf("body is not a JSON object")
	}
	return m
}

// Decode unmarshals the body into v.
func (r *Resp) Decode(v any) {
	r.t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		r.Fatalf("decode into %T: %v", v, err)
	}
}

// ExpectShape fails unless the JSON body matches m. All mismatches are reported at once.
func (r *Resp) ExpectShape(m Matcher) *Resp {
	r.t.Helper()
	if errs := m.Match("$", r.JSON()); len(errs) > 0 {
		r.Fatalf("shape mismatch:\n  %s", strings.Join(errs, "\n  "))
	}
	return r
}

// ExpectDetail fails unless the body is exactly {"detail": want}, FastAPI's HTTPException shape.
// Clients match on these strings, so they are frozen verbatim.
func (r *Resp) ExpectDetail(want string) *Resp {
	r.t.Helper()
	return r.ExpectShape(Obj{"detail": Eq(want)})
}

// Expect is ExpectStatus + ExpectJSONContentType + ExpectShape.
func (r *Resp) Expect(status int, m Matcher) *Resp {
	r.t.Helper()
	return r.ExpectStatus(status).ExpectJSONContentType().ExpectShape(m)
}

// ExpectError is ExpectStatus + ExpectDetail.
func (r *Resp) ExpectError(status int, detail string) *Resp {
	r.t.Helper()
	return r.ExpectStatus(status).ExpectJSONContentType().ExpectDetail(detail)
}
