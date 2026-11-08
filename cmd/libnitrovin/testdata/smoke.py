"""Load libnitrovin through ctypes and check it against `nitrovin stdio`.

    python3 smoke.py <library> <artifact> <requests.jsonl> <replies.jsonl>

replies.jsonl is what `nitrovin stdio --data <artifact>` wrote for
requests.jsonl. Every non-blank request line must get exactly its reply
line from nitrovin_decode, called in order and then from eight threads at
once on one handle. Also checked: nitrovin_open's error, nitrovin_info, a
second handle beside the first, and a NULL handle. Exit 0 when all hold.
"""

import ctypes
import json
import os
import sys
from concurrent.futures import ThreadPoolExecutor

lib_path, artifact, requests_path, replies_path = sys.argv[1:5]

# An absolute path: Windows' loader does not search relative ones.
lib = ctypes.CDLL(os.path.abspath(lib_path))
# Returned strings are c_void_p, not c_char_p, so they can be freed.
lib.nitrovin_open.argtypes = [ctypes.c_char_p, ctypes.POINTER(ctypes.c_void_p)]
lib.nitrovin_open.restype = ctypes.c_void_p
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


def decode(h, line):
    return take(lib.nitrovin_decode(h, line))


h, err = open_(artifact + ".missing")
assert h is None and err, f"opening a missing file: handle {h}, err {err!r}"
print(f"nitrovin_open(missing): NULL, {err}")

h, err = open_(artifact)
assert h and err is None, f"nitrovin_open({artifact}): {err}"
info = json.loads(take(lib.nitrovin_info(h)))
print(f"nitrovin_info: data_month {info['data_month']}, decoder_version {info['decoder_version']}")

with open(requests_path, "rb") as f:
    # A Windows checkout may end the lines with \r\n.
    requests = [l.rstrip(b"\r\n") for l in f if l.strip(b" \t\r\n")]
with open(replies_path, "rb") as f:
    replies = [l.rstrip(b"\r\n") for l in f]
assert len(requests) == len(replies), f"{len(requests)} requests, {len(replies)} replies"

def check(got, n):
    if got != replies[n]:
        sys.exit(f"request {n + 1}: {requests[n]!r}\n  library: {got[:300]!r}\n  stdio:   {replies[n][:300]!r}")

for n, req in enumerate(requests):
    check(decode(h, req), n)
print(f"nitrovin_decode: {len(requests)} replies equal stdio's, in order")

rounds = 4
with ThreadPoolExecutor(8) as pool:
    jobs = [(n % len(requests), pool.submit(decode, h, requests[n % len(requests)]))
            for n in range(rounds * len(requests))]
    for n, job in jobs:
        check(job.result(), n)
print(f"nitrovin_decode: {len(jobs)} replies equal stdio's, from 8 threads on one handle")

h2, err = open_(artifact)
assert h2 and err is None, err
check(decode(h2, requests[0]), 0)
lib.nitrovin_close(h2)
check(decode(h, requests[0]), 0)
print("second handle: decodes, closes, first handle unaffected")

null = json.loads(decode(None, requests[0]))
assert null["error"]["type"].endswith("#internal"), null
print("NULL handle: #internal error reply")

lib.nitrovin_close(h)
print("ok")
