import { api } from "../api.js";
import { t, onChange } from "../i18n.js";
import { $, el } from "../ui.js";

let status = null;
let nodes = [];
let total = 0;
let operation = "";
let sessionGeneration = 0;
let readGeneration = 0;
const busy = new Set();
const sourceIds = ["proxyclean", "proxifly", "thespeedx", "roosterkid", "hookzof", "gfpcom", "dpangestuw", "proxifly-cn", "xyzs-cn", "monosans-cn"];
const safeCode = (value) => typeof value === "string" && /^[a-zA-Z0-9_-]{1,80}$/.test(value) ? value : "—";
const count = (value) => Number.isSafeInteger(value) && value >= 0 ? value : "—";
const timestamp = (value) => typeof value === "string" && Number.isFinite(Date.parse(value)) ? new Date(value).toISOString() : "—";

function syncButtons() {
  const disabled = busy.has("operation") || operation !== "";
  $("#pool-collect").disabled = disabled;
  $("#pool-probe").disabled = disabled;
}

function render() {
  const task = operation === "refresh" ? t("采集中", "pool.collecting") : operation === "probe" ? t("探活中", "pool.probing") : t("空闲", "pool.idle");
  $("#pool-status").textContent = status ? [
    t(`运行状态：${safeCode(status.source_status)}；候选：${count(status.candidate_count)}；可路由：${count(status.healthy_count)}`, "pool.summary", safeCode(status.source_status), count(status.candidate_count), count(status.healthy_count)),
    t(`节点总数：${count(status.total)}；传输健康：${count(status.healthy)}；冷却中：${count(status.cooling)}；已停用：${count(status.disabled)}`, "pool.counts", count(status.total), count(status.healthy), count(status.cooling), count(status.disabled)),
    t(`最近探活：${timestamp(status.last_probe_at)}；下次探活：${timestamp(status.next_probe_at)}；下次采集：${timestamp(status.next_refresh_at)}`, "pool.schedule", timestamp(status.last_probe_at), timestamp(status.next_probe_at), timestamp(status.next_refresh_at)),
    t(`任务：${task}；节点总数：${count(total)}`, "pool.operation", task, count(total)),
    t(`匿名通道受限直到：${timestamp(status.cooldown_until)}`, "pool.laneCooldown", timestamp(status.cooldown_until)),
    status.last_error === "exit_country_unavailable" ? t("无已验证的中国大陆出口（可能是未知、非 CN 或连接失败；不代表全部网络故障）", "pool.exitUnavailable") : "",
  ].join("\n") : "—";
  syncButtons();
  const sources = $("#pool-sources");
  sources.replaceChildren();
  const labels = { pending: "等待采集", ready: "可用", empty: "空源", error: "失败" };
  for (const source of Array.isArray(status?.sources) ? status.sources : []) {
    if (!source || typeof source.id !== "string") continue;
    const custom = /^custom-[0-9a-f]{16}$/.test(source.id);
    if (!custom && !sourceIds.includes(source.id)) continue;
    const candidateNames = { "proxifly-cn": "Proxifly 中国候选", "xyzs-cn": "xyzs996 中国候选", "monosans-cn": "monosans 中国候选" };
    const name = custom ? t(`自定义来源 ${source.id.slice(7, 15)}`, "config.proxyclean.customName", source.id.slice(7, 15)) : Object.hasOwn(candidateNames, source.id) ? t(candidateNames[source.id], `config.proxyclean.sourceName.${source.id}`) : source.id;
    const state = Object.hasOwn(labels, source.status) ? t(labels[source.status], `config.proxyclean.sourceState.${source.status}`) : "—";
    const time = timestamp(source.last_fetch_at);
    sources.append(el("p", { text: t(`${name}：${state}；候选：${count(source.candidate_count)}；最近采集：${time}；安全码：${safeCode(source.last_error)}`, "config.proxyclean.sourceStatus", name, state, count(source.candidate_count), time, safeCode(source.last_error)) }));
  }
  const body = $("#pool-nodes");
  body.replaceChildren();
  for (const node of nodes) {
    if (!node || typeof node.id !== "string" || !node.id) continue;
    const action = node.disabled ? "enable" : "disable";
    const label = node.disabled ? t("恢复", "pool.enable") : t("停用", "pool.disable");
    const button = el("button", { type: "button", class: "btn btn-sm", text: label });
    button.disabled = busy.has(`node:${node.id}`);
    button.addEventListener("click", () => perform(`/api/proxyclean/nodes/${encodeURIComponent(node.id)}/${action}`, `node:${node.id}`, button, false));
    const health = node.disabled ? t("已停用", "pool.disabled") : node.healthy === true ? t("健康", "pool.healthy") : t("未知或不可用", "pool.unhealthy");
    const probe = node.last_probe_ok === true ? t("成功", "pool.probeOK") : node.last_probe_ok === false ? t("失败", "pool.probeFailed") : "—";
    body.append(el("tr", {}, [
      el("td", { text: node.id }),
      el("td", { text: typeof node.endpoint === "string" ? node.endpoint : "—" }),
      el("td", { text: health }),
      el("td", { text: timestamp(node.cooldown_until) }),
      el("td", { text: `${timestamp(node.last_probe_at)} / ${probe}` }),
      el("td", { text: ["http", "https", "socks5", "socks5h"].includes(node.protocol) ? node.protocol.toUpperCase() : "—" }),
      el("td", { text: typeof node.probe_latency_ms === "number" && Number.isFinite(node.probe_latency_ms) && node.probe_latency_ms >= 0 ? String(node.probe_latency_ms) : "—" }),
      el("td", { text: count(node.consecutive_failures) }),
      el("td", { text: typeof node.exit_ip === "string" && node.exit_ip.length <= 45 && /^(?:[0-9]{1,3}(?:\.[0-9]{1,3}){3}|[0-9a-fA-F]*:[0-9a-fA-F:.]+)$/.test(node.exit_ip) ? node.exit_ip : "—" }),
      el("td", { text: typeof node.exit_country === "string" && /^[A-Z]{2}$/.test(node.exit_country) ? (node.exit_country === "CN" ? "CN" : t(`${node.exit_country}（CN 模式不允许）`, "pool.exitExcluded", node.exit_country)) : "—" }),
      el("td", { text: timestamp(node.exit_checked_at) }),
      el("td", { text: timestamp(node.exit_expires_at) }),
      el("td", {}, [button]),
    ]));
  }
  if (!nodes.length) body.append(el("tr", {}, [el("td", { colspan: 13, text: t("暂无节点", "pool.empty") })]));
}

