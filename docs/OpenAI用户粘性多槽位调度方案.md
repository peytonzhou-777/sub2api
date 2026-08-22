# OpenAI 用户粘性多槽位调度实施方案

## 1. 实施基线与契约

- 状态：P1–P6 已完成并通过代码验证，P7 待分阶段发布与线上验收
- 编制日期：2026-08-22
- 代码基线：`fe594f7bf`（`main`）
- 适用平台：仅 OpenAI 上游账号
- 默认兼容值：槽位数 `1`
- 推荐生产目标：槽位数 `2`、常驻 TTL `7d`、会话活跃占用 `1h`

本文件是后续实现和审查的权威清单。实现发生语义偏移时，先修订并重新确认本文件。

槽位上限按 `(user_id, scope_key)` 生效，`scope_key` 沿用现有 `openAIUserAffinityScopeKey`。不同分组、能力或传输 lane 不可互换时，用户跨 scope 的账号总数可以超过 N；填槽时仍优先复用兼容的七日触达账号或其他 scope 常驻账号。

### 1.1 不可破坏的实现契约

必须保持以下契约：

1. 调度优先级：不可迁移协议续链 > 已建立会话绑定 > 常驻槽位 > 空槽 BestFit > 普通调度兜底。
2. 旧会话及 `previous_response_id` 子孙链固定原账号；槽位排序、增减或替换不得批量搬迁旧会话。
3. 常驻槽位、长期会话绑定、短期活跃占用、账号七日触达使用独立状态与 TTL。
4. 新会话选号前原子预留；只有首次有效上游输出后才提交永久状态。
5. 握手失败、首输出前失败、取消或客户端断开必须回滚 provisional 状态。
6. 并发、5h、RPM、居民回流和临时运行时门控必须由不同客户端请求多次重试达到阈值后才允许切号。
7. 切号先在活动常驻槽位内重放；所有槽位均满足迁移条件后才允许 BestFit 替换。
8. 不可完整重建的 `function_call_output`、严格 `previous_response_id` 和 WS 状态续链必须失败关闭。
9. 替换成功后旧槽位进入 `draining`，不接收新会话，但继续服务旧绑定。
10. 槽位上限只统计活动槽位，不统计 `draining`；替换期间实际关联账号数可以短暂超过 N。
11. `max_contact_users` 继续按 `(account_id, user_id)` 唯一用户计数，不按槽位或会话重复计数。
12. Redis 缺失或旧 schema 必须回源/安全降级，不得解释为隐私不合规、无凭据或无容量。
13. 调度缓存不保存 OAuth 凭据、原始请求/响应、原始会话标识或 turn-state blob。
14. HTTP、透传、WS、HTTP bridge 共用账号来源校验；切号不复用旧 Header、暂存响应头或旧 turn-state。
15. 系统级开关关闭后，不读取或写入多槽位状态，请求恢复普通调度。

## 2. 配置与状态模型

在现有版本化 JSON `openai_user_affinity_scheduling` 中增加：

| 字段 | 默认值 | 范围 | 语义 |
| --- | ---: | ---: | --- |
| `resident_account_slot_count` | `1` | `1–5` | 每个 `(user, scope)` 的活动槽位上限 |
| `resident_ttl_seconds` | `604800` | `86400–2592000` | 槽位和长期会话绑定的滑动 TTL |
| `conversation_active_ttl_seconds` | `3600` | `300–86400` | 会话占用槽位的滑动 TTL |

配置规则：

- 会话绑定 TTL 固定等于 `resident_ttl_seconds`，不增加第四个参数。
- 七日触达 TTL 保持独立，不随常驻 TTL 改变。
- 缺失字段补默认值，零值不得成为有效配置。
- 配置继续通过 `expected_version` 整体 CAS 更新。
- 增槽不预填；减槽把低热度槽位转 `draining`；缩短 TTL 按 `last_success_at + 新 TTL` 判断。
- 前端只在总开关启用后展示具体配置，并提供中文名称和说明。

槽位状态：

| 状态 | 计入上限 | 接收新会话 | 服务旧会话 |
| --- | --- | --- | --- |
| `provisional` | 是 | 否 | 仅当前预留 |
| `active` | 是 | 是 | 是 |
| `replacement_pending` | 是 | 否 | 是 |
| `draining` | 否 | 否 | 是 |
| `expired` / `reset` | 否 | 否 | 仅保留仍有效的严格协议绑定 |

## 3. 数据模型

使用加法式迁移，避免直接破坏现有 `(user_id, scope_key)` 单 placement 唯一约束。

