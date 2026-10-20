import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';

const root = new URL('../', import.meta.url);
const html = readFileSync(new URL('index.html', root), 'utf8');
const source = readFileSync(new URL('js/pages/config.js', root), 'utf8');
const translations = readFileSync(new URL('js/i18n.js', root), 'utf8');

// Execute the actual page functions with synthetic DOM and API; never load live config.
function harness(retry = { max_attempts: 3, timeout_seconds: 60 }, proxyclean, status = {}) {
  const nodes = new Map();
  const $ = (selector) => {
    if (!nodes.has(selector)) {
      let value = '';
      nodes.set(selector, {
        get value() { return value; },
        set value(next) { value = String(next); },
        children: [], checked: false,
        replaceChildren() {}, append() {},
        classList: { add() {}, remove() {} },
      });
    }
    return nodes.get(selector);
  };
  const config = {
    retry, proxyclean, webui: {}, upstream: {}, models: {}, performance: {}, logging: {},
  };
  const calls = [], notices = [];
  const context = vm.createContext({
    $, URL, $$: () => [], t: (original) => original,
    store: { get: () => config, set() {} },
    api: async (url, options) => {
      calls.push({ url, method: options.method, body: options.body ? JSON.parse(options.body) : undefined });
      if (url === '/api/proxyclean') {
        if (status instanceof Error) throw status;
        return status;
      }
      return { config, result: { restart_required: false } };
    },
    loadDebugModels: async () => {},
    toast: (...args) => notices.push(args),
  });
  vm.runInContext(source.replace(/^import .*;\n/gm, '').replace('export default {', 'globalThis.page = {'), context);
  context.fillConfig(config);
  return { $, context, calls, notices };
}

for (const [initial, maximum] of [[0, 0], [200, 0], [200, 800], [0, 800], [86400000, 86400000]]) {
  test(`fill/save roundtrip backoff ${initial}/${maximum}`, async () => {
    const retry = { max_attempts: 7, timeout_seconds: 120, backoff_initial_ms: initial, backoff_max_ms: maximum };
    const h = harness(retry);
    assert.equal(h.$('#c-backoff-initial').value, String(initial));
    assert.equal(h.$('#c-backoff-max').value, String(maximum));
    await h.context.save({ preventDefault() {} });
    assert.equal(h.calls.length, 1);
    assert.equal(h.calls[0].url, '/api/config');
    assert.deepEqual(h.calls[0].body.retry, { ...retry, same_account_retries: 0, same_account_status_codes: '401,403,429' });
  });
}

test('old config defaults missing backoff fields to zero', async () => {
  const h = harness({ max_attempts: 3, timeout_seconds: 60 });
  await h.context.save({ preventDefault() {} });
  assert.deepEqual(h.calls[0].body.retry, { max_attempts: 3, timeout_seconds: 60, backoff_initial_ms: 0, backoff_max_ms: 0, same_account_retries: 0, same_account_status_codes: '401,403,429' });
});

for (const [initial, maximum] of [['-1', '0'], ['1.5', '0'], ['86400001', '0'], ['0', '86400001'], ['0', '-1'], ['0', '1.2'], ['', '0'], ['0', ''], ['NaN', '0'], ['Infinity', '0'], ['200', '100']]) {
  test(`invalid backoff ${initial}/${maximum} prevents API write`, async () => {
    const h = harness({ max_attempts: 3, timeout_seconds: 60 });
    h.$('#c-backoff-initial').value = initial;
    h.$('#c-backoff-max').value = maximum;
    await h.context.save({ preventDefault() {} });
    assert.equal(h.calls.length, 0);
    assert.equal(h.notices.length, 1);
    assert.equal(h.notices[0][1], true);
  });
}

test('HTML backoff inputs have labels, integer bounds and accessible help', () => {
  for (const id of ['c-backoff-initial', 'c-backoff-max']) {
    const input = html.match(new RegExp(`<input id="${id}"[^>]+>`))?.[0];
    assert.ok(input);
    for (const attr of ['type="number"', 'min="0"', 'max="86400000"', 'step="1"', 'value="0"', 'required', 'aria-describedby="retry-backoff-help"']) assert.ok(input.includes(attr), attr);
    assert.ok(html.includes(`label for="${id}"`));
  }
});

