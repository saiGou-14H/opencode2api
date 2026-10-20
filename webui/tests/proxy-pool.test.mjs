import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
const root = new URL('../', import.meta.url);
const source = readFileSync(new URL('js/pages/proxy-pool.js', root), 'utf8');
const html = readFileSync(new URL('index.html', root), 'utf8');
const shell = readFileSync(new URL('js/main.js', root), 'utf8');
const apiSource = readFileSync(new URL('js/api.js', root), 'utf8');
function fixture() {
  const elements = new Map(), calls = [];
  const el = (tag, props = {}, children = []) => ({
    tag, ...props, children, events: {}, disabled: false,
    append(...items) { this.children.push(...items); },
    replaceChildren(...items) { this.children = items; },
    addEventListener(name, fn) { this.events[name] = fn; },
  });
  const $ = (id) => { if (!elements.has(id)) elements.set(id, el('div')); return elements.get(id); };
  const context = vm.createContext({ $, el, t: (original) => original, onChange() {},
    api: async (path, options = {}) => {
      calls.push({ path, ...options });
      if (path === '/api/proxyclean/nodes') return { nodes: [{ id: 'opaque/one', endpoint: '192.0.2.1:1080', healthy: true, disabled: false, cooldown_until: '2026-01-01T00:00:00Z', last_probe_at: '2026-01-01T01:00:00Z', last_probe_ok: true, exit_ip: '203.0.113.8', exit_country: 'CN', exit_checked_at: '2026-01-01T01:00:01Z', exit_expires_at: '2026-01-01T01:05:01Z' }, { id: 'two', disabled: true, last_probe_ok: false }], total: 2, operation: '' };
      if (path === '/api/proxyclean') return { source_status: 'ready', candidate_count: 10, healthy_count: 2, cooldown_until: '2026-01-01T02:00:00Z', sources: [{ id: 'proxifly', status: 'ready', candidate_count: 10 }] };
      return path.endsWith('/disable') || path.endsWith('/enable') ? { ok: true } : { accepted: true, operation: path.endsWith('/probe') ? 'probe' : 'refresh' };
    },
  });
  vm.runInContext(source.replace(/^import .*;\n/gm, '').replace('export default {', 'globalThis.page = {'), context);
  context.page.init();
  return { context, $, calls };
}

test('proxy-pool navigation is registered and has matching page and bilingual shell labels', () => {
  assert.match(shell, /import proxyPool from "\.\/pages\/proxy-pool.js"/);
  assert.match(shell, /const PAGES = \[[^\]]*proxyPool/);
  assert.match(html, /data-page="proxy-pool"/);
  assert.match(html, /id="page-proxy-pool" class="page hidden"/);
  assert.equal(fixture().context.page.id, 'proxy-pool');
});

test('page show GETs status and nodes, renders source and safe node actions', async () => {
  const h = fixture();
  await h.context.page.show();
  assert.deepEqual(h.calls.map((c) => [c.path, c.method]), [['/api/proxyclean', 'GET'], ['/api/proxyclean/nodes', 'GET']]);
  assert.match(h.$('#pool-sources').children[0].text, /proxifly/);
  assert.equal(h.$('#pool-nodes').children.length, 2);
  assert.equal(h.$('#pool-nodes').children[0].children[0].text, 'opaque/one');
  const cells = h.$('#pool-nodes').children[0].children;
  assert.equal(cells[1].text, '192.0.2.1:1080');
  assert.equal(cells[3].text, '2026-01-01T00:00:00.000Z');
  assert.match(cells[4].text, /2026-01-01T01:00:00.000Z \/ 成功/);
  assert.match(h.$('#pool-status').textContent, /节点总数：2/);
  assert.match(h.$('#pool-status').textContent, /匿名通道受限直到：2026-01-01T02:00:00.000Z/);
});

