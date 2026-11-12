// nitrovin's IndexedDB in the browser (ADR 0013, ADR 0023), imported by
// the Service Worker (/sw.js, before worker.js) and by the catalog
// compiler (compile.js), after the release's config (self.nitrovinConfig).
// It sets self.nitrovinDisk, what the module's Library calls (ui.Disk).
//
// - "nitrovin": files (records, by name), texts (by blake3) and indexes
//   (by release and blake3); a change is one transaction over the three.
// - "nitrovin-state": state, whose "selection" is the vocabularies the
//   visitor selected as targets by spine ({spine: [name]}), a database of
//   its own so that an older release, which opens "nitrovin" at version 1,
//   still can.
// - "nitrovin-catalog": the browser's one compiled catalog, kept apart
//   for the same reason. catalog's "catalog" is its record and the key of
//   its parts; parts are files kept in Blobs of a MiB, by [key, n], each
//   read back as one Blob: the compiled catalog, and the compile's
//   scratch files while it runs. Neither Go nor the page ever holds a
//   whole file. A compile writes its catalog's parts under a key of its
//   own and commits them in one transaction, which drops the parts of
//   the catalog it replaces; a compile that stopped half way leaves parts
//   no record keeps, which the next compile drops (clearParts).
'use strict';

// opener opens database name once per script, upgrade making its stores.
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
const filesDB = opener('nitrovin', (d) => {
  d.createObjectStore('files', { keyPath: 'name' });
  d.createObjectStore('texts');
  d.createObjectStore('indexes');
});
const stateDB = opener('nitrovin-state', (d) => d.createObjectStore('state'));
const catalogDB = opener('nitrovin-catalog', (d) => {
  d.createObjectStore('catalog');
  d.createObjectStore('parts');
});

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

const indexKey = (blake3) => self.nitrovinConfig.release + '\n' + blake3;
const bytes = (v) => (v === undefined ? null : new Uint8Array(v));
// partsOf is the key range of file key's parts.
const partsOf = (key) => IDBKeyRange.bound([key, 0], [key, Infinity]);

// names is the stored files' names, read once per script and kept in step
// with every change; held is the stored catalog's name ("" for none) the
// same way.
let names = null, held = null;
async function storedNames() {
  if (!names) names = new Set((await tx(['files'], 'readonly', (t) => request(t.objectStore('files').getAllKeys()))));
  return names;
}
async function heldCatalog() {
  if (held === null) {
    const c = await tx(['catalog'], 'readonly', (t) => request(t.objectStore('catalog').get('catalog')), catalogDB);
    held = c ? c.record.name : '';
  }
  return held;
}
// forget makes the next storedNames and heldCatalog read them again.
function forget() {
  names = null;
  held = null;
}

// select changes the stored selection on spine to what f makes of it.
const select = (spine, f) => tx(['state'], 'readwrite', async (t) => {
  const state = t.objectStore('state');
  const s = (await request(state.get('selection'))) || {};
  s[spine] = f(s[spine] || []);
  await request(state.put(s, 'selection'));
}, stateDB);

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
    forget();
    await tx(['state'], 'readwrite', (t) => request(t.objectStore('state').put(selection, 'selection')), stateDB);
  },

  // read is bytes [start, end) of blob, a File or a Blob IndexedDB keeps.
  read: async (blob, start, end) => new Uint8Array(await blob.slice(start, end).arrayBuffer()),
  // putPart keeps part n of file key.
  putPart: (key, n, part) => tx(['parts'], 'readwrite', (t) => request(t.objectStore('parts').put(new Blob([part]), [key, n])), catalogDB),
  // parts is file key, its parts as one Blob.
  parts: (key) => tx(['parts'], 'readonly', (t) => request(t.objectStore('parts').getAll(partsOf(key))), catalogDB).then((ps) => new Blob(ps)),
  dropParts: (key) => tx(['parts'], 'readwrite', (t) => request(t.objectStore('parts').delete(partsOf(key))), catalogDB),
  // clearParts drops every part the stored catalog does not keep.
  clearParts: () => tx(['catalog', 'parts'], 'readwrite', async (t) => {
    const c = await request(t.objectStore('catalog').get('catalog'));
    const parts = t.objectStore('parts');
    for (const key of new Set((await request(parts.getAllKeys())).map(([k]) => k))) {
      if (!c || key !== c.parts) await request(parts.delete(partsOf(key)));
    }
  }, catalogDB),
  // catalog is the stored catalog, {record, blob}, or null.
  catalog: () => tx(['catalog', 'parts'], 'readonly', async (t) => {
    const c = await request(t.objectStore('catalog').get('catalog'));
    if (!c) return null;
    return { record: c.record, blob: new Blob(await request(t.objectStore('parts').getAll(partsOf(c.parts)))) };
  }, catalogDB),
  // commitCatalog makes file key the stored catalog, recorded as record,
  // dropping the parts of the one it replaces.
  commitCatalog: async (key, record) => {
    await tx(['catalog', 'parts'], 'readwrite', async (t) => {
      const store = t.objectStore('catalog');
      const before = await request(store.get('catalog'));
      if (before && before.parts !== key) await request(t.objectStore('parts').delete(partsOf(before.parts)));
      await request(store.put({ record, parts: key }, 'catalog'));
    }, catalogDB);
    forget();
  },
  removeCatalog: async () => {
    await tx(['catalog', 'parts'], 'readwrite', async (t) => {
      const store = t.objectStore('catalog');
      const c = await request(store.get('catalog'));
      if (!c) return;
      await request(t.objectStore('parts').delete(partsOf(c.parts)));
      await request(store.delete('catalog'));
    }, catalogDB);
    forget();
  },
};
