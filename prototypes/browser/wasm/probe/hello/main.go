//go:build js && wasm

// Size probe (#147): the Go runtime alone, the floor under every module.
package main

func main() { println("hello") }