test('Chinese fallback and English copy cover retry contract and validation', () => {
  const context = vm.createContext({});
  vm.runInContext(translations.replace(/^export /gm, '') + '\nglobalThis.dictionary = DICTIONARY;', context);
  const en = context.dictionary.en;
  const keys = ['backoffInitial', 'backoffMax', 'backoffHelp', 'policy'];
  for (const name of keys) {
    const key = `config.retry.${name}`;
    assert.ok(en[key]);
    assert.match(html, new RegExp(`data-i18n="${key}"[^>]*>[^<]*[\\u4e00-\\u9fff]`));
  }
  for (const key of ['invalidRange', 'invalidMax']) assert.ok(en[`config.retry.${key}`]);
  for (const token of ['v1/v2', 'stale-reasoning', '401/403', '429', 'Retry-After']) {
    assert.ok(en['config.retry.policy'].includes(token));
    assert.ok(html.includes(token));
  }
  assert.match(en['config.upstream.attempts'], /including the first/);
  assert.match(en['config.upstream.timeout'], /Total request deadline/);
  assert.doesNotMatch(html + translations, /每个 Key Tier 最大尝试次数|Max attempts per Key Tier|anonymous will use every available proxy/);
});

const pcDefaults = {
  min_healthy_nodes: 0, refill_interval_seconds: 300,
  custom_sources: [],
  auto_switch_proxy: true, auto_collect: true,
  sources: ['proxyclean'],
  enabled: false, scope: 'anonymous-only',
  source_url: 'https://raw.githubusercontent.com/HankNovic/ProxyClean/refs/heads/main/SOCKS5.txt',
  refresh_seconds: 3600, ttl_seconds: 7200, probe_interval_seconds: 300,
  probe_timeout_seconds: 8, probe_concurrency: 8, max_candidates: 100,
  max_nodes: 20, cooldown_seconds: 60, allow_direct_fallback: false, required_exit_country: '',
};
for (const ids of [['proxifly-cn'], ['xyzs-cn'], ['monosans-cn'], ['proxifly-cn', 'xyzs-cn', 'monosans-cn']]) {
  test(`CN-only checked sources ${ids.join(',')} save without global or custom sources`, async () => {
    const h = harness(undefined, { ...pcDefaults, sources: ids, required_exit_country: 'CN', enabled: true });
    for (const id of ids) {
      assert.match(html, new RegExp(`id="c-pc-source-${id}"`));
      assert.equal(h.$('#c-pc-source-' + id).checked, true);
    }
    await h.context.save({ preventDefault() {} });
    assert.equal(h.notices.some(([message]) => /请至少选择/.test(message)), false);
    assert.equal(h.calls.length, 1);
    assert.deepEqual(h.calls[0].body.proxyclean.sources, ids);
    assert.deepEqual(h.calls[0].body.proxyclean.custom_sources, []);
  });
}

for (const country of [undefined, '', 'CN']) {
  test(`exit country roundtrip ${country ?? 'legacy missing'} preserves unrestricted default`, async () => {
    const h = harness(undefined, { ...pcDefaults, required_exit_country: country });
    assert.equal(h.$('#c-pc-exit-country').value, country ?? '');
    assert.equal(h.$('#c-pc-fallback').disabled, country === 'CN');
    await h.context.save({ preventDefault() {} });
    assert.equal(h.calls[0].body.proxyclean.required_exit_country, country ?? '');
    assert.equal(h.calls[0].body.proxyclean.allow_direct_fallback, false);
  });
}

test('selecting CN clears and disables fallback; unrestricted does not silently restore it', () => {
  const h = harness(undefined, { ...pcDefaults, allow_direct_fallback: true });
  h.$('#c-pc-exit-country').value = 'CN';
  h.context.syncExitCountry(true);
  assert.equal(h.$('#c-pc-fallback').checked, false);
  assert.equal(h.$('#c-pc-fallback').disabled, true);
  h.$('#c-pc-exit-country').value = '';
  h.context.syncExitCountry(true);
  assert.equal(h.$('#c-pc-fallback').checked, false);
  assert.equal(h.$('#c-pc-fallback').disabled, false);
  assert.match(source, /\$\("#c-pc-exit-country"\)\.addEventListener\("change", \(\) => syncExitCountry\(true\)\)/);
});

