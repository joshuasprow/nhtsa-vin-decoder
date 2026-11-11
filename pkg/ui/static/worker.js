// nitrovin's Service Worker (ADR 0013), imported by /sw.js after the
// release's config (self.nitrovinConfig) and Go's wasm_exec.js. The
// pages run here as WebAssembly (cmd/nitrovin-sw, the same Go as the
// server's pages) over the files this browser holds in IndexedDB and the
// server's, which it links from their release URLs.
//
// - It answers a lookup on the page (/) only when it names a file this
//   browser holds (its from or a target: named), the page without a
//   lookup once it holds one (its forms over the browser's files too), an
//   entry list (/ui/vocabularies/<name>) of a file it holds, and
//   /ui/files; everything else goes to the server unchanged, a lookup
//   naming no target included, so one URL means one thing with
//   JavaScript on or off, and in every browser.
// - A file reaches it by postMessage from the page, never by a request: no
//   file's bytes are ever sent to the server.
// - It installs beside an older release's worker and waits until that
//   one's pages close, or until the visitor asks for it (Update now:
//   vocab.js posts {op: "skipWaiting"} to it, then reloads); once active it
//   controls the pages that have none (clients.claim). It keeps its
//   release's assets in a cache of its own.
// - It answers only while the server runs its release. The server names
//   its release on every page, asset and /sw.js (Nitrovin-Release), and
//   before answering anything itself the worker asks /sw.js for it (a
//   HEAD, while the module draws the answer); a 404 for one of its
//   release's assets that names none says the same. Once the server runs
//   another release the worker is outdated and answers nothing: every
//   request goes to the server unchanged, it fetches none of its
//   release's assets, it refuses a load in words, and the pages it
//   controls hear {op: "outdated"}, so they say so and offer Update now,
//   until a newer worker replaces it. A server it cannot reach leaves it
//   answering, as offline.
// - It keeps the visitor's selection of targets beside the files, by the
//   spine they are on, which vocab.js ticks in the page's forms: each
//   file loaded joins it, each removed leaves it, a target ticked or
//   cleared changes it, and a lookup naming its targets sets it on its
//   spine to them (the module's pages do, for the ones they answer; this
//   script, for the ones the server answers, by the answer's data-from
//   and data-spine).
'use strict';

const config = self.nitrovinConfig;
const CACHE = 'nitrovin ' + config.release;
const DB = 'nitrovin';
// The assets a page or the module needs, kept per release.
const ASSETS = ['htmx.min.js', 'ui.css', 'copy.js', 'map.js', 'vocab.js', 'worker.js', 'wasm_exec.js', 'sw.wasm', 'spine.json']
  .map((name) => config.assets + '/' + name);
// The header the server names its release in (pkg/ui's ReleaseHeader).
const RELEASE = 'Nitrovin-Release';

// outdated is the release the server runs once it runs another than this
// worker's (a 404 for one of this release's assets that names none:
// "another"), "" until then. From then on the worker answers nothing.
let outdated = '';

// heard reads what res, the server's answer for path, says of the
// server's release, telling the pages this worker controls once it is
// another than the worker's.
function heard(res, path) {
  if (outdated) return res;
  const release = res.headers.get(RELEASE);
  if (release ? release !== config.release : res.status === 404 && path.startsWith(config.assets + '/')) {
    outdated = release || 'another';
    self.clients.matchAll({ type: 'window' })
      .then((pages) => { for (const p of pages) p.postMessage({ op: 'outdated', release: config.release, server: outdated }); })
      .catch(() => {});
  }
  return res;
}

// network is the server's answer to req.
const network = (req) => fetch(req).then((res) => heard(res, new URL(req.url).pathname));

// current resolves whether the server runs this worker's release, asking
// its /sw.js (a HEAD, whose answer names it), one question at a time. A
// server that cannot be reached, or does not answer in 3 s, leaves the
// worker answering, as offline.
let asking = null;
function current() {
  if (outdated) return Promise.resolve(false);
  if (!asking) {
    asking = fetch('/sw.js', { method: 'HEAD', cache: 'no-store', signal: AbortSignal.timeout(3000) })
      .then((res) => { heard(res, '/sw.js'); }, () => {})
      .then(() => {
        asking = null;
        return !outdated;
      });
  }
  return asking;
}

// own answers req with make(), the worker's own answer, while the server
// runs this worker's release, else with the server's. A GET's answer is
// drawn while the server is asked; a POST, which changes the files, waits
// for the server's word.
async function own(req, make) {
  const again = req.clone();
  const making = req.method === 'GET' ? make() : null;
  if (!(await current())) {
    if (making) making.catch(() => {});
    return network(again);
  }
  const res = await (making || make());
  return outdated ? network(again) : res;
}

// stale says in words that the server runs another release than this
// worker's, and what to do.
function stale() {
  const mine = config.release.split('/')[0], theirs = outdated.split('/')[0];
  const now = outdated.includes('/') && theirs !== mine ? 'nitrovin ' + theirs : 'another release of nitrovin';
  return 'the translator in this browser is nitrovin ' + mine + ', and this server now runs ' + now +
    '. Update it (Update now, or close every nitrovin tab), then try again.';
}

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
// holds the path (ui.Remote). An outdated worker fetches nothing.
self.nitrovinGet = async (path) => {
  if (outdated) throw new Error(stale());
  const r = (await caches.match(path, { cacheName: CACHE })) || heard(await fetch(path), path);
  return { status: r.status, body: new Uint8Array(await r.arrayBuffer()) };
};

// ---- The module: started on the first request that needs it, again after
// the browser stops this worker. Go sets self.nitrovin when it is ready.

