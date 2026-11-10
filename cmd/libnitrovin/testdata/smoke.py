"""Load libnitrovin through ctypes and check it against `nitrovin stdio`.

    python3 smoke.py <library> <artifact> <requests.jsonl> <replies.jsonl> \
        <vocab-requests.jsonl> <vocab-replies.jsonl> <vocab-file>...

replies.jsonl is what `nitrovin stdio --data <artifact>` wrote for
requests.jsonl, and vocab-replies.jsonl what it wrote for
vocab-requests.jsonl with the vocabulary files loaded (--vocab). On a
handle from nitrovin_open, and on one from nitrovin_open_vocab with the
files, every non-blank request line must get exactly its reply line from
nitrovin_decode, called in order and then from eight threads at once on
one handle. Also checked: both open functions' errors, nitrovin_info with
and without vocabularies, a second handle beside the first, and a NULL
handle. Exit 0 when all hold.
"""

import ctypes
import json
import os
import sys
from concurrent.futures import ThreadPoolExecutor

(lib_path, artifact, requests_path, replies_path,
 vocab_requests_path, vocab_replies_path, *vocab_files) = sys.argv[1:]
assert vocab_files, "no vocabulary files"

# An absolute path: Windows' loader does not search relative ones.
lib = ctypes.CDLL(os.path.abspath(lib_path))
# Returned strings are c_void_p, not c_char_p, so they can be freed.
lib.nitrovin_open.argtypes = [ctypes.c_char_p, ctypes.POINTER(ctypes.c_void_p)]
lib.nitrovin_open.restype = ctypes.c_void_p
lib.nitrovin_open_vocab.argtypes = [ctypes.c_char_p, ctypes.POINTER(ctypes.c_char_p),
                                    ctypes.c_size_t, ctypes.POINTER(ctypes.c_void_p)]
lib.nitrovin_open_vocab.restype = ctypes.c_void_p
lib.nitrovin_decode.argtypes = [ctypes.c_void_p, ctypes.c_char_p]
lib.nitrovin_decode.restype = ctypes.c_void_p
lib.nitrovin_info.argtypes = [ctypes.c_void_p]
lib.nitrovin_info.restype = ctypes.c_void_p
lib.nitrovin_free.argtypes = [ctypes.c_void_p]
lib.nitrovin_free.restype = None
lib.nitrovin_close.argtypes = [ctypes.c_void_p]
lib.nitrovin_close.restype = None


def take(p):
    """The library's string at p, freed with nitrovin_free."""
    assert p, "the library returned NULL"
    try:
        return ctypes.string_at(p)
    finally:
        lib.nitrovin_free(p)


def open_(path):
    err = ctypes.c_void_p()
    h = lib.nitrovin_open(path.encode(), ctypes.byref(err))
    return h, (take(err.value).decode() if err.value else None)


def open_vocab(path, files, n=None):
    """nitrovin_open_vocab with files (None for a NULL array) and n, by
    default len(files)."""
    paths = None
    if files is not None:
        paths = (ctypes.c_char_p * len(files))(*(f.encode() for f in files))
    if n is None:
        n = len(files)
    err = ctypes.c_void_p()
    h = lib.nitrovin_open_vocab(path.encode(), paths, n, ctypes.byref(err))
    return h, (take(err.value).decode() if err.value else None)


def decode(h, line):
    return take(lib.nitrovin_decode(h, line))


def info(h):
    return json.loads(take(lib.nitrovin_info(h)))


def lines(path):
    with open(path, "rb") as f:
        # A Windows checkout may end the lines with \r\n.
        return [l.rstrip(b"\r\n") for l in f if l.strip(b" \t\r\n")]


def session(h, name, requests, replies):
    """Every request on h gives its reply, in order and from 8 threads."""
    assert len(requests) == len(replies), f"{name}: {len(requests)} requests, {len(replies)} replies"

    def check(got, n):
        if got != replies[n]:
            sys.exit(f"{name} request {n + 1}: {requests[n]!r}\n  library: {got[:300]!r}\n  stdio:   {replies[n][:300]!r}")

    for n, req in enumerate(requests):
        check(decode(h, req), n)
    print(f"{name}: {len(requests)} replies equal stdio's, in order")

    rounds = 4
    with ThreadPoolExecutor(8) as pool:
        jobs = [(n % len(requests), pool.submit(decode, h, requests[n % len(requests)]))
                for n in range(rounds * len(requests))]
        for n, job in jobs:
            check(job.result(), n)
    print(f"{name}: {len(jobs)} replies equal stdio's, from 8 threads on one handle")
    return check


h, err = open_(artifact + ".missing")
assert h is None and err, f"opening a missing file: handle {h}, err {err!r}"
print(f"nitrovin_open(missing): NULL, {err}")

h, err = open_(artifact)
assert h and err is None, f"nitrovin_open({artifact}): {err}"
i = info(h)
assert i["vocabularies"] == [], i["vocabularies"]
print(f"nitrovin_info: data_month {i['data_month']}, decoder_version {i['decoder_version']}, no vocabularies")

requests, replies = lines(requests_path), lines(replies_path)
check = session(h, "nitrovin_open", requests, replies)

h2, err = open_(artifact)
assert h2 and err is None, err
check(decode(h2, requests[0]), 0)
lib.nitrovin_close(h2)
check(decode(h, requests[0]), 0)
print("second handle: decodes, closes, first handle unaffected")

null = json.loads(decode(None, requests[0]))
assert null["error"]["type"].endswith("#internal"), null
print("NULL handle: #internal error reply")

missing = vocab_files[0] + ".missing"
hv, err = open_vocab(artifact, vocab_files + [missing])
assert hv is None and err and os.path.basename(missing) in err, f"a missing vocabulary file: handle {hv}, err {err!r}"
print(f"nitrovin_open_vocab(missing file): NULL, {err}")
hv, err = open_vocab(artifact, None, 1)
assert hv is None and err, f"NULL vocab_paths with n 1: handle {hv}, err {err!r}"
print(f"nitrovin_open_vocab(NULL, 1): NULL, {err}")
hv, err = open_vocab(artifact + ".missing", vocab_files)
assert hv is None and err, f"a missing artifact: handle {hv}, err {err!r}"
hv, err = open_vocab(artifact, None, 0)
assert hv and err is None, err
assert info(hv)["vocabularies"] == []
lib.nitrovin_close(hv)
print("nitrovin_open_vocab(NULL, 0): a handle with no vocabularies")

hv, err = open_vocab(artifact, vocab_files)
assert hv and err is None, f"nitrovin_open_vocab({vocab_files}): {err}"
loaded = info(hv)["vocabularies"]
names = [json.loads(lines(f)[0])["name"] for f in vocab_files]  # each header's
assert [v["name"] for v in loaded] == names, loaded
for v, f in zip(loaded, vocab_files):
    assert os.path.isabs(v["path"]) and os.path.samefile(v["path"], f), (v["path"], f)
print(f"nitrovin_info: vocabularies {', '.join(names)}")

session(hv, "nitrovin_open_vocab", lines(vocab_requests_path), lines(vocab_replies_path))
check(decode(h, requests[0]), 0)
lib.nitrovin_close(hv)
check(decode(h, requests[0]), 0)
print("vocabulary handle: translates, closes, first handle unaffected")

lib.nitrovin_close(h)
print("ok")