| 表 | 核心字段 | 关键约束 |
| --- | --- | --- |
| `openai_user_resident_slots` | `user_id, scope_key, account_id, generation, status, admitted_at, last_success_at, expires_at, usage_score, score_updated_at, replacement_source_slot_id, provisional_token, config_version` | 同一 `(user, scope, account)` 只能有一个未结束槽位；状态 CHECK；提交/回滚校验 `slot_id + generation + token` |
| `openai_user_conversation_bindings` | `user_id, api_key_id, scope_key, conversation_hash, slot_id, account_id, slot_generation, status, active_until, expires_at, last_success_at, provisional_token` | 唯一键包含 `(user, api_key, scope, conversation_hash)`，禁止跨用户/API Key/scope 命中 |
| `openai_user_conversation_aliases` | `binding_id, alias_type, alias_hash, account_id, expires_at` | 只保存作用域化 response ID 哈希；Redis 未命中时数据库回源 |

既有表调整：

- `user_account_capacity_incidents` 增加 `conversation_hash、resident_slot_id、slot_generation`。
- `user_account_placement_events` 增加 `resident_slot_id` 和槽位生命周期事件。
- `account_user_contacts` 保持账号、用户维度。
- `user_account_placements` 兼容期保留为“首选槽位投影”，不再作为多槽位权威来源。
- 使用原生 SQL 迁移，不手工修改生成的 `backend/ent/`。

权威边界：PostgreSQL 保存槽位、绑定、alias 和迁移事实；Redis 只保存版本化热索引和短期协调状态。Redis key/schema 升级版本，新代码不读取旧 key。

## 4. 调度与状态生命周期

### 4.1 会话解析和入口顺序

逻辑会话依次由以下信号解析：已知 `previous_response_id`、显式 `session_id/conversation_id`、`prompt_cache_key/session_hash`、WS 连接内会话；均不存在时创建服务端会话 ID，首个成功 `response.id` 成为别名。

所有标识加入用户、API Key、scope 和类型后再哈希。解析结果必须携带 `conversation_hash` 和“是否可完整重建”。

统一入口：

```text
协议续链
  -> 已绑定会话
  -> 新会话常驻槽位选择
  -> 空槽 BestFit
  -> 普通调度兜底 / 无可用账号
```

当前 [openai_account_scheduler.go](../backend/internal/service/openai_account_scheduler.go) 中用户归属位于 `session_hash` 之前，实施时必须把已建立会话绑定提升到槽位调度之前。

### 4.2 已绑定会话

```text
原账号可准入
  -> 固定原账号

临时失败且未达阈值
  -> 原账号本地等待或返回可重试错误

达到阈值且上下文可重建
  -> 按槽位热度依次尝试其他 active 槽位

不可重建
  -> 返回续链账号不可用，不向其他账号发送
```

成功切换只更新当前会话绑定，不更新同槽位其他会话。

### 4.3 新会话

在 `(user_id, scope_key)` 原子调度锁内执行：

```text
存在 active 且 active_conversation_count = 0 的槽位
  -> 按热度选第一个可准入槽位

全部已有槽位被活跃会话占用，且活动槽位数 < N
  -> 排除已有活动槽位，BestFit 创建 provisional 新槽位

全部槽位被占用，且活动槽位数 = N
  -> 选择热度最高槽位
```

活跃会话占用允许立即使用次选/填槽；单次并发满、5h、RPM 或运行时失败不能立即扩槽，仍须达到容量失败阈值。

### 4.4 槽位排序

只在新会话首次成功时增加热度，后续 turn 不重复加分：

```text
effective_score = stored_score * 0.5 ^ (elapsed / resident_ttl)
new_score = effective_score + 1
```

排序依次为：`effective_score` 降序、`last_success_at` 降序、`admitted_at` 升序、`account_id` 升序。调用数、Token 和费用只供管理端查看。

### 4.5 BestFit

BestFit 只用于填槽和替换，不重排已有槽位。候选必须通过账号状态、能力、隐私、OAuth、渠道、父账号、运行时、触达上限、长冷却、额度需求和保留区准入。

有限窗口评分：

```text
remaining_time_ratio = clamp((reset_at - now) / window_duration, 0, 1)
renewal_credit = 1 - remaining_time_ratio
effective_capacity = available_ratio + renewal_credit
score = effective_capacity - predicted_demand
```

- 当前剩余额度必须先通过 `predicted_demand + reserve` 硬准入。
- 主窗口沿用 `7d_then_5h` / `5h_then_7d`；进入接近容差后优先触达用户较少者。
- `renewal_credit` 最大为 1，不使用重置临近时发散的比例。
- unlimited 独立表示；`window_minutes <= 0` 不得标准化为有限 5h。
- unknown、缺失或陈旧快照刷新/回源，不能直接判为无容量。