for (const country of ['CN', 'HK', 'MO', 'TW', 'US', 'cn', 'invalid']) {
  test(`incompatible fallback or unsupported country ${country} blocks save`, async () => {
    const h = harness(undefined, { ...pcDefaults, required_exit_country: country, allow_direct_fallback: true });
    await h.context.save({ preventDefault() {} });
    assert.equal(h.calls.length, 0);
    assert.equal(h.notices[0][1], true);
  });
}

test('exit filter has accessible bilingual labels and clear probe/routing limitations', () => {
  const context = vm.createContext({});
  vm.runInContext(translations.replace(/^export /gm, '') + '\nglobalThis.en = DICTIONARY.en;', context);
  assert.match(html, /label for="c-pc-exit-country"/);
  assert.match(html, /select id="c-pc-exit-country" aria-describedby="proxyclean-exit-help"/);
  assert.match(html, /option value="" data-i18n="config.proxyclean.exitAny"/);
  assert.match(html, /option value="CN" data-i18n="config.proxyclean.exitCN"/);
  for (const key of ['exitCountry', 'exitAny', 'exitCN', 'exitHelp', 'invalidExitCountry', 'exitFallbackInvalid']) assert.ok(context.en[`config.proxyclean.${key}`]);
  assert.match(context.en['config.proxyclean.exitHelp'], /unknown and non-CN exits cannot route/);
  assert.match(context.en['config.proxyclean.exitHelp'], /every destination uses the same exit/);
  assert.match(html, /未知及非 CN 出口不可路由/);
  assert.match(html, /不能保证访问每个目标都使用同一出口/);
});

const sourceIds = ['proxyclean', 'proxifly', 'thespeedx', 'roosterkid', 'hookzof', 'gfpcom', 'dpangestuw', 'proxifly-cn', 'xyzs-cn', 'monosans-cn'];
for (const sources of [undefined, [], ['proxyclean'], sourceIds.slice(1), sourceIds]) {
  test(`ProxyClean sources roundtrip ${sources?.join(',') || 'legacy default'}`, async () => {
    const expected = sources?.length ? sources : ['proxyclean'];
    const h = harness(undefined, { ...pcDefaults, sources });
    for (const id of sourceIds) {
      assert.equal(h.$(`#c-pc-source-${id}`).checked, expected.includes(id));
      assert.match(html, new RegExp(`id="c-pc-source-${id}" type="checkbox"`));
    }
    await h.context.save({ preventDefault() {} });
    assert.deepEqual(h.calls[0].body.proxyclean.sources, expected);
    assert.equal(h.calls[0].body.proxyclean.enabled, false);
  });
}

test('ProxyClean no selected sources blocks PUT; selecting six direct sources writes unique stable IDs', async () => {
  const h = harness();
  for (const id of sourceIds) h.$(`#c-pc-source-${id}`).checked = false;
  await h.context.save({ preventDefault() {} });
  assert.equal(h.calls.length, 0);
  assert.match(h.notices[0][0], /至少选择一个/);
  for (const id of sourceIds.slice(1)) h.$(`#c-pc-source-${id}`).checked = true;
  await h.context.save({ preventDefault() {} });
  assert.deepEqual(h.calls[0].body.proxyclean.sources, sourceIds.slice(1));
});

test('ProxyClean duplicate selections normalize to unique IDs and unknown IDs are never sent', async () => {
  const h = harness(undefined, { ...pcDefaults, sources: ['proxifly', 'proxifly', 'unknown'] });
  await h.context.save({ preventDefault() {} });
  assert.deepEqual(h.calls[0].body.proxyclean.sources, ['proxifly']);
});

