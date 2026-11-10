// PROTOTYPE (#147): the plain-JS variant. The page instantiates wasm/core
// (Go or TinyGo, `serve -core`), keeps files in IndexedDB, fetches
// /v1/decode for a VIN and renders the JSON answers itself.
const $ = (id) => document.getElementById(id);
const el = (tag, text, cls) => { const e = document.createElement(tag); if (text != null) e.textContent = text; if (cls) e.className = cls; return e; };
const timings = (window.protoTimings = {});

async function boot() {
  const t0 = performance.now();
  const go = new Go();
  const { instance } = await WebAssembly.instantiateStreaming(fetch(`/ui/core/${document.documentElement.dataset.core}/core.wasm`), go.importObject);
  go.run(instance);
  const t1 = performance.now();
  const spine = await (await fetch('/ui/spine.json')).text();
  const t2 = performance.now();
  const sp = JSON.parse(nitrovin.setSpine(spine));
  if (sp.error) throw new Error(sp.error);
  const t3 = performance.now();
  const recs = await idbAll();
  const t4 = performance.now();
  const ix = JSON.parse(nitrovin.index(recs));
  const t5 = performance.now();
  Object.assign(timings, {
    wasm_ms: t1 - t0, spine_fetch_ms: t2 - t1, spine_index_ms: t3 - t2, idb_read_ms: t4 - t3,
    reindex_ms: t5 - t4, boot_ms: t5 - t0, files: recs.length, spine: sp, index: ix,
  });
  $('status').textContent = `WebAssembly ready in ${timings.boot_ms.toFixed(0)} ms (module ${timings.wasm_ms.toFixed(0)}, spine bundle ${(timings.spine_fetch_ms + timings.spine_index_ms).toFixed(0)}, ${recs.length} stored file(s) re-indexed in ${timings.reindex_ms.toFixed(0)}).`;
  render();
  $('app').hidden = false;
  document.documentElement.dataset.ready = 'yes';
}

function catalog() {
  const l = JSON.parse(nitrovin.list('{}'));
  return l.entries.filter((e) => e.kind === 'vocabulary');
}

function render() {
  const files = JSON.parse(nitrovin.files()) || [];
  const t = $('files');
  t.replaceChildren();
  if (!files.length) { t.append(el('caption', 'None loaded yet.')); }
  else {
    const head = el('tr');
    for (const h of ['Name', 'Kind', 'Version', 'Entries', 'Drift', 'Size', 'blake3', '']) head.append(el('th', h));
    t.append(head);
    for (const f of files) {
      const tr = el('tr');
      for (const v of [f.Name, f.Kind, f.Version, f.Entries, f.Drift, `${Math.round(f.Bytes / 1024)} KB`]) tr.append(el('td', v));
      tr.append(el('td', f.Blake3.slice(0, 12), 'mono'));
      const b = el('button', 'Remove');
      b.type = 'button';
      b.onclick = async () => { nitrovin.remove(f.Key); await idbDelete(f.Key); render(); };
      const td = el('td'); td.append(b); tr.append(td);
      t.append(tr);
    }
  }
  const vocabs = catalog();
  const from = $('from');
  const keep = from.value;
  from.replaceChildren(new Option('a VIN', 'vin'));
  for (const c of vocabs) from.append(new Option(`an entry of ${c.name} (${(c.fields || []).join(', ')})`, c.name));
  from.value = [...from.options].some((o) => o.value === keep) ? keep : 'vin';
  const to = $('to');
  to.replaceChildren(el('legend', 'To'));
  for (const c of vocabs) {
    const label = el('label', null, 'check');
    const box = el('input'); box.type = 'checkbox'; box.name = 'to'; box.value = c.name;
    label.append(box, ` ${c.name}`);
    to.append(label);
  }
}