for (const [button, path] of [['#pool-collect', '/api/proxyclean/refresh'], ['#pool-probe', '/api/proxyclean/probe']]) {
  test(`${button} POST uses helper and reports asynchronous acceptance, not completion`, async () => {
    const h = fixture();
    await h.$(button).events.click();
    assert.deepEqual(h.calls, [{ path, method: 'POST' }]);
    assert.match(h.$('#pool-message').textContent, /异步执行/);
    assert.match(h.$('#pool-message').textContent, /不代表.*完成/);
    assert.equal(h.$(button).disabled, true);
    await h.$('#pool-refresh-status').events.click();
    assert.equal(h.$(button).disabled, false);
  });
}

test('node disable/enable buttons use encoded IDs and POST with no secret payload', async () => {
  const h = fixture();
  await h.context.page.show();
  const rows = h.$('#pool-nodes').children;
  await rows[0].children[12].children[0].events.click();
  await rows[1].children[12].children[0].events.click();
  assert.deepEqual(h.calls.filter((call) => call.method === 'POST'), [
    { path: '/api/proxyclean/nodes/opaque%2Fone/disable', method: 'POST' },
    { path: '/api/proxyclean/nodes/two/enable', method: 'POST' },
  ]);
  assert.match(h.$('#pool-message').textContent, /读取运行状态确认/);
});

for (const [status, code, expected] of [[409, 'proxyclean_busy', /已有任务运行中/], [409, 'proxyclean_disabled', /已关闭/], [503, 'proxyclean_unavailable', /暂不可用/], [404, 'proxyclean_node_not_found', /节点已不存在/], [500, 'proxyclean_state_failed', /状态保存失败/], [500, 'unknown', /操作失败/]]) {
  test(`HTTP ${status}/${code} shows safe message and reenables action`, async () => {
    const h = fixture();
    h.context.api = async () => { throw Object.assign(new Error('SYNTHETIC_PRIVATE_DETAIL'), { status, code }); };
    await h.$('#pool-collect').events.click();
    assert.match(h.$('#pool-message').textContent, expected);
    assert.doesNotMatch(h.$('#pool-message').textContent, /SYNTHETIC_PRIVATE_DETAIL/);
    assert.equal(h.$('#pool-collect').disabled, false);
  });
}

test('GET failures clear stale nodes; reset hides previous session state', async () => {
  const h = fixture();
  await h.context.page.show();
  h.context.api = async () => { throw new Error('PRIVATE'); };
  await h.context.page.refresh();
  assert.match(h.$('#pool-message').textContent, /无法读取/);
  assert.equal(h.$('#pool-nodes').children.length, 1);
  assert.match(h.$('#pool-nodes').children[0].children[0].text, /暂无节点/);
  h.context.page.reset();
  assert.equal(h.$('#pool-message').textContent, '');
});

test('duplicate action clicks only send one POST while pending', async () => {
  const h = fixture();
  let finish, calls = 0;
  h.context.api = () => { calls++; return new Promise((resolve) => { finish = resolve; }); };
  const pending = h.$('#pool-probe').events.click();
  await h.$('#pool-probe').events.click();
  assert.equal(calls, 1);
  finish({ accepted: true, operation: 'probe' });
  await pending;
});

test('GET operation prevents duplicate collection/probe and idle refresh reenables both', async () => {
  const h = fixture();
  let operation = 'probe', posts = 0;
  h.context.api = async (path, options) => {
    if (options.method === 'POST') posts++;
    return path.endsWith('/nodes') ? { nodes: [], total: 0, operation } : { source_status: 'ready' };
  };
  await h.context.page.refresh();
  assert.match(h.$('#pool-status').textContent, /探活中/);
  assert.equal(h.$('#pool-collect').disabled, true);
  await h.$('#pool-collect').events.click();
  assert.equal(posts, 0);
  operation = '';
  await h.context.page.refresh();
  assert.equal(h.$('#pool-collect').disabled, false);
  assert.equal(h.$('#pool-probe').disabled, false);
});

