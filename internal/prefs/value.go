// Package prefs owns the typed hub preference contract. It has no persistence,
// authentication or worker dependencies; durations are whole seconds on the wire.
package prefs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

type Type string

const (
	TypeBool     Type = "bool"
	TypeInt      Type = "int"
	TypeDuration Type = "duration"
	TypeEnum     Type = "enum"
)

// Value is a closed, comparable scalar union. Its zero value is invalid, so an
// omitted or null input cannot accidentally become an explicit false or zero.
type Value struct {
	kind    Type
	boolean bool
	integer int64
	enum    string
}

func Bool(v bool) Value              { return Value{kind: TypeBool, boolean: v} }
func Int(v int64) Value              { return Value{kind: TypeInt, integer: v} }
func Duration(seconds int64) Value   { return Value{kind: TypeDuration, integer: seconds} }
func Enum(v string) Value            { return Value{kind: TypeEnum, enum: v} }
func (v Value) Type() Type           { return v.kind }
func (v Value) Bool() (bool, bool)   { return v.boolean, v.kind == TypeBool }
func (v Value) Int64() (int64, bool) { return v.integer, v.kind == TypeInt || v.kind == TypeDuration }
func (v Value) Enum() (string, bool) { return v.enum, v.kind == TypeEnum }

func (v Value) MarshalJSON() ([]byte, error) {
	switch v.kind {
	case TypeBool:
		return json.Marshal(v.boolean)
	case TypeInt, TypeDuration:
		if v.integer >= 0 {
			return []byte(strconv.FormatInt(v.integer, 10)), nil
		}
	case TypeEnum:
		if knownEnum(v.enum) {
			return json.Marshal(v.enum)
		}
	}
	return nil, invalid("", "scalar")
}

// UnmarshalJSON accepts only the closed scalar vocabulary, never floats or
// arbitrary strings. A descriptor/patch supplies the numeric duration tag.
func (v *Value) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	var next Value
	switch string(data) {
	case "true":
		next = Bool(true)
	case "false":
		next = Bool(false)
	default:
		if len(data) > 0 && data[0] == '"' {
			var s string
			if json.Unmarshal(data, &s) != nil || !knownEnum(s) {
				return invalid("", "enum")
			}
			next = Enum(s)
		} else {
			if len(data) == 0 {
				return invalid("", "integer")
			}
			for _, c := range data {
				if c < '0' || c > '9' {
					return invalid("", "integer")
				}
			}
			if len(data) > 1 && data[0] == '0' {
				return invalid("", "integer")
			}
			n, err := strconv.ParseInt(string(data), 10, 64)
			if err != nil {
				return invalid("", "integer")
			}
			next = Int(n)
		}
	}
	*v = next
	return nil
}

func knownEnum(s string) bool {
	switch s {
	case "1h", "6h", "12h", "1d", "7d", "30d", "off":
		return true
	}
	return false
}

// strictJSON checks every object before ordinary decoding can discard duplicate
// fields. Token() unescapes names, catching equivalent escaped duplicates too.
// All parse errors are replaced with a safe reason, never rejected raw values.
func strictJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := checkJSONValue(d); err != nil {
		return invalidBody()
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return invalidBody()
	}
	return nil
}

func checkJSONValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("null")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return errors.New("duplicate field")
			}
			seen[key] = true
			if err := checkJSONValue(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := checkJSONValue(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected delimiter")
	}
	_, err = d.Token()
	return err
}
