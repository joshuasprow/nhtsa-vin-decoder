// PROTOTYPE (#147): enhances /vocab. Without it the server renders the page
// and every form and link works (vPIC and the server's --vocab files). With
// it, the Service Worker takes over the /ui routes that read vocabularies
// and the page re-renders from the worker, which adds the browser's files.
document.documentElement.classList.add('js');

const swReady = (async () => {
  if (!('serviceWorker' in navigator)) return false;
  const t0 = performance.now();
  await navigator.serviceWorker.register('/sw.js', { scope: '/' });
  await navigator.serviceWorker.ready;
  if (!navigator.serviceWorker.controller) {
    await new Promise((r) => navigator.serviceWorker.addEventListener('controllerchange', r, { once: true }));
  }
  const app = document.getElementById('vocab-app');
  if (app && app.dataset.where !== 'browser') {
    await htmx.ajax('GET', '/ui/vocab' + location.search, { target: '#vocab-app', swap: 'outerHTML' });
  }
  window.protoSwReadyMs = performance.now() - t0;
  document.documentElement.dataset.sw = 'ready';
  return true;
})();

function swCall(msg) {
  return new Promise((resolve) => {
    const ch = new MessageChannel();
    ch.port1.onmessage = (e) => resolve(e.data);
    navigator.serviceWorker.controller.postMessage(msg, [ch.port2]);
  });
}

document.addEventListener('change', async (e) => {
  if (e.target.id !== 'vocab-file') return;
  await swReady;
  const said = [];
  const say = (s) => { said.push(s); const el = document.getElementById('load-status'); if (el) el.textContent = said.join(' '); };
  for (const file of e.target.files) {
    say(`Reading ${file.name}…`);
    const t0 = performance.now();
    const text = await file.text();
    const res = await swCall({ op: 'load', name: file.name, text });
    said.pop();
    if (res.error) { say(`${file.name}: ${res.error}`); continue; }
    say(`${file.name}: ${res.entries} entries, checked and indexed in ${res.load_ms.toFixed(0)} ms, stored in ${res.idb_put_ms.toFixed(0)} ms (${(performance.now() - t0).toFixed(0)} ms in all).`);
    window.protoLastLoad = res;
  }
  await htmx.ajax('GET', '/ui/vocab', { target: '#vocab-app', swap: 'outerHTML' });
  say('');
});
