// Command genfbs prints the flatbuffers schema for the model. PROTOTYPE.
package main

import (
	"fmt"

	"github.com/joshuasprow/nitrovin/prototypes/artifact-format/model"
)

//go:generate sh -c "go run . > ../../artifact.fbs && cd ../.. && rm -rf nvfb && flatc --go --go-namespace nvfb -o . artifact.fbs"

func main() { fmt.Print(model.FBS()) }
