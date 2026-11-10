// /vocab's browser files (ADR 0013): registers the Service Worker
// (worker.js), says what state it is in, and hands the files the visitor
// picks to it by postMessage. Without this script the page says that
// loading files needs JavaScript; everything else on it works.
//
// States, on #browser's data-state and in words in #browser-status:
// starting, ready (the worker controls the page and lists the files in
// #browser-files), paused (an installed worker does not control this
// page: a hard reload bypasses it), unsupported (no Service Workers), and
// failed. An update waiting for the old worker's pages to close is said
// beside the state (data-update="waiting").
'use strict';

(() => {
  const section = document.getElementById('browser');
  if (!section || !section.dataset.worker) return;
  const status = document.getElementById('browser-status');
  // stall is how long a load may go without a word from the worker: a
  // minute, or #browser's data-stall (milliseconds), which a test sets.
  const stall = () => Number(section.dataset.stall) || 60000;
  let update = '';

  const say = (state, text) => {
    section.dataset.state = state;
    status.textContent = text + update;
  };

  if (!('serviceWorker' in navigator)) {
    say('unsupported', 'This browser has no Service Workers, which keep and translate your own files here: they cannot be loaded. Translation through vPIC and this server\'s own files works.');
    return;
  }
  const sw = navigator.serviceWorker;

  const showFiles = () => htmx.ajax('GET', '/ui/files', { target: '#browser-files', swap: 'innerHTML' });

  const watchUpdates = (reg) => {
    const waiting = () => {
      update = ' A new release of nitrovin is ready: it starts once every nitrovin tab is closed.';
      section.dataset.update = 'waiting';
      status.textContent = status.textContent.replace(/ A new release.*$/, '') + update;
    };
    if (reg.waiting && sw.controller) waiting();
    reg.addEventListener('updatefound', () => {
      const w = reg.installing;
      if (!w) return;
      w.addEventListener('statechange', () => { if (w.state === 'installed' && sw.controller) waiting(); });
    });
  };

  (async () => {
    say('starting', 'Starting the translator in this browser…');
    const before = await sw.getRegistration('/');
    if (before && before.active && !sw.controller) {
      // Installed, yet not in control: this load bypassed it (a hard reload).
      watchUpdates(before);
      say('paused', 'Your own files are paused for this load: a hard reload bypasses the Service Worker that holds them. Reload the page normally to use them.');
      return;
    }
    const reg = await sw.register('/sw.js', { scope: '/' });
    watchUpdates(reg);
    if (!sw.controller) await new Promise((resolve) => sw.addEventListener('controllerchange', resolve, { once: true }));
    await showFiles();
    say('ready', 'Your files stay in this browser and are translated here.');
  })().catch((err) => say('failed', 'The translator in this browser could not start: ' + err + '.'));

  // load hands files to the worker as one batch. Their bytes go by
  // postMessage (transferred, never copied to a request).
  const load = async (picked) => {
    // The panel is swapped after the load: the line is looked up each time.
    const tell = (text) => {
      const line = document.getElementById('load-status');
      if (line) line.textContent = text;
    };
    if (navigator.storage && navigator.storage.persist) navigator.storage.persist().catch(() => {});
    tell('Reading ' + picked.map((f) => f.name).join(', ') + '…');
    const files = await Promise.all(picked.map(async (f) => ({ name: f.name, data: await f.arrayBuffer() })));
    const done = await new Promise((resolve) => {
      const ch = new MessageChannel();
      let timer;
      const wait = () => {
        clearTimeout(timer);
        timer = setTimeout(() => resolve({ ok: false, message: 'The load stalled: no word from the translator in ' + stall() / 1000 + ' s. Nothing was stored; try again.' }), stall());
      };
      ch.port1.onmessage = (e) => {
        if (e.data.done) {
          clearTimeout(timer);
          resolve(e.data);
          return;
        }
        tell(e.data.progress);
        wait();
      };
      wait();
      sw.controller.postMessage({ op: 'load', files }, [ch.port2, ...files.map((f) => f.data)]);
    });
    await showFiles();
    let usage = '';
    if (done.ok && navigator.storage && navigator.storage.estimate) {
      const { usage: used, quota } = await navigator.storage.estimate();
      usage = ' This site stores ' + (used / 1e6).toFixed(1) + ' MB of the ' + (quota / 1e6).toFixed(0) + ' MB the browser allows it.';
    }
    tell((done.ok ? done.message : 'Nothing was loaded: ' + done.message) + usage);
  };

  document.addEventListener('change', (e) => {
    if (e.target.id !== 'vocab-files' || !sw.controller || e.target.files.length === 0) return;
    load([...e.target.files]).catch((err) => {
      const line = document.getElementById('load-status');
      if (line) line.textContent = 'Nothing was loaded: ' + err + '.';
    });
  });
})();
