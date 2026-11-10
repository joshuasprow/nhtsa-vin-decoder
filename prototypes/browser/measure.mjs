// PROTOTYPE (#147): drives headless Chromium against a running
// prototypes/browser server (measure.sh starts it) and writes
// results/measurements.json plus screenshots in results/. Playwright is
// resolved from $PLAYWRIGHT_PKG (a package.json whose node_modules has it;
// default: the pw skill's runtime).
import { createRequire } from 'node:module';
import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(process.env.PLAYWRIGHT_PKG ?? `${process.env.HOME}/.agents/skills/playwright/package.json`);
const { chromium } = require('playwright');

const here = dirname(fileURLToPath(import.meta.url));
const out = join(here, 'results');
mkdirSync(out, { recursive: true });
const BASE = process.env.BASE ?? 'http://127.0.0.1:8147';
const EPA = join(here, 'data', 'epa-vehicles.jsonl');
const EBAY = join(here, 'data', 'ebay-synthetic.jsonl');
const VIN = '1HGCM82633A004352';
const NAME_Q = '/ui/translate?from=epa-vehicles&make=Honda&model=Accord&model_year=2003&to=epa-vehicles&to=ebay-synthetic';
const VIN_Q = `/ui/translate?from=vin&vin=${VIN}&to=epa-vehicles&to=ebay-synthetic`;
const N = Number(process.env.RUNS ?? 25);
const results = { base: BASE, runs: N, userAgent: '' };
const checks = [];
const check = (name, ok, detail = '') => { checks.push({ name, ok: !!ok, detail }); console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? ` (${detail})` : ''}`); };
const stats = (xs) => {
  const s = [...xs].sort((a, b) => a - b);
  const q = (p) => s[Math.min(s.length - 1, Math.floor(p * s.length))];
  return { n: s.length, median: +q(0.5).toFixed(2), p90: +q(0.9).toFixed(2), min: +s[0].toFixed(2), max: +s[s.length - 1].toFixed(2) };
};
const shot = (page, name) => page.screenshot({ path: join(out, `${name}.png`), fullPage: true });

const browser = await chromium.launch();
results.userAgent = await (async () => { const p = await browser.newPage(); const ua = await p.evaluate(() => navigator.userAgent); await p.close(); return ua; })();

// timed fragment fetches from the page, through whatever controls it
async function fragmentRuns(page, url, n = N) {
  return page.evaluate(async ({ url, n }) => {
    const rt = [], wasm = [];
    for (let i = 0; i < n; i++) {
      const t0 = performance.now();
      const r = await fetch(url, { headers: { 'HX-Request': 'true' } });
      await r.text();
      rt.push(performance.now() - t0);
      const st = r.headers.get('Server-Timing');
      if (st) wasm.push(Number(/dur=([\d.]+)/.exec(st)[1]));
    }
    return { rt, wasm };
  }, { url, n });
}

// ---- htmx + Service Worker variant, JavaScript on ----
{
  const context = await browser.newContext({ viewport: { width: 1100, height: 900 } });
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const r = (results.htmx = {});

  // decode form with htmx
  await page.goto(`${BASE}/`);
  let navs = 0;
  page.on('framenavigated', (f) => { if (f === page.mainFrame()) navs++; });
  await page.fill('#vin', VIN);
  await page.click('form[hx-get="/ui/decode"] button');
  await page.waitForSelector('#result h2');
  check('JS on: / decodes through htmx without a page load', navs === 0 || page.url().includes('/ui/decode?vin='), `url ${page.url()}`);
  await shot(page, 'decode-js-on');

  // first visit: install the worker, boot it, re-render /vocab from it
  await page.goto(`${BASE}/vocab`);
  await page.waitForSelector('html[data-sw=ready]', { timeout: 60000 });
  r.first_visit_sw_ready_ms = await page.evaluate(() => window.protoSwReadyMs);
  r.first_boot = await page.evaluate(async () => (await fetch('/ui/proto-timings')).json());
  check('JS on: /vocab re-rendered by the Service Worker', (await page.getAttribute('#vocab-app', 'data-where')) === 'browser');
  await shot(page, 'vocab-js-on-empty');

  const load = async (path, name) => {
    await page.evaluate(() => { window.protoLastLoad = null; });
    const t0 = Date.now();
    await page.setInputFiles('#vocab-file', path);
    await page.waitForFunction(() => window.protoLastLoad, null, { timeout: 300000 });
    await page.waitForSelector(`#files td:text-is("${name}")`, { timeout: 300000 });
    return { ...(await page.evaluate(() => window.protoLastLoad)), wall_ms: Date.now() - t0 };
  };
  r.load_epa = await load(EPA, 'epa-vehicles');
  const cdp = await context.newCDPSession(page);
  await cdp.send('ServiceWorker.enable');
  const cold = async (label) => {
    const runs = [];
    for (let i = 0; i < 5; i++) {
      await cdp.send('ServiceWorker.stopAllWorkers');
      await page.waitForTimeout(400);
      runs.push(await page.evaluate(async (url) => {
        const t0 = performance.now();
        const res = await fetch(url, { headers: { 'HX-Request': 'true' } });
        await res.text();
        const first = performance.now() - t0;
        return { first_fragment_ms: first, ...(await (await fetch('/ui/proto-timings')).json()) };
      }, NAME_Q.replace('&to=ebay-synthetic', '')));
    }
    return { runs, first_fragment_ms: stats(runs.map((x) => x.first_fragment_ms)), boot_ms: stats(runs.map((x) => x.boot_ms)), wasm_ms: stats(runs.map((x) => x.wasm_ms)), spine_ms: stats(runs.map((x) => x.spine_fetch_ms + x.spine_index_ms)), idb_read_ms: stats(runs.map((x) => x.idb_read_ms)), reindex_ms: stats(runs.map((x) => x.reindex_ms)), label };
  };
  r.cold_epa = await cold('EPA only in IndexedDB');
  r.load_ebay = await load(EBAY, 'ebay-synthetic');
  r.cold_epa_ebay = await cold('EPA + synthetic eBay in IndexedDB');
  const name = await fragmentRuns(page, NAME_Q);
  const vin = await fragmentRuns(page, VIN_Q);
  r.fragment_name = { rt: stats(name.rt), wasm: stats(name.wasm) };
  r.fragment_vin = { rt: stats(vin.rt), wasm: stats(vin.wasm) };

  // htmx end to end: submit the form, wait for the swap to settle
  await page.goto(`${BASE}/vocab`);
  await page.waitForSelector('html[data-sw=ready]');
  await page.selectOption('#from', 'vin');
  await page.fill('#tvin', VIN);
  await page.check('input[name=to][value=epa-vehicles]');
  await page.check('input[name=to][value=ebay-synthetic]');
  r.htmx_submit_ms = stats(await page.evaluate(async (n) => {
    const form = document.querySelector('form[hx-get="/ui/translate"]');
    const xs = [];
    for (let i = 0; i < n; i++) {
      const t0 = performance.now();
      await new Promise((res) => { document.body.addEventListener('htmx:afterSettle', res, { once: true }); htmx.trigger(form, 'submit'); });
      xs.push(performance.now() - t0);
    }
    return xs;
  }, 10));
  await page.waitForSelector('#result h2');
  check('JS on: /vocab translates a VIN through browser-held files', /epa-vehicles/.test(await page.textContent('#result')) && /Accord/.test(await page.textContent('#result')));
  await shot(page, 'vocab-js-on-translate-vin');
  await page.selectOption('#from', 'epa-vehicles');
  await page.fill('#tvin', '');
  await page.fill('#make', 'Honda');
  await page.fill('#model', 'Accord');
  await page.fill('#tyear', '2003');
  await page.click('form[hx-get="/ui/translate"] button');
  await page.waitForFunction(() => /From epa-vehicles/.test(document.querySelector('#result')?.textContent ?? ''));
  await shot(page, 'vocab-js-on-translate-name');
  await page.click('#files tr:has(td:text-is("ebay-synthetic")) button');
  await page.waitForFunction(() => ![...document.querySelectorAll('#files td')].some((td) => td.textContent === 'ebay-synthetic'));
  check('JS on: Remove drops a stored file', true);
  r.page_errors = errors;
  check('JS on: no page errors', errors.length === 0, errors.join('; '));
  await context.close();
}

