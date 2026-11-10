"""Print ultravin's decode values for a VIN list, for `nitrovin-bench agree`.

Run inside an ultravin checkout's environment (scripts/bench.sh agree does):

    uv run --project <ultravin> --frozen python scripts/bench/ultravin_values.py VINS

One JSON line per VIN, in input order: {"vin", "values": [[element id, value]]},
every element row of ultravin's full decode at the benchmark's frozen clock,
None as "". The same shape as `nitrovin-bench values`.
"""

import json
import sys
from datetime import datetime, timezone

import ultravin

NOW = datetime(2026, 9, 1, tzinfo=timezone.utc)


def main(path: str) -> None:
    with open(path) as f:
        vins = [line.strip() for line in f if line.strip()]
    out = sys.stdout
    for vin, dec in zip(vins, ultravin.decode_batch(vins, full=True, now=NOW), strict=True):
        values = [[str(e["element_id"]), e["value"] or ""] for e in dec["elements"]]
        out.write(json.dumps({"vin": vin, "values": values}) + "\n")


if __name__ == "__main__":
    main(sys.argv[1])