test('stale accepted response after session reset cannot restore previous state', async () => {
  const h = fixture();
  let finish;
  h.context.api = () => new Promise((resolve) => { finish = resolve; });
  const pending = h.$('#pool-collect').events.click();
  h.context.page.reset();
  finish({ accepted: true, operation: 'refresh' });
  await pending;
  assert.equal(h.$('#pool-message').textContent, '');
  assert.equal(h.$('#pool-collect').disabled, false);
});

test('invalid POST acceptance is not presented as task started', async () => {
  const h = fixture();
  h.context.api = async () => ({});
  await h.$('#pool-collect').events.click();
  assert.match(h.$('#pool-message').textContent, /操作失败/);
});

test('pool source status accepts custom fingerprints without exposing URL metadata', async () => {
  const h = fixture();
  h.context.api = async (path) => path.endsWith('/nodes') ? { nodes: [], total: 0, operation: '' } : { sources: [
    { id: 'custom-0123456789abcdef', status: 'ready', candidate_count: 4, url: 'DO_NOT_DISPLAY' },
    { id: 'custom-not-hex', status: 'ready' }, { id: 'other', status: 'ready' },
  ] };
  await h.context.page.refresh();
  const rows = h.$('#pool-sources').children;
  assert.equal(rows.length, 1);
  assert.match(rows[0].text, /自定义来源 01234567/);
  assert.doesNotMatch(rows[0].text, /DO_NOT_DISPLAY/);
});

test('node table displays only four supported protocols; does not infer protocol from endpoint', async () => {
  const h = fixture();
  const protocols = ['http', 'https', 'socks5', 'socks5h', 'socks4', undefined];
  h.context.api = async (path) => path.endsWith('/nodes') ? { nodes: protocols.map((protocol, i) => ({ id: String(i), endpoint: 'https://example.org', protocol })), total: 6, operation: '' } : {};
  await h.context.page.refresh();
  const labels = h.$('#pool-nodes').children.map((row) => row.children[5].text);
  assert.deepEqual(labels, ['HTTP', 'HTTPS', 'SOCKS5', 'SOCKS5H', '—', '—']);
});

test('pool metrics distinguish restricted routing, show schedule and use status operation', async () => {
  const h = fixture();
  h.context.api = async (path) => path.endsWith('/nodes') ? {
    total: 10, operation: '', nodes: [
      { id: 'one', healthy: true, probe_latency_ms: 0, consecutive_failures: 0 },
      { id: 'two', probe_latency_ms: 12.5, consecutive_failures: 3 },
      { id: 'unknown' },
    ],
  } : {
    source_status: 'ready', healthy_count: 0, candidate_count: 10,
    total: 10, healthy: 5, cooling: 3, disabled: 2, operation: 'probe',
    last_probe_at: '2026-01-01T00:00:00Z', next_probe_at: '2026-01-01T00:05:00Z', next_refresh_at: '2026-01-01T01:00:00Z', cooldown_until: '2026-01-01T01:00:00Z',
  };
  await h.context.page.refresh();
  const text = h.$('#pool-status').textContent;
  assert.match(text, /可路由：0/);
  assert.match(text, /节点总数：10；传输健康：5；冷却中：3；已停用：2/);
  assert.match(text, /最近探活：2026-01-01T00:00:00.000Z/);
  assert.match(text, /下次探活：2026-01-01T00:05:00.000Z/);
  assert.match(text, /下次采集：2026-01-01T01:00:00.000Z/);
  assert.match(text, /探活中/);
  assert.equal(h.$('#pool-probe').disabled, true);
  const rows = h.$('#pool-nodes').children;
  assert.equal(rows[0].children[6].text, '0');
  assert.equal(rows[0].children[7].text, 0);
  assert.equal(rows[1].children[6].text, '12.5');
  assert.equal(rows[1].children[7].text, 3);
  assert.equal(rows[2].children[6].text, '—');
  assert.equal(rows[2].children[7].text, '—');
});

