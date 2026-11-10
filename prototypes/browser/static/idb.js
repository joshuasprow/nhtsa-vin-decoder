// PROTOTYPE (#147): the IndexedDB store of vocabulary files, shared by the
// Service Worker (htmx variant) and the plain-JS page. One record per file:
// {key: "name@blake3", name, text, stored}. Both variants share it.
const IDB_NAME = 'nitrovin-vocab-prototype-147';
const IDB_STORE = 'files';

function idbOpen() {
  return new Promise((resolve, reject) => {
    const r = indexedDB.open(IDB_NAME, 1);
    r.onupgradeneeded = () => r.result.createObjectStore(IDB_STORE, { keyPath: 'key' });
    r.onsuccess = () => resolve(r.result);
    r.onerror = () => reject(r.error);
  });
}

async function idbTx(mode, f) {
  const db = await idbOpen();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(IDB_STORE, mode);
    const req = f(tx.objectStore(IDB_STORE));
    tx.oncomplete = () => { db.close(); resolve(req.result); };
    tx.onerror = () => { db.close(); reject(tx.error); };
  });
}

globalThis.idbPut = (rec) => idbTx('readwrite', (s) => s.put(rec));
globalThis.idbAll = () => idbTx('readonly', (s) => s.getAll());
globalThis.idbDelete = (key) => idbTx('readwrite', (s) => s.delete(key));
