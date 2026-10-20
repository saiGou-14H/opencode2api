# ProxyClean 适配与重试配置

本 fork 将 [HankNovic/ProxyClean](https://github.com/HankNovic/ProxyClean) 及其致谢的六个公开 SOCKS5 仓库作为**候选节点数据源**，不是部署 ProxyClean 的私有后端服务。其公开仓库提供列表及 API 文档；私有 API 的服务器地址和凭据并未公开。本实现直接定时读取白名单内的官方 raw 文件，不依赖私有 API、不抓取或保存 API Key。

## 保存后清空公共代理池

管理界面每次成功执行“验证、保存并应用”（`PUT /api/config`），即使配置未变化，也会丢弃旧公共代理节点、健康状态及出口证据，再按最新配置建立空池。自动采集开启时后台重新采集、验证；关闭自动采集时保持空池，可手动采集。验证或持久化失败不清空当前池。节点停用记录、来源抓取限流/在途标记、出口地理服务缓存/限流和上游权限/429通道冷却均保留，不能通过重复保存绕过限制。付费账号/静态代理不属于此次清空范围。

## 自动采集来源

`proxyclean.sources` 选择需要采集的内置来源，管理界面支持独立勾选。兼容旧配置时，仅在内置和自定义来源同时为空时使用 `["proxyclean"]`；内置未知或重复 ID 被拒绝。

| ID | 数据仓库 |
|---|---|
| `proxyclean` | [HankNovic/ProxyClean](https://github.com/HankNovic/ProxyClean) |
| `proxifly` | [proxifly/free-proxy-list](https://github.com/proxifly/free-proxy-list) |
| `thespeedx` | [TheSpeedX/PROXY-List](https://github.com/TheSpeedX/PROXY-List) |
| `roosterkid` | [roosterkid/openproxylist](https://github.com/roosterkid/openproxylist) |
| `hookzof` | [hookzof/socks5_list](https://github.com/hookzof/socks5_list) |
| `gfpcom` | [gfpcom/free-proxy-list](https://github.com/gfpcom/free-proxy-list) |
| `dpangestuw` | [dpangestuw/Free-Proxy](https://github.com/dpangestuw/Free-Proxy) |
| `proxifly-cn` | [Proxifly CN文本](https://raw.githubusercontent.com/proxifly/free-proxy-list/main/proxies/countries/CN/data.txt)，保留多协议前缀 |
| `xyzs-cn` | [xyzs996 CN JSON](https://raw.githubusercontent.com/xyzs996/free-proxy-health-list/main/proxies/countries/cn/data.json)，按协议解析 |
| `monosans-cn` | [monosans JSON](https://raw.githubusercontent.com/monosans/proxy-list/main/proxies.json)，筛选country.iso_code==CN |

三个CN适配器只提供**发布方标记的中国候选**，实际准入仍需开启 `required_exit_country: "CN"`。xyzs996 的 `https_connect_probe` 表示HTTP代理支持CONNECT到HTTPS目标，适配为 `http://`，不能混同为到代理本身也使用TLS的 `https://`。未支持的SOCKS4被丢弃。CN JSON源每个限4MiB，不递归抓取其中的链接。

2026-10-20核实：以上三个数据文件当日有提交且GET200；这不保证节点在线。另查vakhov/fresh-proxy-list虽然仓库有近期提交，代理数据文件最后修改却在2026-02-07，未选用。ProxyClean的“国内可用”指可访问性，不代表出口位于中国大陆。

采集并发上限为 3，每个来源有独立请求超时和体积限制（ProxyClean 2 MiB/20 秒，gfpcom wiki 32 MiB/45 秒，其余 4 MiB/25 秒）；原七个适配器仅接纳 SOCKS5，新增CN适配器按来源格式接纳受支持的HTTP/HTTPS/SOCKS5/SOCKS5H候选，统一规范化、轮询公平合并及去重后再探活。单源失败不阻断其他源；失败源不把旧地址重新注入新快照。全部来源失败时，仅保留原快照至其原始 TTL，不无限延期。状态接口和管理界面显示每源候选数、最近采集时间与安全错误码；候选数不等于已探活数量。

选择六个直接源的示例：

```json
{"proxyclean":{"sources":["proxifly","thespeedx","roosterkid","hookzof","gfpcom","dpangestuw"]}}
```

上述仅为字段片段，合并进现有配置而非覆盖全配置。`source_url` 是保留的旧版官方地址字段；实际采集由 `sources` 的固定适配器决定，不能填写任意 URL。

## 自定义来源与多协议

管理页面支持添加最多 10 个自定义 HTTPS 文本来源；可与内置源并用，也可只选择自定义源。来源必须是公网 HTTPS URL（443端口），不接受账号密码、query、fragment、回环/内网地址；抓取时继续验证 DNS 所有结果并固定拨号，禁止重定向和环境代理。自定义源每个限 4 MiB、25 秒，失败独立显示安全错误码。

```json
{
  "proxyclean": {
    "sources": [],
    "custom_sources": [
      {"url":"https://example.com/proxies.txt","default_protocol":"http"}
    ]
  }
}
```

`example.com` 仅是格式示例，不是提供给你的真实代理源。兼容旧版自定义字符串 URL，默认协议为 `socks5`。

自定义列表支持 `http://IP:port`、`https://IP:port`、`socks5://IP:port`、`socks5h://IP:port` 混合条目；裸 `IP:port` 使用该来源的 `default_protocol`。仅接纳公网 literal IP，当前不接纳域名节点、带凭据节点、SOCKS4、Clash/V2Ray 订阅或递归来源链接。HTTPS **来源 URL** 与节点 **代理协议** 是不同概念。

去重键包含协议，因此同IP端口的 HTTP 与 SOCKS5 不会合并。内置源仍按其声明的 SOCKS5 解析，防止把不明混合内容错误识别为代理。HTTP代理使用CONNECT承载目标HTTPS，HTTPS代理还验证代理端TLS；两种客户端均不能关闭证书校验。

## 中国大陆出口准入

`proxyclean.required_exit_country` 默认空字符串（不限制国家）；设置为 `"CN"` 时只接受**已检测为中国大陆出口**的公共节点，不包含香港（HK）、澳门（MO）、台湾（TW）。CN模式禁止同时开启 `allow_direct_fallback`。没有合格节点时返回不可用，不自动回退海外节点或付费线路。

仅CN候选的配置片段（合并到原配置，不覆盖账号和密钥）：

```json
{"proxyclean":{"sources":["proxifly-cn","xyzs-cn","monosans-cn"],"custom_sources":[],"required_exit_country":"CN","allow_direct_fallback":false}}
```

中国候选源的维护许可：Proxifly GPL-3.0、xyzs996与monosans MIT；许可不保证被收录代理的安全或使用授权，不打包原始地址清单进本仓库。

来源的国家标签只是候选信息，不能替代实际出口检查。节点必须先通过目标HTTPS连通性探活，再通过该节点访问固定HTTPS IP回显服务取得出口IP，使用固定地理查询服务验证country_code。非CN、未知、超时或证据过期的节点都不能入可路由池。节点视图包含 `exit_ip`、`exit_country`、`exit_checked_at`、`exit_expires_at`。

**限制：**这里验证的是访问IP回显服务时的出口；按目的地动态选路的代理可能对模型站点使用不同出口，地理数据库也可能不准确或更新滞后，因此它不是对所有目标的绝对地域保证。要求稳定、可合同保证的中国大陆出口，应使用可信的固定出口代理服务。本功能不保证模型权限，不用于绕过上游401/403/429限制，也不因换国家清除通道冷却。

## 安全与启用边界

- 默认 `proxyclean.enabled=false`；管理员需明确开启。
- `scope` 固定为 `anonymous-only`：公共池仅用于匿名免费模型，付费 Zen/Go Key、管理请求和模型目录请求保持原有线路。
- 公共代理并不可信，即使探活成功也不代表长期稳定。HTTPS 保留端到端证书链与主机名验证，不安装代理 CA、不关闭 TLS 校验。代理仍能观察连接目标和流量元数据。
- 来源为内置官方 HTTPS 列表或管理员配置的合规公网 HTTPS 文本 URL；只接受经过解析、去重、公网 IP 检查和本机探活的节点。私网、回环、链路本地等节点不会进入池。
- 来源429/503的 `Retry-After` 也会遵守；热更新和全局开关切换不清除尚未过期的来源冷却。冷却状态是进程内状态，整个服务重启并不持久保留它。HTTP代理CONNECT协商头限64KiB，成功后的隧道流量不受该头部预算限制。
- 来源刷新、探活和池状态管理是本机职责；ProxyClean 的原始探活通过不等于本机能访问模型上游。
- 默认空池/过期池不偷偷直连。只有显式开启 `allow_direct_fallback` 才允许该回退；不要误将无健康节点显示为正常运行。
- 代理故障切换不用于绕过授权或风控：401/403/429 可以按管理员配置在**同账号、同代理、同 origin**有限原地重试，耗尽后结束请求，不通过更换 IP 或 Key 继续。429 等待完整 `Retry-After`（总 deadline 仍可提前结束），最终保留该响应头。公共匿名通道随后进入共享冷却，至少等待 `cooldown_seconds`，更长的 `Retry-After` 完整保留；主页探活、来源刷新或开关热更新不能清除此限制。状态含 `cooldown_until` 与 `upstream_restricted`。
- 开启公共池后，匿名请求失败不会自动转入付费 Key 通道；直接请求付费模型仍走原有线路。
- 请求开始输出后不会因流式中断自动换代理重放，避免重复内容与不确定的重复计费。

## 配置

管理界面“配置中心”提供退避与 ProxyClean 设置；修改后沿用既有配置验证、持久化和热应用机制。示例参见根目录 `config.example.json`。

| 字段 | 默认值 | 含义 |
|---|---:|---|
| `proxyclean.enabled` | false | 是否启用匿名代理池 |
| `proxyclean.scope` | anonymous-only | 固定匿名范围 |
| `proxyclean.sources` | ["proxyclean"] | 选择十个固定来源中的一个或多个；可仅使用custom_sources |
| `proxyclean.required_exit_country` | 空 | 空不限制，CN仅允许有新鲜中国大陆出口证据的节点；CN禁止直连兜底 |
| `proxyclean.custom_sources` | [] | 最多10个{url,default_protocol}自定义公网HTTPS文本来源 |
| `proxyclean.auto_switch_proxy` | true（省略） | 匿名公共池故障是否切换到其他代理；不改变付费Key线路 |
| `proxyclean.auto_collect` | true（省略） | 是否启动/定时自动采集；false时仍允许管理员手动采集 |
| `proxyclean.min_healthy_nodes` | 0 | 低水位补池阈值，0关闭，范围0..max_nodes；生产可设5 |
| `proxyclean.refill_interval_seconds` | 300 | 补池尝试最小间隔，30..86400秒；失败也计入间隔 |
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
| `retry.same_account_retries` | 0 | 首次外同账号/同代理追加次数，0..100，仍受max_attempts总上限约束 |
| `retry.same_account_status_codes` | 空（401,403,429） | 英文逗号分隔100..599整数；网络错误也会原地重试 |
| `retry.timeout_seconds` | 300 | 请求总时限，包含退避等待和所有尝试 |
| `retry.backoff_initial_ms` | 0 | 初始退避；0 关闭退避 |
| `retry.backoff_max_ms` | 0 | 退避上限；初始值>0且本值=0时固定使用初始值 |

建议先保留既有总时限，设置有限次数与适度退避，再根据真实日志调整；不要用无限重试或更换代理来处理权限拒绝。达到请求 deadline 或客户端取消时应立即停止等待。

## 代理池管理菜单

独立“代理池管理”菜单提供来源状态、节点协议/endpoint、健康与停用状态、冷却与探活时间，以及手动采集、探活、停用、恢复。动作走既有管理会话及 CSRF，不新增公网管理端口。

| API | 语义 |
|---|---|
| `GET /api/proxyclean` | 池、来源及匿名通道冷却状态 |
| `GET /api/proxyclean/nodes` | 有界节点列表与当前任务 |
| `POST /api/proxyclean/refresh` | 异步采集；202仅表示受理，随后刷新状态查看结果 |
| `POST /api/proxyclean/probe` | 异步探活，与采集共用单飞；运行中返回409 |
| `POST /api/proxyclean/nodes/{id}/disable` | 持久停用，定时探活和来源刷新不会自动恢复 |
| `POST /api/proxyclean/nodes/{id}/enable` | 恢复调度资格；不会清除整个匿名通道的权限/限流冷却 |

停用ID存储于配置文件同目录的 `proxyclean-disabled.json`，权限0600并原子写入；生产路径为 `/var/lib/opencode2api/proxyclean-disabled.json`，由既有Docker状态卷持久化。写盘失败时不假装停用成功。关闭自动采集仍允许手动采集；关闭全局代理池后拒绝运维网络任务。

同账号追加重试的实际调用次数不会超过 `max_attempts` 的剩余预算。例如总上限3、追加次数2，至多3次HTTP调用，而不是3×3次。配置2xx状态码会重新发送尚未向客户端输出的成功请求，可能重复计费；通常不应这样配置。已开始SSE输出后绝不重放。

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
