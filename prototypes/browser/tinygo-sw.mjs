// PROTOTYPE (#147): does package ui's handler run under TinyGo? Loads
// build/tinygo/sw.wasm in Node with TinyGo's wasm_exec.js, feeds it the
// spine bundle (build/spine.json, written by measure.sh) and asks for
// /ui/vocab. Run: mise x node@24.21.0 -- node prototypes/browser/tinygo-sw.mjs
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const build = join(here, 'build');
await import(join(build, 'tinygo', 'wasm_exec.js'));
const go = new globalThis.Go();
const { instance } = await WebAssembly.instantiate(readFileSync(join(build, 'tinygo', 'sw.wasm')), go.importObject);
go.run(instance);
console.log('setSpine:', globalThis.nitrovin.setSpine(readFileSync(join(build, 'spine.json'), 'utf8')));
try {
  const r = await globalThis.nitrovin.serve('GET', 'http://localhost/ui/vocab', 'true', '', '');
  console.log('serve /ui/vocab:', r.status, String(r.body).slice(0, 300));
} catch (e) {
  console.log('serve /ui/vocab failed:', String(e).split('\n')[0]);
}
