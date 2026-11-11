// nitrovin's Service Worker (ADR 0013), imported by /sw.js after the
// release's config (self.nitrovinConfig) and Go's wasm_exec.js. The
// pages run here as WebAssembly (cmd/nitrovin-sw, the same Go as the
// server's pages) over the files this browser holds in IndexedDB and the
// server's, which it links from their release URLs.
//
// - It answers the page (/) once this browser holds a file, an entry list
//   (/ui/vocabularies/<name>) of a file it holds, and /ui/files;
//   everything else goes to the server unchanged, so one URL means one
//   thing with JavaScript on or off.
// - A file reaches it by postMessage from the page, never by a request: no
//   file's bytes are ever sent to the server.
// - It installs beside an older release's worker and waits until that
//   one's pages close (no skipWaiting); once active it controls the pages
//   that have none (clients.claim). It keeps its release's assets in a
//   cache of its own, so pages it renders keep working while the server
//   runs a newer release.
// - It keeps the visitor's selection of targets beside the files, by the
//   spine they are on, which the page's forms tick: each file loaded
//   joins it, each removed leaves it, a target ticked or cleared changes
//   it, and a translation asked for, a link's own included, sets it on
//   its spine to its targets (the module's pages do, for the ones they
//   answer; this script, for the ones it sends to the server).
'use strict';

const config = self.nitrovinConfig;
const CACHE = 'nitrovin ' + config.release;
const DB = 'nitrovin';
// The assets a page or the module needs, kept per release.
const ASSETS = ['htmx.min.js', 'ui.css', 'copy.js', 'map.js', 'vocab.js', 'worker.js', 'wasm_exec.js', 'sw.wasm', 'spine.json']
  .map((name) => config.assets + '/' + name);

