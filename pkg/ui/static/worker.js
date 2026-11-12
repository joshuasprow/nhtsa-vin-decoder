// nitrovin's Service Worker (ADR 0013), imported by /sw.js after the
// release's config (self.nitrovinConfig), Go's wasm_exec.js and disk.js
// (IndexedDB). The
// pages run here as WebAssembly (cmd/nitrovin-sw, the same Go as the
// server's pages) over the files this browser holds in IndexedDB and the
// server's, which it links from their release URLs.
//
// - It answers a lookup on the page (/) only when it names a file this
//   browser holds (its from or a target: named), the page without a
//   lookup once it holds one (its forms over the browser's files too), an
//   entry list (/ui/vocabularies/<name>) of a file it holds, and
//   /ui/files, the fit page (/ui/fit) while it holds the catalog and the
//   server has none (ADR 0023), or when it names a file this browser
//   holds, and that catalog's download (/ui/files/catalog);
//   everything else goes to the server unchanged, a lookup
//   naming no target included, so one URL means one thing with
//   JavaScript on or off, and in every browser.
// - A file reaches it by postMessage from the page, never by a request: no
//   file's bytes are ever sent to the server. A yard stock file's VINs
//   alone go to the server's POST /v1/decode, once, as it loads.
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
// The assets a page or the module needs, kept per release.
const ASSETS = ['htmx.min.js', 'ui.css', 'copy.js', 'map.js', 'vocab.js', 'disk.js', 'worker.js', 'compile.js', 'wasm_exec.js', 'sw.wasm', 'spine.json']
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
    await clearLeftParts();
    await self.clients.claim();
  })());
});

// IndexedDB is disk.js's, imported before this script.

// clearLeftParts drops what a compile that stopped half way left (its tab
// closed), when no compile runs: one holds the lock while it writes. A
// browser without Web Locks leaves them to its next compile.
function clearLeftParts() {
  if (!self.navigator.locks) return Promise.resolve();
  return navigator.locks.request(COMPILE_LOCK, { ifAvailable: true }, (lock) => lock && self.nitrovinDisk.clearParts()).catch(() => {});
}

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
// post is a POST of body, JSON, to path on this origin (ui.Remote): only
// POST /v1/decode, a yard file's VINs as it loads, never a row.
self.nitrovinPost = async (path, body) => {
  if (outdated) throw new Error(stale());
  const r = heard(await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body }), path);
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
// to it; the fit page; and the browser's compiled catalog, as a download.
const PAGE = '/';
const MOVED = '/ui/translate';
const FIT = '/ui/fit';
const DOWNLOAD = '/ui/files/catalog';

// fileName is the name a downloaded catalog is saved as.
const fileName = (c) => c.record.name.replace(/[^A-Za-z0-9._-]/g, '_') + '.compiled';

// download answers with the compiled catalog this browser holds, read from
// IndexedDB as it streams to the visitor's disk. vocab.js saves it from a
// blob URL instead ({op: "download"}): a browser may send a link's
// download to the server without asking the worker.
async function download() {
  const c = await self.nitrovinDisk.catalog();
  if (!c) {
    return new Response('This browser holds no compiled catalog to download.', { status: 404, headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
  }
  return new Response(c.blob, {
    headers: {
      'Content-Type': 'application/octet-stream',
      'Content-Length': String(c.blob.size),
      'Content-Disposition': 'attachment; filename="' + fileName(c) + '"',
    },
  });
}

// named is what a lookup on the page names, as the module's pages read
// it (pkg/ui, named): the from's vocabulary, spine or extension set, and
// the targets.
function named(q) {
  const out = [q.get('from.vocabulary'), q.get('from.spine'), ...q.getAll('to')];
  const ext = q.get('from.ext');
  if (ext && ext.startsWith('ext:')) out.push(ext.slice(4).split('/')[0]);
  return out.filter(Boolean);
}

// fitNamed is what a fit names (pkg/ui, fitNamed): named, and the part's
// vocabulary and spine.
const fitNamed = (q) => named(q).concat([q.get('part.vocabulary'), q.get('part.spine')].filter(Boolean));

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
  if (url.pathname === DOWNLOAD) {
    e.respondWith(own(e.request, download));
    return;
  }
  // The fit page is the module's while this browser holds the catalog and
  // the server has none, else the server's (ADR 0023): one catalog per
  // nitrovin. A fit naming a file of this browser's never goes to the
  // server: the module answers it, or refuses it in words.
  if (url.pathname === FIT) {
    const q = url.searchParams;
    e.respondWith(Promise.all([heldCatalog(), storedNames()]).then(([name, stored]) => {
      const mine = name && !config.catalog;
      const names = fitNamed(q);
      if (mine || names.some((n) => stored.has(n) || n === name)) return own(e.request, () => serve(e.request));
      return network(e.request);
    }));
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
    if (req.url.endsWith('/ui/files/remove')) forget();
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
// A catalog compiles in a dedicated worker the page starts (compile.js),
// not here: a browser may stop a Service Worker that spends minutes on one
// event. {op: "compiler"} on a MessageChannel answers with what it needs,
// {config}, or {refused} in words when this worker is outdated; {op:
// "catalog"} after it stored one opens the files again, answered on a
// MessageChannel once done. {op: "download"} answers with the stored
// catalog, {blob, name}, or {message}; {op: "left"} drops what a compile
// not running left and answers with what is left, {bytes}: a compile's
// running.
self.addEventListener('message', (e) => {
  const op = e.data && e.data.op;
  if (op === 'compiler' && e.ports[0]) {
    const port = e.ports[0];
    e.waitUntil(current().then((ok) => port.postMessage(ok ? { config } : { refused: stale() })));
    return;
  }
  if (op === 'download' && e.ports[0]) {
    const port = e.ports[0];
    e.waitUntil(self.nitrovinDisk.catalog().then(
      (c) => port.postMessage(c ? { blob: c.blob, name: fileName(c) } : { message: 'this browser holds no compiled catalog.' }),
      (err) => port.postMessage({ message: String(err) })));
    return;
  }
  if (op === 'left' && e.ports[0]) {
    const port = e.ports[0];
    e.waitUntil(clearLeftParts().then(() => self.nitrovinDisk.leftBytes()).then((bytes) => port.postMessage({ bytes }), () => port.postMessage({ bytes: 0 })));
    return;
  }
  if (op === 'catalog') {
    forget();
    if (self.nitrovin) self.nitrovin.reset();
    if (e.ports[0]) e.ports[0].postMessage({ done: true });
    return;
  }
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
      forget();
      port.postMessage({ done: true, ok: r.ok, message: !r.ok && outdated ? stale() : r.message });
    } catch (err) {
      port.postMessage({ done: true, ok: false, message: outdated ? stale() : String(err) });
    }
  })());
});