test('new controls have bilingual labels and explicit duplicate-charge warning', () => {
  const dictionarySource = readFileSync(new URL('js/i18n.js', root), 'utf8');
  const context = vm.createContext({});
  vm.runInContext(dictionarySource.replace(/^export /gm, '') + '\nglobalThis.en = DICTIONARY.en;', context);
  for (const [, key] of html.matchAll(/data-i18n="((?:pool\.|config\.retry\.sameAccount|config\.proxyclean\.auto)[^"]+)"/g)) assert.ok(context.en[key], key);
  assert.match(html, /代理 HTTPS 连通 ≠ 模型可用/);
  assert.match(context.en['pool.connectivityWarning'], /not proxy transport failures/);
  assert.match(html, /2xx.*重复计费/);
  assert.match(context.en['config.retry.sameAccountHelp'], /2xx.*duplicate charges/);
  assert.match(context.en['config.retry.sameAccountHelp'], /max_attempts hard limit/);
});

test('optional exit fields render safely and do not infer routing or country from endpoint', async () => {
  const h = fixture();
  await h.context.page.show();
  const first = h.$('#pool-nodes').children[0].children;
  assert.equal(first[8].text, '203.0.113.8');
  assert.equal(first[9].text, 'CN');
  assert.equal(first[10].text, '2026-01-01T01:00:01.000Z');
  assert.equal(first[11].text, '2026-01-01T01:05:01.000Z');
  for (const cell of h.$('#pool-nodes').children[1].children.slice(8, 12)) assert.equal(cell.text, '—');
  h.context.api = async (path) => path.endsWith('/nodes') ? { nodes: [
    { id: 'bad', exit_ip: '<img src=x>', exit_country: '<script>', exit_checked_at: 'bad', exit_expires_at: '<script>' },
    { id: 'typed', exit_ip: {}, exit_country: ['CN'], exit_checked_at: 123, exit_expires_at: {} },
    ...['HK', 'MO', 'TW', 'US'].map((country) => ({ id: country, healthy: true, endpoint: 'CN node', exit_country: country, exit_ip: '2001:db8::1' })),
  ], total: 6 } : {};
  await h.context.page.refresh();
  const rows = h.$('#pool-nodes').children;
  for (const row of rows.slice(0, 2)) for (const cell of row.children.slice(8, 12)) assert.equal(cell.text, '—');
  for (const [i, country] of ['HK', 'MO', 'TW', 'US'].entries()) {
    assert.equal(rows[i + 2].children[9].text, `${country}（CN 模式不允许）`);
    assert.equal(rows[i + 2].children[8].text, '2001:db8::1');
  }
  assert.match(html, /仅 CN 模式排除香港（HK）、澳门（MO）、台湾（TW）/);
  assert.match(html, /不是节点端点归属或模型就绪证明/);
  h.context.page.reset();
  assert.match(html, /CN 出口证据也必须在有效期内/);
  assert.equal(h.$('#pool-nodes').children[0].children[0].colspan, 13);
});

test('API helper adds CSRF to POST, accepts 202 and preserves 409 status', async () => {
  const requests = [];
  let status = 202;
  const context = vm.createContext({ t: (original) => original, store: {}, fetch: async (path, options) => {
    requests.push({ path, ...options });
    return { status, ok: status < 400, json: async () => ({ accepted: true, error: { code: 'proxyclean_disabled', message: 'PRIVATE' } }) };
  } });
  vm.runInContext(apiSource.replace(/^import .*;\n/gm, '').replace(/^export /gm, ''), context);
  context.setCsrf('synthetic-csrf');
  await context.api('/api/proxyclean/refresh', { method: 'POST' });
  assert.equal(requests[0].headers['X-CSRF-Token'], 'synthetic-csrf');
  assert.equal(requests[0].body, undefined);
  status = 409;
  await assert.rejects(() => context.api('/api/proxyclean/probe', { method: 'POST' }), (error) => error.status === 409 && error.code === 'proxyclean_disabled');
});
