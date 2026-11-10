//go:build js && wasm

// Size probe (#147): net/http alone (the type http.Handler drags in).
package main

import "net/http"

func main() {
	r, _ := http.NewRequest("GET", "/", nil)
	println(r != nil, http.StatusOK)
}
