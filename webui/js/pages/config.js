// Page: 配置中心 — masked config editing with validation, hot reload and reveal.
import { api } from "../api.js";
import { loadConfig, loadDebugModels } from "../data.js";
import * as store from "../store.js";
import { t, onChange } from "../i18n.js";
import { $, $$, bindTabs, el, openDialog, toast } from "../ui.js";

function renderChips(name) {
  const host = $(`#${name}-chips`);
  host.replaceChildren();
  (store.get("config")?.[name] || []).forEach((item) => {
    const remove = el("button", { type: "button", title: t("移除", "common.remove"), text: "×" });
    const chip = el("span", { class: "chip", dataset: { id: item.id } });
    remove.addEventListener("click", () => chip.remove());
    chip.append(el("span", { text: item.display }), remove);
    host.append(chip);
  });
}

function renderRestartNotice(value) {
  const notice = $("#restart-notice");
  if (value?.restart_required_fields?.length) {
    notice.textContent = t(
      `已保存但尚未生效：${value.restart_required_fields.join(", ")}。当前 API ${value.effective.listen}、WebUI ${value.effective.webui_listen}。请重启进程。`,
      "config.restartNotice",
      value.restart_required_fields.join(", "),
      value.effective.listen,
      value.effective.webui_listen,
    );
    notice.classList.remove("hidden");
  } else {
    notice.classList.add("hidden");
  }
}

function secretInputs(name) {
  const kept = [...$(`#${name}-chips`).children].map((node) => ({ id: node.dataset.id }));
  const added = $(`#${name}-new`)
    .value.split(/\r?\n/)
    .map((value) => value.trim())
    .filter(Boolean)
    .map((value) => ({ value }));
  return kept.concat(added);
}

const numberValue = (selector) => Number($(selector).value);

function retryInputs() {
  const retriesRaw = $("#c-same-account-retries").value.trim();
  const retries = Number(retriesRaw);
  if (!retriesRaw || !Number.isInteger(retries) || retries < 0 || retries > 100) {
    throw new Error(t("错误重试次数必须是 0–100 的整数", "config.retry.invalidRetries"));
  }
  const codesRaw = $("#c-same-account-status-codes").value.trim();
  const tokens = codesRaw ? codesRaw.split(",").map((code) => code.trim()) : ["401", "403", "429"];
  if (tokens.some((code) => !/^[0-9]+$/.test(code) || Number(code) < 100 || Number(code) > 599)) {
    throw new Error(t("状态码须为英文逗号分隔的 100–599 十进制整数，不能有空项", "config.retry.invalidCodes"));
  }
  const codes = [...new Set(tokens.map(Number))].join(",");
  const readBackoff = (selector) => {
    const raw = $(selector).value.trim();
    const value = Number(raw);
    if (!raw || !Number.isInteger(value) || value < 0 || value > 86400000) {
      throw new Error(t("退避必须是 0–86400000 毫秒的整数", "config.retry.invalidRange"));
    }
    return value;
  };
  const initial = readBackoff("#c-backoff-initial");
  const maximum = readBackoff("#c-backoff-max");
  if (maximum > 0 && maximum < initial) {
    throw new Error(t("最大退避大于 0 时不得小于首轮退避", "config.retry.invalidMax"));
  }
  return {
    max_attempts: numberValue("#c-attempts"),
    timeout_seconds: numberValue("#c-timeout"),
    backoff_initial_ms: initial,
    backoff_max_ms: maximum,
    same_account_retries: retries,
    same_account_status_codes: codes,
  };
}

const proxyCleanSource = "https://raw.githubusercontent.com/HankNovic/ProxyClean/refs/heads/main/SOCKS5.txt";
const proxyCleanSourceIds = ["proxyclean", "proxifly", "thespeedx", "roosterkid", "hookzof", "gfpcom", "dpangestuw", "proxifly-cn", "xyzs-cn", "monosans-cn"];
// Defaults and bounds mirror the restricted backend contract.
const proxyCleanNumbers = {
  refresh_seconds: [3600, 30, 86400],
  ttl_seconds: [7200, 30, 172800],
  probe_interval_seconds: [300, 10, 3600],
  probe_timeout_seconds: [8, 1, 30],
  probe_concurrency: [8, 1, 32],
  max_candidates: [100, 1, 1000],
  max_nodes: [20, 1, 100],
  cooldown_seconds: [60, 1, 3600],
  min_healthy_nodes: [0, 0, 100],
  refill_interval_seconds: [300, 30, 86400],
};

