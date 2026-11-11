// /vocab's browser files (ADR 0013): registers the Service Worker
// (worker.js), says what state it is in, swaps the worker's panel (the
// merged catalog, with the browser's files, and the translate forms over
// it, the visitor's selection ticked) over the server's, and hands the
// files the visitor picks or drops, and EPA's vocabulary on a click, to
// the worker by postMessage, and the targets the visitor ticks too.
// Without this script the page says that loading files needs JavaScript;
// everything else on it works.
//
// States, on #browser's data-state and in words in #browser-status:
// starting, ready (the worker controls the page and its panel is in
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
  // tell says how a load went, in #load-status, which no swap replaces.
  const tell = (text) => {
    document.getElementById('load-status').textContent = text;
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

  const ready = () => section.dataset.state === 'ready' && sw.controller;

  // The targets ticked in /vocab's translate forms are the visitor's
  // selection, which the worker keeps beside the files: a tick goes to it
  // at once, and to every form's box for the same vocabulary.
  document.addEventListener('change', (e) => {
    const box = e.target;
    if (!box.matches || !box.matches('#vocab-translate input[name=to]') || !ready()) return;
    for (const other of document.querySelectorAll('#vocab-translate input[name=to]')) {
      if (other.value === box.value) other.checked = box.checked;
    }
    sw.controller.postMessage({ op: 'select', name: box.value, spine: box.closest('fieldset').dataset.spine, checked: box.checked });
  });

  // An example clicked on /vocab translates to the targets ticked there
  // now on its spine, else to its own (data-to), not to those ticked when
  // the forms were drawn.
  document.addEventListener('htmx:configRequest', (e) => {
    const a = e.detail.elt;
    if (!a.matches || !a.matches('#vocab-translate a.example')) return;
    const boxes = document.querySelector('#vocab-translate fieldset[data-spine="' + CSS.escape(a.dataset.spine) + '"]');
    if (!boxes) return;
    const ticked = [...boxes.querySelectorAll('input[name=to]:checked')].map((b) => b.value);
    const url = new URL(e.detail.path, location.origin);
    url.searchParams.delete('to');
    for (const name of ticked.length ? ticked : a.dataset.to.split(' ').filter(Boolean)) url.searchParams.append('to', name);
    e.detail.path = url.pathname + url.search;
  });

  // usage says, below the panel, how much this site keeps in the browser
  // and whether the browser may clear it.
  const usage = async () => {
    const line = document.getElementById('storage-usage');
    if (!line || !navigator.storage || !navigator.storage.estimate) return;
    const { usage: used, quota } = await navigator.storage.estimate();
    const kept = navigator.storage.persisted && await navigator.storage.persisted();
    line.textContent = 'This site keeps ' + (used / 1e6).toFixed(1) + ' MB in this browser, of the ' + Math.round(quota / 1e6).toLocaleString('en') +
      ' MB the browser allows it' + (kept ? '; the browser keeps it until you remove it.' : '; the browser may clear it when it runs short of space.');
  };
  document.addEventListener('htmx:afterSettle', () => { usage().catch(() => {}); });

  // load hands files to the worker as one batch: [{name, read}], read
  // giving each file's bytes, which go by postMessage (transferred, never
  // copied to a request). what names the batch in the status line.
  const load = async (what, picked) => {
    if (navigator.storage && navigator.storage.persist) navigator.storage.persist().catch(() => {});
    tell('Reading ' + what + '…');
    const files = await Promise.all(picked.map(async (f) => ({ name: f.name, data: await f.read() })));
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
    tell(done.ok ? done.message : 'Nothing was loaded: ' + done.message);
  };
  const failed = (err) => tell('Nothing was loaded: ' + (err && err.message ? err.message : err));

  // fromDisk is a picked or dropped file to load.
  const fromDisk = (f) => ({
    name: f.name,
    read: () => f.arrayBuffer().catch((err) => { throw new Error(f.name + ' could not be read (' + err.message + ').'); }),
  });
  const loadFiles = (list) => {
    const picked = [...list];
    if (picked.length === 0) return;
    if (!ready()) {
      tell('Nothing was loaded: files load once the translator in this browser is ready.');
      return;
    }
    load(picked.map((f) => f.name).join(', '), picked.map(fromDisk)).catch(failed);
  };

  document.addEventListener('change', (e) => {
    if (e.target.id === 'vocab-files') loadFiles(e.target.files);
  });

  // Files dropped anywhere on the page load as if picked: some browsers
  // (embedded ones among them) open no dialog for a file input.
  const carriesFiles = (e) => e.dataTransfer && [...e.dataTransfer.types].includes('Files');
  let over = 0;
  document.addEventListener('dragenter', (e) => {
    if (!carriesFiles(e)) return;
    over++;
    section.classList.add('dropping');
  });
  document.addEventListener('dragleave', (e) => {
    if (carriesFiles(e) && --over <= 0) {
      over = 0;
      section.classList.remove('dropping');
    }
  });
  document.addEventListener('dragover', (e) => {
    if (!carriesFiles(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
  });
  document.addEventListener('drop', (e) => {
    if (!carriesFiles(e)) return;
    e.preventDefault();
    over = 0;
    section.classList.remove('dropping');
    loadFiles(e.dataTransfer.files);
  });

  // EPA's vocabulary, from this release's assets: one click.
  document.addEventListener('click', (e) => {
    const button = e.target.closest && e.target.closest('#load-epa');
    if (!button || !ready()) return;
    load("EPA's vocabulary", [{
      name: button.dataset.file,
      read: async () => {
        let r;
        try {
          r = await fetch(button.dataset.url);
        } catch {
          throw new Error("EPA's vocabulary could not be downloaded: this server could not be reached, and this browser may be offline.");
        }
        if (!r.ok) throw new Error("EPA's vocabulary could not be downloaded: this server answered HTTP " + r.status + '. Reload the page and try again.');
        return r.arrayBuffer();
      },
    }]).catch(failed);
  });
})();
