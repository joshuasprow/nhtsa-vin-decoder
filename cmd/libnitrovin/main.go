// Command libnitrovin is the C library: built with -buildmode=c-shared it is
// libnitrovin.so, libnitrovin.dylib or libnitrovin.dll, and the header cgo
// writes beside it, shipped as nitrovin.h, declares the five functions
// below. docs/api/README.md ("C ABI") is the contract.
//
// The library is lean: it carries no data, and nitrovin_open loads the
// artifact file --data takes. nitrovin_decode answers one StdioRequest with
// the StdioReply `nitrovin stdio` writes for it, without the newline.
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
// char *nitrovin_decode(nitrovin_t *h, const char *request_json);
//     request_json: one StdioRequest. Returns one StdioReply, exactly the
//     line `nitrovin stdio` would write, without the newline. Never NULL.
// char *nitrovin_info(nitrovin_t *h);
//     The /v1/info object as compact JSON; NULL only when h is NULL.
// void nitrovin_free(char *s);
//     Free any string this library returned. NULL is a no-op.
// void nitrovin_close(nitrovin_t *h);
//     Release a handle. Using it afterwards is undefined. NULL is a no-op.

typedef struct nitrovin nitrovin_t;
typedef const char nitrovin_cchar; // const char, as cgo can name it
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
)

// A nitrovin_t is a block of C memory holding the cgo.Handle of its
// *nitrovin.Decoder: C keeps no Go pointer, and the handle's address is
// unique for its lifetime.

//export nitrovin_open
func nitrovin_open(artifact_path *C.nitrovin_cchar, err **C.char) (h *C.nitrovin_t) {
	defer func() {
		if r := recover(); r != nil {
			h = nil
			setErr(err, fmt.Sprintf("nitrovin_open: internal error: %v", r))
		}
	}()
	if artifact_path == nil {
		setErr(err, "nitrovin_open: artifact_path is NULL")
		return nil
	}
	d, oerr := nitrovin.Open(C.GoString((*C.char)(artifact_path)))
	if oerr != nil {
		setErr(err, oerr.Error())
		return nil
	}
	p := C.malloc(C.size_t(unsafe.Sizeof(cgo.Handle(0))))
	*(*cgo.Handle)(p) = cgo.NewHandle(d)
	return (*C.nitrovin_t)(p)
}

func setErr(errOut **C.char, msg string) {
	if errOut != nil {
		*errOut = C.CString(msg)
	}
}

func decoderOf(h *C.nitrovin_t) *nitrovin.Decoder {
	return (*(*cgo.Handle)(unsafe.Pointer(h))).Value().(*nitrovin.Decoder)
}

// nitrovin_decode answers with stdio.Reply, the bytes `nitrovin stdio`
// writes for the same line. A NULL handle, and a panic that would otherwise
// take the host process down, are an #internal error reply without an id.
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
	return C.CString(string(stdio.Reply(decoderOf(h), line)))
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
	b, err := json.Marshal(decoderOf(h).Info())
	if err != nil { // Info is strings, ints and a list of them: unreachable
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

// ci-matrix probe
