// The page's browser files (ADR 0013), behind its Vocabularies button:
// registers the Service Worker (worker.js), says what state it is in,
// swaps the worker's panel (the merged catalog, with the browser's files)
// over the server's catalog in the dialog, and hands the files the visitor
// picks or drops, and EPA's vocabulary on a click, to the worker by
// postMessage, and the targets the visitor ticks too. Without this script
// the dialog says that loading files needs JavaScript; everything else
// works.
//
// The worker is the module's 12 MB, so a visitor who never opens the
// dialog never gets it: it is registered when the dialog first opens or a
// file is dropped on the page, and started at once only in a browser that
// has it already.
//
// States, on #browser's data-state and in words in #browser-status:
// none before the worker is asked for, then starting, ready (the worker
// controls the page and its panel is in #browser-files), paused (an
// installed worker does not control this page: a hard reload bypasses
// it), unsupported (no Service Workers), and failed. An update waiting for
// the old worker's pages to close is said beside the state
// (data-update="waiting").
'use strict';

(() => {
  const section = document.getElementById('browser');
  if (!section || !section.dataset.worker) return;
  const dialog = document.getElementById('vocabularies');
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
  // redraw asks for the page's main again: its forms and its answer are
  // over the files loaded, which just changed.
  const redraw = () => htmx.ajax('GET', location.pathname + location.search, { target: '#ui-main', swap: 'innerHTML' });

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

  // start registers the worker, once, and settles when its state is said.
  let starting = null;
  const start = () => {
    if (!starting) {
      starting = (async () => {
        say('starting', 'Starting the translator in this browser…');
        const before = await sw.getRegistration('/');
        if (before && before.active && !sw.controller) {
          // Installed, yet not in control: this load bypassed it (a hard reload).
          watchUpdates(before);
          say('paused', 'Your own files are paused for this load: a hard reload bypasses the Service Worker that holds them. Reload the page normally to use them.');
          return;
        }
        const reg = await sw.register(section.dataset.worker, { scope: '/' });
        watchUpdates(reg);
        if (!sw.controller) await new Promise((resolve) => sw.addEventListener('controllerchange', resolve, { once: true }));
        await showFiles();
        say('ready', 'Your files stay in this browser and are translated here.');
      })().catch((err) => say('failed', 'The translator in this browser could not start: ' + err + '.'));
    }
    return starting;
  };
  sw.getRegistration('/').then((reg) => { if (reg) start(); });
  dialog.addEventListener('toggle', (e) => { if (e.newState === 'open') start(); });

  const ready = () => section.dataset.state === 'ready' && sw.controller;

  // retarget is href, a link to a lookup, to the targets ticked now on
  // spine, else to fallback (names): a link the page drew names its
  // targets when the page has them.
  const retarget = (href, spine, fallback) => {
    const url = new URL(href, location.origin);
    url.searchParams.delete('to');
    const boxes = spine && document.querySelector('#controls fieldset[data-spine="' + CSS.escape(spine) + '"]');
    const ticked = boxes ? [...boxes.querySelectorAll('input[name=to]:checked')].map((b) => b.value) : [];
    for (const name of ticked.length ? ticked : fallback) url.searchParams.append('to', name);
    return url.pathname + url.search;
  };
  // An example goes to the targets ticked now on its spine (data-spine),
  // else to its own (data-to), not to those ticked when the form was
  // drawn.
  const relink = () => {
    for (const a of document.querySelectorAll('#controls a.example')) {
      a.setAttribute('href', retarget(a.getAttribute('href'), a.dataset.spine, (a.dataset.to || '').split(' ').filter(Boolean)));
    }
  };

  // The targets ticked in the page's form are the visitor's selection,
  // which the worker keeps beside the files: a tick goes to it at once.
  document.addEventListener('change', (e) => {
    const box = e.target;
    if (!box.matches || !box.matches('#controls input[name=to]')) return;
    relink();
    if (ready()) sw.controller.postMessage({ op: 'select', name: box.value, spine: box.closest('fieldset').dataset.spine, checked: box.checked });
  });

  // tick ticks the visitor's selection in the forms, on each spine it
  // holds targets for, when the page's lookup (path) named none of its
  // own: so what the visitor asks next names its targets (to=), and a
  // link naming none means the same in every browser. The selection is
  // the worker's, asked for whenever one controls the page.
  const tick = async (path) => {
    if (!sw.controller || new URL(path, location.origin).searchParams.has('to')) return;
    const selection = await new Promise((resolve) => {
      const ch = new MessageChannel();
      ch.port1.onmessage = (e) => resolve(e.data || {});
      sw.controller.postMessage({ op: 'selection' }, [ch.port2]);
    });
    for (const set of document.querySelectorAll('#controls fieldset[data-spine]')) {
      const names = selection[set.dataset.spine];
      if (!names) continue;
      for (const box of set.querySelectorAll('input[name=to]')) box.checked = names.includes(box.value);
    }
    relink();
  };
  tick(location.pathname + location.search).catch(() => {});
  document.addEventListener('htmx:afterSettle', (e) => {
    if (e.detail.target && e.detail.target.id === 'ui-main') tick((e.detail.pathInfo && e.detail.pathInfo.requestPath) || location.href).catch(() => {});
  });

  // htmx asks with the href a link had when it was drawn: an example
  // asks for its targets as relink sets them, and any other link to a
  // lookup that names no target, a map's card among them, for the
  // targets ticked now on the answer's spine.
  document.addEventListener('htmx:configRequest', (e) => {
    const a = e.detail.elt;
    if (!a.matches || !a.matches('#ui-main a[href]')) return;
    if (a.matches('#controls a.example')) {
      e.detail.path = retarget(e.detail.path, a.dataset.spine, (a.dataset.to || '').split(' ').filter(Boolean));
      return;
    }
    const url = new URL(e.detail.path, location.origin);
    if (url.pathname !== '/' || url.searchParams.has('to') || ![...url.searchParams.keys()].some((k) => k.startsWith('from.'))) return;
    const result = document.getElementById('result');
    e.detail.path = retarget(e.detail.path, result && result.dataset.spine, []);
  });

  // A file removed changes what the page's forms and answer are over.
  document.addEventListener('htmx:afterRequest', (e) => {
    if (e.detail.successful && e.detail.pathInfo.requestPath === '/ui/files/remove') redraw();
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
    if (done.ok) await redraw();
  };
  const failed = (err) => tell('Nothing was loaded: ' + (err && err.message ? err.message : err));

  // fromDisk is a picked or dropped file to load.
  const fromDisk = (f) => ({
    name: f.name,
    read: () => f.arrayBuffer().catch((err) => { throw new Error(f.name + ' could not be read (' + err.message + ').'); }),
  });
  // loadFiles loads the files of list in the dialog, where a load is
  // told, once the worker is ready.
  const loadFiles = async (list) => {
    const picked = [...list];
    if (picked.length === 0) return;
    if (!dialog.matches(':popover-open')) dialog.showPopover();
    await start();
    if (!ready()) {
      tell('Nothing was loaded: files load once the translator in this browser is ready.');
      return;
    }
    await load(picked.map((f) => f.name).join(', '), picked.map(fromDisk));
  };

  document.addEventListener('change', (e) => {
    if (e.target.id === 'vocab-files') loadFiles(e.target.files).catch(failed);
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
    loadFiles(e.dataTransfer.files).catch(failed);
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
