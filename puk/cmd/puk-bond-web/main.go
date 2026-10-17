//go:build js && wasm

package main

import (
	"encoding/json"
	"github.com/welkin-moon/sing-box/puk/bond"
	"syscall/js"
)

func main() {
	fn := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) != 1 {
			return `{"error":"one JSON argument required"}`
		}
		v, e := bond.Compile([]byte(args[0].String()))
		if e != nil {
			b, _ := json.Marshal(map[string]string{"error": e.Error()})
			return string(b)
		}
		b, _ := json.Marshal(v)
		return string(b)
	})
	js.Global().Set("pukCompile", fn)
	select {}
}