### 4.6 预留、成功和回滚

选号后、首次有效输出前：创建 provisional 会话占用和必要的 provisional 槽位；占用立即参与并发选号，但不刷新触达 TTL、常驻 TTL 或热度。

首次有效输出统一提交：

- 会话转 active，设置 `active_until = now + active_ttl`、`expires_at = now + resident_ttl`。
- 槽位转 active 或刷新 `last_success_at/expires_at`。
- 新会话热度增加一次；返回的 `response.id` 绑定同一会话。
- 按现有 `TouchSuccessMode` 刷新七日触达事实。

后续成功 turn 只刷新会话/槽位 TTL 和请求统计，不增加热度。失败使用 provisional token CAS 恢复状态，同时清理触达预留、由该预留启动的长冷却、Header 副本、暂存响应头和 turn-state。

## 5. 失败重放与替换

容量事故唯一维度调整为：

```text
(user_id, scope_key, conversation_hash, resident_slot_id, slot_generation)
```

继续以 `request_id_hash` 去重同一客户端请求；不同会话不得共同凑满阈值。纳入窗口的错误限于并发无法容纳、5h 暂时耗尽、RPM、居民回流和临时运行时门控；客户端取消、参数错误、基础设施错误和不可重试业务错误不得累计。

槽位内重放：

1. 达到阈值和稳定等待期后，校验会话及 slot generation 未变化。
2. 校验上下文可完整重建，并排除本轮已尝试账号。
3. 从当前账号之后按槽位热度依次尝试 active 槽位。
4. 新账号首次有效输出后 CAS 更新当前会话绑定；提交前旧绑定继续有效。

槽位替换：只有本轮已检查全部活动槽位，且每个槽位永久不可用或分别取得有效迁移授权时，才替换热度最低槽位。

```text
victim active
  -> victim replacement_pending
  -> target provisional
  -> target 首次有效输出
  -> target active + victim draining
```

target 失败时删除 provisional target 并恢复 victim。`draining` 只服务原有会话，直至会话自然过期或逐会话迁移完成。

## 6. 原子性、缓存和代码边界

统一事务锁顺序：

1. `(user_id, scope_key)` 调度锁。
2. 按 `account_id` 升序锁账号行。
3. 锁槽位行。
4. 锁会话绑定和触达预留行。
5. 重新校验槽位数、generation、账号状态、触达容量和冷却。
6. 写 provisional 并提交。

数据库提交后更新 Redis；缓存写失败由数据库读穿自愈。调度快照仍只负责选号，OAuth Token 继续由权威 Token Provider 获取。

新增逻辑优先放在用户粘性专用文件，通用调度器只调整统一入口顺序，降低后续合并 Wei-Shaw 上游的冲突：

| 范围 | 主要文件 |
| --- | --- |
| 配置/契约 | `openai_user_affinity_config.go`、`openai_user_affinity_contracts.go` |
| 选号/调度 | `openai_user_affinity_selection.go`、`openai_user_affinity_scheduling.go` |
| 成功/失败 | `openai_user_affinity_success.go`、`openai_user_affinity_reentry.go` |
| 统一入口 | `openai_account_scheduler.go` |
| 仓储/清理 | `openai_user_affinity_*_repo.go`、reconcile/cleanup 文件 |
| response 索引 | `openai_ws_state_store.go` |
| 管理端 | affinity handler/API、设置组件、居民弹窗和号池筛选 |

主要流程和公共方法使用简短中文注释；不手改生成文件；每个阶段独立中文提交，上一阶段退出条件满足后再进入下一阶段。

## 7. 分阶段实施

### P1：配置与加法式数据模型（已完成）

实施：新增配置、槽位/会话/alias 表、事故和事件字段；回填有效 placement 为槽位 1；槽位数仍强制按 1 执行。

退出：旧 JSON 无损补默认值；回填幂等；数据库与缓存读路径一致；真实 PostgreSQL 集成测试覆盖迁移及 NULL 参数；形成回滚说明。

### P2：会话优先级与双 TTL（已完成）

实施：调整为协议续链 > 会话绑定 > 槽位；拆分 7d 绑定和 1h 占用；HTTP/SSE/WS/透传/bridge 共用生命周期；alias 支持回源。

退出：槽位数 1 行为等价；占用过期后旧会话仍回原账号；首输出前失败不留状态；跨账号状态保护回归通过。

### P3：多槽位新会话调度（已完成）

实施：热度排序、原子占用、空闲槽位、按需填槽、全占用回首选、时间感知 BestFit，并保留全部现有准入门控。

