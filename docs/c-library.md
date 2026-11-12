# The C library

`libnitrovin` is the decoder as a C shared library, for any language with a
C FFI. It speaks the stdio surface's JSON: `nitrovin_decode` takes one
request line, a decode, a translation, a list or a fit, and returns the reply line
`nitrovin stdio` would write, built by the same Go function, so the two
cannot drift apart. The ABI is fixed by
[the C ABI contract](api/README.md#c-abi-libnitrovin-nitrovinh); this page
is how to use it.

## Get it

Each [nitrovin release](https://github.com/joshuasprow/nitrovin/releases)
has one archive per platform, holding the library, `nitrovin.h`, `LICENSE`
and `NOTICE`:

| Archive | Library | Needs |
| --- | --- | --- |
| `libnitrovin_<version>_linux_amd64.tar.gz`, `…_linux_arm64.tar.gz` | `libnitrovin.so` | glibc 2.17 or newer; not musl |
| `libnitrovin_<version>_darwin_amd64.tar.gz`, `…_darwin_arm64.tar.gz` | `libnitrovin.dylib` (install name `@rpath/libnitrovin.dylib`) | macOS 11 or newer |
| `libnitrovin_<version>_windows_amd64.zip`, `…_windows_arm64.zip` | `libnitrovin.dll` | the Universal CRT |

The library carries no data, so it is 3–4 MB instead of the CLI's 70 MB.
Download an artifact, the data file, from a
[nitrovin-data release](https://github.com/joshuasprow/nitrovin-data/releases);
any month of the same format major loads. Verify both downloads as the
[README](../README.md#verify-a-release) shows.

```sh
gh release download v0.1.2 -R joshuasprow/nitrovin -p 'libnitrovin_0.1.2_darwin_arm64.tar.gz'
tar -xzf libnitrovin_0.1.2_darwin_arm64.tar.gz
gh release download v1.202610.1 -R joshuasprow/nitrovin-data -p nitrovin-2026_10.artifact
```

## C

With the README's [`decode.c`](../README.md#c-library) beside the library:

```sh
cc -Wall -Wextra -o decode decode.c -L. -lnitrovin -Wl,-rpath,.
./decode
```

The same two commands build and run it on Linux against `libnitrovin.so`.

## Python (ctypes)

```python
import ctypes, json

lib = ctypes.CDLL("./libnitrovin.dylib")  # libnitrovin.so on Linux, libnitrovin.dll on Windows
lib.nitrovin_open.restype = ctypes.c_void_p
lib.nitrovin_open.argtypes = [ctypes.c_char_p, ctypes.POINTER(ctypes.c_void_p)]
lib.nitrovin_decode.restype = ctypes.c_void_p
lib.nitrovin_decode.argtypes = [ctypes.c_void_p, ctypes.c_char_p]
lib.nitrovin_free.argtypes = [ctypes.c_void_p]
lib.nitrovin_close.argtypes = [ctypes.c_void_p]

err = ctypes.c_void_p()
h = lib.nitrovin_open(b"nitrovin-2026_10.artifact", ctypes.byref(err))
if not h:
    raise OSError(ctypes.string_at(err.value).decode())
p = lib.nitrovin_decode(h, json.dumps({"vin": "1HGCM82633A004352"}).encode())
reply = json.loads(ctypes.string_at(p))
lib.nitrovin_free(p)
print(reply["decode"]["attributes"]["Make"], reply["decode"]["model_year"])
lib.nitrovin_close(h)
```

```text
HONDA 2003
```

Declare every return type as `c_void_p`, not `c_char_p`: ctypes would copy
a `c_char_p` into a Python string and lose the pointer `nitrovin_free`
needs.

### Vocabularies

`nitrovin_open_vocab` opens the artifact with vocabularies, extension sets,
declared spines and a catalog loaded beside it, the files or directories
`--vocab` takes; a request with `"op": "translate"`, `"op": "list"` or
`"op": "fit"` then answers from them
([Translation](api/README.md#translation-translate),
[Listing](api/README.md#listing-list), [Fit](api/README.md#fit-fit)):

```python
lib.nitrovin_open_vocab.restype = ctypes.c_void_p
lib.nitrovin_open_vocab.argtypes = [ctypes.c_char_p, ctypes.POINTER(ctypes.c_char_p),
                                    ctypes.c_size_t, ctypes.POINTER(ctypes.c_void_p)]

paths = (ctypes.c_char_p * 1)(b"vocab/")  # a directory of *.jsonl
h = lib.nitrovin_open_vocab(b"nitrovin-2026_10.artifact", paths, 1, ctypes.byref(err))
if not h:
    raise OSError(ctypes.string_at(err.value).decode())
req = {"op": "translate", "from": "1GCVKREC6EZ123456", "to": ["yard"]}
p = lib.nitrovin_decode(h, json.dumps(req).encode())
reply = json.loads(ctypes.string_at(p))
lib.nitrovin_free(p)
print([e["entry"] for e in reply["translation"]["to"][0]["entries"]])
lib.nitrovin_close(h)
```

```text
[{'make': 'Chevy', 'model': 'Silverado'}]
```

That is with the repository's `testdata/vocab` as `vocab/`. In C the paths
are a `const char *` array and its length. With a catalog loaded too (the
synthetic one is `testdata/vocab/fitment`), `{"op": "fit", "from":
"1D7RV1GT5AS123456", "part": {"spine": "part-types", "id": "mirror"}}`
answers `{"fit": {"status": "found", "candidates": […], …}}`; on a handle
without a catalog, `{"fit": {"status": "no-catalog", …}}`.

## The rules

- **Requests and replies** are one JSON object each, UTF-8 and
  NUL-terminated: a request is a stdio line, a decode (`{"id": …, "vin": …,
  "model_year": …, "shape": …, "full": …, "now": …}`) or, with `"op":
  "translate"`, `"op": "list"` or `"op": "fit"`, a translation, a list or a
  fit; a reply is `{"id": …, "decode": {…}}`, `{"id": …, "translation":
  {…}}`, `{"id": …, "list": {…}}`, `{"id": …, "fit": {…}}` or `{"id": …,
  "error": {…}}`. A refused request
  is an `error` reply, never a NULL.
- **`nitrovin_info(h)`** returns the `/v1/info` object: decoder version,
  data month, artifact hash, the element table, and the vocabularies the
  handle loaded with their drift counts (`[]` for `nitrovin_open`). A
  library has no stderr, so drift shows only there.
- **A vocabulary fault fails the open**: `nitrovin_open_vocab` returns NULL
  with `*err` naming the file and line, as `--vocab` refuses.
- **Free every string** the library returns with `nitrovin_free`, never your
  own `free()`. On Windows a host built against another C runtime corrupts
  its heap otherwise.
- **Handles are thread-safe.** Decode from as many threads as you like on
  one handle. Two handles may hold two data months in one process.
- **Never unload the library** (`dlclose`, `FreeLibrary`,
  `NativeLibrary.Free`): it starts the Go runtime when it loads, and Go
  cannot be unloaded ([golang/go#11100](https://github.com/golang/go/issues/11100)).
- **Bad handles do not crash.** A NULL handle, or a panic inside a decode,
  gets an `#internal` error reply; `nitrovin_info(NULL)` is NULL;
  `nitrovin_free(NULL)` and `nitrovin_close(NULL)` do nothing. Using a
  handle after `nitrovin_close` is undefined.

## How it is tested

`.github/workflows/libnitrovin.yml` builds all six libraries with `zig cc`
on one Linux job (`scripts/libnitrovin.sh`) and loads each one through
ctypes on its own native runner: Linux, macOS and Windows on amd64 and
arm64. On each, 42 requests (decodes, and a fit with no catalog) must give byte for byte the replies
`nitrovin stdio` gives, again from 8 threads on one handle, and a second
handle must not disturb the first
([#44](https://github.com/joshuasprow/nitrovin/issues/44)). Then a handle
from `nitrovin_open_vocab` with `testdata/vocab` and the synthetic catalog
in `testdata/vocab/fitment` must give the replies `nitrovin --vocab
testdata/vocab --vocab testdata/vocab/fitment stdio` gives to decodes,
translations, lists, fits and refused lines, in order and from 8 threads
([#138](https://github.com/joshuasprow/nitrovin/issues/138),
[#241](https://github.com/joshuasprow/nitrovin/issues/241)).