let booting = null;
// started is how long this worker took to start the module, as a
// Server-Timing metric beside the module's own (Library.Opened).
let started = '';
function boot() {
  if (outdated) return Promise.reject(new Error(stale()));
  if (!booting) {
    booting = (async () => {
      const t0 = performance.now();
      const ready = new Promise((resolve) => { self.nitrovinReady = resolve; });
      const go = new Go();
      const module = config.assets + '/sw.wasm';
      const r = (await caches.match(module, { cacheName: CACHE })) || heard(await fetch(module), module);
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

// named is what a lookup on the page names, as the module's pages read
// it (pkg/ui, named): the from's vocabulary, spine or extension set, and
// the targets.
function named(q) {
  const out = [q.get('from.vocabulary'), q.get('from.spine'), ...q.getAll('to')];
  const ext = q.get('from.ext');
  if (ext && ext.startsWith('ext:')) out.push(ext.slice(4).split('/')[0]);
  return out.filter(Boolean);
}

// asks reports whether q asks a lookup: a from beside a vocabulary's
// name, a form's choice.
const asks = (q) => [...q].some(([k, v]) => k.startsWith('from.') && k !== 'from.vocabulary' && v.trim() !== '');

// The worker answers a request only while it is not outdated, and what it
// draws itself (own) only while the server runs its release.
self.addEventListener('fetch', (e) => {
  const url = new URL(e.request.url);
  if (url.origin !== location.origin || outdated) return;
  if (url.pathname.startsWith(config.assets + '/')) {
    e.respondWith(caches.match(e.request, { cacheName: CACHE }).then((r) => r || network(e.request)));
    return;
  }
  if (url.pathname === '/ui/files' || url.pathname === '/ui/files/remove') {
    e.respondWith(own(e.request, () => serve(e.request)));
    return;
  }
  // An old link may name a file of this browser's: it moves here, as the
  // server moves it, and the server never sees it.
  if (url.pathname === MOVED) {
    e.respondWith(own(e.request, async () => Response.redirect(new URL(PAGE + url.search, location.origin).href, 301)));
    return;
  }
  if (url.pathname === PAGE) {
    const q = url.searchParams;
    e.respondWith(storedNames().then((stored) => {
      if (named(q).some((n) => stored.has(n)) || (stored.size > 0 && !asks(q))) return own(e.request, () => serve(e.request));
      return network(e.request).then((r) => {
        e.waitUntil(remember(q, e.request, r.clone()).catch(() => {}));
        return r;
      });
    }));
    return;
  }
  // An entry list is the module's when the file is this browser's.
  const list = '/ui/vocabularies/';
  if (!url.pathname.startsWith(list)) return;
  const name = decodeURIComponent(url.pathname.slice(list.length));
  e.respondWith(storedNames().then((stored) => (stored.has(name) ? own(e.request, () => serve(e.request)) : network(e.request))));
});

// remember makes the targets of a lookup the server answered, a link's
// own included, the visitor's selection on their spine, as the module's
// pages do for the ones they answer (ui's remember): the answer says
// which from it is of and its spine (data-from, data-spine). A lookup
// naming no target, a refusal, and history going back to an older page
// change nothing.
async function remember(q, req, res) {
  if (!res.ok || !q.has('to') || req.headers.get('HX-History-Restore-Request')) return;
  const m = /<section id="result"[^>]* data-from="([^"]+)" data-spine="([^"]+)"/.exec(await res.text());
  if (!m) return;
  const to = q.getAll('to');
  await select(m[2], () => to);
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
// message}; it gives up after a minute without either. An outdated worker
// refuses it in words. A tick: {op: "select", name, spine, checked}, a
// target the visitor ticked in the page's form or cleared, kept in the
// selection on its spine or dropped from it. Questions on a
// MessageChannel: {op: "selection"}, answered with the selection ({spine:
// [name]}), which vocab.js ticks; {op: "release"}, answered with {release,
// server}, this worker's release and, once it is outdated, the server's.
// {op: "skipWaiting"} is Update now: a waiting worker takes over at once.
self.addEventListener('message', (e) => {
  const op = e.data && e.data.op;
  if (op === 'skipWaiting') {
    e.waitUntil(self.skipWaiting());
    return;
  }
  if (op === 'select' && typeof e.data.name === 'string' && typeof e.data.spine === 'string') {
    const { name, spine, checked } = e.data;
    e.waitUntil(select(spine, (s) => s.filter((n) => n !== name).concat(checked ? [name] : [])));
    return;
  }
  const port = e.ports[0];
  if (port && op === 'release') {
    port.postMessage({ release: config.release, server: outdated });
    return;
  }
  if (port && op === 'selection') {
    e.waitUntil(tx(['state'], 'readonly', (t) => request(t.objectStore('state').get('selection')), stateDB)
      .then((s) => port.postMessage(s || {}), () => port.postMessage({})));
    return;
  }
  if (!port || op !== 'load') return;
  e.waitUntil((async () => {
    try {
      port.postMessage({ progress: 'Starting the translator…' });
      if (!(await current())) {
        port.postMessage({ done: true, ok: false, message: stale() });
        return;
      }
      await boot();
      const files = e.data.files.map((f) => ({ name: f.name, data: new Uint8Array(f.data) }));
      const r = await self.nitrovin.load(files, (text) => port.postMessage({ progress: text }));
      names = null;
      port.postMessage({ done: true, ok: r.ok, message: !r.ok && outdated ? stale() : r.message });
    } catch (err) {
      port.postMessage({ done: true, ok: false, message: outdated ? stale() : String(err) });
    }
  })());
});
