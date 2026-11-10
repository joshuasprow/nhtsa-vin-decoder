// Package decoder is a Go port of NHTSA's vPIC VIN decoder, the Postgres
// procedure vpic.spvindecode, with ultravin
// (https://github.com/blackthorn-interstellar/ultravin) as reference and
// oracle (ADR 0001).
//
// Every function mirrors a named piece of the procedure, cited as
// <file>:<lines> against the 2026_10 dump's function bodies
// (pg_get_functiondef), and is tested against Postgres running that body.
// The engine reads no clock: the current year is always an argument.
package decoder

// ci-matrix probe

// ci-matrix probe 2
