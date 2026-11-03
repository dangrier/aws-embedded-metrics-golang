package emf_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"

	"github.com/dangrier/aws-embedded-metrics-golang/emf"
)

// failingMarshaler is a json.Marshaler that always fails.
type failingMarshaler struct{}

func (failingMarshaler) MarshalJSON() ([]byte, error) { return nil, errors.New("broken") }

// TestPropertyValueNotEncodable checks a value that can't be encoded as JSON
// is skipped and reported when PropertyValue is called, with an error that
// wraps both ErrInvalid and the error from encoding/json.
func TestPropertyValueNotEncodable(t *testing.T) {
	var unsupportedType *json.UnsupportedTypeError
	var unsupportedValue *json.UnsupportedValueError
	var marshalerErr *json.MarshalerError
	tcs := []struct {
		name  string
		value any
		as    any // the encoding/json error the report wraps, if it is the same in every Go version
	}{
		{"channel", make(chan int), &unsupportedType},
		{"func", func() {}, &unsupportedType},
		{"complex", complex(1, 2), &unsupportedType},
		{"NaN", math.NaN(), &unsupportedValue},
		{"infinity", math.Inf(1), &unsupportedValue},
		{"NaN in a map", map[string]float64{"ratio": math.NaN()}, &unsupportedValue},
		{"func in a slice", []any{1, func() {}}, &unsupportedType},
		{"map with struct keys", map[struct{}]int{{}: 1}, nil},
		{"failing Marshaler", failingMarshaler{}, &marshalerErr},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			var errs []error
			emf.New(emf.WithWriter(&buf), emf.WithoutDimensions(), emf.WithErrorHandler(func(err error) {
				errs = append(errs, err)
			})).
				PropertyValue("bad", tc.value).
				Metric("good", 1).
				Log()

			if len(errs) != 1 {
				t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
			}
			if !errors.Is(errs[0], emf.ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid", errs[0])
			}
			if tc.as != nil && !errors.As(errs[0], tc.as) {
				t.Errorf("error %v does not wrap a %T", errs[0], tc.as)
			}
			assertCompliant(t, buf.Bytes())
			if bytes.Contains(buf.Bytes(), []byte(`"bad"`)) {
				t.Errorf("skipped property in output: %s", buf.Bytes())
			}
			if !bytes.Contains(buf.Bytes(), []byte(`"good":1`)) {
				t.Errorf("valid metric missing from output: %s", buf.Bytes())
			}
		})
	}
}

// TestPropertyValueCopied checks changing a map, slice or struct pointer
// after PropertyValue doesn't change what is logged.
func TestPropertyValueCopied(t *testing.T) {
	type order struct {
		Items []string `json:"items"`
	}
	m := map[string]int{"a": 1}
	s := []int{1, 2}
	o := &order{Items: []string{"x"}}

	var buf bytes.Buffer
	logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions()).
		PropertyValue("map", m).
		PropertyValue("slice", s).
		PropertyValue("order", o).
		Metric("m", 1)
	m["a"] = 2
	m["b"] = 3
	s[0] = 9
	o.Items[0] = "y"
	o.Items = append(o.Items, "z")
	logger.Log()

	for _, want := range []string{`"map":{"a":1}`, `"slice":[1,2]`, `"order":{"items":["x"]}`} {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("expected %s in output: %s", want, buf.Bytes())
		}
	}
}

// loggingMarshaler uses the logger while it is being encoded.
type loggingMarshaler struct{ logger *emf.Logger }

func (m loggingMarshaler) MarshalJSON() ([]byte, error) {
	m.logger.Metric("fromMarshaler", 1)
	return []byte(`"ok"`), nil
}

// TestPropertyValueMarshalerCanUseLogger checks a json.Marshaler can call
// back into the logger without deadlocking.
func TestPropertyValueMarshalerCanUseLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions())
	logger.PropertyValue("p", loggingMarshaler{logger}).Log()

	for _, want := range []string{`"p":"ok"`, `"fromMarshaler":1`} {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("expected %s in output: %s", want, buf.Bytes())
		}
	}
}

// TestPropertyValueConcurrent sets property values from many goroutines,
// and changes each map straight after, while other goroutines log. Run with
// -race (just test-race) to catch the logger keeping a caller's map.
func TestPropertyValueConcurrent(t *testing.T) {
	var out syncBuffer
	logger := emf.New(emf.WithWriter(&out), emf.WithoutDimensions(), emf.WithErrorHandler(func(err error) {
		t.Errorf("unexpected error: %v", err)
	}))
	logger.Metric("m", 1)

	const (
		goroutines = 8
		iterations = 25
	)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			m := map[string]any{"n": 0}
			s := []int{0}
			for i := range iterations {
				logger.PropertyValue(fmt.Sprintf("g%d", g), m).
					PropertyValue("shared", s).
					Property(fmt.Sprintf("g%d_s", g), "x").
					PropertyValue(fmt.Sprintf("g%d_s", g), i)
				m["n"] = i
				m[fmt.Sprint(i)] = []int{i}
				s[0] = i
				s = append(s, i)
			}
		})
		wg.Go(func() {
			for range iterations / 5 {
				logger.Log()
			}
		})
	}
	wg.Wait()
	logger.Log()

	for line := range bytes.Lines(out.buf.Bytes()) {
		assertCompliant(t, line)
	}
}