退出：并发不突破 N；A 空闲选 A、A 占用选 B、A/B 占用填 C、满槽回首选；单次临时失败不扩槽；provisional 完整回滚。

### P4：会话级失败与槽位内重放（已完成）

实施：事故收紧到会话和 slot generation；不同会话隔离；达到阈值后按槽位排序重放；只迁移当前会话；不可迁移续链失败关闭。

退出：低于阈值不切号；不同会话不能共同凑阈值；重放不移动其他会话；429、取消和基础设施错误计数符合既有闭环。

### P5：槽位替换与排空（已完成）

实施：`replacement_pending`、target provisional、最低热度替换、失败恢复、`draining` 排空和后台清理；减槽复用同一机制。

退出：未检查完全部槽位不能替换；成功不影响 victim 旧会话；失败不丢 victim 或泄漏 target；generation 竞争下旧请求 CAS 失败。

### P6：管理端与审计（已完成）

实施：设置页新增三字段；用户返回 `resident_slots` 并保留首选 `placement` 投影；账号/用户双向展示槽位；当前居住匹配任一 active 槽位；新增首选账号筛选；重置整个 scope 并排除全部原活动账号；补槽位事件和指标。

退出：账号和用户视图事实一致；旧 API 消费方兼容；手动重置不破坏严格续链；前端 lint、类型检查和目标 Vitest 通过。

### P7：发布收敛（待运维执行）

1. 部署后保持槽位 `1`、TTL `14d`，验证回填和行为等价。
2. 调整 TTL 为 `7d`，槽位仍为 `1`，观察一个活跃周期。
3. 调整槽位为 `2`，观察拥挤、失败率、槽位命中和用户扩散。
4. 仅当槽位 `2` 仍不足时调整为 `3`。

退出：无新增 `pool=0`、隐私误判、无凭据或缺失额度窗口 503；旧会话命中稳定；槽位、draining、触达数和实际使用可核对；部署、迁移、验证和回滚已写入部署待办。

## 8. 测试与审查清单

必须覆盖的测试：

| 范围 | 用例 |
| --- | --- |
| 选择 | 槽位 1 等价；空闲/占用/未满/已满；热度衰减；确定性同分 |
| BestFit | reset 远近；剩余额度；触达人数；unlimited/unknown/缺失/过期窗口 |
| 会话 | `session_hash` 优先；response 子孙继承；1h 释放占用、7d 保持绑定；draining 只服务旧会话 |
| 并发 | 同用户并发预留；活动槽位上限；首输出前回滚；generation/token CAS；替换竞争 |
| 失败 | 请求去重；会话隔离；低于阈值不切；槽位内重放；全槽位后替换；不可迁移失败关闭 |
| 缓存 | Redis 缺失/旧 schema 回源；快照不带凭据；数据库有 Token、快照无 Token 路径 |
| 状态保护 | A 首输出前失败后 B 不接收 A Header、响应头或 turn-state；WS bridge 使用独立 Header |
| 管理 | placement 回填/投影；增减槽；TTL 缩短；手动重置；触达不按会话重复；总开关关闭 |

每个阶段提交前逐项审查：

- [ ] 四类状态和 TTL 未被合并。
- [ ] 已建立会话绑定优先于用户槽位。
- [ ] 永久状态只在首次有效输出后提交。
- [ ] 每个 provisional 写入都有 token/generation CAS 回滚。
- [ ] 临时容量失败仍要求不同客户端请求达到阈值。
- [ ] 容量事故按会话隔离，没有跨会话累计。
- [ ] 切号先尝试活动槽位，未提前 BestFit。
- [ ] 替换使用 `draining`，没有批量搬迁旧会话。
- [ ] 活动槽位上限在事务内重新校验，所有路径锁顺序一致。
- [ ] 缓存缺失会回源，不会转成隐私、凭据或容量错误。
- [ ] 调度缓存未携带 OAuth 密钥或原始状态。
- [ ] `window_minutes <= 0` 未被标准化为有限 5h。
- [ ] BestFit 先执行当前额度和保留区硬准入。
- [ ] HTTP、透传、WS、bridge 共用来源校验和切号清理。
- [ ] 手动重置排除全部原活动槽位并跳过七日触达优先。
- [ ] 管理端活动槽位、draining 和触达统计可相互核对。
- [ ] 新增逻辑集中在 affinity 文件，未手改生成代码或扩大无关改动。
- [ ] 聚焦测试、`go test ./internal/... -count=1`、编译检查、前端目标检查和 `git diff --check` 通过。
- [ ] 涉及部署、迁移或配置时已追加不含敏感信息的部署待办。

完成 P1–P7 且全部审查项通过后，方可认定多槽位用户粘性调度闭环落地。
