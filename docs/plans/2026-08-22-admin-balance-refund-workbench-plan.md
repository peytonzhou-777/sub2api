# 管理员余额清退工作台实施落地方案

## 1. 实施基线

- 状态：主体实现完成，待真实 PostgreSQL 与生产只读影子验收
- 编制日期：2026-08-22
- 代码基线：`fe594f7bf`（`main`）
- 菜单名称：`余额清退`
- 前端路由：`/admin/balance-refunds`
- 后端接口前缀：`/api/v1/admin/payment/account-refunds`

本文件是实现与审查的共同依据。实施中如需改变资金口径、状态转换或网关安全约束，必须先修订本文件。

## 2. 不可偏离的技术契约

1. 余额清退使用独立管理员页面，不进入支付概览，也不接受 7/30/90 天筛选。
2. 用户单笔试算、管理员列表、管理员详情和提交前复算必须调用同一权威计算器；禁止在 SQL、handler 或前端复制公式。
3. `recharge_bonus_rate` 是百分数，统一执行 `rate / 100`；活动退款系数为 `1 / (1 + rate / 100)`，不得硬编码 `0.8333`。
4. 永久余额按“活动永久余额先消耗、原价永久余额后消耗”归属，优先保留原价余额。
5. 历史订单纳入 `COMPLETED`、`PARTIALLY_REFUNDED`、`REFUNDED`；剩余本金容量为 `amount - refund_amount`。
6. 非活动限时额度和异常赠额不退款，成功清退时清空；有效充值赠额按关联活动订单参与计算。
7. 用户退款先按币种四舍五入到最小货币单位，再守恒分配至原订单；不得超过各订单剩余网关退款容量。
8. 多币种分别展示和汇总，禁止跨币种相加。
9. “金额可核算”与“能否自动退款”必须分离。关闭用户自助退款不能导致真实退款负债从统计中消失。
10. GET 接口只读。排空检查、网关查询、状态推进和本地收尾均使用 POST 动作接口。
11. 已提交但结果未知的退款禁止自动重试。EasyPay 等不支持幂等键或退款查询的渠道必须进入人工核验。
12. 人工核验不得手填金额覆盖不闭合试算；只能确认一条已计算路由的外部结果，且必须记录依据。
13. 管理员发起清退时使用计费栅栏和 `refund_locked`；成功或取消后恢复发起前用户状态。终态遗留锁必须有幂等恢复入口。
14. 一期不提供批量退款，不新增历史资金归属表，不调用真实退款接口做开发验证。

## 3. 后端目标结构

### 3.1 文件与职责

```text
backend/internal/service/account_refund.go
  清退状态机、网关执行、终态处理及单用户共用报价装配

backend/internal/service/account_refund_admin.go
  管理员批量装配、摘要、列表、详情、动作权限和人工核验

backend/internal/handler/admin/payment_handler.go
  参数解析、管理员 actor、响应映射

backend/internal/server/routes/payment.go
  管理员余额清退路由
```

核心调用关系：

```text
单用户加载 ─┐
             ├─> CalculateAccountRefundQuote ─> 用户试算/管理员详情/提交前复算
批量加载 ───┘

管理员动作 ─> 状态版本校验 ─> 既有状态机 ─> 网关或本地收尾 ─> 审计事件
```

### 3.2 统一试算模型

将当前 `AccountRefundQuote.Eligible` 拆为独立维度，并保留旧字段的兼容映射：

| 字段 | 值 | 用途 |
| --- | --- | --- |
| `calculation_status` | `verified` / `manual_review` / `none` | 金额能否按权威规则闭合 |
| `self_service_eligible` | `true` / `false` | 用户是否可自行清退 |
| `admin_execution_mode` | `automatic` / `manual_external` / `blocked` | 管理员自动退款、线下处理或禁止处理 |
| `review_reason_code` | 见状态章节 | 人工核验原因 |
| `failure_stage` | `pre_gateway` / `gateway` / `post_gateway` | 限制后续动作 |

支付能力判定：

- 用户自助：支付实例同时满足 `refund_enabled` 和 `allow_user_refund`。
- 管理员自动清退：只要求 `refund_enabled`，不要求 `allow_user_refund`。
- 金额已闭合但无法自动退款：`manual_external`。
- 订单、赠额、币种或容量无法闭合：`blocked`，不生成可执行金额。