// ---- server-rendered /ui (what JS off gets), same fragments ----
{
  const context = await browser.newContext({ serviceWorkers: 'block' });
  const page = await context.newPage();
  await page.goto(`${BASE}/vocab`);
  const name = await fragmentRuns(page, NAME_Q.replace('&to=ebay-synthetic', ''));
  const vin = await fragmentRuns(page, VIN_Q.replace('&to=ebay-synthetic', ''));
  results.server = { fragment_name: { rt: stats(name.rt) }, fragment_vin: { rt: stats(vin.rt) }, note: 'the server has epa-vehicles loaded with -vocab; ebay-synthetic is browser-only' };
  await context.close();
}

// ---- JavaScript off ----
{
  const context = await browser.newContext({ javaScriptEnabled: false, viewport: { width: 1100, height: 900 } });
  const page = await context.newPage();
  await page.goto(`${BASE}/`);
  await page.fill('#vin', VIN);
  await Promise.all([page.waitForNavigation(), page.click('form[action="/ui/decode"] button')]);
  check('JS off: / decode form submits to /ui/decode as a full page', page.url().includes('/ui/decode?vin=') && !!(await page.$('#result h2')), page.url());
  await shot(page, 'decode-js-off');

  await page.goto(`${BASE}/vocab`);
  check('JS off: /vocab says browser-held vocabularies need JavaScript', /needs JavaScript/.test(await page.textContent('#files')));
  check('JS off: no file input is offered (nothing can be uploaded)', !(await page.$('input[type=file]')));
  await shot(page, 'vocab-js-off');
  await page.selectOption('#from', 'epa-vehicles');
  await page.fill('#tvin', VIN);
  await page.fill('#make', 'Honda');
  await page.fill('#model', 'Accord');
  await page.check('input[name=to][value=epa-vehicles]');
  await page.check('input[name=to][value=yard]');
  await Promise.all([page.waitForNavigation(), page.click('form[action="/ui/translate"] button')]);
  check('JS off: translate form answers as a full page from the server', page.url().includes('/ui/translate?') && /Translation/.test(await page.textContent('#result')), page.url());
  await shot(page, 'vocab-js-off-translate');
  await Promise.all([page.waitForNavigation(), page.click('a[href="/ui/vocabularies?of=epa-vehicles"]')]);
  check('JS off: catalog link lists entries as a full page', /Acura/.test(await page.textContent('#listing')));
  await Promise.all([page.waitForNavigation(), page.click('#listing a:text("Next page")')]);
  check('JS off: next-page link works', page.url().includes('after='));
  await page.goto(`${BASE}/vocab-js`);
  check('JS off: /vocab-js says it needs JavaScript', /needs JavaScript/.test(await page.textContent('main')));
  await shot(page, 'vocab-plain-js-off');
  await context.close();
}

