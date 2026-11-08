// Package contract is nitrovin's decode contract in Go: the wire types every
// surface emits (Decode, Item, Problem, Info), the one request every surface
// accepts (Request, with its parsing and validation), and the two layouts of
// a decode's attributes, as pure functions of the decoder's items.
//
// Marshalling these types with encoding/json gives exactly what
// docs/api/decode.schema.json describes; docs/api/README.md is the prose
// contract and the source of the rules cited here.
//
// The engine (package decoder) hands this package a Projection: the
// sanitized VIN, its WMI, the check digit's verdict and the projected items
// of one decode. NewDecode turns it into a Decode. Nothing here reads a
// clock: a Request's Now is resolved by the caller.
package contract