### 3.3 批量加载

管理员摘要和列表必须使用固定查询次数：

1. 加载候选用户及账号状态。
2. 批量加载余额充值历史订单。
3. 批量加载限时额度和充值赠额。
4. 批量加载涉及的支付实例能力。
5. 批量加载每个用户最新清退审计记录。
6. 在内存按用户分组后调用统一纯计算器。

禁止逐用户、逐订单查询支付实例。测试必须断言查询次数不随用户数线性增长。

当前阶段继续使用 `payment_audit_logs` 保存清退快照，不新增业务状态表。上线前对现网规模执行 `EXPLAIN ANALYZE`；只有查询计划不足时才通过 SQL 迁移增加局部索引，不手工修改生成的 `ent/` 文件。

## 4. 统计与列表口径

### 4.1 摘要

```text
refundable_totals          按币种的当前待退总额
automatic_totals           可自动原路退金额
manual_external_totals     需线下原路处理金额
refundable_users           有已核验待退金额的用户数
automatic_users            可快捷清退用户数
processing_users           非终态清退用户数
manual_review_users        需人工核验用户数
calculated_at              本次快照时间
```

金额规则：

- 无活跃清退：使用当前权威试算的 `gateway_totals`。
- 有活跃清退：使用锁定报价中尚未成功路由的 `GatewayRefund`。
- 已成功外部退款不得重复计入当前待退金额。
- `manual_external` 金额计入退款负债并单独汇总。
- `calculation_status=manual_review` 不猜测金额，只计入核验人数。
- 先完成用户级、订单级分配和舍入，再汇总全局金额。

### 4.2 列表与详情

列表标签：`refundable`、`processing`、`manual_review`、`completed`、`all`。

列表行仅返回用户摘要、余额构成、分币种待退金额、核算状态、流程状态、更新时间、`state_revision` 和 `available_actions`。完整报价、订单路由和审计时间线只在详情接口返回。

当前资格与最近清退终态分开表达。例如，取消后余额仍存在的用户可以同时显示“当前可清退”和“上次已取消”。

## 5. 状态、动作与恢复

后端是动作权限的唯一权威，前端只渲染 `available_actions`。

| 状态/原因 | 可用动作 | 关键限制 |
| --- | --- | --- |
| 已核算且无活跃清退 | `start` | 先校验报价和状态版本 |
| `draining` | `advance`、`cancel` | 未排空不得调用网关 |
| `ready_to_confirm` | `confirm`、`cancel` | 使用锁定后的最终报价 |
| `failed` | `continue`、条件满足时 `cancel` | 仅重试明确失败路由 |
| `partial_external_success` | `continue` | 不得取消已部分成功清退 |
| `submitting` / `pending` 且可查询 | `advance` | 只查询，不重复提交 |
| `submitting` / `pending` 且不可查询 | 转 `manual_review` | 禁止自动重试 |
| `manual_review: gateway_unknown` | `reconcile` | 逐订单确认外部成功或失败 |
| `manual_review: manual_external_required` | `reconcile` | 先在线下原渠道处理并留证 |
| `manual_review: quote_inconsistent` | `recalculate`、安全时 `cancel` | 禁止人工输入金额 |
| `manual_review: finalize_failed` | `finalize` | 只重试本地收尾，不调用网关 |
| 终态但仍 `refund_locked` | `restore-access` | 幂等恢复，不改变退款事实 |

结构化原因码：

```text
quote_inconsistent
gateway_unknown
provider_unavailable
gateway_query_failed
manual_external_required
finalize_failed
access_restore_failed
legacy_unknown
```

历史记录缺少原因码时由后端保守归类；无法可靠判断时使用 `legacy_unknown`，不得开放可能重复退款的动作。

管理员快捷清退保持两个资金确认点：

1. `start`：确认用户、金额和待清空额度后建立计费栅栏并锁定用户。
2. `confirm`：排空完成后再次确认分币种到账金额，随后才调用网关。

管理员入口不签发用户退款专用 Token。允许为 `active`、`disabled` 用户发起，终态恢复原状态；软删除用户和已有活跃清退的用户不可重复发起。

## 6. 并发、幂等与审计

### 6.1 状态版本

