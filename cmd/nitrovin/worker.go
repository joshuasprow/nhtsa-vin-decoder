package main

import (
	"embed"

	"github.com/joshuasprow/nitrovin/pkg/ui"
)

// swFiles holds the browser's Service Worker module (ADR 0013) when
// scripts/sw.sh built it into sw/ before this binary, and EPA's
// vocabulary when scripts/epa.sh fetched it there: release builds do both
// (.goreleaser.yaml). A build without the module, such as `go install`,
// serves /vocab without the visitor's own files, and says so; one without
// EPA's vocabulary offers no one-click load of it.
//
//go:embed all:sw
var swFiles embed.FS

// embeddedWorker is the module this binary embeds, with EPA's vocabulary
// when it has it; nil when it has no module.
func embeddedWorker() *ui.Worker {
	module, err := swFiles.ReadFile("sw/sw.wasm")
	if err != nil {
		return nil
	}
	exec, err := swFiles.ReadFile("sw/wasm_exec.js")
	if err != nil {
		return nil
	}
	epa, _ := swFiles.ReadFile("sw/epa-vehicles.jsonl")
	return &ui.Worker{Module: module, Exec: exec, EPA: epa}
}