test('ProxyClean per-source status keeps successful sources visible when others fail', async () => {
  const h = harness(undefined, undefined, { enabled: true, source_status: 'degraded', sources: [
    { id: 'proxifly', status: 'error', candidate_count: 0, last_error: 'source_unavailable' },
    { id: 'thespeedx', status: 'ready', candidate_count: 25, last_fetch_at: '2026-01-02T03:04:05Z' },
    { id: 'roosterkid', status: 'pending', candidate_count: 0 },
    { id: 'hookzof', status: 'empty', candidate_count: 0 },
    { id: 'gfpcom', status: 'error', candidate_count: 0, last_error: '<script>bad</script>' },
    { id: '<script>', status: 'error' }, null,
  ] });
  await h.context.refreshProxyCleanStatus();
  const text = h.$('#proxyclean-status').textContent;
  for (const token of ['proxifly：失败', 'source_unavailable', 'thespeedx：可用；候选：25', '2026-01-02T03:04:05.000Z', 'roosterkid：等待采集', 'hookzof：空源', 'gfpcom：失败']) assert.ok(text.includes(token), token);
  assert.doesNotMatch(text, /<script>|bad/);
  assert.equal(h.calls[0].method, 'GET');
  assert.equal(h.$('#c-pc-source-proxyclean').checked, true);
  assert.equal(h.$('#c-pc-source-proxifly').checked, false);
});

test('ProxyClean automatic switches preserve explicit false through fill/save', async () => {
  const h = harness(undefined, { ...pcDefaults, auto_switch_proxy: false, auto_collect: false });
  assert.equal(h.$('#c-pc-auto-switch').checked, false);
  assert.equal(h.$('#c-pc-auto-collect').checked, false);
  await h.context.save({ preventDefault() {} });
  assert.equal(h.calls[0].body.proxyclean.auto_switch_proxy, false);
  assert.equal(h.calls[0].body.proxyclean.auto_collect, false);
});

for (const [raw, expected] of [['', '401,403,429'], ['   ', '401,403,429'], [' 401,403, 429,401 ', '401,403,429'], ['100,200,599', '100,200,599'], ['0401,401', '401']]) {
  test(`same-account status code normalization: ${raw}`, async () => {
    const h = harness({ max_attempts: 3, timeout_seconds: 60, same_account_retries: 100, same_account_status_codes: raw });
    await h.context.save({ preventDefault() {} });
    assert.equal(h.calls[0].body.retry.same_account_retries, 100);
    assert.equal(h.calls[0].body.retry.same_account_status_codes, expected);
  });
}
for (const raw of ['401,', ',401', '401,,403', '401, ,403', '99', '600', '-401', '4e2', '0x191', '401.0', 'abc', '401，403']) {
  test(`same-account rejects invalid codes: ${raw}`, async () => {
    const h = harness();
    h.$('#c-same-account-status-codes').value = raw;
    await h.context.save({ preventDefault() {} });
    assert.equal(h.calls.length, 0);
    assert.equal(h.notices[0][1], true);
  });
}
for (const raw of ['', '-1', '101', '1.5', 'NaN']) {
  test(`same-account retry count rejects ${raw}`, async () => {
    const h = harness();
    h.$('#c-same-account-retries').value = raw;
    await h.context.save({ preventDefault() {} });
    assert.equal(h.calls.length, 0);
  });
}

for (const sources of [[], undefined]) {
  test(`custom-only object source preserves no built-ins (${sources ? 'empty' : 'omitted'})`, async () => {
    const custom_sources = [{ url: 'https://example.org/list', default_protocol: 'http' }];
    const h = harness(undefined, { ...pcDefaults, sources, custom_sources });
    for (const id of sourceIds) assert.equal(h.$(`#c-pc-source-${id}`).checked, false);
    await h.context.save({ preventDefault() {} });
    assert.deepEqual(h.calls[0].body.proxyclean.sources, []);
    assert.deepEqual(h.calls[0].body.proxyclean.custom_sources, custom_sources);
  });
}

test('custom-only legacy strings default SOCKS5; deleting all sources blocks save', async () => {
  const h = harness(undefined, { ...pcDefaults, sources: [], custom_sources: ['https://example.org/list'] });
  await h.context.save({ preventDefault() {} });
  assert.deepEqual(h.calls[0].body.proxyclean.sources, []);
  assert.deepEqual(h.calls[0].body.proxyclean.custom_sources, [{ url: 'https://example.org/list', default_protocol: 'socks5' }]);
  h.$('#c-pc-custom-sources').value = '';
  await h.context.save({ preventDefault() {} });
  assert.equal(h.calls.length, 1);
  assert.match(h.notices.at(-1)[0], /内置来源.*有效自定义来源/);
});

test('invalid custom-only source never bypasses URL validation or silently enables built-in', async () => {
  const h = harness(undefined, { ...pcDefaults, sources: [], custom_sources: ['http://example.org/list'] });
  await h.context.save({ preventDefault() {} });
  assert.equal(h.calls.length, 0);
  assert.equal(h.$('#c-pc-source-proxyclean').checked, false);
});

