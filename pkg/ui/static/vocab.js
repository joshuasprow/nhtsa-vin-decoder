// The page's browser files (ADR 0013), behind its Vocabularies button:
// registers the Service Worker (worker.js), says what state it is in,
// swaps the worker's panel (the merged catalog, with the browser's files)
// over the server's catalog in the dialog, and hands the files the visitor
// picks or drops, and EPA's vocabulary on a click, to the worker by
// postMessage, and the targets the visitor ticks too. A catalog picked on
// its own is compiled in a dedicated worker it starts (compile.js,
// ADR 0023), the File handed over and read in place. Without this script
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
// it), outdated (the worker in control is of another release than the
// page, #browser's data-release, or the server, or does not answer the
// page at all: it answers nothing, so every page is the server's and the
// visitor's files wait for the update), unsupported (no Service Workers),
// and failed. An update waiting for the old worker's pages to close is
// said beside the state (data-update="waiting"), with Update now, which
// hands the page to it at once and reloads. #browser's data-ticked is
// set once the page's first tick of the visitor's selection is done,
// whether it ticked anything or not.
'use strict';

(() => {
  const section = document.getElementById('browser');
  if (!section || !section.dataset.worker) return;
  const dialog = document.getElementById('vocabularies');
  const status = document.getElementById('browser-status');
  // release is the release that drew the page, which the worker in
  // control must be of.
  const release = section.dataset.release || '';
  const version = (r) => r.split('/')[0];
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
  let registration = null;

  // ask asks the worker in control msg on a MessageChannel, resolving with
  // its answer, or with null after ms without one: a worker of another
  // release may not know msg's op.
  const ask = (msg, ms) => new Promise((resolve) => {
    const ch = new MessageChannel();
    const timer = setTimeout(() => resolve(null), ms);
    ch.port1.onmessage = (e) => {
      clearTimeout(timer);
      resolve(e.data);
    };
    sw.controller.postMessage(msg, [ch.port2]);
  });

  const showFiles = () => htmx.ajax('GET', '/ui/files', { target: '#browser-files', swap: 'innerHTML' });
  // redraw asks for the page's main again: its forms and its answer are
  // over the files loaded, which just changed.
  const redraw = () => htmx.ajax('GET', location.pathname + location.search, { target: '#ui-main', swap: 'innerHTML' });

  // Update now hands the page to the waiting worker at once (it skips
  // waiting) and reloads, once the new worker controls the page; another
  // page's Update now reloads this one too, once it was the old worker's.
  const updateNow = document.createElement('button');
  updateNow.type = 'button';
  updateNow.id = 'update-now';
  updateNow.textContent = 'Update now';
  updateNow.hidden = true;
  status.after(updateNow);
  let controlled = Boolean(sw.controller), updating = false;
  sw.addEventListener('controllerchange', () => {
    if (controlled || updating) location.reload();
    controlled = true;
  });
  updateNow.addEventListener('click', () => {
    const waiting = registration && registration.waiting;
    updating = true;
    updateNow.disabled = true;
    status.textContent = 'Updating the translator in this browser…';
    // None waits any more: another page took the update already.
    if (!waiting) location.reload();
    else waiting.postMessage({ op: 'skipWaiting' });
  });

  const watchUpdates = (reg) => {
    const waiting = () => {
      update = ' A new release of nitrovin is ready: it starts once every nitrovin tab is closed, or now with Update now.';
      section.dataset.update = 'waiting';
      status.textContent = status.textContent.replace(/ A new release.*$/, '') + update;
      updateNow.hidden = false;
    };
    // track waits for w, a worker installing, to be installed.
    const track = (w) => {
      if (w) w.addEventListener('statechange', () => { if (w.state === 'installed' && sw.controller) waiting(); });
    };
    if (reg.waiting && sw.controller) waiting();
    track(reg.installing);
    reg.addEventListener('updatefound', () => track(reg.installing));
  };

  // outdated says the worker in control answers nothing, mine its answer
  // to {op: "release"} ({release, server}), null when it gave none, and
  // has the browser look for the new release's worker.
  const outdated = (mine) => {
    const server = mine && mine.server && mine.server.includes('/') ? mine.server : release;
    const what = !mine ? 'does not answer this page, so it is of another release of nitrovin than this server\'s'
      : version(mine.release) !== version(server) ? 'is nitrovin ' + version(mine.release) + ', and this server runs ' + version(server)
        : 'is of another release of nitrovin than this server\'s';
    say('outdated', 'Your own files are paused: the translator in this browser ' + what + '. Until it updates, every page comes from the server.');
    if (registration && !registration.waiting && !registration.installing) registration.update().catch(() => {});
  };
  // The worker in control tells the page once it learns the server runs
  // another release.
  sw.addEventListener('message', (e) => {
    if (e.data && e.data.op === 'outdated' && section.dataset.state !== 'outdated') outdated(e.data);
  });
  sw.startMessages();

  // mine is the worker in control's answer to {op: "release"}, asked once
  // a page ({release, server}; null when it gave none, asked again next
  // time); matched reports whether that worker is of the page's release
  // and not outdated.
  let asked = null;
  const mine = () => asked || (asked = ask({ op: 'release' }, 5000).then((m) => {
    if (!m) asked = null;
    return m;
  }));
  const matched = (m) => Boolean(m) && !m.server && m.release === release;

  // start registers the worker, once, and settles when its state is said.
  let starting = null;
  const start = () => {
    if (!starting) {
      starting = (async () => {
        say('starting', 'Starting the translator in this browser…');
        const before = await sw.getRegistration('/');
        if (before && before.active && !sw.controller) {
          // Installed, yet not in control: this load bypassed it (a hard reload).
          registration = before;
          watchUpdates(before);
          say('paused', 'Your own files are paused for this load: a hard reload bypasses the Service Worker that holds them. Reload the page normally to use them.');
          return;
        }
        const reg = await sw.register(section.dataset.worker, { scope: '/' });
        registration = reg;
        watchUpdates(reg);
        if (!sw.controller) await new Promise((resolve) => sw.addEventListener('controllerchange', resolve, { once: true }));
        const m = await mine();
        if (!matched(m)) {
          outdated(m);
          return;
        }
        await showFiles();
        say('ready', 'Your files stay in this browser and are translated here.');
        // The panel settled before the page was ready, so what a compile
        // not finished left is dropped and said now.
        usage().catch(() => {});
      })().catch((err) => say('failed', 'The translator in this browser could not start: ' + err + '.'));
    }
    return starting;
  };
  sw.getRegistration('/').then((reg) => { if (reg) start(); });
  dialog.addEventListener('toggle', (e) => { if (e.newState === 'open') start(); });

  const ready = () => section.dataset.state === 'ready' && sw.controller;
  // why is why files cannot load now, by the state.
  const why = () => ({
    outdated: 'the translator in this browser is of another release of nitrovin than this server\'s. Update it (Update now, or close every nitrovin tab), then load them again.',
    paused: 'a hard reload bypassed the translator in this browser for this page. Reload the page normally, then load them again.',
    failed: 'the translator in this browser could not start. Reload the page, then load them again.',
  })[section.dataset.state] || 'files load once the translator in this browser is ready.';

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
  // the worker's, asked for whenever one of the page's release controls
  // the page, beside that question: the forms are ticked without waiting
  // for the dialog's panel.
  const tick = async (path) => {
    if (!sw.controller || new URL(path, location.origin).searchParams.has('to')) return;
    const [m, selection] = await Promise.all([mine(), ask({ op: 'selection' }, 5000)]);
    if (!matched(m) || section.dataset.state === 'outdated' || !selection) return;
    for (const set of document.querySelectorAll('#controls fieldset[data-spine]')) {
      const names = selection[set.dataset.spine];
      if (!names) continue;
      for (const box of set.querySelectorAll('input[name=to]')) box.checked = names.includes(box.value);
    }
    relink();
  };
  // data-ticked on #browser marks the end of the page's first tick,
  // whatever came of it: the forms hold what the page will show.
  tick(location.pathname + location.search).catch(() => {}).finally(() => { section.dataset.ticked = ''; });
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
  // and whether the browser may clear it; and what a compile not finished
  // keeps, which the worker drops (left) whenever no compile runs: one
  // running in another tab, or this one, holds it until it is done.
  const usage = async () => {
    const line = document.getElementById('storage-usage');
    if (!line || !navigator.storage || !navigator.storage.estimate) return;
    const left = ready() && !compiling ? await ask({ op: 'left' }, 5000) : null;
    const { usage: used, quota } = await navigator.storage.estimate();
    const kept = navigator.storage.persisted && await navigator.storage.persisted();
    line.textContent = 'This site keeps ' + (used / 1e6).toFixed(1) + ' MB in this browser, of the ' + Math.round(quota / 1e6).toLocaleString('en') +
      ' MB the browser allows it' + (kept ? '; the browser keeps it until you remove it.' : '; the browser may clear it when it runs short of space.') +
      (left && left.bytes > 0 ? ' A compile not finished keeps ' + (left.bytes / 1e6).toFixed(1) + ' MB of it, dropped once no compile runs.' : '');
    section.dataset.leftBytes = left ? String(left.bytes) : '';
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
    if (ready()) await showFiles();
    tell(done.ok ? done.message : 'Nothing was loaded: ' + done.message);
    if (done.ok) await redraw();
  };
  const failed = (err) => tell('Nothing was loaded: ' + (err && err.message ? err.message : err));

  // fromDisk is a picked or dropped file to load.
  const fromDisk = (f) => ({
    name: f.name,
    read: () => f.arrayBuffer().catch((err) => { throw new Error(f.name + ' could not be read (' + err.message + ').'); }),
  });
  // kindOf is the kind f's header, its first line that is not blank,
  // says, read from f's first bytes alone: a catalog may be hundreds of
  // MB, never read into the page.
  const kindOf = async (f) => {
    const line = (await f.slice(0, 1 << 16).text()).split('\n').find((l) => l.trim() !== '');
    try {
      return (line && JSON.parse(line).kind) || '';
    } catch {
      return '';
    }
  };

  // compiling is true while a catalog compiles in this page: leaving it
  // stops the compile, so the browser asks first (stay).
  let compiling = false;
  const stay = (e) => e.preventDefault();

  // compile compiles file, a catalog's JSON Lines, as this browser's
  // catalog (ADR 0023), in a dedicated worker this page starts
  // (compile.js), which reads the File in place: the page says how far it
  // is and that the tab must stay open. Once it is stored the Service
  // Worker opens the files again.
  const compile = async (file) => {
    if (navigator.storage && navigator.storage.persist) navigator.storage.persist().catch(() => {});
    const keep = ' Keep this tab open until it is done.';
    tell('Compiling ' + file.name + '…' + keep);
    const c = await ask({ op: 'compiler' }, 5000);
    if (!c || c.refused) {
      tell('Nothing was compiled: ' + (c ? c.refused : 'the translator in this browser did not answer. Reload the page, then try again.'));
      return;
    }
    compiling = true;
    window.addEventListener('beforeunload', stay);
    const worker = new Worker(c.config.assets + '/compile.js');
    let done;
    try {
      done = await new Promise((resolve) => {
        worker.onmessage = (e) => {
          if (e.data.done) resolve(e.data);
          else tell(e.data.progress + keep);
        };
        worker.onerror = (e) => resolve({ ok: false, message: 'the compiler in this browser stopped: ' + (e.message || 'an error') + '.' });
        worker.postMessage({ op: 'compile', config: c.config, file });
      });
    } finally {
      compiling = false;
      window.removeEventListener('beforeunload', stay);
      worker.terminate();
    }
    section.dataset.compileMemory = done.memory || '';
    if (done.ok) await ask({ op: 'catalog' }, 5000);
    if (ready()) await showFiles();
    tell(done.ok ? done.message : 'Nothing was compiled: ' + done.message);
    if (done.ok) await redraw();
  };

  // loadFiles loads the files of list in the dialog, where a load is
  // told, once the worker is ready. A catalog picked on its own is
  // compiled instead.
  const loadFiles = async (list) => {
    const picked = [...list];
    if (picked.length === 0) return;
    if (!dialog.matches(':popover-open')) dialog.showPopover();
    await start();
    if (!ready()) {
      tell('Nothing was loaded: ' + why());
      return;
    }
    if (compiling) {
      tell('Nothing was loaded: a catalog is compiling in this tab. Wait until it is done, then load them again.');
      return;
    }
    const what = picked.map((f) => f.name).join(', ');
    tell('Reading ' + what + '…');
    const kinds = await Promise.all(picked.map(kindOf));
    if (kinds.includes('catalog')) {
      if (picked.length > 1) {
        tell('Nothing was loaded: ' + picked[kinds.indexOf('catalog')].name + ' is a catalog. Pick it on its own: a catalog is compiled, not loaded with other files.');
        return;
      }
      await compile(picked[0]);
      return;
    }
    await load(what, picked.map(fromDisk));
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

  // Download: the compiled catalog is the worker's, from IndexedDB, as a
  // Blob (handed over by reference, never copied), saved from a blob URL.
  // A browser may send a link's download to the server without asking
  // the worker; the link stays for a page without this script.
  document.addEventListener('click', async (e) => {
    const a = e.target.closest && e.target.closest('a[href="/ui/files/catalog"]');
    if (!a || !ready()) return;
    e.preventDefault();
    const got = await ask({ op: 'download' }, 10000);
    if (!got || !got.blob) {
      tell('The catalog could not be downloaded: ' + (got && got.message ? got.message : 'the translator in this browser did not answer. Reload the page, then try again.'));
      return;
    }
    const url = URL.createObjectURL(got.blob);
    const save = document.createElement('a');
    save.href = url;
    save.download = got.name;
    document.body.append(save);
    save.click();
    save.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60000);
  });

  // EPA's vocabulary, from this release's assets: one click.
  document.addEventListener('click', (e) => {
    const button = e.target.closest && e.target.closest('#load-epa');
    if (!button) return;
    if (!ready()) {
      tell('Nothing was loaded: ' + why());
      return;
    }
    load("EPA's vocabulary", [{
      name: button.dataset.file,
      read: async () => {
        let r;
        try {
          r = await fetch(button.dataset.url);
        } catch {
          throw new Error("EPA's vocabulary could not be downloaded: this server could not be reached, and this browser may be offline.");
        }
        if (r.ok) return r.arrayBuffer();
        const server = r.headers.get('Nitrovin-Release');
        throw new Error("EPA's vocabulary could not be downloaded: this server answered HTTP " + r.status + (server && server !== release
          ? ', as it now runs another release of nitrovin than this page. Update the translator in this browser (Update now, or close every nitrovin tab), then try again.'
          : '. Reload the page and try again.'));
      },
    }]).catch(failed);
  });
})();
