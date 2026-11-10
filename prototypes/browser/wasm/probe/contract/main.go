//go:build js && wasm

// Size probe (#147): pkg/vocab + pkg/contract + encoding/json.
package main

import (
	"encoding/json"
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

func main() {
	f, err := vocab.Read("x", strings.NewReader(""))
	r, err2 := contract.ParseTranslateRequest([]byte(`{}`))
	b, _ := json.Marshal(r)
	println(f != nil, err != nil, err2 != nil, len(b))
}