test('custom source URLs roundtrip unchanged; blank lines and duplicates are removed', async () => {
  const urls = ['https://example.org/list.txt', 'https://example.net:443/SOCKS5.txt'];
  const h = harness(undefined, { ...pcDefaults, custom_sources: urls });
  assert.equal(h.$('#c-pc-custom-sources').value, urls.join('\n'));
  h.$('#c-pc-custom-sources').value += `\n\n ${urls[0]} \n`;
  await h.context.save({ preventDefault() {} });
  assert.deepEqual(h.calls[0].body.proxyclean.custom_sources, urls.map((url) => ({ url, default_protocol: 'socks5' })));
});

test('custom object sources preserve four distinct node protocols despite HTTPS URL and selector changes', async () => {
  const entries = ['http', 'https', 'socks5', 'socks5h'].map((default_protocol) => ({ url: `https://example.org/${default_protocol}.txt`, default_protocol }));
  const h = harness(undefined, { ...pcDefaults, custom_sources: entries });
  h.$('#c-pc-custom-protocol').value = 'http';
  h.context.renderCustomProtocols();
  assert.match(h.$('#c-pc-custom-protocol-preview').textContent, /来源 4：SOCKS5H/);
  await h.context.save({ preventDefault() {} });
  assert.deepEqual(h.calls[0].body.proxyclean.custom_sources, entries);
});

test('new URLs use chosen node protocol without altering existing sources; explicit apply-all changes all', async () => {
  const first = { url: 'https://example.org/first.txt', default_protocol: 'socks5h' };
  const h = harness(undefined, { ...pcDefaults, custom_sources: [first] });
  h.$('#c-pc-custom-sources').value += '\nhttps://example.org/new.txt';
  h.$('#c-pc-custom-protocol').value = 'http';
  await h.context.save({ preventDefault() {} });
  assert.deepEqual(h.calls[0].body.proxyclean.custom_sources, [first, { url: 'https://example.org/new.txt', default_protocol: 'http' }]);
  h.$('#c-pc-custom-protocol').value = 'https';
  h.context.applyCustomProtocolToAll();
  await h.context.save({ preventDefault() {} });
  assert.deepEqual(h.calls[1].body.proxyclean.custom_sources.map((entry) => entry.default_protocol), ['https', 'https']);
});

test('custom source URL reorder and deletion preserve association, not line-position protocol', async () => {
  const entries = [{ url: 'https://example.org/a', default_protocol: 'http' }, { url: 'https://example.org/b', default_protocol: 'socks5h' }];
  const h = harness(undefined, { ...pcDefaults, custom_sources: entries });
  h.$('#c-pc-custom-sources').value = entries[1].url + '\n' + entries[0].url;
  await h.context.save({ preventDefault() {} });
  assert.deepEqual(h.calls[0].body.proxyclean.custom_sources, [...entries].reverse());
  h.$('#c-pc-custom-sources').value = entries[1].url;
  await h.context.save({ preventDefault() {} });
  assert.deepEqual(h.calls[1].body.proxyclean.custom_sources, [entries[1]]);
});

