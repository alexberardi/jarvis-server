package nodesettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"
)

// Object is a command-data record: a JSON object that keeps its key order and its values'
// exact bytes. A Python command saves a dict and gets the same dict back, in the same order,
// as from the Pi's command_data table; the SDK's default display_summary titles a row with its
// first string field, so order is visible in the app. The zero value is an empty object.
type Object struct {
	keys []string
	vals map[string]json.RawMessage
}

// ObjectOf builds an Object from a map, in key order (for tests and Go callers).
func ObjectOf(m map[string]any) (Object, error) {
	var o Object
	for _, k := range slices.Sorted(maps.Keys(m)) {
		b, err := json.Marshal(m[k])
		if err != nil {
			return Object{}, err
		}
		o.Set(k, b)
	}
	return o, nil
}

// Keys are the object's keys in order.
func (o Object) Keys() []string { return slices.Clone(o.keys) }

// Len is the number of keys.
func (o Object) Len() int { return len(o.keys) }

// Get is one value's JSON.
func (o Object) Get(k string) (json.RawMessage, bool) {
	v, ok := o.vals[k]
	return v, ok
}

// Value decodes one value (numbers as json.Number); nil when absent.
func (o Object) Value(k string) any {
	raw, ok := o.vals[k]
	if !ok {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	_ = dec.Decode(&v)
	return v
}

// Set adds or replaces a value; a new key goes last, a replaced one keeps its place (a dict).
func (o *Object) Set(k string, v json.RawMessage) {
	if o.vals == nil {
		o.vals = map[string]json.RawMessage{}
	}
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = slices.Clone(v)
}

// Clone is an independent copy.
func (o Object) Clone() Object {
	var c Object
	for _, k := range o.keys {
		c.Set(k, o.vals[k])
	}
	return c
}

// Without is a copy minus the keys drop says so.
func (o Object) Without(drop func(string) bool) Object {
	var c Object
	for _, k := range o.keys {
		if !drop(k) {
			c.Set(k, o.vals[k])
		}
	}
	return c
}

// MarshalJSON writes the keys in order.
func (o Object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(o.vals[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON reads an object, keeping its order (a repeated key keeps its first place and
// its last value, as Python's json.loads). JSON null is an empty object.
func (o *Object) UnmarshalJSON(data []byte) error {
	*o = Object{}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("command data: not a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		k, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return err
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, v); err != nil {
			return err
		}
		o.Set(k, compact.Bytes())
	}
	_, err = dec.Token() // '}'
	return err
}
