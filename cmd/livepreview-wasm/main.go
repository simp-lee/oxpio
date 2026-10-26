//go:build js && wasm

package main

import (
	"encoding/json"
	"syscall/js"

	"github.com/simp-lee/oxpio/internal/livepreview"
)

func main() {
	js.Global().Set("oxpioLivePreviewRender", js.FuncOf(render))
	select {}
}

func render(_ js.Value, args []js.Value) any {
	if len(args) != 1 {
		return js.ValueOf(`{"error":"source is required"}`)
	}
	if args[0].Type() != js.TypeString {
		return js.ValueOf(`{"error":"source must be a string"}`)
	}
	result, err := livepreview.Render(args[0].String())
	if err != nil {
		data, _ := json.Marshal(map[string]string{"error": err.Error()})
		return js.ValueOf(string(data))
	}
	data, err := json.Marshal(result)
	if err != nil {
		return js.ValueOf(`{"error":"could not encode render result"}`)
	}
	return js.ValueOf(string(data))
}