$('file').addEventListener('change', async (e) => {
  const said = [];
  for (const file of e.target.files) {
    const t0 = performance.now();
    const text = await file.text();
    const res = JSON.parse(nitrovin.load(file.name, text));
    const t1 = performance.now();
    if (res.error) { said.push(`${file.name}: ${res.error}`); continue; }
    await idbPut({ key: res.key, name: file.name, text, stored: Date.now() });
    const t2 = performance.now();
    window.protoLastLoad = { ...res, load_ms: t1 - t0, idb_put_ms: t2 - t1 };
    said.push(`${file.name}: ${res.entries} entries, checked and indexed in ${(t1 - t0).toFixed(0)} ms, stored in ${(t2 - t1).toFixed(0)} ms.`);
  }
  $('load-status').textContent = said.join(' ');
  render();
});

$('tform').addEventListener('submit', async (e) => {
  e.preventDefault();
  const t0 = performance.now();
  const f = new FormData(e.target);
  const vin = f.get('vin').trim();
  const req = { to: f.getAll('to') };
  if (f.get('model_year').trim()) req.model_year = Number(f.get('model_year'));
  if (f.get('from') === 'vin') req.from = vin;
  else {
    const entry = {};
    if (f.get('make').trim()) entry.make = f.get('make').trim();
    if (f.get('model').trim()) entry.model = f.get('model').trim();
    req.from = { vocabulary: f.get('from'), entry };
    if (vin) req.vin = vin;
  }
  let dec = '';
  if (vin) {
    const q = req.model_year ? `&model_year=${req.model_year}` : '';
    const r = await fetch(`/v1/decode/${encodeURIComponent(vin)}?full=true${q}`);
    dec = await r.text();
    if (!r.ok) { show({ problem: JSON.parse(dec) }); return; }
  }
  const t1 = performance.now();
  const out = JSON.parse(nitrovin.translate(JSON.stringify(req), dec));
  const t2 = performance.now();
  window.protoLastTranslate = { decode_ms: t1 - t0, translate_ms: t2 - t1 };
  show(out, `Decoded in ${(t1 - t0).toFixed(1)} ms, translated in ${(t2 - t1).toFixed(1)} ms.`);
});

const ref = (r) => r.make ? `vpic make ${r.make}` : r.model ? `vpic model ${r.model}` : r.ext ? r.ext : `${r.spine}:${r.id}`;
const years = (v) => v.first == null && v.last == null ? 'any year' : v.first === v.last ? `${v.first}` : `${v.first ?? '…'}–${v.last ?? '…'}`;
const via = (v) => `via ${ref(v)}, ${years(v)}${v.origin ? `, ${v.origin}` : ''}${v.note ? `: ${v.note}` : ''}`;
const fields = (m) => Object.keys(m).sort().map((k) => `${k}=${m[k]}`).join(' ');

function show(t, took) {
  const r = $('result');
  r.replaceChildren();
  if (t.problem || t.error) { r.append(el('p', t.problem ? t.problem.detail : t.error, 'error')); return; }
  r.append(el('h2', 'Translation'));
  const from = t.from.vin ? `VIN ${t.from.vin}` : t.from.vocabulary ? `${t.from.vocabulary} ${fields(t.from.entry || {})}` : ref(t.from);
  r.append(el('p', `From ${from}: ${t.from.status}${t.from.note ? ` (${t.from.note})` : ''}`));
  r.append(el('h3', 'Spine'));
  const ul = el('ul');
  for (const s of t.spine) ul.append(el('li', `${ref(s)} ${s.name || ''}${s.under ? ` under ${ref(s.under)}` : ''} ${(s.via || []).map(via).join(' ')}`));
  if (!t.spine.length) ul.append(el('li', 'Nothing on the spine.'));
  r.append(ul);
  for (const to of t.to) {
    r.append(el('h3', to.vocabulary));
    const l = el('ul');
    for (const e of to.entries) l.append(el('li', `${fields(e.entry)} ${e.via.map(via).join(' ')}${e.unconfirmed ? ` unconfirmed ${JSON.stringify(e.unconfirmed)}` : ''}`));
    if (!to.entries.length) l.append(el('li', 'No entry.'));
    r.append(l);
  }
  const d = el('details'); d.append(el('summary', 'JSON'), el('pre', JSON.stringify(t, null, 2)));
  r.append(d);
  if (took) r.append(el('p', took, 'hint'));
}

boot().catch((err) => { $('status').textContent = `WebAssembly failed: ${err}`; $('status').className = 'error'; });
