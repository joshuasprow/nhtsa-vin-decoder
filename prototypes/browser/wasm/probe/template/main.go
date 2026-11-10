//go:build js && wasm

// Size probe (#147): pkg/vocab + pkg/contract + html/template, no net/http.
package main

import (
	"html/template"
	"os"
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

var t = template.Must(template.New("").Parse(`{{.}}`))

func main() {
	f, err := vocab.Read("x", strings.NewReader(""))
	r, err2 := contract.ParseTranslateRequest([]byte(`{}`))
	_ = t.Execute(os.Stdout, r)
	println(f != nil, err != nil, err2 != nil)
}
