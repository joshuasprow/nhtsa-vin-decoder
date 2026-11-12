// Command libnitrovin is the C library: built with -buildmode=c-shared it is
// libnitrovin.so, libnitrovin.dylib or libnitrovin.dll, and the header cgo
// writes beside it, shipped as nitrovin.h, declares the six functions
// below. docs/api/README.md ("C ABI") is the contract.
//
// The library is lean: it carries no data, and nitrovin_open loads the
// artifact file --data takes; nitrovin_open_vocab loads the vocabulary
// files --vocab takes beside it. nitrovin_decode answers one StdioRequest,
// of any op, with the StdioReply `nitrovin stdio` writes for it, without
// the newline.
//
//	go build -buildmode=c-shared -o libnitrovin.so ./cmd/libnitrovin
package main

/*
#include <stdlib.h>

// The nitrovin C library (libnitrovin). Contract: docs/api/README.md, "C ABI",
// in github.com/joshuasprow/nitrovin.
//
// - Strings are UTF-8 and NUL-terminated in both directions.
// - Handles are safe to use from several threads at once. Two handles may
//   hold two data months in one process.
// - The library starts the Go runtime when it loads and cannot be unloaded
//   (golang/go#11100): never dlclose, FreeLibrary or NativeLibrary.Free it.
// - Every string the library returns comes from its own allocator: release
//   it with nitrovin_free, never the caller's free().
//
// nitrovin_t *nitrovin_open(const char *artifact_path, char **err);
//     Load an artifact. NULL on failure, with *err (when err is not NULL)
//     set to a message the caller frees with nitrovin_free.
// nitrovin_t *nitrovin_open_vocab(const char *artifact_path,
//                                 const char *const *vocab_paths, size_t n,
//                                 char **err);
//     nitrovin_open, then load the n vocabulary, extension set, spine and
//     catalog paths (files, or directories of *.jsonl) beside the artifact, as
//     --vocab does. A fault in a file fails as nitrovin_open does; drift
//     is served and counted in nitrovin_info. vocab_paths may be NULL
//     when n is 0.
// char *nitrovin_decode(nitrovin_t *h, const char *request_json);
//     request_json: one StdioRequest of any op (decode, translate, list,
//     fit).
//     Returns one StdioReply, exactly the line `nitrovin stdio` would
//     write, without the newline. Never NULL.
// char *nitrovin_info(nitrovin_t *h);
//     The /v1/info object as compact JSON, the loaded vocabularies
//     included; NULL only when h is NULL.
// void nitrovin_free(char *s);
//     Free any string this library returned. NULL is a no-op.
// void nitrovin_close(nitrovin_t *h);
//     Release a handle. Using it afterwards is undefined. NULL is a no-op.

typedef struct nitrovin nitrovin_t;
typedef const char nitrovin_cchar; // const char, as cgo can name it
typedef const char *const nitrovin_cpath; // const char *const, likewise
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"runtime/cgo"
	"unsafe"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/stdio"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// A nitrovin_t is a block of C memory holding the cgo.Handle of its
// *nitrovin.Translator: C keeps no Go pointer, and the handle's address is
// unique for its lifetime.

//export nitrovin_open
func nitrovin_open(artifact_path *C.nitrovin_cchar, err **C.char) *C.nitrovin_t {
	return open("nitrovin_open", artifact_path, nil, 0, err)
}

//export nitrovin_open_vocab
func nitrovin_open_vocab(artifact_path *C.nitrovin_cchar, vocab_paths *C.nitrovin_cpath, n C.size_t, err **C.char) *C.nitrovin_t {
	return open("nitrovin_open_vocab", artifact_path, vocab_paths, n, err)
}

// open loads the artifact, then the n vocabulary paths beside it, as
// --data and --vocab do, into a new handle. fn names the C function in
// errors about its arguments and in a panic's.
func open(fn string, artifact_path *C.nitrovin_cchar, vocab_paths *C.nitrovin_cpath, n C.size_t, err **C.char) (h *C.nitrovin_t) {
	defer func() {
		if r := recover(); r != nil {
			h = nil
			setErr(err, fmt.Sprintf("%s: internal error: %v", fn, r))
		}
	}()
	if artifact_path == nil {
		setErr(err, fn+": artifact_path is NULL")
		return nil
	}
	if vocab_paths == nil && n > 0 {
		setErr(err, fn+": vocab_paths is NULL")
		return nil
	}
	paths := make([]string, n)
	for i, p := range unsafe.Slice(vocab_paths, n) {
		if p == nil {
			setErr(err, fmt.Sprintf("%s: vocab_paths[%d] is NULL", fn, i))
			return nil
		}
		paths[i] = C.GoString((*C.char)(unsafe.Pointer(p)))
	}
	d, oerr := nitrovin.Open(C.GoString((*C.char)(artifact_path)))
	if oerr != nil {
		setErr(err, oerr.Error())
		return nil
	}
	files, oerr := vocab.Open(paths...)
	var t *nitrovin.Translator
	if oerr == nil {
		t, oerr = nitrovin.NewTranslator(d, files...)
	}
	if oerr != nil {
		setErr(err, oerr.Error())
		return nil
	}
	p := C.malloc(C.size_t(unsafe.Sizeof(cgo.Handle(0))))
	*(*cgo.Handle)(p) = cgo.NewHandle(t)
	return (*C.nitrovin_t)(p)
}

func setErr(errOut **C.char, msg string) {
	if errOut != nil {
		*errOut = C.CString(msg)
	}
}

func translatorOf(h *C.nitrovin_t) *nitrovin.Translator {
	return (*(*cgo.Handle)(unsafe.Pointer(h))).Value().(*nitrovin.Translator)
}

// nitrovin_decode answers with stdio.Reply, the bytes `nitrovin stdio`
// writes for the same line, whatever its op. A NULL handle, and a panic
// that would otherwise take the host process down, are an #internal error
// reply without an id.
//
//export nitrovin_decode
func nitrovin_decode(h *C.nitrovin_t, request_json *C.nitrovin_cchar) (out *C.char) {
	defer func() {
		if r := recover(); r != nil {
			out = internalReply(fmt.Sprintf("nitrovin_decode: %v", r))
		}
	}()
	if h == nil {
		return internalReply("nitrovin_decode: the handle is NULL")
	}
	var line []byte
	if request_json != nil {
		line = []byte(C.GoString((*C.char)(request_json)))
	}
	return C.CString(string(stdio.Reply(translatorOf(h), line)))
}

// internalReply is the StdioReply {"error": <#internal problem>}.
func internalReply(detail string) *C.char {
	b, err := json.Marshal(struct {
		Error *contract.Problem `json:"error"`
	}{contract.NewProblem(contract.Internal, "", detail)})
	if err != nil { // a problem is strings and an int: unreachable
		panic(err)
	}
	return C.CString(string(b))
}

//export nitrovin_info
func nitrovin_info(h *C.nitrovin_t) *C.char {
	if h == nil {
		return nil
	}
	b, err := json.Marshal(translatorOf(h).Info())
	if err != nil { // Info is strings, ints and lists of them: unreachable
		panic(err)
	}
	return C.CString(string(b))
}

//export nitrovin_free
func nitrovin_free(s *C.char) {
	C.free(unsafe.Pointer(s))
}

//export nitrovin_close
func nitrovin_close(h *C.nitrovin_t) {
	if h == nil {
		return
	}
	(*(*cgo.Handle)(unsafe.Pointer(h))).Delete()
	C.free(unsafe.Pointer(h))
}

func main() {}
