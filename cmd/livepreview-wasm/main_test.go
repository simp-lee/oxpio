//go:build js && wasm

package main

import (
	"syscall/js"
	"testing"
)

func TestRenderRejectsNonStringSource(t *testing.T) {
	tests := []struct {
		name  string
		value js.Value
	}{
		{name: "null", value: js.Null()},
		{name: "number", value: js.ValueOf(42)},
		{name: "boolean", value: js.ValueOf(true)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, ok := render(js.Undefined(), []js.Value{test.value}).(js.Value)
			if !ok {
				t.Fatal("render returned a non-JavaScript value")
			}
			if got := result.String(); got != `{"error":"source must be a string"}` {
				t.Fatalf("render() = %q, want source type error", got)
			}
		})
	}
}
