package main

import (
	"embed"

	"github.com/joshuasprow/nitrovin/pkg/ui"
)

// swFiles holds the browser's Service Worker module (ADR 0013) when
// scripts/sw.sh built it into sw/ before this binary: release builds do
// (.goreleaser.yaml). A build without it, such as `go install`, serves
// /vocab without the visitor's own files, and says so.
//
//go:embed all:sw
var swFiles embed.FS

// embeddedWorker is the module this binary embeds, nil when it has none.
func embeddedWorker() *ui.Worker {
	module, err := swFiles.ReadFile("sw/sw.wasm")
	if err != nil {
		return nil
	}
	exec, err := swFiles.ReadFile("sw/wasm_exec.js")
	if err != nil {
		return nil
	}
	return &ui.Worker{Module: module, Exec: exec}
}
