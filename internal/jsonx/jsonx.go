// Package jsonx is a tiny order-preserving JSON object model. muster.json is
// edited through it so saving keeps unknown keys and the original key order
// (and therefore clean git diffs).
package jsonx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// Object is an ordered JSON object. Values are *Object, []any, string,
// json.Number, bool or nil.
type Object struct {
	keys []string
	vals map[string]any
}

// New returns an empty object.
func New() *Object { return &Object{vals: map[string]any{}} }

// Parse decodes a JSON object.
func Parse(data []byte) (*Object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected a JSON object")
	}
	obj, err := parseObject(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after JSON object")
	}
	return obj, nil
}

func parseObject(dec *json.Decoder) (*Object, error) {
	o := New()
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("expected object key")
		}
		v, err := parseValue(dec)
		if err != nil {
			return nil, err
		}
		o.Put(key, v)
	}
	if _, err := dec.Token(); err != nil { // closing }
		return nil, err
	}
	return o, nil
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return parseObject(dec)
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return t, nil
	}
}

// Keys returns keys in order.
func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.keys...)
}

// Len returns the number of keys.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Put sets key to v, appending the key if new.
func (o *Object) Put(key string, v any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

// Value returns the direct child value.
func (o *Object) Value(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[key]
	return v, ok
}

// Remove deletes a direct child key.
func (o *Object) Remove(key string) {
	if o == nil {
		return
	}
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Get walks a path of object keys.
func (o *Object) Get(path ...string) (any, bool) {
	var cur any = o
	for _, p := range path {
		obj, ok := cur.(*Object)
		if !ok || obj == nil {
			return nil, false
		}
		cur, ok = obj.vals[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// Obj returns the object at path, or nil.
func (o *Object) Obj(path ...string) *Object {
	v, _ := o.Get(path...)
	obj, _ := v.(*Object)
	return obj
}

// String returns the string at path ("" if missing or not a string).
func (o *Object) String(path ...string) string {
	v, _ := o.Get(path...)
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	}
	return ""
}

// Int returns the integer at path (accepts numbers and numeric strings).
func (o *Object) Int(path ...string) (int, bool) {
	v, _ := o.Get(path...)
	switch t := v.(type) {
	case json.Number:
		n, err := strconv.Atoi(t.String())
		return n, err == nil
	case string:
		n, err := strconv.Atoi(t)
		return n, err == nil
	}
	return 0, false
}

// Bool returns the bool at path and whether it was present as a bool.
func (o *Object) Bool(path ...string) (bool, bool) {
	v, _ := o.Get(path...)
	b, ok := v.(bool)
	return b, ok
}

// Strings returns a string array at path.
func (o *Object) Strings(path ...string) []string {
	v, _ := o.Get(path...)
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Set assigns v at path, creating intermediate objects.
func (o *Object) Set(v any, path ...string) {
	cur := o
	for i, p := range path {
		if i == len(path)-1 {
			cur.Put(p, normalize(v))
			return
		}
		next, ok := cur.vals[p].(*Object)
		if !ok || next == nil {
			next = New()
			cur.Put(p, next)
		}
		cur = next
	}
}

// Delete removes the value at path.
func (o *Object) Delete(path ...string) {
	if len(path) == 0 {
		return
	}
	parent := o.Obj(path[:len(path)-1]...)
	if len(path) == 1 {
		parent = o
	}
	parent.Remove(path[len(path)-1])
}

func normalize(v any) any {
	switch t := v.(type) {
	case int:
		return json.Number(strconv.Itoa(t))
	case []string:
		arr := make([]any, len(t))
		for i, s := range t {
			arr[i] = s
		}
		return arr
	}
	return v
}

// Clone deep-copies the object.
func (o *Object) Clone() *Object {
	if o == nil {
		return nil
	}
	c := New()
	for _, k := range o.keys {
		c.Put(k, cloneValue(o.vals[k]))
	}
	return c
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case *Object:
		return t.Clone()
	case []any:
		c := make([]any, len(t))
		for i, x := range t {
			c[i] = cloneValue(x)
		}
		return c
	}
	return v
}

// MarshalJSON encodes compactly.
func (o *Object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	if err := writeValue(&b, o, "", ""); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Pretty encodes with two-space indentation and a trailing newline (jq style).
func (o *Object) Pretty() ([]byte, error) {
	var b bytes.Buffer
	if err := writeValue(&b, o, "", "  "); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}

func writeValue(b *bytes.Buffer, v any, prefix, indent string) error {
	nl := func(p string) {
		if indent != "" {
			b.WriteByte('\n')
			b.WriteString(p)
		}
	}
	switch t := v.(type) {
	case *Object:
		if t == nil || len(t.keys) == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteByte('{')
		inner := prefix + indent
		for i, k := range t.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			nl(inner)
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			if indent != "" {
				b.WriteByte(' ')
			}
			if err := writeValue(b, t.vals[k], inner, indent); err != nil {
				return err
			}
		}
		nl(prefix)
		b.WriteByte('}')
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteByte('[')
		inner := prefix + indent
		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			nl(inner)
			if err := writeValue(b, x, inner, indent); err != nil {
				return err
			}
		}
		nl(prefix)
		b.WriteByte(']')
	case json.Number:
		b.WriteString(t.String())
	default:
		enc, err := json.Marshal(t)
		if err != nil {
			return err
		}
		b.Write(enc)
	}
	return nil
}