// ---- plain-JS variant, Go and TinyGo modules ----
for (const core of ['go', 'tinygo']) {
  const context = await browser.newContext({ viewport: { width: 1100, height: 900 } });
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  const r = (results[`plain_${core}`] = {});
  await page.goto(`${BASE}/vocab-js?core=${core}`);
  try {
    await page.waitForSelector('html[data-ready=yes]', { timeout: 60000 });
  } catch {
    r.failed = (await page.textContent('#status')) + ' ' + errors.join('; ');
    check(`plain-JS ${core}: boots`, false, r.failed);
    await shot(page, `vocab-plain-${core}-failed`);
    await context.close();
    continue;
  }
  r.first_boot = await page.evaluate(() => window.protoTimings);
  const load = async (path) => {
    await page.evaluate(() => { window.protoLastLoad = null; });
    const t0 = Date.now();
    await page.setInputFiles('#file', path);
    await page.waitForFunction(() => window.protoLastLoad, null, { timeout: 300000 });
    return { ...(await page.evaluate(() => window.protoLastLoad)), wall_ms: Date.now() - t0 };
  };
  r.load_epa = await load(EPA);
  r.load_ebay = await load(EBAY);
  const warm = await page.evaluate(async ({ vin, n }) => {
    const nameReq = JSON.stringify({ from: { vocabulary: 'epa-vehicles', entry: { make: 'Honda', model: 'Accord' } }, model_year: 2003, to: ['epa-vehicles', 'ebay-synthetic'] });
    const vinReq = JSON.stringify({ from: vin, to: ['epa-vehicles', 'ebay-synthetic'] });
    const name = [], vinRt = [];
    for (let i = 0; i < n; i++) {
      let t0 = performance.now();
      JSON.parse(nitrovin.translate(nameReq, ''));
      name.push(performance.now() - t0);
      t0 = performance.now();
      const d = await (await fetch(`/v1/decode/${vin}?full=true`)).text();
      JSON.parse(nitrovin.translate(vinReq, d));
      vinRt.push(performance.now() - t0);
    }
    return { name, vinRt, sample: JSON.parse(nitrovin.translate(vinReq, await (await fetch(`/v1/decode/${vin}?full=true`)).text())) };
  }, { vin: VIN, n: N });
  r.translate_name_ms = stats(warm.name);
  r.translate_vin_with_decode_ms = stats(warm.vinRt);
  check(`plain-JS ${core}: VIN translation reaches EPA entries`, (warm.sample.to?.[0]?.entries?.length ?? 0) > 0, JSON.stringify(warm.sample).slice(0, 200));
  await page.fill('#vin', VIN);
  await page.check('input[name=to][value=epa-vehicles]');
  await page.check('input[name=to][value=ebay-synthetic]');
  await page.click('#tform button');
  await page.waitForSelector('#result h2');
  await shot(page, `vocab-plain-${core}-js-on`);
  const reloads = [];
  for (let i = 0; i < 3; i++) {
    await page.reload();
    await page.waitForSelector('html[data-ready=yes]', { timeout: 120000 });
    reloads.push(await page.evaluate(() => window.protoTimings));
  }
  r.cold_page_load = { runs: reloads, boot_ms: stats(reloads.map((x) => x.boot_ms)), wasm_ms: stats(reloads.map((x) => x.wasm_ms)), reindex_ms: stats(reloads.map((x) => x.reindex_ms)) };
  r.page_errors = errors;
  check(`plain-JS ${core}: no page errors`, errors.length === 0, errors.join('; '));
  await context.close();
}

await browser.close();
results.checks = checks;
writeFileSync(join(out, 'measurements.json'), JSON.stringify(results, null, 2) + '\n');
console.log(`wrote ${join(out, 'measurements.json')}`);
process.exitCode = checks.every((c) => c.ok) ? 0 : 1;
