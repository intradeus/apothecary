package event

import (
	"reflect"
	"strings"
	"unicode/utf8"
)

// Clean makes payloads fit to store, in place. Their text comes from outside,
// an agent's tool output above all, and may be anything; Postgres refuses a
// NUL in any of it and invalid UTF-8 in a column. A refused append strands the
// run it belongs to, so what can't be stored is replaced with U+FFFD, the mark
// the JSON encoder leaves for invalid text anyway. Every path to the store
// goes through here, which is why this walks the payload rather than naming
// fields: a field added later is covered.
func Clean(ps ...Payload) {
	for _, p := range ps {
		if v := reflect.ValueOf(p); v.Kind() == reflect.Pointer && !v.IsNil() {
			v.Elem().Set(cleaned(v.Elem()))
		}
	}
}

// CleanText is what Clean does to one string.
func CleanText(s string) string {
	if utf8.ValidString(s) && strings.IndexByte(s, 0) < 0 {
		return s
	}
	return strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "�")
}

// cleaned returns v with its text clean, as a copy: neither a map's entries nor
// what an interface holds can be set in place.
func cleaned(v reflect.Value) reflect.Value {
	t := v.Type()
	out := reflect.New(t).Elem()
	switch v.Kind() {
	case reflect.String:
		out.SetString(CleanText(v.String()))
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out.Set(cleaned(v.Elem()))
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out.Set(reflect.New(t.Elem()))
		out.Elem().Set(cleaned(v.Elem()))
	case reflect.Struct:
		out.Set(v)
		for i := range t.NumField() {
			if t.Field(i).IsExported() {
				out.Field(i).Set(cleaned(v.Field(i)))
			}
		}
	case reflect.Slice:
		if v.IsNil() || t.Elem().Kind() == reflect.Uint8 { // bytes are not text
			return v
		}
		out.Set(reflect.MakeSlice(t, v.Len(), v.Len()))
		for i := range v.Len() {
			out.Index(i).Set(cleaned(v.Index(i)))
		}
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out.Set(reflect.MakeMapWithSize(t, v.Len()))
		for it := v.MapRange(); it.Next(); {
			out.SetMapIndex(cleaned(it.Key()), cleaned(it.Value()))
		}
	default:
		return v
	}
	return out
}
