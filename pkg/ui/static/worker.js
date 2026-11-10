// nitrovin's Service Worker (ADR 0013), imported by /sw.js after the
// release's config (self.nitrovinConfig) and Go's wasm_exec.js. The
// vocabulary pages run here as WebAssembly (cmd/nitrovin-sw, the same Go
// as the server's pages) over the files this browser holds in IndexedDB
// and the server's, which it links from their release URLs.
//
// - It answers a /ui request only when it names a file this browser holds,
//   and /ui/files; everything else goes to the server unchanged, so one URL
//   means one thing with JavaScript on or off.
// - A file reaches it by postMessage from the page, never by a request: no
//   file's bytes are ever sent to the server.
// - It installs beside an older release's worker and waits until that
//   one's pages close (no skipWaiting); once active it controls the pages
//   that have none (clients.claim). It keeps its release's assets in a
//   cache of its own, so pages it renders keep working while the server
//   runs a newer release.
'use strict';

const config = self.nitrovinConfig;
const CACHE = 'nitrovin ' + config.release;
const DB = 'nitrovin';
// The assets a page or the module needs, kept per release.
const ASSETS = ['htmx.min.js', 'ui.css', 'copy.js', 'vocab.js', 'worker.js', 'wasm_exec.js', 'sw.wasm', 'spine.json']
  .map((name) => config.assets + '/' + name);

self.addEventListener('install', (e) => {
  // Every /ui request goes to the fetch handler, never to the network
  // first: a browser that preloads a navigation while its worker starts
  // (Chrome's ServiceWorkerAutoPreload) would otherwise send the server a
  // URL naming a file of this browser's, its entry's fields in the query.
  // A static route to the fetch handler is the opt-out.
  if (e.addRoutes && self.URLPattern) {
    e.waitUntil(e.addRoutes({ condition: { urlPattern: new URLPattern({ pathname: '/ui/*' }) }, source: 'fetch-event' }));
  }
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(ASSETS.concat(config.files.map((f) => f.url)))));
});

self.addEventListener('activate', (e) => {
  e.waitUntil((async () => {
    for (const key of await caches.keys()) {
      if (key.startsWith('nitrovin ') && key !== CACHE) await caches.delete(key);
    }
    await dropStaleIndexes();
    await self.clients.claim();
  })());
});

// ---- IndexedDB: files (records, by name), texts (by blake3), indexes (by
// release and blake3). A change is one transaction over all three.

let dbOpen = null;
function db() {
  if (!dbOpen) {
    dbOpen = new Promise((resolve, reject) => {
      const r = indexedDB.open(DB, 1);
      r.onupgradeneeded = () => {
        r.result.createObjectStore('files', { keyPath: 'name' });
        r.result.createObjectStore('texts');
        r.result.createObjectStore('indexes');
      };
      r.onsuccess = () => resolve(r.result);
      r.onerror = () => reject(r.error);
    });
    dbOpen.catch(() => { dbOpen = null; });
  }
  return dbOpen;
}

// tx runs f over the stores in one transaction, resolving with f's result
// once the transaction completes.
async function tx(stores, mode, f) {
  const d = await db();
  return new Promise((resolve, reject) => {
    const t = d.transaction(stores, mode);
    let out;
    Promise.resolve(f(t)).then((v) => { out = v; }, (err) => { t.abort(); reject(err); });
    t.oncomplete = () => resolve(out);
    t.onerror = () => reject(t.error);
    t.onabort = () => reject(t.error || new Error('the transaction was aborted'));
  });
}

const request = (r) => new Promise((resolve, reject) => {
  r.onsuccess = () => resolve(r.result);
  r.onerror = () => reject(r.error);
});

const indexKey = (blake3) => config.release + '\n' + blake3;
const bytes = (v) => (v === undefined ? null : new Uint8Array(v));

// names is the stored files' names, read once per worker and kept in step
// with every change.
let names = null;
async function storedNames() {
  if (!names) names = new Set((await tx(['files'], 'readonly', (t) => request(t.objectStore('files').getAllKeys()))));
  return names;
}

// disk is what the module's Library calls (ui.Disk).
self.nitrovinDisk = {
  records: () => tx(['files'], 'readonly', (t) => request(t.objectStore('files').getAll())),
  index: (blake3) => tx(['indexes'], 'readonly', (t) => request(t.objectStore('indexes').get(indexKey(blake3)))).then(bytes),
  putIndex: (blake3, index) => tx(['indexes'], 'readwrite', (t) => request(t.objectStore('indexes').put(index.buffer, indexKey(blake3)))),
  text: (blake3) => tx(['texts'], 'readonly', (t) => request(t.objectStore('texts').get(blake3))).then((v) => {
    if (v === undefined) throw new Error('its text is not stored');
    return bytes(v);
  }),
  // change puts [{record, text, index}] and deletes the records named
  // remove; a text or index no record keeps any more goes with them.
  change: async (put, remove) => {
    await tx(['files', 'texts', 'indexes'], 'readwrite', async (t) => {
      const files = t.objectStore('files'), texts = t.objectStore('texts'), indexes = t.objectStore('indexes');
      const before = await request(files.getAll());
      for (const name of remove) await request(files.delete(name));
      for (const p of put) {
        await request(files.put(p.record));
        await request(texts.put(p.text.buffer, p.record.blake3));
        await request(indexes.put(p.index.buffer, indexKey(p.record.blake3)));
      }
      const kept = new Set((await request(files.getAll())).map((r) => r.blake3));
      for (const r of before) {
        if (!kept.has(r.blake3)) {
          await request(texts.delete(r.blake3));
          await request(indexes.delete(indexKey(r.blake3)));
        }
      }
    });
    names = null;
  },
};