- 使用最新 `payment_audit_logs.id` 作为 `state_revision`。
- 所有写接口提交 `expected_state_revision`。
- 事务内锁定用户、订单和最新清退记录后再次比较版本。
- 版本不一致返回 `409 REFUND_STATE_CHANGED`，不执行后续动作。

### 6.2 幂等与未知结果

- `start` 使用 `Idempotency-Key`，同一管理员、用户和请求键只能创建一笔清退。
- 网关路由继续使用稳定 `RequestID`。
- 已成功路由永不再次提交。
- 可查询渠道在恢复时只执行 `QueryRefund`。
- 不可查询渠道一旦提交状态不确定，立即转人工核验；服务重启、页面刷新和 `continue` 均不得再次提交。
- `finalize` 只处理本地订单、余额和额度终态，禁止进入网关执行路径。

### 6.3 操作人

新增统一 `AccountRefundActor`：

```text
actor_type: user | admin | system
actor_id
actor_label
request_id
```

修正现有状态日志固定写 `user:<id>` 的行为。每个事件记录操作人、动作前后状态、目标订单、结果、原因码和核验依据。

人工核验请求包含 `order_id`、`outcome`、`external_refund_id`、`verified_at`、`evidence`、`note`。成功时外部退款号必填；渠道确实不提供时必须在依据中说明。核验信息不得包含密码、Token 或完整支付凭据。

## 7. API 清单

### 7.1 只读

```text
GET /api/v1/admin/payment/account-refunds/summary
GET /api/v1/admin/payment/account-refunds
GET /api/v1/admin/payment/account-refunds/:user_id
```

列表支持标签、具体状态、币种、用户搜索、排序和分页。GET 不推进排空或网关状态。

### 7.2 动作

```text
POST /api/v1/admin/payment/account-refunds/:user_id/start
POST /api/v1/admin/payment/account-refunds/:user_id/advance
POST /api/v1/admin/payment/account-refunds/:user_id/confirm
POST /api/v1/admin/payment/account-refunds/:user_id/continue
POST /api/v1/admin/payment/account-refunds/:user_id/recalculate
POST /api/v1/admin/payment/account-refunds/:user_id/reconcile
POST /api/v1/admin/payment/account-refunds/:user_id/finalize
POST /api/v1/admin/payment/account-refunds/:user_id/cancel
POST /api/v1/admin/payment/account-refunds/:user_id/restore-access
```

所有动作携带 `expected_state_revision`；涉及报价时同时携带 `quote_hash`。响应返回最新完整详情，前端随后刷新摘要和当前列表页。

## 8. 前端落点

```text
frontend/src/views/admin/orders/AdminBalanceRefundsView.vue
frontend/src/components/admin/payment/refunds/BalanceRefundStats.vue
frontend/src/components/admin/payment/refunds/BalanceRefundTable.vue
frontend/src/components/admin/payment/refunds/BalanceRefundDetailDrawer.vue
frontend/src/components/admin/payment/refunds/BalanceRefundActionDialog.vue
frontend/src/components/admin/payment/refunds/BalanceRefundReconcileDialog.vue
frontend/src/api/admin/accountRefunds.ts
frontend/src/types/accountRefund.ts
```

- 在侧栏“订单管理”子菜单中加入“余额清退”，位置在“订单管理”之后、“订阅计划”之前。
- 路由使用 `requiresAdmin: true`、`requiresPayment: true`，导航键为 `nav.balanceRefunds`。
- 顶部显示分币种摘要和刷新按钮，不显示日期筛选。
- 首次进入和手动刷新执行全量试算；仅对活跃清退做轻量状态轮询。
- 动作按钮完全依据 `available_actions`。
- 所有资金动作二次确认并显示用户、币种金额、待清空额度及是否调用网关。
- 动作期间锁定该用户操作；收到 `409` 时刷新详情。
- 请求失败显示 `--`，不得用 `0.00` 掩盖统计失败。
- 标签、搜索、排序和分页写入 URL query。

## 9. 分阶段实施

### 阶段 A：统一试算基础

- [x] 建立单用户/批量共用的数据输入和纯计算器，避免复制资金公式。
- [x] 集中百分数转换、舍入、容量分配和多币种汇总。
- [x] 拆分核算状态、自助资格和管理员执行方式。
- [x] 保持现有用户侧报价 JSON 和行为兼容。
- [x] 补齐 20%、25%、混合充值、赠额消费/过期、部分退款、多订单和多币种测试。