self.addEventListener('install', (e) => {
  // Every page's request goes to the fetch handler, never to the network
  // first: a browser that preloads a navigation while its worker starts
  // (Chrome's ServiceWorkerAutoPreload) would otherwise send the server a
  // URL naming a file of this browser's, its entry's fields in the query.
  // A static route to the fetch handler is the opt-out.
  if (e.addRoutes && self.URLPattern) {
    e.waitUntil(e.addRoutes(['/', '/ui/*'].map((pathname) => ({ condition: { urlPattern: new URLPattern({ pathname }) }, source: 'fetch-event' }))));
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

// ---- IndexedDB "nitrovin": files (records, by name), texts (by blake3)
// and indexes (by release and blake3); a change is one transaction over
// the three. Beside it, "nitrovin-state": state, whose "selection" is the
// vocabularies the visitor selected as targets by spine ({spine:
// [name]}), a database of its own so that an older release, which opens
// "nitrovin" at version 1, still can.

// opener opens database name once per worker, upgrade making its stores.
function opener(name, upgrade) {
  let opening = null;
  return () => {
    if (!opening) {
      opening = new Promise((resolve, reject) => {
        const r = indexedDB.open(name, 1);
        r.onupgradeneeded = () => upgrade(r.result);
        r.onsuccess = () => resolve(r.result);
        r.onerror = () => reject(r.error);
      });
      opening.catch(() => { opening = null; });
    }
    return opening;
  };
}
const filesDB = opener(DB, (d) => {
  d.createObjectStore('files', { keyPath: 'name' });
  d.createObjectStore('texts');
  d.createObjectStore('indexes');
});
const stateDB = opener(DB + '-state', (d) => d.createObjectStore('state'));

// tx runs f over the stores of database open in one transaction,
// resolving with f's result once the transaction completes.
async function tx(stores, mode, f, open = filesDB) {
  const d = await open();
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

// select changes the stored selection on spine to what f makes of it.
const select = (spine, f) => tx(['state'], 'readwrite', async (t) => {
  const state = t.objectStore('state');
  const s = (await request(state.get('selection'))) || {};
  s[spine] = f(s[spine] || []);
  await request(state.put(s, 'selection'));
}, stateDB);

// disk is what the module's Library calls (ui.Disk).
self.nitrovinDisk = {
  records: () => tx(['files'], 'readonly', (t) => request(t.objectStore('files').getAll())),
  index: (blake3) => tx(['indexes'], 'readonly', (t) => request(t.objectStore('indexes').get(indexKey(blake3)))).then(bytes),
  putIndex: (blake3, index) => tx(['indexes'], 'readwrite', (t) => request(t.objectStore('indexes').put(index.buffer, indexKey(blake3)))),
  text: (blake3) => tx(['texts'], 'readonly', (t) => request(t.objectStore('texts').get(blake3))).then((v) => {
    if (v === undefined) throw new Error('its text is not stored');
    return bytes(v);
  }),
  selection: () => tx(['state'], 'readonly', (t) => request(t.objectStore('state').get('selection')), stateDB).then((v) => v || {}),
  // change puts [{record, text, index}] and deletes the records named
  // remove, a text or index no record keeps any more going with them;
  // then it stores selection.
  change: async (put, remove, selection) => {
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
    await tx(['state'], 'readwrite', (t) => request(t.objectStore('state').put(selection, 'selection')), stateDB);
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

// The page, and the page that translated before it, whose old links move
// to it.
const PAGE = '/';
const MOVED = '/ui/translate';

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
  // An old link may name a file of this browser's: it moves here, as the
  // server moves it, and the server never sees it.
  if (url.pathname === MOVED) {
    e.respondWith(Response.redirect(new URL(PAGE + url.search, location.origin).href, 301));
    return;
  }
  // The page is the module's once this browser holds a file: its forms
  // and answers are over the browser's files too.
  if (url.pathname === PAGE) {
    e.respondWith(storedNames().then((stored) => {
      if (stored.size > 0) return serve(e.request);
      e.waitUntil(remember(url, e.request));
      return fetch(e.request);
    }));
    return;
  }
  // An entry list is the module's when the file is this browser's.
  const list = '/ui/vocabularies/';
  if (!url.pathname.startsWith(list)) return;
  const name = decodeURIComponent(url.pathname.slice(list.length));
  e.respondWith(storedNames().then((stored) => (stored.has(name) ? serve(e.request) : fetch(e.request))));
});

// remember makes the targets of a lookup sent to the server, a link's own
// included, the visitor's selection on their spine, as the module's pages
// do for the ones they answer (ui's remember): vPIC's for a VIN, a vPIC
// entry or an extension's, else the from's spine. A form's targets before
// anything is asked, and history going back to an older page, change
// nothing.
async function remember(url, req) {
  const q = url.searchParams;
  const asked = [...q].some(([k, v]) => k.startsWith('from.') && k !== 'from.vocabulary' && v.trim() !== '');
  if (!asked || !q.has('to') || req.headers.get('HX-History-Restore-Request')) return;
  let spine = 'vpic';
  if (q.has('from.spine')) {
    spine = q.get('from.spine');
  } else if (q.has('from.vocabulary')) {
    const f = config.files.find((f) => f.name === q.get('from.vocabulary'));
    if (!f || !f.spine) return; // nothing loaded by its name: the translation is refused
    spine = f.spine;
  }
  const to = q.getAll('to');
  await select(spine, () => to);
}

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
// message}; it gives up after a minute without either. A tick: {op:
// "select", name, spine, checked}, a target the visitor ticked in the
// page's form or cleared, kept in the selection on its spine or dropped
// from it.
self.addEventListener('message', (e) => {
  if (e.data && e.data.op === 'select' && typeof e.data.name === 'string' && typeof e.data.spine === 'string') {
    const { name, spine, checked } = e.data;
    e.waitUntil(select(spine, (s) => s.filter((n) => n !== name).concat(checked ? [name] : [])));
    return;
  }
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
