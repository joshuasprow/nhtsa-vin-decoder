// Command embedded is PROTOTYPE: the go:embed shape of each candidate. The
// measure script copies one artifact (raw or .zst) to ./artifact and its
// "<format> <raw|zstd>" to ./kind.txt, then builds one binary per variant.
package main

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/joshuasprow/nitrovin/prototypes/artifact-format/bake"
)

//go:embed artifact
var artifact []byte

//go:embed kind.txt
var kind string

func main() {
	t0 := time.Now()
	format, comp, _ := strings.Cut(strings.TrimSpace(kind), " ")
	s, err := bake.OpenBytes(format, artifact, comp == "zstd")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	open := time.Since(t0)
	d := bake.NewDecoder(s)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).UnixMicro()
	for _, v := range os.Args[1:] {
		t := time.Now()
		items := d.Decode(v, now, 2026)
		fmt.Printf("%s items=%d decode_us=%d\n", v, len(items), time.Since(t).Microseconds())
	}
	fmt.Printf("kind=%q open_ms=%.2f\n", strings.TrimSpace(kind), float64(open.Microseconds())/1e3)
}
