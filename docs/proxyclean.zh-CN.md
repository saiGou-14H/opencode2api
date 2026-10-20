# ProxyClean 适配与重试配置

本 fork 将 [HankNovic/ProxyClean](https://github.com/HankNovic/ProxyClean) 及其致谢的六个公开 SOCKS5 仓库作为**候选节点数据源**，不是部署 ProxyClean 的私有后端服务。其公开仓库提供列表及 API 文档；私有 API 的服务器地址和凭据并未公开。本实现直接定时读取白名单内的官方 raw 文件，不依赖私有 API、不抓取或保存 API Key。

## 自动采集来源

`proxyclean.sources` 选择需要采集的来源，管理界面支持独立勾选。兼容旧配置时，省略或空数组使用 `["proxyclean"]`；未知或重复 ID 被拒绝。

| ID | 数据仓库 |
|---|---|
| `proxyclean` | [HankNovic/ProxyClean](https://github.com/HankNovic/ProxyClean) |
| `proxifly` | [proxifly/free-proxy-list](https://github.com/proxifly/free-proxy-list) |
| `thespeedx` | [TheSpeedX/PROXY-List](https://github.com/TheSpeedX/PROXY-List) |
| `roosterkid` | [roosterkid/openproxylist](https://github.com/roosterkid/openproxylist) |
| `hookzof` | [hookzof/socks5_list](https://github.com/hookzof/socks5_list) |
| `gfpcom` | [gfpcom/free-proxy-list](https://github.com/gfpcom/free-proxy-list) |
| `dpangestuw` | [dpangestuw/Free-Proxy](https://github.com/dpangestuw/Free-Proxy) |

采集并发上限为 3，每个来源有独立请求超时和体积限制（ProxyClean 2 MiB/20 秒，gfpcom wiki 32 MiB/45 秒，其余 4 MiB/25 秒）；仅接纳 SOCKS5 候选，统一规范化、轮询公平合并及去重后再探活。单源失败不阻断其他源；失败源不把旧地址重新注入新快照。全部来源失败时，仅保留原快照至其原始 TTL，不无限延期。状态接口和管理界面显示每源候选数、最近采集时间与安全错误码；候选数不等于已探活数量。

选择六个直接源的示例：

```json
{"proxyclean":{"sources":["proxifly","thespeedx","roosterkid","hookzof","gfpcom","dpangestuw"]}}
```

上述仅为字段片段，合并进现有配置而非覆盖全配置。`source_url` 是保留的旧版官方地址字段；实际采集由 `sources` 的固定适配器决定，不能填写任意 URL。

## 安全与启用边界

- 默认 `proxyclean.enabled=false`；管理员需明确开启。
- `scope` 固定为 `anonymous-only`：公共池仅用于匿名免费模型，付费 Zen/Go Key、管理请求和模型目录请求保持原有线路。
- 公共代理并不可信，即使探活成功也不代表长期稳定。HTTPS 保留端到端证书链与主机名验证，不安装代理 CA、不关闭 TLS 校验。代理仍能观察连接目标和流量元数据。
- 来源限定白名单内的官方 HTTPS 列表；只接受经过解析、去重、公网 IP 检查和本机探活的 SOCKS5 节点。私网、回环、链路本地等节点不会进入池。
- 来源刷新、探活和池状态管理是本机职责；ProxyClean 的原始探活通过不等于本机能访问模型上游。
- 默认空池/过期池不偷偷直连。只有显式开启 `allow_direct_fallback` 才允许该回退；不要误将无健康节点显示为正常运行。
- 代理故障切换不用于绕过授权或风控：401/403/429 立即结束当前请求，不通过更换 IP 重放。429 原样回传 `Retry-After`；公共匿名通道随后进入共享冷却，至少等待 `cooldown_seconds`，更长的 `Retry-After` 完整保留，主页探活或来源刷新不能清除此限制。状态含 `cooldown_until` 与 `upstream_restricted`。
- 开启公共池后，匿名请求失败不会自动转入付费 Key 通道；直接请求付费模型仍走原有线路。
- 请求开始输出后不会因流式中断自动换代理重放，避免重复内容与不确定的重复计费。

## 配置

管理界面“配置中心”提供退避与 ProxyClean 设置；修改后沿用既有配置验证、持久化和热应用机制。示例参见根目录 `config.example.json`。

| 字段 | 默认值 | 含义 |
|---|---:|---|
| `proxyclean.enabled` | false | 是否启用匿名代理池 |
| `proxyclean.scope` | anonymous-only | 固定匿名范围 |
| `proxyclean.sources` | ["proxyclean"] | 选择七个固定来源中的一个或多个 |
| `proxyclean.source_url` | 官方 SOCKS5.txt | 保留旧版兼容字段，来源选择由 sources 决定 |
| `proxyclean.refresh_seconds` | 3600 | 刷新候选列表间隔 |
| `proxyclean.ttl_seconds` | 7200 | 本地列表最长可用年龄，不是上游有效性担保 |
| `proxyclean.probe_interval_seconds` | 300 | 节点健康检查间隔 |
| `proxyclean.probe_timeout_seconds` | 8 | 单节点探活超时 |
| `proxyclean.probe_concurrency` | 8 | 最大并发探活数 |
| `proxyclean.max_candidates` | 100 | 最多处理候选数 |
| `proxyclean.max_nodes` | 20 | 最多保留节点数 |
| `proxyclean.cooldown_seconds` | 60 | 故障节点冷却 |
| `proxyclean.allow_direct_fallback` | false | 空池是否允许明确直连回退 |
| `retry.max_attempts` | 3 | 一次请求的 HTTP 尝试硬上限，含首次调用 |
| `retry.timeout_seconds` | 300 | 请求总时限，包含退避等待和所有尝试 |
| `retry.backoff_initial_ms` | 0 | 初始退避；0 关闭退避 |
| `retry.backoff_max_ms` | 0 | 退避上限；初始值>0且本值=0时固定使用初始值 |

建议先保留既有总时限，设置有限次数与适度退避，再根据真实日志调整；不要用无限重试或更换代理来处理权限拒绝。达到请求 deadline 或客户端取消时应立即停止等待。

## 状态与排障

鉴权后的 `GET /api/proxyclean` 显示启用范围、候选数量、健康数量、最近刷新/过期时间和安全错误码，不返回公共节点凭据或原始错误正文。

- `disabled`：未开启。
- `loading`：尚在拉取或检查。
- `ready`：有可用健康节点。
- `degraded`：存在刷新或健康问题，检查 last_error 与过期时间。
- `empty`：当前无可用节点。
- `expired`：旧列表已到本地 TTL，不应继续无限使用。

公共代理质量由第三方决定，不能保证成功率；健康检查也不能保证特定模型一定可用。需分别核对源下载、SOCKS 连接、TLS、上游 HTTP 状态和 SSE 终态，不要把 HTTP 200 一概视为生成成功。

## 归属

数据来源见上表。检查时 ProxyClean 与 proxifly 包含 GNU GPL v3 许可，gfpcom 包含 MIT 许可；其余四源未发现明确许可证，不能据此认为数据可任意再分发。本实现仅提供采集适配器，不打包或提交节点清单，也不复制未公开的 ProxyClean 后端。列表会变化；生产中使用前须自行评估代理合法性、信任和隐私风险，遵守上游服务条款，保留来源归属。