const customNodeProtocols = ["http", "https", "socks5", "socks5h"];
let customSourceProtocols = new Map();
const customSourceURLs = () => [...new Set($("#c-pc-custom-sources").value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean))];
const customProtocolFor = (url) => customSourceProtocols.get(url) ?? $("#c-pc-custom-protocol").value;

function renderCustomProtocols() {
  $("#c-pc-custom-protocol-preview").textContent = customSourceURLs().map((url, index) => {
    const protocol = customProtocolFor(url);
    const label = customNodeProtocols.includes(protocol) ? protocol.toUpperCase() : "—";
    return t(`来源 ${index + 1}：${label}`, "config.proxyclean.protocolPreview", index + 1, label);
  }).join("；");
}

function applyCustomProtocolToAll() {
  const protocol = $("#c-pc-custom-protocol").value;
  if (!customNodeProtocols.includes(protocol)) return;
  customSourceProtocols = new Map(customSourceURLs().map((url) => [url, protocol]));
  renderCustomProtocols();
}

function customSourceInputs() {
  const values = customSourceURLs();
  const invalid = () => new Error(t("自定义来源最多 10 条，须为公网 HTTPS URL，不含凭证、查询或片段，端口仅允许 443", "config.proxyclean.customInvalid"));
  if (values.length > 10) throw invalid();
  for (const value of values) {
    let url;
    try { url = new URL(value); } catch { throw invalid(); }
    if (!/^https:\/\//i.test(value) || /[\s\\?#]/.test(value) || url.protocol !== "https:" || url.username || url.password || (url.port && url.port !== "443") || value.slice(value.indexOf("://") + 3).split("/")[0].includes("@")) throw invalid();
    const host = url.hostname.toLowerCase().replace(/\.$/, "");
    if (host.startsWith("[")) {
      // Only globally routable IPv6 candidates; DNS/network validation remains server-side.
      if (!/^\[[23][0-9a-f]{3}:/.test(host) || host.startsWith("[2001:db8:")) throw invalid();
    } else if (/^\d+\.\d+\.\d+\.\d+$/.test(host)) {
      const [a, b, c] = host.split(".").map(Number);
      if (a === 0 || a === 10 || a === 127 || a >= 224 || (a === 100 && b >= 64 && b <= 127) || (a === 169 && b === 254) || (a === 172 && b >= 16 && b <= 31) || (a === 192 && (b === 168 || b === 0 || (b === 88 && c === 99))) || (a === 198 && (b === 18 || b === 19 || (b === 51 && c === 100))) || (a === 203 && b === 0 && c === 113)) throw invalid();
    } else if (!host.includes(".") || /(?:^|\.)(localhost|local|internal|invalid|test)$/.test(host)) throw invalid();
  }
  return values.map((url) => {
    const protocol = customProtocolFor(url);
    if (!customNodeProtocols.includes(protocol)) {
      throw new Error(t("节点默认协议仅支持 HTTP、HTTPS、SOCKS5、SOCKS5H", "config.proxyclean.invalidProtocol"));
    }
    return { url, default_protocol: protocol };
  });
}

function syncExitCountry(clearFallback = false) {
  const mainlandOnly = $("#c-pc-exit-country").value === "CN";
  const fallback = $("#c-pc-fallback");
  if (mainlandOnly && clearFallback) fallback.checked = false;
  fallback.disabled = mainlandOnly;
}

function fillProxyClean(value = {}) {
  $("#c-pc-exit-country").value = value.required_exit_country ?? "";
  $("#c-pc-enabled").checked = !!value.enabled;
  $("#c-pc-fallback").checked = !!value.allow_direct_fallback;
  syncExitCountry();
  $("#c-pc-auto-switch").checked = value.auto_switch_proxy ?? true;
  $("#c-pc-auto-collect").checked = value.auto_collect ?? true;
  $("#c-pc-scope").value = value.scope ?? "anonymous-only";
  $("#c-pc-source").value = value.source_url ?? proxyCleanSource;
  customSourceProtocols = new Map((Array.isArray(value.custom_sources) ? value.custom_sources : []).map((entry) =>
    typeof entry === "string" ? [entry, "socks5"] : [entry.url, entry.default_protocol],
  ));
  $("#c-pc-custom-sources").value = [...customSourceProtocols.keys()].join("\n");
  $("#c-pc-custom-protocol").value = "socks5";
  renderCustomProtocols();
  const configuredSources = Array.isArray(value.sources) ? value.sources : [];
  const sources = configuredSources.length || customSourceProtocols.size ? configuredSources : ["proxyclean"];
  for (const id of proxyCleanSourceIds) $("#c-pc-source-" + id).checked = sources.includes(id);
  for (const [key, [fallback]] of Object.entries(proxyCleanNumbers)) {
    $(`#c-pc-${key}`).value = value[key] ?? fallback;
  }
}

function proxyCleanInputs() {
  const requiredExitCountry = $("#c-pc-exit-country").value;
  if (!["", "CN"].includes(requiredExitCountry)) {
    throw new Error(t("出口国家筛选仅支持不限或中国大陆（CN）", "config.proxyclean.invalidExitCountry"));
  }
  if (requiredExitCountry === "CN" && $("#c-pc-fallback").checked) {
    throw new Error(t("仅中国大陆出口不能启用直连回退；请重新选择出口筛选以关闭回退后保存", "config.proxyclean.exitFallbackInvalid"));
  }
  const sources = proxyCleanSourceIds.filter((id) => $("#c-pc-source-" + id).checked);
  const customSources = customSourceInputs();
  if (!sources.length && !customSources.length) {
    throw new Error(t("请至少选择一个内置来源或填写一个有效自定义来源", "config.proxyclean.sourcesRequired"));
  }
  const value = {
    sources,
    custom_sources: customSources,
    enabled: $("#c-pc-enabled").checked,
    scope: "anonymous-only",
    source_url: proxyCleanSource,
    required_exit_country: requiredExitCountry,
    allow_direct_fallback: $("#c-pc-fallback").checked,
    auto_switch_proxy: $("#c-pc-auto-switch").checked,
    auto_collect: $("#c-pc-auto-collect").checked,
  };
  for (const [key, [, min, max]] of Object.entries(proxyCleanNumbers)) {
    const raw = $(`#c-pc-${key}`).value.trim();
    const number = Number(raw);
    if (!raw || !Number.isInteger(number) || number < min || number > max) {
      throw new Error(t(`ProxyClean ${key} 必须是 ${min}–${max} 的整数`, "config.proxyclean.invalidRange", key, min, max));
    }
    value[key] = number;
  }
  if (value.ttl_seconds < value.refresh_seconds) {
    throw new Error(t("ProxyClean 节点有效期不得小于刷新间隔", "config.proxyclean.invalidTTL"));
  }
  if (value.max_nodes > value.max_candidates) {
    throw new Error(t("ProxyClean 最大可用节点数不得超过候选数", "config.proxyclean.invalidNodes"));
  }
  if (value.min_healthy_nodes > value.max_nodes) {
    throw new Error(t("低水位健康节点数不得超过最大节点数", "config.proxyclean.invalidLowWater"));
  }
  return value;
}

let proxyCleanStatus = null;
let proxyCleanStatusFailed = false;
function renderProxyCleanStatus() {
  const host = $("#proxyclean-status");
  if (proxyCleanStatusFailed) {
    host.textContent = t("无法读取 ProxyClean 状态，请检查登录状态或稍后重试。", "config.proxyclean.statusFailed");
    return;
  }
  if (!proxyCleanStatus) return;
  const value = proxyCleanStatus;
  const states = { disabled: "已关闭", loading: "加载中", ready: "可用", degraded: "降级", empty: "空池", expired: "已过期" };
  const status = Object.hasOwn(states, value.source_status)
    ? t(states[value.source_status], `config.proxyclean.state.${value.source_status}`)
    : t("未知", "config.proxyclean.unknown");
  const count = (n) => Number.isSafeInteger(n) && n >= 0 ? n : "—";
  const timestamp = (s) => typeof s === "string" && Number.isFinite(Date.parse(s)) ? new Date(s).toISOString() : "—";
  const errorCode = typeof value.last_error === "string" && /^[a-zA-Z0-9_-]{1,80}$/.test(value.last_error) ? value.last_error : "—";
  const enabled = value.enabled ? t("已启用", "config.proxyclean.on") : t("已关闭", "config.proxyclean.state.disabled");
  const scope = value.scope === "anonymous-only" ? value.scope : "—";
  // Only documented status fields are rendered as text, never HTML or raw errors.
  host.textContent = [
    t(`运行状态：${enabled} / ${status}；作用域：${scope}`, "config.proxyclean.statusSummary", enabled, status, scope),
    t(`候选节点：${count(value.candidate_count)}；可路由节点：${count(value.healthy_count)}`, "config.proxyclean.statusCounts", count(value.candidate_count), count(value.healthy_count)),
    t(`节点总数：${count(value.total)}；传输健康：${count(value.healthy)}；冷却中：${count(value.cooling)}；已停用：${count(value.disabled)}`, "pool.counts", count(value.total), count(value.healthy), count(value.cooling), count(value.disabled)),
    t(`最近探活：${timestamp(value.last_probe_at)}；下次探活：${timestamp(value.next_probe_at)}；下次采集：${timestamp(value.next_refresh_at)}`, "pool.schedule", timestamp(value.last_probe_at), timestamp(value.next_probe_at), timestamp(value.next_refresh_at)),
    t(`最近刷新：${timestamp(value.last_refresh_at)}；过期时间：${timestamp(value.expires_at)}`, "config.proxyclean.statusTimes", timestamp(value.last_refresh_at), timestamp(value.expires_at)),
    t(`最近错误（安全码）：${errorCode}`, "config.proxyclean.statusError", errorCode),
    t(`匿名通道受限直到：${timestamp(value.cooldown_until)}`, "pool.laneCooldown", timestamp(value.cooldown_until)),
  ].join("\n");
  const sourceStates = { pending: "等待采集", ready: "可用", empty: "空源", error: "失败" };
  const seen = new Set();
  for (const source of Array.isArray(value.sources) ? value.sources : []) {
    if (!source || typeof source.id !== "string" || seen.has(source.id)) continue;
    const custom = /^custom-[0-9a-f]{16}$/.test(source.id);
    if (!custom && !proxyCleanSourceIds.includes(source.id)) continue;
    const candidateNames = { "proxifly-cn": "Proxifly 中国候选", "xyzs-cn": "xyzs996 中国候选", "monosans-cn": "monosans 中国候选" };
    const name = custom ? t(`自定义来源 ${source.id.slice(7, 15)}`, "config.proxyclean.customName", source.id.slice(7, 15)) : Object.hasOwn(candidateNames, source.id) ? t(candidateNames[source.id], `config.proxyclean.sourceName.${source.id}`) : source.id;
    seen.add(source.id);
    const state = Object.hasOwn(sourceStates, source.status)
      ? t(sourceStates[source.status], `config.proxyclean.sourceState.${source.status}`)
      : t("未知", "config.proxyclean.unknown");
    const code = typeof source.last_error === "string" && /^[a-zA-Z0-9_-]{1,80}$/.test(source.last_error) ? source.last_error : "—";
    const fetched = timestamp(source.last_fetch_at);
    const candidates = count(source.candidate_count);
    host.textContent += "\n" + t(`${name}：${state}；候选：${candidates}；最近采集：${fetched}；安全码：${code}`, "config.proxyclean.sourceStatus", name, state, candidates, fetched, code);
  }
}

async function refreshProxyCleanStatus() {
  const button = $("#proxyclean-status-refresh");
  if (button.disabled) return;
  button.disabled = true;
  try {
    proxyCleanStatus = await api("/api/proxyclean", { method: "GET" });
    proxyCleanStatusFailed = !proxyCleanStatus || typeof proxyCleanStatus !== "object";
  } catch {
    proxyCleanStatusFailed = true;
  } finally {
    renderProxyCleanStatus();
    button.disabled = false;
  }
}

function fillConfig(value) {
  if (!value) return;
  $("#c-listen").value = value.listen;
  $("#c-prefer").value = value.prefer;
  $("#c-web-listen").value = value.webui.listen;
  $("#c-session").value = value.webui.session_ttl_minutes;
  $("#c-web-enabled").checked = value.webui.enabled;
  $("#c-anonymous").checked = !!value.anonymous;
  $("#c-proxyfile").value = value.proxyfile;
  fillProxyClean(value.proxyclean ?? {});
  $("#c-up-zen").value = value.upstream.zen;
  $("#c-up-go").value = value.upstream.go;
  $("#c-attempts").value = value.retry.max_attempts;
  $("#c-timeout").value = value.retry.timeout_seconds;
  $("#c-backoff-initial").value = value.retry.backoff_initial_ms ?? 0;
  $("#c-backoff-max").value = value.retry.backoff_max_ms ?? 0;
  $("#c-same-account-retries").value = value.retry.same_account_retries ?? 0;
  $("#c-same-account-status-codes").value = value.retry.same_account_status_codes ?? "401,403,429";
  $("#c-refresh").value = value.models.refresh_seconds;
  $("#c-protocols").value = JSON.stringify(value.models.protocols || {}, null, 2);
  $("#c-free-only").checked = !!value.models.free_only;
  const reasoning = value.reasoning || {};
  $("#c-reasoning-effort").value = reasoning.effort || "";
  $("#c-reasoning-by-model").value = JSON.stringify(reasoning.effort_by_model || {}, null, 2);
  $("#c-idle").value = value.performance.max_idle_conns;
  $("#c-idle-host").value = value.performance.max_idle_conns_per_host;
  $("#c-max-host").value = value.performance.max_conns_per_host;
  $("#c-idle-timeout").value = value.performance.idle_conn_timeout_seconds;
  $("#c-connect").value = value.performance.connect_timeout_seconds;
  $("#c-cooldown").value = value.performance.failure_cooldown_seconds;
  $("#c-attempt-timeout").value = value.performance.attempt_timeout_seconds;
  $("#c-first-event-timeout").value = value.performance.first_event_timeout_seconds || 0;
  $("#c-level").value = value.logging.level;
  $("#c-ring").value = value.logging.ring_size;
  $("#c-dump-bodies").checked = !!value.logging.dump_request_bodies;
  ["server_keys", "zen_keys", "go_keys", "proxies"].forEach(renderChips);
  renderRestartNotice(value);
}

async function save(event) {
  event.preventDefault();
  const current = store.get("config");
  if (!current) return;
  try {
    const update = {
      listen: $("#c-listen").value,
      server_keys: secretInputs("server_keys"),
      zen_keys: secretInputs("zen_keys"),
      go_keys: secretInputs("go_keys"),
      anonymous: $("#c-anonymous").checked,
      proxies: secretInputs("proxies"),
      proxyfile: $("#c-proxyfile").value,
      prefer: $("#c-prefer").value,
      upstream: { zen: $("#c-up-zen").value, go: $("#c-up-go").value },
      retry: retryInputs(),
      proxyclean: proxyCleanInputs(),
      models: {
        refresh_seconds: numberValue("#c-refresh"),
        protocols: JSON.parse($("#c-protocols").value || "{}"),
        free_only: $("#c-free-only").checked,
      },
      reasoning: {
        effort: $("#c-reasoning-effort").value,
        effort_by_model: JSON.parse($("#c-reasoning-by-model").value || "{}"),
      },
      performance: {
        max_idle_conns: numberValue("#c-idle"),
        max_idle_conns_per_host: numberValue("#c-idle-host"),
        max_conns_per_host: numberValue("#c-max-host"),
        idle_conn_timeout_seconds: numberValue("#c-idle-timeout"),
        connect_timeout_seconds: numberValue("#c-connect"),
        failure_cooldown_seconds: numberValue("#c-cooldown"),
        attempt_timeout_seconds: numberValue("#c-attempt-timeout"),
        first_event_timeout_seconds: numberValue("#c-first-event-timeout"),
      },
      logging: {
        level: $("#c-level").value,
        ring_size: numberValue("#c-ring"),
        dump_request_bodies: $("#c-dump-bodies").checked,
      },
      webui: {
        enabled: $("#c-web-enabled").checked,
        listen: $("#c-web-listen").value,
        username: current.webui.username,
        session_ttl_minutes: numberValue("#c-session"),
      },
    };
    const result = await api("/api/config", { method: "PUT", body: JSON.stringify(update) });
    store.set("config", result.config);
    ["server_keys", "zen_keys", "go_keys", "proxies"].forEach(
      (name) => ($(`#${name}-new`).value = ""),
    );
    await loadDebugModels();
    toast(
      result.result.restart_required
        ? t(
            `已应用；${result.result.restart_fields.join(", ")} 需重启`,
            "toast.config.restart",
            result.result.restart_fields.join(", "),
          )
        : t("配置已保存并立即应用", "toast.config.saved"),
    );
  } catch (error) {
    toast(error.message, true);
  }
}

async function reload() {
  try {
    const result = await api("/api/config/reload", { method: "POST", body: "{}" });
    await loadConfig();
    await loadDebugModels();
    toast(
      result.restart_required
        ? t("磁盘配置已重载，监听字段需重启", "toast.config.reloadedRestart")
        : t("磁盘配置已重载", "toast.config.reloaded"),
    );
  } catch (error) {
    toast(error.message, true);
  }
}

function reveal() {
  const input = el("input", { type: "password", autocomplete: "current-password" });
  const submit = async (close) => {
    try {
      const values = await api("/api/config/reveal", {
        method: "POST",
        body: JSON.stringify({ password: input.value }),
      });
      close();
      openDialog({
        title: t("敏感配置", "modal.title"),
        size: "dialog-lg",
        body: el("pre", { text: JSON.stringify(values, null, 2) }),
        actions: [{ label: t("关闭", "modal.close") }],
      });
    } catch (error) {
      toast(error.message, true);
    }
  };
  const dialog = openDialog({
    title: t("查看敏感配置", "modal.revealTitle"),
    body: el("div", { class: "field" }, [
      el("label", { text: t("请再次输入管理密码", "config.revealPrompt") }),
      input,
    ]),
    actions: [
      { label: t("取消", "common.cancel") },
      { label: t("查看", "common.view"), kind: "btn-primary", onClick: submit },
    ],
  });
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") {
      event.preventDefault();
      submit(dialog.close);
    }
  });
}

export default {
  id: "config",
  title: "配置中心",
  sub: "验证后原子保存，并热应用可变字段。",
  init() {
    store.on("config", fillConfig);
    bindTabs($("#page-config"));
    $("#config-form").addEventListener("submit", save);
    $("#reload-config").addEventListener("click", reload);
    $("#reveal").addEventListener("click", reveal);
    $("#proxyclean-status-refresh").addEventListener("click", refreshProxyCleanStatus);
    $("#c-pc-exit-country").addEventListener("change", () => syncExitCountry(true));
    $("#c-pc-custom-sources").addEventListener("input", renderCustomProtocols);
    $("#c-pc-custom-protocol").addEventListener("change", renderCustomProtocols);
    $("#c-pc-custom-protocol-apply").addEventListener("click", applyCustomProtocolToAll);
    onChange(() => {
      renderCustomProtocols();
      renderProxyCleanStatus();
      const value = store.get("config");
      if (value) renderRestartNotice(value);
      // Update chip tooltips in place so pending unsaved chip removals survive a language switch.
      $$(".chips .chip button").forEach((button) => (button.title = t("移除", "common.remove")));
    });
  },
};
