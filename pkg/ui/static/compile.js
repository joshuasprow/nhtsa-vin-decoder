// nitrovin's catalog compiler (ADR 0023): a dedicated worker the page
// (vocab.js) starts when the visitor picks a catalog's JSON Lines, running
// the same module as the Service Worker (cmd/nitrovin-sw, sw.wasm) and the
// same streaming compiler as `nitrovin vocab compile`. It runs here, not
// in the Service Worker, because a browser may stop a Service Worker that
// spends minutes on one event; so the page shows its progress and must
// stay open while it runs.
//
// The page posts {op: "compile", config, file}: the release's config, as
// the Service Worker has it, and the picked File, which is read in place,
// a slice at a time, and never stored. The compile sorts in IndexedDB and
// streams the compiled file there (disk.js); the Service Worker then reads
// it by range. It posts {progress} at each step, then {done, ok, message,
// memory}, memory the bytes of WebAssembly memory it used at most. One
// compile runs at a time in a browser: another tab's waits for none and is
// refused in words.
'use strict';

self.addEventListener('message', async (e) => {
  if (!e.data || e.data.op !== 'compile') return;
  const { config, file } = e.data;
  const say = (m) => self.postMessage(m);
  try {
    self.nitrovinConfig = config;
    importScripts(config.assets + '/wasm_exec.js', config.assets + '/disk.js');
    const cache = 'nitrovin ' + config.release;
    // get is a GET on this origin, answered by the release's cache when it
    // holds the path (ui.Remote), as the Service Worker's is.
    self.nitrovinGet = async (path) => {
      const r = (await caches.match(path, { cacheName: cache })) || await fetch(path);
      return { status: r.status, body: new Uint8Array(await r.arrayBuffer()) };
    };
    say({ progress: 'Starting the compiler…' });
    const ready = new Promise((resolve) => { self.nitrovinReady = resolve; });
    const go = new Go();
    const module = config.assets + '/sw.wasm';
    const { instance } = await WebAssembly.instantiateStreaming((await caches.match(module, { cacheName: cache })) || fetch(module), go.importObject);
    go.run(instance);
    await ready;
    const compile = () => self.nitrovin.compile(file, (text) => say({ progress: text }));
    const r = navigator.locks
      ? await navigator.locks.request('nitrovin-compile', { ifAvailable: true }, (lock) => (lock ? compile()
        : { ok: false, message: 'a catalog is compiling in another tab of this browser: wait until it is done, then try again.' }))
      : await compile();
    say({ done: true, ok: r.ok, message: r.message, memory: instance.exports.mem.buffer.byteLength });
  } catch (err) {
    say({ done: true, ok: false, message: 'the compiler in this browser stopped: ' + err + '.' });
  }
});
