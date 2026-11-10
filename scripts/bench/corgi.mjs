// corgi's row of the README's benchmarks (docs/benchmarks.md): decode a VIN
// list in process, one VIN at a time, and print one JSON object.
//
//   node scripts/bench/corgi.mjs v2|v3 VINS
//
// v2 is @cardog/corgi 2.0.1 (SQLite, its bundled vpic.lite.db), v3 the
// newest v3, 3.0.1-beta.0 (binary index). 3.0.1-beta.0 ships no index files,
// so it reads those of 3.0.0-alpha.1, the one v3 that ships them; the two
// share index-reader.js byte for byte. Install with `npm ci` in this
// directory (package.json pins all three). corgi has no decode clock.
//
// The decoder loads untimed and decodes the first WARM VINs untimed, then
// complete passes over the list are timed until at least SECONDS have
// elapsed, as nitrovin's and ultravin's harnesses do.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const WARM = 20;
const SECONDS = 10;
const [engine, path] = process.argv.slice(2);
if (!["v2", "v3"].includes(engine) || !path) {
  console.error("usage: node scripts/bench/corgi.mjs v2|v3 VINS");
  process.exit(2);
}
const vins = readFileSync(path, "utf8").split("\n").map((l) => l.trim()).filter(Boolean);
const modules = join(dirname(fileURLToPath(import.meta.url)), "node_modules");
const pkg = (name) => JSON.parse(readFileSync(join(modules, name, "package.json"), "utf8"));

let decode, close, version;
const loadStarted = performance.now();
if (engine === "v2") {
  const corgi = await import("corgi2");
  const d = await corgi.createDecoder();
  decode = (vin) => d.decode(vin);
  close = () => d.close();
  version = pkg("corgi2").version;
} else {
  const corgi = await import("corgi3");
  const index = join(modules, "corgi3-index", "dist");
  const d = await corgi.createDecoder(index);
  decode = async (vin) => d.decode(vin);
  close = async () => {};
  version = `${pkg("corgi3").version} (index ${pkg("corgi3-index").version})`;
}
const loadSeconds = (performance.now() - loadStarted) / 1000;

for (const vin of vins.slice(0, WARM)) await decode(vin);
let valid = 0;
let passes = 0;
const started = performance.now();
while (performance.now() - started < SECONDS * 1000) {
  valid = 0;
  for (const vin of vins) {
    const r = await decode(vin);
    if (r.valid) valid++;
  }
  passes++;
}
const seconds = (performance.now() - started) / 1000;
await close();
console.log(
  JSON.stringify({
    engine: `corgi ${engine}`,
    version,
    node: process.version,
    vins: vins.length,
    warm_vins: WARM,
    passes,
    rows: passes * vins.length,
    seconds,
    vins_per_second: (passes * vins.length) / seconds,
    load_seconds: loadSeconds,
    valid,
  }),
);
