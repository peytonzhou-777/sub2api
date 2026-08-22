# CodexCLI 出站请求拟真改造计划

## 1. 文档信息

- 状态：阶段 1–3 已实施并通过完整服务回归；阶段 0 的源码/UA contract 已冻结，完整多路径真实抓包复核保留为部署前置；阶段 4 仅保留 ADR
- 编制日期：2026-08-21
- 最近修订：2026-08-22（收敛为官方 `0.149.0` 单一行为基线，移除专项性能测试并改为测试站全量发布）
- sub2api 基线：`e45ac11c9`（`main`）
- 本机 CodexCLI 目标版本：`codex-cli 0.149.0`
- 官方源码行为基线：`rust-v0.149.0` / `758ef40f50c1a458425c7cfbf1eb12cbc07af0b0`
- 官方发布依据：[Codex Changelog](https://developers.openai.com/codex/changelog)、[Codex `rust-v0.149.0` 源码](https://github.com/openai/codex/tree/rust-v0.149.0)
- UA 模板校准环境：Windows `10.0.26100`、x64；目标终端 token 为官方 `WindowsTerminal`；该环境只用于生成同类主机模板，不代表账号实际绑定此设备
- 全量发布目标 profile：`codex_cli_0_149_0`（启用前必须在阶段 0 冻结参考 Windows 环境的真实请求 fixture）

### 1.1 实施结果（2026-08-22）

- 已冻结官方 `rust-v0.149.0` 源码 contract 与本机 Windows Terminal 脱敏 UA，固定 UA 为 `codex_cli_rs/0.149.0 (Windows 10.0.26100; x86_64) WindowsTerminal`；现有 fixture 已驱动头集合和字段顺序断言，但不冒充完整原始抓包，HTTP/WS/compact/models 的完整真实捕获复核仍是部署前置。
- 已增加全局默认 profile、全局 `legacy` 紧急回滚开关和账号 `extra.codex_outbound_profile` 故障隔离覆写；标准配置加载和服务构造入口均将缺省值归一为 `codex_cli_0_149_0`。
- 已在最终账号选定后生成统一出站快照，并将 HTTP、HTTP passthrough、WS、WS 预热、compact 和 `/models` 投影到同一 profile；现有 Session、Thread、Prompt Cache、epoch 与用户粘性调度作用域未改变。
- 严格 profile 已固定 Windows UA、去除旧兼容头、主动合成受控 metadata、按官方顶层顺序编码 JSON，并仅对普通 OAuth `/responses` 启用 zstd level 3；compact 和 WS 不启用 HTTP body 压缩。
- WS 在真正写业务帧或预热帧前补充字符串形态的 `x-codex-ws-stream-request-start-ms`；最终写帧使用有序原始 JSON，并在语义未变化时复用 `tools`、`input` 等原始子树。
- `/models` 已固定 `client_version=0.149.0`，复用统一 UA/originator 且不发送 `Version`；账号管理端已展示生效 profile、UA 与 zstd 状态，并支持继承、严格、单账号 legacy 三态编辑。
- 不可信下游 metadata 会被丢弃；只有现有指纹状态和本站明确可确认的父子代理字段进入快照。当前没有可靠来源的 parent/root turn、agent、sandbox 或工具命名空间字段继续省略，不伪造或透传。
- 独立审查后已进一步禁止 strict snapshot 在指纹 `off/device` 缺少服务端 ID 时回读下游身份：缺失字段直接省略，原始 Prompt Cache Key 删除，不暗中改变 Session 收敛 opt-in；未知顶层字段则让 body、identity 和 compression 整请求回退 legacy，并记录低基数 fallback 指标。
- 后台 Codex 用量探测已复用业务 gateway profile；WS 连接池使用不出站的 snapshot topology scope 保持根/子/兄弟线程隔离；非 OAuth `/models` 不再继承全局严格版本；Codex 导入入口统一拒绝非法账号 profile。
- 已加入进程级低基数统计并并入现有 OpenAI WS 性能快照；统计不包含 Authorization、完整 Session、Thread、Prompt Cache、body 或 metadata。
- 后端 `internal/service` 完整回归、`internal/config`、`internal/handler/admin` 回归及前端目标 lint、完整类型检查均通过；按本计划不执行专项性能测试。
- 阶段 4 已由 `docs/ADR/0017-codex-rust-transport-sidecar.md` 明确延期，本次只承诺应用层高度一致，不宣称 TLS/HTTP2/WebSocket 线级指纹完全一致。

本文档用于指导后续线程实施 Codex OAuth 出站请求拟真改造。实施时只以仓库当前代码和官方 `rust-v0.149.0` 源码为行为基线；未来升级必须新增或原子迁移版本化 profile 和相应 golden fixture，不应把其他版本行为直接混入本 profile，也不应在各请求路径分散打补丁。

## 2. 背景与核心目标

sub2api 会把多个下游用户的请求转发到 OpenAI Codex OAuth 上游。当前实现已经具备账号级 Session 收敛、HTTP/WS 上下文池、账号 failover、Prompt Cache 收敛和 turn-state 来源保护，但出站请求仍存在以下可见差异：

1. 同时携带官方连字符头和历史下划线兼容头。
2. 请求头、扁平 `client_metadata`、内嵌 `x-codex-turn-metadata` 不是由同一份权威快照生成。
3. 默认 User-Agent 固定为 Ubuntu/xterm，与账号登录时采用的 Windows 客户端类别以及计划模拟的 Windows CodexCLI 主机类别矛盾。
4. 普通 OAuth HTTP 请求未采用官方默认的 zstd 请求压缩。
5. Go `map` 重编码形成固定字典序 JSON，与 CodexCLI 的 Rust struct 序列化不同。
6. 当前使用 Go TLS、HTTP/2 和 WebSocket 栈，线级指纹与 CodexCLI 的 rustls/hyper/tungstenite 不同。

项目目标按以下优先级执行：

1. 第一优先级：降低多用户共享的上游可见特征。
2. 第二优先级：保持或改善 TTFT，不引入远程状态查询和额外连接等待。
3. 第三优先级：在不破坏现有业务语义的前提下，使应用层及最终线级请求接近真实 CodexCLI。

所有 OAuth 账号都在 Windows 环境完成登录。本文不把 User-Agent 当作设备身份，也不假设这些账号来自同一台物理主机、同一个 OS 安装或同一个 CodexCLI 安装；目标是使用一个低熵、真实且常见的“Windows CodexCLI 客户端类别”模板，近似表示一组 UA 相同、环境形态相近的 Windows 主机。登录阶段用于确定该客户端类别，调用阶段继续使用同一规范化 UA 模板，以减少登录环境与后续 Codex 请求之间的类别矛盾。浏览器只参与 OAuth 授权，后续 `/responses` 请求仍使用 CodexCLI UA，而不是把浏览器导航 UA 发送给 Codex backend。

因此，所有账号使用同一个固定 Windows CodexCLI UA 是客户端类别收敛，不是设备收敛。该 UA 只表达 CodexCLI 版本、操作系统类别、架构和终端类别，不承载设备 ID、installation ID、Session、Thread 或账号归属；按账号伪造不同 OS、架构或终端会制造不必要的高熵组合，但统一 UA 也不得被解释为这些账号共享同一真实设备。

## 3. 强制边界

### 3.1 必须保持不变

- `session_id` 继续按“上游账号 + 客户端类型 + 指纹 epoch”收敛。
- HTTP 和 WS 传输切换不拆分 Session。
- 收敛模式下 `prompt_cache_key` 继续使用收敛后的 `session_id`。
- `thread_id`、`turn_id`、`window_id` 的现有作用域和父子代理谱系不借本次改造扩大或缩小。
- 账号 failover 后继续按新账号 seed 派生新的账号侧指纹。
- `x-codex-turn-state` 继续执行账号来源校验，禁止跨账号复用。
- 严格 profile 下所有 OAuth 账号固定使用同一个经参考 Windows 环境抓包确认的 CodexCLI 类别 UA；该模板不表示同一设备或同一安装。
- 不修改本地“用户粘性调度”、账号 placement、触达、generation、冷却和回流策略。
- 不改变非 Codex API Key 请求、第三方 OpenAI 兼容上游和普通透传请求的既有语义。

### 3.2 禁止引入

- 不为拟真计算访问 Redis、数据库或上游。
- 不增加探活、实时速率判断、请求级随机轮换或 key 映射表。
- 不把下游用户、API Key、连接 ID、代理 IP、请求时间或 Prompt 哈希纳入 ID、UA 或 profile 派生；官方 turn metadata 所需的回合开始时间除外。
- 不从统一 UA 反推出、绑定或轮换 installation、device、Session、Thread 等身份字段；UA 模板与这些字段的生命周期相互独立。
- 不使用浏览器 uTLS profile 冒充 CodexCLI；官方 CodexCLI 不是浏览器 TLS 栈。
- 不伪造无法正确生成的 attestation、服务端 turn-state 或其他上游签发状态。
- 不安排 microbenchmark、负载测试、压力测试或按请求尺寸统计 CPU、内存分配和 TTFT 的专项性能测试；性能风险仅通过既有生产监控在全量发布后观察。

## 4. 已对齐能力

以下行为已经与目标一致，本计划只补充回归测试，不重新设计：

- OAuth Codex endpoint、Bearer 认证和 `ChatGPT-Account-ID`。
- HTTP `/responses` 显式使用 `Accept: text/event-stream`；compact 使用 JSON body 和 `Content-Type: application/json`，官方 `0.149.0` 源码不显式增加 `Accept`。
- OAuth 请求固定 `store=false`、流式请求 `stream=true`。
- 普通 Responses 固定携带 `reasoning`，且 `include=["reasoning.encrypted_content"]`。
- 指纹 Session、Prompt Cache、UUIDv7 和 epoch 轮换。
- HTTP/WS 同一逻辑会话使用同一 Session。
- WS `responses_websockets=2026-02-06` 协商值。
- WebSocket permessage-deflate、重连及增量 `previous_response_id` 续链。
- `remote_compaction_v2` 默认功能声明。
- OAuth 普通 `/responses` 的 zstd 请求压缩能力；官方 `enable_request_compression` 在 `0.149.0` 默认启用。
- `x-codex-turn-state` 的账号来源绑定和跨账号剥离。
- 上下文池按协议、指纹和拓扑兼容条件隔离。

### 4.1 `0.149.0` 出站行为基线

严格 profile 直接以官方 `rust-v0.149.0` tag 的实际行为为目标：

| 范围 | `0.149.0` 行为 | 计划约束 |
| --- | --- | --- |
| User-Agent | `{originator}/{version} ({OS} {OS_version}; {arch}) {terminal}` | 固定 Windows `0.149.0` 类别模板，实际 Windows 版本段由阶段 0 抓包冻结 |
| 默认头 | `originator`、`User-Agent`、可选 residency | 不增加通用 `version`、`Accept-Language` 或浏览器头 |
| Responses 身份头 | 普通 HTTP/WS 使用 `session-id`、`thread-id`、`x-client-request-id=thread-id` | 全部从同一权威快照投影 |
| Prompt Cache | 默认使用当前 Responses metadata 的 `session_id` | `prompt_cache_key` 固定使用收敛后的 `session_id` |
| 父子代理 | 根代理与子代理 Thread/Request ID 不同，但 Session 和 Prompt Cache Key 相同 | 保持根缓存谱系，不用子代理临时 Thread 派生 Prompt Cache |
| Routing hint | OAuth Codex backend 的 HTTP、模型已知的 WS 握手和 compact 条件发送 `x-codex-routing-hint: model=<model>[;tier=<tier>]` | 三条路径使用同一模型和 service tier 生成规则，纯 WS preconnect 省略 |
| Responses body | `reasoning` 始终存在、`include` 固定含 encrypted content、`store=false`、`stream=true` | 严格 profile 固定生成该形态，不继承下游缺省差异 |
| `stream_options` | concurrent summaries 已启用、目标是 OpenAI 且 `reasoning.summary` 非空时发送 | 默认省略，仅生成已知的 `sequential_cutoff` 结构 |
| turn metadata | 包含 session/thread/turn/window、agent、parent/root turn、sandbox、工具命名空间等受支持语义 | 仅投影网关能可靠确认的字段，不透传下游伪造值，不制造身份矛盾 |
| 直接 metadata 头 | `x-codex-turn-metadata` 移除无界 `tool_namespaces_info`，完整对象留在 body `client_metadata` | 权威快照生成完整和有界两个受控投影，不能简单复制同一字符串 |
| Tools JSON | WS 从 HTTP 请求借用已编码的 `RawValue` | 保留 tools 子树的字段顺序、数值和 escape 形态 |
| Compact 请求头 | 使用 installation/session/thread、兼容 metadata 和条件 routing hint；不设置 `x-client-request-id`，不显式设置 `Accept` | 不向 compact 补普通 Responses 专属头 |
| `/models` 版本表达 | 使用 `client_version` 查询参数，不设置 `Version` 头 | 只保留查询参数并复用统一 UA/originator |

## 5. 目标架构

### 5.1 版本化全局出站 profile 与账号级故障隔离

新增可统一解析的 `codex_outbound_profile`，建议首期取值：

- `legacy`：保持当前行为，仅用于紧急回滚和故障隔离。
- `codex_cli_0_149_0`：启用经阶段 0 验证的严格应用层形态，作为全局默认值。

`0.149.0` 官方 tag 是本 profile 唯一可复现源码行为基线。本机已经安装 `0.149.0`；阶段 0 仍必须从 Windows Terminal 启动该版本并冻结脱敏真实请求 fixture，用于校准同类 Windows 主机的 transport 头和 UA 版本段。该 fixture 是类别模板证据，不是账号设备归属证据；不得只凭源码或修改版本字符串宣称 profile 已完成验证。

实现要求：

- profile 在最终账号选定后解析；未设置账号覆写时继承全局默认 `codex_cli_0_149_0`。
- 同一个账号的 HTTP、WS、compact 和 `/models` 必须使用同一 profile。
- 不使用多个可以互相冲突的独立布尔开关组合协议形态。
- 全局紧急回滚开关优先于账号覆写，切回 `legacy` 后所有 OAuth 新请求立即使用旧形态。
- 账号覆写可存放在现有账号 `extra` 中，避免新增数据库列；它只用于故障隔离，不作为日常分批放量机制。
- 后续升级 CodexCLI 时新增 profile 或原子升级 profile 内容，并同步更新 golden fixture。
- 首次发布即让全部现有及新增 OAuth 账号使用 `codex_cli_0_149_0`，不执行按账号比例灰度。
- 严格 profile 不接受账号级自定义 User-Agent 覆写；确需使用自定义 UA 的账号必须显式回退 `legacy`。

### 5.2 单一权威出站快照

账号选定后，为每次逻辑请求生成一份只读 `CodexOutboundSnapshot`。建议至少包含：

```text
profile
account_id（仅本地使用，不出站）
originator
user_agent
beta_features
installation_id
session_id
prompt_cache_key
thread_id
turn_id
window_id
parent_thread_id
forked_from_thread_id
parent_turn_id
root_turn_id
agent_name
subagent_kind
request_kind
turn_started_at_unix_ms
model
service_tier
routing_hint
```

生成规则：

- 所有 ID 继续复用现有指纹 v2 派生结果，不建立第二套算法。
- `x-client-request-id` 不再单独派生，固定等于快照中的 `thread_id`。
- `prompt_cache_key` 固定等于快照中的收敛 `session_id`；这与官方 149 默认行为及其父子代理测试一致。
- `x-codex-routing-hint` 只能在最终账号、模型和 service tier 确定后生成，值为 `model=<model>` 或 `model=<model>;tier=<tier>`。
- `turn_started_at_unix_ms` 在逻辑请求进入网关时捕获一次；同一请求安全重试复用，不能在每次账号尝试中重新取当前时间。
- failover 到新账号时，账号相关 ID 使用新账号 seed 重派生，但逻辑请求时间和语义谱系保持不变。
- HTTP、WS、compact 的请求头和 body 都只能从该快照投影，不能再次从下游头读取另一套 ID。
- 官方 149 新增的 agent、parent/root turn、sandbox 和工具清单字段只允许从最终出站语义派生；来源不可靠时省略，不得原样信任下游 metadata。

### 5.3 官方形态投影

#### 普通 HTTP `/responses`

严格 profile 应发送：

- `Authorization`
- `ChatGPT-Account-ID`
- `User-Agent`
- `originator`
- `Content-Type: application/json`
- `Accept: text/event-stream`
- `Content-Encoding: zstd`（严格 profile 下启用）
- `session-id`
- `thread-id`
- `x-client-request-id`，值等于 `thread-id`
- `x-codex-window-id`
- `x-codex-turn-metadata`
- OAuth Codex backend 条件发送 `x-codex-routing-hint`，值由最终 body 的 model 和 service tier 生成
- 按需发送 `x-codex-parent-thread-id`、`x-openai-subagent`、`x-codex-turn-state` 和已启用的 beta features

普通 `/responses` 不应发送：

- `version`
- `session_id`
- `conversation_id`
- 直接的 `x-codex-installation-id`
- 下游 `Accept-Language`
- 下游原始 User-Agent、Session、Thread、Request ID 和未经校验的 beta features
- 默认关闭的 `x-responsesapi-include-timing-metrics`；只有未来 profile 明确复现开启该官方功能时才发送

#### WebSocket 握手

在普通 HTTP 头集合基础上：

- 使用 `OpenAI-Beta: responses_websockets=2026-02-06`。
- OAuth Codex backend 在模型已知的 turn-time 握手或 WS 预热握手中携带与该业务请求相同的 `x-codex-routing-hint`；纯连接 preconnect 尚无模型语义时按官方 149 省略。
- 不使用 HTTP body 压缩；继续使用 WebSocket permessage-deflate。
- Session、Thread、Request ID 和 metadata 必须与该连接上传输的首个业务请求快照一致。
- 物理连接池复用不能改变逻辑快照，也不能把旧连接的身份头覆盖到新请求。
- WS body 在真正发送前生成 `x-codex-ws-stream-request-start-ms`；它是传输计时字段，不进入任何身份或缓存派生。
- `traceparent`/`tracestate` 仅从本站实际 tracing 上下文生成；没有可信 tracing 时省略，禁止透传下游伪造值。

#### `/responses/compact`

- 使用 JSON body 和 `Content-Type: application/json`，不主动增加官方 149 源码未设置的 `Accept`。
- 保留官方 compact 直接使用的 `x-codex-installation-id`。
- 使用连字符 `session-id`、`thread-id`；官方 149 compact 不设置 `x-client-request-id`，严格 profile 也不补。
- 条件发送与普通 HTTP 同规则生成的 `x-codex-routing-hint`。
- `request_kind` 使用 `compaction`；具备可靠来源时补充 compaction 细节。
- 删除 `version`、`session_id`、`conversation_id`、`Accept-Language` 和下游伪造的 `x-client-request-id`。
- 不为 compact 强行增加普通 Responses body `client_metadata`，也不启用普通 `/responses` 的 zstd body 压缩。

#### `/models`

- 保留 `client_version` 查询参数。
- 使用与账号 profile 相同的 User-Agent 和 originator。
- 删除额外 `Version` 请求头。

### 5.4 `client_metadata` 与 turn metadata

普通 HTTP 和 WS body 的 `client_metadata` 至少投影：

```json
{
  "x-codex-installation-id": "...",
  "session_id": "...",
  "thread_id": "...",
  "x-codex-window-id": "...",
  "turn_id": "...",
  "x-codex-turn-metadata": "{...}"
}
```

按需增加：

- `x-openai-subagent`
- `x-codex-parent-thread-id`
- `parent_turn_id`
- `root_turn_id`

内嵌 `x-codex-turn-metadata` 是权威对象，扁平 metadata 和直接请求头只是兼容投影。即使下游没有提供该对象，严格 profile 也必须主动构造，不能只在已有字符串上做替换。官方 149 的直接 `x-codex-turn-metadata` 头是有界投影：必须移除可能无界增长的 `tool_namespaces_info`；body `client_metadata` 中的内嵌对象可以保留从最终有效 tools 派生的完整清单。

最低字段要求：

- 普通业务请求：`request_kind=turn`。
- WS generate=false 预热：`request_kind=prewarm`。
- compact：`request_kind=compaction`。
- 字段中的 installation、session、thread、turn、window、parent、forked、parent turn 和 root turn ID 必须与同一快照一致。
- 保留经过验证的 `agent_name`、`subagent_kind`、`thread_source`、sandbox、sandbox mode、workspace 和最终 tools 语义；不得原样保留下游身份 ID 或不可信状态字段。
- `responses_api_metadata` 额外项执行官方 149 同级限制：保留键白名单/保留键过滤，并限制数量、键长和值长，避免 metadata 成为新的任意透传通道。

## 6. 分阶段实施

### 阶段 0：固定基线与捕获工具

目标：在改变生产请求前建立可重复验证基线。

任务：

1. 以官方 `rust-v0.149.0` / `758ef40f50c1a458425c7cfbf1eb12cbc07af0b0` 冻结源码派生 contract，记录 UA、默认头、各 endpoint 头集合、body 字段顺序和条件字段规则。
2. 从 Windows Terminal 启动本机 `codex-cli 0.149.0`，捕获其 HTTP、WS、compact 和 `/models` 请求。
3. 从真实捕获中冻结完整 User-Agent，确认 Windows 版本段、x64 架构和 `WindowsTerminal` token，不手工猜测。
4. 增加脱敏的官方 CodexCLI 请求 fixture；fixture 不保存 Authorization、账号 ID、原始 Session、Prompt 或用户内容。
5. 增加出站捕获测试工具，可记录头集合、解压后的 body、JSON 顶层字段顺序、tools 原始子树和 WS 事件序列。
6. 建立 profile golden tests，明确哪些字段按请求动态变化、哪些字段必须稳定。
7. 记录当前 `legacy` 请求作为兼容回归基线。

验收：

- fixture 可在离线测试中重放。
- 源码 contract 与真实捕获的应用层字段一致；若 transport 自动头存在差异，以真实 149 抓包为准并记录原因。
- fixture 中的固定 User-Agent 与 Windows Terminal 实际捕获完全一致。
- 动态字段经过占位符归一化后，重复执行结果稳定。
- 测试产物和日志不含凭据或原始用户标识。

### 阶段 1：权威快照与严格头集合

目标：先消除最强的应用层组合差异。

任务：

1. 新增 `CodexOutboundSnapshot` 及统一构造器。
2. HTTP、WS、compact、Chat/Messages 兼容入口统一复用快照。
3. 主动构造 body 内完整 `x-codex-turn-metadata` 与直接头的有界投影；后者排除 `tool_namespaces_info`。
4. 普通 HTTP/WS 固定 `x-client-request-id=thread_id`；compact 明确删除该头。
5. 三条 OAuth Codex 路径统一生成 `x-codex-routing-hint`，且 body、WS 握手与 compact 使用同一 model/service tier 输入。
6. 严格 profile 删除 `version`、下划线 Session 头、`conversation_id`、`Accept-Language`；compact 还删除下游 `Accept` 和 `x-client-request-id`。
7. 普通 HTTP/WS 删除直接 `x-codex-installation-id`，compact 保留。
8. beta features 按 profile 能力白名单生成，不原样信任下游值。
9. 纳入 149 的 parent/root turn lineage；只从本站可信父子代理状态构造，来源不明时省略。
10. 保留现有 turn-state provenance guard，并增加严格 profile 回归测试。

主要涉及文件：

- `backend/internal/service/openai_codex_fingerprint.go`
- `backend/internal/service/openai_codex_fingerprint_v2.go`
- `backend/internal/service/openai_gateway_forward.go`
- `backend/internal/service/openai_gateway_passthrough.go`
- `backend/internal/service/openai_ws_forwarder_payload.go`
- `backend/internal/service/openai_gateway_messages.go`
- `backend/internal/service/openai_gateway_chat_completions.go`
- `backend/internal/service/openai_compact_body_signal.go`

验收：

- HTTP/WS/compact 的同一请求使用同一快照。
- 头、扁平 metadata、内嵌 metadata 不存在身份矛盾。
- 严格 profile 不出现禁止头。
- HTTP、WS 和 compact 的 routing hint 与各自最终 body 一致；普通 HTTP/WS 有 Request ID，compact 没有。
- failover 后仅账号相关派生值改变。
- Session 收敛、Prompt Cache 和用户粘性调度测试全部保持通过。

### 阶段 2：固定 Windows User-Agent、JSON 与 zstd

目标：消除错误的 Ubuntu UA、Go map 字典序和未压缩请求三类稳定特征。

#### 2.1 全局固定 Windows CodexCLI User-Agent

任务：

1. 使用官方格式：`{originator}/{version} ({OS} {OS_version}; {arch}) {terminal}`。
2. 从 Windows Terminal 启动本机 `codex-cli 0.149.0`，通过脱敏捕获确认 `os_info` 实际输出的 Windows 版本段，并将其作为同类 Windows 主机 UA 的校准样本。
3. 冻结唯一模板：`codex_cli_rs/0.149.0 (Windows <已验证版本>; x86_64) WindowsTerminal`。
4. 所有严格 profile OAuth 账号共用该低熵客户端类别模板，不使用账号 seed、下游用户、设备指纹或客户端输入选择 UA。
5. CodexCLI 发起的 OAuth token 交换/刷新、HTTP、WS、compact、models、账号测试及配额请求共用同一个 UA 解析器；浏览器授权页面的导航请求仍由浏览器使用自身 UA。
6. 升级 CodexCLI 时，版本段和 fixture 原子更新；OS/架构/终端形态继续保持选定的 Windows 客户端类别基线，除非管理员明确迁移目标类别。
7. 严格 profile 忽略账号 `openai_user_agent` 覆写，避免同一客户端类别出现按账号分裂的高熵环境组合。
8. UA 与 account seed、installation/device、Session、Thread 和 epoch 解耦：这些字段变化不轮换 UA，UA 相同也不合并它们的既有作用域。

禁止：

- 按请求随机 UA。
- 按下游用户选择 UA。
- 按上游账号选择不同 UA。
- Session 轮换时同步轮换 UA。
- 继续使用 Ubuntu、macOS、浏览器或 `dumb` 作为生产严格 profile UA。
- 在没有参考 Windows 环境抓包证据时猜测 Windows 版本段。

#### 2.2 Codex 有序 JSON 编码

任务：

1. 为严格 profile 增加专用 Responses/WS/compact 顶层编码结构。
2. 普通 HTTP 按官方字段顺序编码：`model`、`instructions`、`input`、`tools`、`tool_choice`、`parallel_tool_calls`、`reasoning`、`store`、`stream`、`stream_options`、`include`、`service_tier`、`prompt_cache_key`、`text`、`client_metadata`。
3. 严格 profile 始终输出 `reasoning` 对象、`store=false`、`stream=true` 和 `include=["reasoning.encrypted_content"]`；不因下游缺省而形成多个形态。
4. `stream_options` 默认省略，仅在 concurrent summaries 已启用、目标为 OpenAI 且 `reasoning.summary` 非空时输出 `reasoning_summary_delivery=sequential_cutoff`。
5. WS 使用 `type=response.create` 外壳，并按官方 WS payload 字段顺序编码。
6. HTTP 构造完成的 tools 子树以原始 JSON 复用于 WS，避免 map 解码后再次编码导致字段顺序、数值或 escape 形态漂移。
7. 保持紧凑 JSON、关闭 HTML escape，不追加换行。
8. 对 profile 未识别的新增字段记录兼容告警并走明确 fallback，禁止静默丢字段。

#### 2.3 HTTP zstd 请求压缩

任务：

1. 仅对 OAuth 普通 `/responses` 严格 profile 启用 zstd level 3。
2. 压缩在所有 body 改写和有序序列化完成后执行。
3. 设置 `Content-Encoding: zstd` 并重新计算 Content-Length。
4. compact 和 WS 不启用该 HTTP body 压缩。
5. 增加压缩耗时、压缩前后字节数和失败回退指标，不记录 body。
6. 压缩失败时回退未压缩请求并记录指标，不触发换号或调度失败。

验收：

- 所有严格 profile OAuth 账号的 UA 模板完全相同，并与参考 Windows Terminal CodexCLI fixture 一致；测试不得据此断言账号来自同一设备。
- 顶层 JSON 顺序与 fixture 一致。
- mock 上游可解压并得到与压缩前完全相同的 JSON。
- 不发生 Redis、数据库或上游附加调用。

### 阶段 3：WS、预热、compact 与 models 完整对齐

目标：补齐低频入口及请求序列形态。

任务：

1. 将现有 WS `generate=false` 预热纳入版本化 profile，而不是独立漂移的协议行为。
2. 预热请求使用 `request_kind=prewarm`，正常请求使用 `request_kind=turn`。
3. 预热失败继续使用现有无损回退，不新增换号或调度惩罚。
4. WS 重连保持同一逻辑快照；物理连接 ID 不进入任何身份派生。
5. WS 每次实际发送前写入 transport 计时 metadata，但重连不得改变 Session、Prompt Cache 或 lineage。
6. HTTP/WS/compact 补齐 149 的 routing hint，并验证 service tier 有/无两种格式；纯连接 WS preconnect 验证为省略。
7. compact 完成严格头集合和 `request_kind=compaction` 对齐，确认无显式 `Accept`、无 `x-client-request-id`、无 body `client_metadata`。
8. `/models` 删除 `Version`，共用全局固定 Windows CodexCLI UA。
9. 对官方支持的 `stream_options` 结构进行白名单生成，不主动伪造或透传下游值。

验收：

- WS 首包、预热、业务请求和重连序列与 fixture 一致。
- HTTP/WS 切换不改变 Session 和 Prompt Cache。
- compact 不带普通 Responses body metadata、`Accept` 或 `x-client-request-id`，也不出现旧兼容头。
- 父子代理 Thread/Request ID 分离，但 Session 和 Prompt Cache Key 相同；可信 parent/root turn lineage 保持一致。
- `/models` 只通过查询参数表达 `client_version`。

### 阶段 4：线级传输栈可行性验证

目标：评估是否需要解决 Go 与 rustls/hyper/tungstenite 的线级差异。

该阶段不与前三阶段捆绑上线，应先形成独立 ADR 和原型。

候选方案：

1. 常驻 Rust transport sidecar，使用与目标 CodexCLI profile 接近的 reqwest/hyper/rustls/tungstenite 版本。
2. Go 进程通过本机 IPC 调用 sidecar，sidecar 负责连接池、HTTP/2 和 WS 生命周期。
3. 保持现有 Go transport 作为回滚路径。

不推荐方案：

- 使用 Chrome/Firefox uTLS profile，因为它与真实 CodexCLI 的 rustls 指纹不一致。
- 为拟真关闭 HTTP/2，因为官方 CodexCLI 使用 HTTP/2，且会损害连接复用和 TTFT。
- 仅修改 TLS ClientHello 而忽略 HTTP/2 SETTINGS、窗口、HPACK 和 WS 握手差异。

验收维度：

- TLS ClientHello / JA3 / JA4。
- ALPN 与证书根行为。
- HTTP/2 SETTINGS、窗口更新、PING、连接复用和头编码形态。
- WS 握手、扩展协商、mask、分帧和压缩形态。
- sidecar 崩溃恢复、请求取消、超时和回滚路径。

只有在线级拟真收益明确大于运维复杂度，且全量发布后的观察期通过既有监控未发现明显 TTFT 回归时，才保留生产实施；本计划不为该阶段另行建设性能测试套件。

## 7. 配置与管理闭环

建议增加以下全局配置：

```yaml
codex_outbound_profile_default: codex_cli_0_149_0
codex_outbound_force_legacy: false
```

账号 `extra` 和管理员账号编辑接口只保留可选的故障隔离覆写：

```yaml
codex_outbound_profile: legacy | codex_cli_0_149_0
```

解析优先级固定为：全局 `codex_outbound_force_legacy=true` > 账号覆写 > 全局默认值。全局默认值缺省时也必须解析为 `codex_cli_0_149_0`，避免升级后因漏配继续使用旧形态。

管理要求：

- 全局默认使用 `codex_cli_0_149_0`，首次发布时同时覆盖全部现有及新增 OAuth 账号。
- 提供优先级最高的全局 `legacy` 紧急回滚开关；账号级 `legacy` 覆写只用于单账号故障隔离。
- 管理端展示实际解析后的 profile、固定 UA 模板版本和是否启用 zstd，但不展示 seed。
- 全局切换和账号级故障隔离都需要管理员审计记录。
- 回滚 profile 不清空指纹 seed、epoch、Session 状态或用户粘性 placement。
- 不提供请求级或下游用户级 profile 覆写。
- 不提供账号级严格 UA 选择；所有严格 profile 账号使用同一模板。

## 8. 可观测性

新增或复用以下低基数指标：

- `codex_outbound_profile_requests_total{profile,transport,path}`
- `codex_outbound_profile_fallback_total{profile,reason}`
- `codex_outbound_metadata_synthesized_total{request_kind}`
- `codex_outbound_forbidden_header_stripped_total{header_class}`
- `codex_outbound_zstd_duration_ms`
- `codex_outbound_zstd_ratio`
- `codex_outbound_serialize_duration_ms{transport}`
- `codex_outbound_ws_prewarm_total{result}`

同时观察既有：

- 上游 401/403/404/429。
- `server_is_overloaded` 和容量降载比例。
- HTTP TTFT、WS 首事件延迟和握手失败率。
- compact 成功率。
- WS reconnect、fallback 和连接池复用率。
- Prompt Cache 相关可观测信号（若上游提供）。

日志只能记录账号 ID、profile、transport、request kind、scope hash 截断值和错误原因，禁止记录 Authorization、原始或收敛后的完整 Session、Thread、Prompt Cache Key、turn-state、请求 body 和完整 metadata。

## 9. 测试矩阵

### 9.1 协议矩阵

| 场景 | HTTP | WS | Compact | Models |
| --- | --- | --- | --- | --- |
| 首次请求 | 必测 | 必测 | 必测 | 必测 |
| 同会话连续请求 | 必测 | 必测 | 不适用 | 不适用 |
| WS generate=false 预热 | 不适用 | 必测 | 不适用 | 不适用 |
| WS 重连 | 不适用 | 必测 | 不适用 | 不适用 |
| HTTP/WS 传输切换 | 必测 | 必测 | 不适用 | 不适用 |
| 同账号安全重试 | 必测 | 必测 | 必测 | 可选 |
| 账号 failover | 必测 | 必测 | 必测 | 可选 |
| 父子代理 | 必测 | 必测 | 可选 | 不适用 |
| Chat/Messages 兼容入口 | 必测 | 按支持情况 | 可选 | 不适用 |

### 9.2 必测断言

1. 同一账号、客户端类型、epoch 的 Session 保持现有收敛结果。
2. HTTP 与 WS 使用相同 Session 和 Prompt Cache Key。
3. 普通 HTTP/WS 的 `x-client-request-id` 等于 `thread-id`；compact 不存在该头。
4. 头、扁平 metadata 和内嵌 metadata 的所有身份 ID 一致；允许直接 metadata 头按 149 规则比 body 内嵌对象少 `tool_namespaces_info`。
5. 严格 profile 不出现 `version`、`session_id`、`conversation_id` 和 `Accept-Language`；compact 还不出现显式 `Accept`。
6. 普通 HTTP/WS 不直接发送 installation header；compact 会发送。
7. HTTP、模型已知的 WS 握手和 compact 的 routing hint 与最终 body model/service tier 一致；纯连接 preconnect 省略。
8. 普通 Responses 始终包含 `reasoning`、`store=false`、`stream=true` 和 encrypted-content include；默认不含 `stream_options`。
9. 下游原始 ID、UA、Prompt Cache Key 和不可信 metadata 不出现在 body、头和日志中。
10. 相同逻辑请求的重试保持 `turn_started_at_unix_ms` 不变；WS send timestamp 可按每次实际发送刷新但不得参与身份派生。
11. failover 后账号相关 ID 和 routing hint 按新账号的最终请求重建，用户粘性调度状态不被附带修改。
12. 所有严格 profile OAuth 账号使用完全相同的已验证 Windows CodexCLI 类别 UA；改变账号 seed、device、Session 或 epoch 不改变 UA，而 UA 相同也不改变这些字段的既有隔离作用域。
13. zstd body 可解压，解压内容与有序序列化结果一致。
14. HTTP 到 WS 的 tools 原始子树保持字段顺序、数值和 escape 形态，不发生二次 map 编码漂移。
15. WS 重连不因物理连接变化而改变逻辑快照。
16. 父子代理 Thread/Request ID 不同，Session 和 Prompt Cache Key 相同，并保留可信 parent/root turn lineage。
17. turn-state 不能跨账号携带。
18. `/models` 仅使用 `client_version` 查询参数，不存在 `Version` 请求头。
19. 非 OAuth、非 Codex 和第三方兼容上游行为不变。
20. 拟真路径不新增 Redis、数据库或上游网络访问。

### 9.3 测试范围说明

- 本计划不执行专项性能测试，不增加 microbenchmark、负载测试或压力测试。
- 不按请求尺寸采集测试环境中的 CPU 时间、内存分配、压缩比或端到端 TTFT。
- 仍保留“不得新增 Redis、数据库、上游调用、全局锁或额外连接等待”的架构审查，并在全量发布后复用现有 TTFT、WS 首事件延迟和错误率监控发现回归；这些监控不属于专项性能测试。

## 10. 全量发布与回滚

### 10.1 全量发布步骤

1. 发布前完成 HTTP、WS、compact、models、failover 和父子代理的聚焦功能测试，确认全局 `legacy` 回滚开关可用。
2. 记录发布前 `legacy` 的错误率、TTFT、WS 首事件、compact 和连接 fallback 现状，只作为回滚判断基线。
3. 一次性发布代码和配置，把全局默认 profile 设置为 `codex_cli_0_149_0`；全部现有及新增 OAuth 账号在切换后的新请求中统一生效。
4. 发布后立即执行 HTTP、WS、compact、models 和父子代理冒烟验证，并观察既有上游错误与连接指标。
5. 若触发回滚条件，直接启用全局 `legacy` 开关，不等待分批缩量，也不新增换号机制。

发布切换点前已经在途的请求允许按原 profile 完成；切换后的新请求必须统一使用同一全局 profile。禁止把同一账号的新请求随机分流到两个 profile，否则上游会看到同一账号和同一 Session 的请求形态来回跳变；这项约束不依赖也不推断真实设备身份。

### 10.2 整体回滚条件

出现以下任一条件时由管理员启用全局 `legacy` 回滚，但不自动换号：

- 上游 401/403/404 相对发布前基线显著升高。
- RPM 429、容量降载或 WS 握手失败率显著升高。
- compact 或 `/models` 出现 profile 相关失败。
- 既有监控显示 TTFT 或 WS 首事件延迟出现明显持续回归。
- metadata 身份一致性断言失败。

### 10.3 回滚

- 启用全局 `legacy` 紧急开关，使全部 OAuth 新请求整体回到旧形态；必要时可仅对单个异常账号设置 `legacy` 覆写进行故障隔离。
- 不回滚数据库、不清空账号 seed、不修改 epoch。
- 不关闭用户粘性调度，不重置 placement。
- 不删除已经建立的 turn-state provenance；旧状态按现有 TTL 自然失效。
- zstd、严格头集合、有序 JSON 和账号 UA 随 profile 一次性回退，避免产生混合形态。

## 11. 交付拆分

建议按以下独立提交交付，每个提交均可单独回滚：

1. `test(openai): 固定 CodexCLI 出站请求基线`
2. `refactor(openai): 统一 Codex 出站身份快照`
3. `fix(openai): 对齐 CodexCLI 请求头与 metadata`
4. `feat(openai): 增加 Codex 全局出站 profile 与紧急回滚`
5. `fix(openai): 对齐 CodexCLI User-Agent 与 JSON 形态`
6. `feat(openai): 启用 Codex OAuth 请求 zstd 压缩`
7. `fix(openai): 对齐 WS 预热、compact 与 models 请求`
8. 独立 ADR：`docs(openai): 评估 Codex Rust 传输栈`

不要把阶段 4 的 Rust transport 与阶段 1 至 3 合并成同一个大提交或同一次全量发布。

## 12. 完成定义

应用层改造完成需同时满足：

- 严格 profile 的 HTTP、WS、compact、models 请求通过全部 golden tests。
- 所有 OAuth 账号完成迁移后，共用经参考 Windows 环境抓包验证的固定 CodexCLI 类别 UA，不再出现 Ubuntu、macOS、浏览器或账号自定义 UA 混用；该结果只表示同类近似主机，不表示同一物理设备或同一安装。
- Session、Prompt Cache、Thread 谱系和 turn-state 安全边界无回归。
- 用户粘性调度相关文件、状态和指标没有被拟真路径写入或重置。
- 非 Codex 请求没有行为变化。
- 全量发布后的观察期通过既有监控未发现明显的错误率、TTFT 或 WS 首事件回归，不要求执行专项性能测试。
- 管理员可以查看各账号实际解析出的 profile，并通过全局开关一键整体回滚；账号级覆写只用于故障隔离。
- 无敏感标识进入日志、fixture 或指标标签。

达到以上条件后，可称为“应用层请求与目标 CodexCLI profile 高度一致”。只有阶段 4 完成并通过真实抓包验证后，才能进一步声称“线级请求几乎无差异”。