async function refresh() {
  const epoch = ++readGeneration;
  const button = $("#pool-refresh-status");
  button.disabled = true;
  try {
    const [nextStatus, nextNodes] = await Promise.all([api("/api/proxyclean", { method: "GET" }), api("/api/proxyclean/nodes", { method: "GET" })]);
    if (epoch !== readGeneration) return;
    if (!nextStatus || !Array.isArray(nextNodes?.nodes)) throw new Error("invalid status response");
    status = nextStatus;
    nodes = nextNodes.nodes.slice(0, 1000);
    total = nextNodes.total;
    const reportedOperation = nextStatus.operation ?? nextNodes.operation;
    operation = ["refresh", "probe"].includes(reportedOperation) ? reportedOperation : "";
    render();
  } catch {
    if (epoch !== readGeneration) return;
    status = null;
    nodes = [];
    total = 0;
    operation = "";
    render();
    $("#pool-message").textContent = t("无法读取代理池状态，请检查登录状态或稍后重试。", "pool.readFailed");
  } finally {
    if (epoch === readGeneration) button.disabled = false;
  }
}

async function perform(path, key, button, asynchronous = true) {
  if (busy.has(key) || (asynchronous && operation)) return;
  const epoch = sessionGeneration;
  busy.add(key);
  button.disabled = true;
  syncButtons();
  try {
    const result = await api(path, { method: "POST" });
    if (epoch !== sessionGeneration) return;
    if (asynchronous) {
      if (result?.accepted !== true || !["refresh", "probe"].includes(result.operation)) throw new Error("invalid acceptance");
      operation = result.operation;
      $("#pool-message").textContent = t("任务已受理，正在异步执行；不代表采集或探活完成，请读取运行状态确认。", "pool.accepted");
      render();
    } else {
      if (result?.ok !== true) throw new Error("invalid node result");
      $("#pool-message").textContent = t("节点操作已受理，请读取运行状态确认最新结果。", "pool.nodeAccepted");
      await refresh();
    }
  } catch (error) {
    if (epoch !== sessionGeneration) return;
    const messages = {
      exit_country_unavailable: ["无已验证的中国大陆出口（可能是未知、非 CN 或连接失败；不代表全部网络故障）", "pool.exitUnavailable"],
      proxyclean_busy: ["已有任务运行中，请稍后读取运行状态；未启动重复任务。", "pool.running"],
      proxyclean_disabled: ["代理池或匿名通道已关闭，请先在配置中心启用。", "pool.notEnabled"],
      proxyclean_unavailable: ["代理池暂不可用，请稍后重试。", "pool.unavailable"],
      proxyclean_node_not_found: ["节点已不存在，请刷新节点列表。", "pool.notFound"],
      node_not_found: ["节点已不存在，请刷新节点列表。", "pool.notFound"],
      proxyclean_state_failed: ["节点状态保存失败，请刷新状态后重试。", "pool.stateFailed"],
      state_failed: ["节点状态保存失败，请刷新状态后重试。", "pool.stateFailed"],
    };
    const message = Object.hasOwn(messages, error.code) ? messages[error.code] : ["操作失败，请检查登录状态或稍后重试。", "pool.actionFailed"];
    $("#pool-message").textContent = t(...message);
  } finally {
    if (epoch === sessionGeneration) {
      busy.delete(key);
      button.disabled = false;
      render();
    }
  }
}

export default {
  id: "proxy-pool",
  title: "代理池管理",
  sub: "匿名公共代理池的采集、探活与节点管理。",
  init() {
    $("#pool-refresh-status").addEventListener("click", () => { $("#pool-message").textContent = ""; return refresh(); });
    $("#pool-collect").addEventListener("click", () => perform("/api/proxyclean/refresh", "operation", $("#pool-collect")));
    $("#pool-probe").addEventListener("click", () => perform("/api/proxyclean/probe", "operation", $("#pool-probe")));
    onChange(render);
  },
  show: refresh,
  refresh,
  reset() { sessionGeneration++; readGeneration++; status = null; nodes = []; total = 0; operation = ""; busy.clear(); render(); $("#pool-message").textContent = ""; $("#pool-refresh-status").disabled = false; },
};
