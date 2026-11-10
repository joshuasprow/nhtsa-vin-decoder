// PROTOTYPE (#147): the Service Worker. It runs package ui's Go handlers,
// compiled to WebAssembly (wasm/sw), for the /ui routes that read
// vocabularies, over the spine bundle and the files in IndexedDB. Every
// other request (statics, /ui/decode, /v1) goes to the network. A file is
// handed over by postMessage, never by a request, so it cannot reach the
// server even when the worker is gone.
importScripts('/ui/wasm_exec.js', '/ui/idb.js');

const LOCAL = new Set(['/ui/vocab', '/ui/translate', '/ui/vocabularies', '/ui/files', '/ui/files/remove']);
const timings = {};
let booting = null;

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

// boot loads the module, the spine bundle and every stored file: on the
// worker's first request after the browser (re)started it.
function boot() {
  if (!booting) {
    booting = (async () => {
      const t0 = performance.now();
      const go = new Go();
      const { instance } = await WebAssembly.instantiateStreaming(fetch('/ui/sw.wasm'), go.importObject);
      go.run(instance);
      const t1 = performance.now();
      const spine = await (await fetch('/ui/spine.json')).text();
      const t2 = performance.now();
      const sp = JSON.parse(nitrovin.setSpine(spine));
      const t3 = performance.now();
      const recs = await idbAll();
      const t4 = performance.now();
      const ix = JSON.parse(nitrovin.index(recs));
      const t5 = performance.now();
      Object.assign(timings, {
        wasm_ms: t1 - t0, spine_fetch_ms: t2 - t1, spine_index_ms: t3 - t2,
        idb_read_ms: t4 - t3, reindex_ms: t5 - t4, boot_ms: t5 - t0,
        files: recs.length, bytes: recs.reduce((n, r) => n + r.text.length, 0), spine: sp, index: ix,
      });
    })();
    booting.catch(() => { booting = null; });
  }
  return booting;
}

self.addEventListener('fetch', (e) => {
  const u = new URL(e.request.url);
  if (u.origin !== location.origin) return;
  if (u.pathname === '/ui/proto-timings') { // prototype instrumentation only
    e.respondWith(boot().then(() => Response.json(timings)));
    return;
  }
  if (!LOCAL.has(u.pathname)) return;
  e.respondWith(handle(e.request));
});

async function handle(req) {
  await boot();
  const t0 = performance.now();
  const body = req.method === 'POST' ? await req.text() : '';
  const r = await nitrovin.serve(req.method, req.url, req.headers.get('HX-Request') || '',
    req.headers.get('HX-History-Restore-Request') || '', body);
  if (r.status === 303) return Response.redirect(new URL(r.headers.Location, location.origin).href, 303);
  const h = new Headers(r.headers);
  h.set('Server-Timing', `wasm;dur=${(performance.now() - t0).toFixed(2)}`);
  return new Response(r.body, { status: r.status, headers: h });
}

// {op: 'load', name, text} on a MessageChannel: check and index the file,
// store it, answer with the timing (or the faults).
self.addEventListener('message', (e) => {
  const port = e.ports[0];
  e.waitUntil((async () => {
    await boot();
    const t0 = performance.now();
    const res = JSON.parse(nitrovin.load(e.data.name, e.data.text));
    const t1 = performance.now();
    if (!res.error) await idbPut({ key: res.key, name: e.data.name, text: e.data.text, stored: Date.now() });
    const t2 = performance.now();
    port.postMessage({ ...res, load_ms: t1 - t0, idb_put_ms: t2 - t1 });
  })());
});
