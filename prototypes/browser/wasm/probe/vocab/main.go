//go:build js && wasm

// Size probe (#147): pkg/vocab alone.
package main

import (
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

func main() {
	f, err := vocab.Read("x", strings.NewReader(""))
	println(f != nil, err != nil)
}
