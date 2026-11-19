package dropapi

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// Build makes the request of one operation from its typed input, checking
// that the operation exists and that input is its input type (a value or a
// pointer). The CLI and the MCP tools build every call this way.
func Build(op string, input any, idempotencyKey string) (Request, error) {
	spec, ok := Lookup(op)
	if !ok {
		return Request{}, fmt.Errorf("unknown operation %q", op)
	}
	want := reflect.TypeOf(spec.Input).Elem()
	got := reflect.TypeOf(input)
	if got != nil && got.Kind() == reflect.Pointer {
		got = got.Elem()
	}
	if got != want {
		return Request{}, fmt.Errorf("operation %s takes %s, not %v", op, want.Name(), got)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return Request{}, err
	}
	if idempotencyKey != "" && !IdempotencyKeyPattern.MatchString(idempotencyKey) {
		return Request{}, fmt.Errorf("an idempotency key is 1-128 letters, digits, '.', '_', ':' or '-'")
	}
	return Request{Op: op, Input: raw, IdempotencyKey: idempotencyKey}, nil
}

// MustBuild is Build for inputs known to be valid; it panics otherwise.
func MustBuild(op string, input any) Request {
	r, err := Build(op, input, "")
	if err != nil {
		panic(err)
	}
	return r
}