test('unsupported node defaults are rejected and dropdown exposes only the four protocols', async () => {
  const h = harness();
  h.$('#c-pc-custom-sources').value = 'https://example.org/list';
  h.$('#c-pc-custom-protocol').value = 'socks4';
  await h.context.save({ preventDefault() {} });
  assert.equal(h.calls.length, 0);
  assert.match(h.notices[0][0], /HTTP.*HTTPS.*SOCKS5.*SOCKS5H/);
  const select = html.match(/<select id="c-pc-custom-protocol">([\s\S]*?)<\/select>/)[1];
  assert.deepEqual([...select.matchAll(/value="([^"]+)"/g)].map((match) => match[1]), ['http', 'https', 'socks5', 'socks5h']);
});

test('custom source limit permits 10 unique URLs but rejects 11', async () => {
  const h = harness();
  const urls = Array.from({ length: 11 }, (_, i) => `https://example.org/${i}.txt`);
  h.$('#c-pc-custom-sources').value = urls.join('\n');
  await h.context.save({ preventDefault() {} });
  assert.equal(h.calls.length, 0);
  h.$('#c-pc-custom-sources').value = urls.slice(0, 10).join('\n');
  await h.context.save({ preventDefault() {} });
  assert.equal(h.calls[0].body.proxyclean.custom_sources.length, 10);
});

for (const url of ['http://example.org/list', 'https://user:pass@example.org/list', 'https://@example.org/list', 'https://example.org/list?key=x', 'https://example.org/list?', 'https://example.org/list#', 'https://example.org:444/list', 'https://localhost/list', 'https://foo.local/list', 'https://127.1/list', 'https://10.0.0.1/list', 'https://169.254.169.254/list', 'https://172.16.1.1/list', 'https://192.168.1.1/list', 'https://[::1]/list', 'https://[fd00::1]/list', 'https://[fe80::1]/list', 'not a URL']) {
  test(`custom URL validation rejects invalid or private fixture ${url}`, async () => {
    const h = harness();
    h.$('#c-pc-custom-sources').value = url;
    await h.context.save({ preventDefault() {} });
    assert.equal(h.calls.length, 0);
    assert.equal(h.notices[0][1], true);
    assert.ok(!h.notices[0][0].includes(url));
  });
}

test('config status accepts exact custom fingerprint only and never displays source URL', async () => {
  const h = harness(undefined, undefined, { sources: [
    { id: 'custom-0123456789abcdef', status: 'ready', candidate_count: 5, url: 'DO_NOT_DISPLAY' },
    { id: 'custom-wrong', status: 'ready' }, { id: 'unknown', status: 'ready' },
  ] });
  await h.context.refreshProxyCleanStatus();
  const text = h.$('#proxyclean-status').textContent;
  assert.match(text, /自定义来源 01234567/);
  assert.doesNotMatch(text, /DO_NOT_DISPLAY|custom-wrong|unknown/);
});

for (const [minimum, maximum, interval] of [[0, 20, 300], [5, 20, 30], [20, 20, 86400]]) {
  test(`low-water config roundtrips ${minimum}/${maximum}/${interval} with auto collection disabled`, async () => {
    const h = harness(undefined, { ...pcDefaults, min_healthy_nodes: minimum, max_nodes: maximum, refill_interval_seconds: interval, auto_collect: false });
    await h.context.save({ preventDefault() {} });
    assert.equal(h.calls[0].body.proxyclean.min_healthy_nodes, minimum);
    assert.equal(h.calls[0].body.proxyclean.refill_interval_seconds, interval);
    assert.equal(h.calls[0].body.proxyclean.auto_collect, false);
  });
}

test('low-water minimum cannot exceed current max nodes', async () => {
  const h = harness(undefined, { ...pcDefaults, min_healthy_nodes: 21 });
  await h.context.save({ preventDefault() {} });
  assert.equal(h.calls.length, 0);
  assert.match(h.notices[0][0], /低水位.*最大节点数/);
});

test('config status distinguishes transport-healthy from zero routable during lane restriction', async () => {
  const h = harness(undefined, undefined, { source_status: 'ready', candidate_count: 10, healthy_count: 0, total: 10, healthy: 5, cooling: 3, disabled: 2, last_probe_at: '2026-01-01T00:00:00Z', next_probe_at: '2026-01-01T00:05:00Z', next_refresh_at: '2026-01-01T01:00:00Z' });
  await h.context.refreshProxyCleanStatus();
  const text = h.$('#proxyclean-status').textContent;
  assert.match(text, /可路由节点：0/);
  assert.match(text, /节点总数：10；传输健康：5；冷却中：3；已停用：2/);
  assert.match(text, /下次探活：2026-01-01T00:05:00.000Z/);
  assert.match(text, /下次采集：2026-01-01T01:00:00.000Z/);
});

const pcBounds = {
  min_healthy_nodes: [0, 100], refill_interval_seconds: [30, 86400],
  refresh_seconds: [30, 86400], ttl_seconds: [30, 172800],
  probe_interval_seconds: [10, 3600], probe_timeout_seconds: [1, 30],
  probe_concurrency: [1, 32], max_candidates: [1, 1000],
  max_nodes: [1, 100], cooldown_seconds: [1, 3600],
};

for (const [name, value] of [['missing', undefined], ['null', null], ['empty', {}], ['default', pcDefaults]]) {
  test(`ProxyClean ${name} object uses full safe defaults in PUT`, async () => {
    const h = harness(undefined, value);
    assert.equal(h.$('#c-pc-enabled').checked, false);
    assert.equal(h.$('#c-pc-fallback').checked, false);
    await h.context.save({ preventDefault() {} });
    assert.equal(h.calls.length, 1);
    assert.equal(h.calls[0].method, 'PUT');
    assert.deepEqual(h.calls[0].body.proxyclean, pcDefaults);
  });
}

test('ProxyClean non-default GET config roundtrips every field through PUT', async () => {
  const value = { ...pcDefaults, enabled: true, allow_direct_fallback: true,
    refresh_seconds: 1800, ttl_seconds: 5000, probe_interval_seconds: 60,
    probe_timeout_seconds: 4, probe_concurrency: 3, max_candidates: 99,
    max_nodes: 12, cooldown_seconds: 40 };
  const h = harness(undefined, value);
  for (const key of Object.keys(pcBounds)) assert.equal(h.$(`#c-pc-${key}`).value, String(value[key]));
  await h.context.save({ preventDefault() {} });
  assert.deepEqual(h.calls[0].body.proxyclean, value);
});

test('ProxyClean edited booleans save explicit false, and fixed source/scope resist DOM tampering', async () => {
  const h = harness(undefined, { ...pcDefaults, enabled: true, allow_direct_fallback: true });
  h.$('#c-pc-enabled').checked = false;
  h.$('#c-pc-fallback').checked = false;
  h.$('#c-pc-source').value = 'https://invalid.example/list';
  h.$('#c-pc-scope').value = 'all';
  await h.context.save({ preventDefault() {} });
  assert.deepEqual(h.calls[0].body.proxyclean, pcDefaults);
});

for (const [key, [min, max]] of Object.entries(pcBounds)) {
  test(`ProxyClean ${key} rejects invalid bounds/empty/fractional input and exposes matching HTML constraints`, async () => {
    for (const value of [min - 1, max + 1, '', 'NaN', 'Infinity', '1.5']) {
      const h = harness();
      h.$(`#c-pc-${key}`).value = value;
      await h.context.save({ preventDefault() {} });
      assert.equal(h.calls.length, 0, `${key}=${value}`);
      assert.equal(h.notices[0][1], true);
      assert.match(h.notices[0][0], /ProxyClean/);
    }
    const input = html.match(new RegExp(`<input id="c-pc-${key}"[^>]+>`))?.[0];
    for (const attr of [`min="${min}"`, `max="${max}"`, 'step="1"', 'required']) assert.ok(input?.includes(attr), `${key}: ${attr}`);
  });
}

for (const values of [{ ttl_seconds: 30 }, { max_candidates: 1 }]) {
  test(`ProxyClean cross-field validation ${Object.keys(values)[0]} blocks write`, async () => {
    const h = harness(undefined, { ...pcDefaults, ...values });
    await h.context.save({ preventDefault() {} });
    assert.equal(h.calls.length, 0);
    assert.equal(h.notices[0][1], true);
  });
}

for (const [state, label] of Object.entries({ disabled: '已关闭', loading: '加载中', ready: '可用', degraded: '降级', empty: '空池', expired: '已过期' })) {
  test(`ProxyClean status ${state}: GET-only, counts/timestamps/safe code; preserves pending form`, async () => {
    const h = harness(undefined, undefined, {
      enabled: true, scope: 'anonymous-only', source_status: state,
      candidate_count: 100, healthy_count: 12, last_error: 'source_unavailable',
      last_refresh_at: '2026-01-02T03:04:05Z', expires_at: '2026-01-02T05:04:05Z',
    });
    h.$('#c-pc-max_nodes').value = '13';
    h.$('#c-pc-enabled').checked = true;
    await h.context.refreshProxyCleanStatus();
    assert.deepEqual(h.calls, [{ url: '/api/proxyclean', method: 'GET', body: undefined }]);
    assert.equal(h.$('#c-pc-max_nodes').value, '13');
    assert.equal(h.$('#c-pc-enabled').checked, true);
    assert.equal(h.$('#proxyclean-status-refresh').disabled, false);
    const text = h.$('#proxyclean-status').textContent;
    for (const token of [label, '100', '12', 'anonymous-only', 'source_unavailable', '2026-01-02T03:04:05.000Z', '2026-01-02T05:04:05.000Z']) assert.ok(text.includes(token), token);
  });
}

test('ProxyClean status errors hide raw errors, retain form and re-enable button; next GET recovers', async () => {
  const h = harness(undefined, undefined, new Error('synthetic-private-error-do-not-render'));
  h.$('#c-pc-refresh_seconds').value = '500';
  await h.context.refreshProxyCleanStatus();
  assert.match(h.$('#proxyclean-status').textContent, /无法读取/);
  assert.doesNotMatch(h.$('#proxyclean-status').textContent, /synthetic-private/);
  assert.equal(h.$('#c-pc-refresh_seconds').value, '500');
  assert.equal(h.$('#proxyclean-status-refresh').disabled, false);
  h.context.api = async () => ({ enabled: false, scope: 'anonymous-only', source_status: 'disabled', candidate_count: 0, healthy_count: 0 });
  await h.context.refreshProxyCleanStatus();
  assert.doesNotMatch(h.$('#proxyclean-status').textContent, /无法读取/);
  assert.match(h.$('#proxyclean-status').textContent, /候选节点：0/);
});

test('ProxyClean status handles null response and untrusted optional fields safely', async () => {
  const h = harness(undefined, undefined, null);
  await h.context.refreshProxyCleanStatus();
  assert.match(h.$('#proxyclean-status').textContent, /无法读取/);
  h.context.api = async () => ({ enabled: false, source_status: '<script>', candidate_count: -2,
    healthy_count: 'bad', scope: '<script>', last_error: '<img onerror=bad>', last_refresh_at: 'invalid' });
  await h.context.refreshProxyCleanStatus();
  const text = h.$('#proxyclean-status').textContent;
  assert.match(text, /未知/);
  assert.match(text, /候选节点：—；可路由节点：—/);
  assert.match(text, /最近刷新：—；过期时间：—/);
  assert.doesNotMatch(text, /<script>|<img|invalid|bad/);
});

test('ProxyClean status suppresses duplicate GETs while pending', async () => {
  const h = harness();
  let resolve, calls = 0;
  h.context.api = () => { calls++; return new Promise((done) => { resolve = done; }); };
  const pending = h.context.refreshProxyCleanStatus();
  await h.context.refreshProxyCleanStatus();
  assert.equal(calls, 1);
  resolve({ source_status: 'loading' });
  await pending;
  assert.equal(h.$('#proxyclean-status-refresh').disabled, false);
});

test('ProxyClean Chinese/English UI covers all fields, safety and translated status', () => {
  const context = vm.createContext({});
  vm.runInContext(translations.replace(/^export /gm, '') + '\nglobalThis.dictionary = DICTIONARY;', context);
  const en = context.dictionary.en;
  for (const [, key] of html.matchAll(/data-i18n="(config\.proxyclean\.[^"]+)"/g)) assert.ok(en[key], key);
  for (const key of ['c-pc-scope', 'c-pc-source']) assert.match(html, new RegExp(`<input id="${key}"[^>]+readonly`));
  assert.match(html, /公共免费节点/);
  assert.match(html, /端到端 TLS/);
  assert.match(html, /不会通过换 IP 绕过 401\/403\/429/);
  assert.match(html, /空池默认失败/);
  assert.match(en['config.proxyclean.warning'], /paid Keys keep their existing routes/);
  assert.match(en['config.proxyclean.warning'], /End-to-end TLS/);
  assert.match(en['config.proxyclean.warning'], /401\/403\/429/);
  assert.match(en['config.proxyclean.warning'], /without silently going direct/);
  const h = harness(undefined, undefined, { enabled: true, scope: 'anonymous-only', source_status: 'ready', candidate_count: 5, healthy_count: 2 });
  h.context.t = (original, key, ...args) => (en[key] || original).replace(/\{(\d+)\}/g, (_, i) => args[i]);
  return h.context.refreshProxyCleanStatus().then(() => {
    assert.match(h.$('#proxyclean-status').textContent, /Runtime: Enabled \/ Ready; scope: anonymous-only/);
    assert.match(h.$('#proxyclean-status').textContent, /Candidates: 5; routable: 2/);
  });
});
