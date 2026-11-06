package emf

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

// TestDuplicateKey checks duplicateKey finds a repeated key at any depth,
// compares keys as a reader sees them, and isn't fooled by braces, quotes
// or colons inside strings.
func TestDuplicateKey(t *testing.T) {
	tcs := []struct {
		name, json, key string // key is "" if no key is repeated
	}{
		{"number", `1`, ""},
		{"string with braces", `"{\"a\":1,\"a\":2}"`, ""},
		{"empty object", `{}`, ""},
		{"unique keys", `{"a":1,"b":{"a":2,"b":[{"a":3}]},"c":"a"}`, ""},
		{"same key in sibling objects", `[{"a":1},{"a":2}]`, ""},
		{"same key in parent and child", `{"a":{"a":{"a":1}}}`, ""},
		{"repeated", `{"a":1,"a":2}`, "a"},
		{"repeated after a nested object", `{"a":{"b":1,"c":2},"a":3}`, "a"},
		{"repeated in a nested object", `{"x":[1,{"y":{"b":1,"b":2}}]}`, "b"},
		{"escaped", `{"a":1,"\u0061":2}`, "a"},
		{"escaped quote and backslash", `{"\"\\":1,"\u0022\u005c":2}`, `"\`},
		{"key with braces and colons", `{"{:}":1,"}":{"{:}":2},"{:}":3}`, "{:}"},
		{"invalid UTF-8", "{\"\xff\":1,\"\xfe\":2}", "\uFFFD"},
		{"invalid UTF-8 and U+FFFD", "{\"\xff\":1,\"\uFFFD\":2}", "\uFFFD"},
		{"escaped U+FFFD and U+FFFD", `{"\ufffd":1,"` + "\uFFFD" + `":2}`, "\uFFFD"},
		{"lone surrogate and U+FFFD", `{"\ud800":1,"\ufffd":2}`, "\uFFFD"},
		{"non-ASCII", `{"café":1,"cafe":2,"日本":3}`, ""},
		{"many keys", `{"k0":0,"k1":1,"k2":2,"k3":3,"k4":4,"k5":5,"k6":6,"k7":7,"k8":8,"k9":9,` +
			`"k10":10,"k11":11,"k12":12,"k13":13,"k14":14,"k15":15,"k16":16,"k17":17,"k3":18}`, "k3"},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			key, ok := duplicateKey([]byte(tc.json))
			if ok != (tc.key != "") || key != tc.key {
				t.Errorf("expected %q, %v, got %q, %v", tc.key, tc.key != "", key, ok)
			}
			if want, wantOK := referenceDuplicateKey([]byte(tc.json)); wantOK != ok {
				t.Errorf("reference found %q, %v", want, wantOK)
			}
		})
	}
}

// FuzzDuplicateKey checks duplicateKey agrees with a reference that walks
// the JSON with json.Decoder, for any value json.Marshal could write.
func FuzzDuplicateKey(f *testing.F) {
	f.Add(`{"a":1,"b":{"a":2}}`)
	f.Add(`{"a":1,"a":2}`)
	f.Add(`[{"a":{"b":1}},{"c":"{\"d\":1}","c":2}]`)
	f.Add("{\"\xff\":1,\"\\ufffd\":2}")
	f.Add(`{ "a" : 1 , "\u0061" : [ { } ] }`)
	f.Fuzz(func(t *testing.T, s string) {
		// Encode it as json.Marshal does, which checks it and removes
		// whitespace.
		encoded, err := json.Marshal(json.RawMessage(s))
		if err != nil {
			return
		}
		key, ok := duplicateKey(encoded)
		want, wantOK := referenceDuplicateKey(encoded)
		if ok != wantOK {
			t.Fatalf("got %q, %v, reference got %q, %v for %s", key, ok, want, wantOK, encoded)
		}
	})
}

// referenceDuplicateKey reports whether an object in encoded has a key more
// than once, by walking it with json.Decoder.
func referenceDuplicateKey(encoded []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(encoded))
	var stack [][]string // the keys of each open object or array, nil for an array
	expectKey := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", false
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, []string{})
			expectKey = true
			continue
		case json.Delim('['):
			stack = append(stack, nil)
			expectKey = false
			continue
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
		default:
			if key, isKey := tok.(string); isKey && expectKey {
				top := stack[len(stack)-1]
				if slices.Contains(top, key) {
					return key, true
				}
				stack[len(stack)-1] = append(top, key)
				expectKey = false
				continue
			}
		}
		// A value ended, so the next token in an object is a key.
		expectKey = len(stack) > 0 && stack[len(stack)-1] != nil
	}
}