完成门禁：现有用户侧退款测试全部通过；新计算器与改造前单用户报价逐例一致。

### 阶段 B：管理员只读工作台

- [x] 实现批量数据加载、摘要、分页列表和详情接口。
- [x] 实现资格状态与流程状态合并、剩余待退金额去重和 `available_actions`。
- [x] 新增管理员路由、侧栏菜单、页面、摘要、表格和详情抽屉。
- [x] 增加查询次数断言和前端只读页面测试。
- [ ] 使用现网只读快照执行批量报价与逐用户单笔报价对比。

完成门禁：逐用户金额、订单路由和全局汇总差异均为 `0.00`；列表加载无 N+1；未调用支付网关。

### 阶段 C：管理员快捷清退

- [x] 引入 `AccountRefundActor`、`state_revision` 和发起幂等键。
- [x] 抽取 user/admin 共用锁定流程，管理员入口不签发用户 Token。
- [x] 实现 `start`、`advance`、`confirm`、`continue`、`cancel`。
- [x] 实现详情抽屉内的两次资金确认。
- [x] 覆盖 active/disabled 状态恢复、排空、并发版本保护和部分成功。

完成门禁：并发管理员操作只产生一次有效推进；明确失败可恢复，未知结果不可重试。

### 阶段 D：人工核验与恢复

- [x] 增加 `review_reason_code`、`failure_stage` 和历史记录兼容归类。
- [x] 扩展逐路由核验字段和管理员审计。
- [x] 实现 `recalculate`、`reconcile`、`finalize`、`restore-access`。
- [x] 覆盖线下原路退款登记、网关未知、试算不闭合、本地收尾失败和终态遗留锁。
- [x] 验证核验后列表、摘要、详情和时间线立即刷新。

完成门禁：所有 `manual_review` 原因均有安全出口或明确禁止项，不存在只能查看而无法处理的状态。

### 阶段 E：整体验证与上线准备

- [x] 运行余额清退、支付退款和 EasyPay 相关 Go 测试。
- [x] 运行管理员余额清退 Vitest、类型检查和 lint。
- [ ] 运行真实 PostgreSQL 集成测试，覆盖事务锁、状态版本和可空参数。
- [ ] 在现网只读快照上再次完成影子报价，不调用真实支付接口。
- [ ] 执行 `EXPLAIN ANALYZE`，按结果决定是否增加局部索引迁移。
- [x] 记录目标版本、迁移、发布、重启、验证和回滚步骤到运维部署待办。

完成门禁：所有验收项通过后方可进入部署流程；开发线程不得部署或修改生产数据。

## 10. 实施与审查门禁

### 金额正确性

- [x] 所有入口共用一个权威计算器。
- [x] 生产比例 `20/25` 单位解释正确，无硬编码退款系数。
- [x] 用户级金额先舍入，订单级分配严格守恒且不超容量。
- [x] 多币种未混加，已成功路由未重复计入待退金额。
- [x] 无法闭合用户未被猜测金额。
- [ ] 现网影子报价的批量与逐用户结果差异为 `0.00`。

### 状态与网关安全

- [x] GET 接口无状态推进或外部调用。
- [x] 所有写动作校验 `state_revision`，发起操作具备幂等键。
- [x] `submitting` 重启后只查询或转人工核验。
- [x] EasyPay 未知结果在任何路径下均不会重复提交。
- [x] 部分成功后只处理剩余路由，不能取消整笔清退。
- [x] `finalize` 不进入支付网关。
- [x] 成功、取消、打赏及异常恢复后用户不会永久停留在 `refund_locked`。

### 管理员与审计

- [x] 管理员可完成发起、确认、继续、取消、人工核验、本地收尾和访问恢复。
- [x] 前端只使用后端 `available_actions`，不复制状态机。
- [x] 人工核验有外部结果、时间、依据和操作人。
- [x] 用户、管理员和系统事件的 operator 正确。
- [x] 未实现批量退款，未提供人工覆盖金额入口。

### 工程质量

- [x] 批量查询次数固定，无逐用户/逐订单 N+1。
- [x] 未手工修改生成的 `ent/` 文件。
- [ ] 后端单元、集成测试及前端测试、类型检查、lint 通过。
- [x] 开发验证未调用真实退款接口，未修改生产数据。