// dropStaleIndexes drops the indexes of other releases, and of files
// neither stored nor the server's.
async function dropStaleIndexes() {
  await tx(['files', 'indexes'], 'readwrite', async (t) => {
    const keep = new Set((await request(t.objectStore('files').getAll())).map((r) => indexKey(r.blake3)));
    for (const f of config.files) keep.add(indexKey(f.blake3));
    const indexes = t.objectStore('indexes');
    for (const key of await request(indexes.getAllKeys())) {
      if (!keep.has(key)) await request(indexes.delete(key));
    }
  });
}

// get is a GET on this origin, answered by the release's cache when it
// holds the path (ui.Remote).
self.nitrovinGet = async (path) => {
  const r = (await caches.match(path, { cacheName: CACHE })) || (await fetch(path));
  return { status: r.status, body: new Uint8Array(await r.arrayBuffer()) };
};

// ---- The module: started on the first request that needs it, again after
// the browser stops this worker. Go sets self.nitrovin when it is ready.

let booting = null;
// started is how long this worker took to start the module, as a
// Server-Timing metric beside the module's own (Library.Opened).
let started = '';
function boot() {
  if (!booting) {
    booting = (async () => {
      const t0 = performance.now();
      const ready = new Promise((resolve) => { self.nitrovinReady = resolve; });
      const go = new Go();
      const r = (await caches.match(config.assets + '/sw.wasm', { cacheName: CACHE })) || (await fetch(config.assets + '/sw.wasm'));
      const { instance } = await WebAssembly.instantiateStreaming(r, go.importObject);
      go.run(instance).finally(() => { booting = null; });
      await ready;
      started = 'module;dur=' + (performance.now() - t0).toFixed(1);
    })();
    booting.catch(() => { booting = null; });
  }
  return booting;
}

// named is what a /ui request names, as the module's pages read it
// (pkg/ui, named): the file a list is of, or a translation's from and
// targets.
function named(url) {
  const q = url.searchParams;
  if (url.pathname.startsWith('/ui/vocabularies/')) return [decodeURIComponent(url.pathname.slice('/ui/vocabularies/'.length))];
  if (url.pathname !== '/ui/translate') return [];
  const out = [q.get('from.vocabulary'), q.get('from.spine'), ...q.getAll('to')];
  const ext = q.get('from.ext');
  if (ext && ext.startsWith('ext:')) out.push(ext.slice(4).split('/')[0]);
  return out.filter(Boolean);
}

self.addEventListener('fetch', (e) => {
  const url = new URL(e.request.url);
  if (url.origin !== location.origin) return;
  if (url.pathname.startsWith(config.assets + '/')) {
    e.respondWith(caches.match(e.request, { cacheName: CACHE }).then((r) => r || fetch(e.request)));
    return;
  }
  if (url.pathname === '/ui/files' || url.pathname === '/ui/files/remove') {
    e.respondWith(serve(e.request));
    return;
  }
  const n = named(url);
  if (n.length === 0) return;
  e.respondWith(storedNames().then((stored) => (n.some((name) => stored.has(name)) ? serve(e.request) : fetch(e.request))));
});

// serve answers req from the module's pages.
async function serve(req) {
  try {
    await boot();
    const body = req.method === 'POST' ? await req.text() : '';
    const r = await self.nitrovin.serve(req.method, req.url, req.headers.get('HX-Request') || '',
      req.headers.get('HX-History-Restore-Request') || '', body);
    if (req.url.endsWith('/ui/files/remove')) names = null;
    if (r.status >= 300 && r.status < 400 && r.headers.Location) {
      return Response.redirect(new URL(r.headers.Location, location.origin).href, r.status);
    }
    if (started) r.headers['Server-Timing'] = [started, r.headers['Server-Timing']].filter(Boolean).join(', ');
    return new Response(r.body, { status: r.status, headers: r.headers });
  } catch (err) {
    return new Response('nitrovin could not start its translator in this browser: ' + err + '. Reload the page to try again.',
      { status: 503, headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
  }
}

// A load: {op: "load", files: [{name, data: ArrayBuffer}]} on a
// MessageChannel. The page hears {progress} at each step, then {done, ok,
// message}; it gives up after a minute without either.
self.addEventListener('message', (e) => {
  const port = e.ports[0];
  if (!port || !e.data || e.data.op !== 'load') return;
  e.waitUntil((async () => {
    try {
      port.postMessage({ progress: 'Starting the translator…' });
      await boot();
      const files = e.data.files.map((f) => ({ name: f.name, data: new Uint8Array(f.data) }));
      const r = await self.nitrovin.load(files, (text) => port.postMessage({ progress: text }));
      names = null;
      port.postMessage({ done: true, ok: r.ok, message: r.message });
    } catch (err) {
      port.postMessage({ done: true, ok: false, message: String(err) });
    }
  })());
});
