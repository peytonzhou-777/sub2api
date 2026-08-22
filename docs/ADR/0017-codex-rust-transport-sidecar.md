# ADR 0017：暂缓引入 Codex Rust 传输 Sidecar

## 状态

已决策，暂不实施。

## 背景

应用层严格 profile 已以官方 `rust-v0.149.0` 和 Windows Terminal 实际捕获为基线，统一 OAuth Codex 的 User-Agent、请求头、metadata、JSON 顶层顺序、zstd、WS、compact 和 models 形态。但 sub2api 仍使用 Go 的 TLS、HTTP/2 和 WebSocket 实现，无法宣称与 CodexCLI 的 rustls/hyper/tungstenite 线级指纹一致。

可选方案是在 Go 服务旁运行常驻 Rust sidecar，由 sidecar 负责 Codex 上游连接池、HTTP/2、WebSocket、取消和超时；Go 仅通过本机 IPC 传递经过严格 profile 处理的请求。

## 决策

当前不引入 Rust sidecar，继续使用现有 Go transport，并把“高度一致”的完成边界限制在应用层。

原因：

1. 当前核心目标是降低多用户共享的应用层可见特征，固定 UA、统一 Session/Prompt Cache、严格头集合和 metadata 已直接覆盖主要差异。
2. sidecar 会新增进程部署、IPC、连接所有权、崩溃恢复、取消传播和双传输回滚复杂度。
3. 使用浏览器 uTLS profile 不能代表 CodexCLI 的 rustls，不作为折中方案。
4. 为拟真关闭 HTTP/2 会损害连接复用和 TTFT，也不符合官方客户端行为。
5. 当前测试站点直接全量应用层 profile；先观察既有错误率、TTFT、WS 首事件和 fallback 指标，再判断线级收益是否值得承担运维成本。

## 后续重新评估条件

仅在以下条件同时满足时重新开启 sidecar 原型：

- 应用层 profile 已稳定运行，且上游仍存在可重复、可归因于线级差异的问题。
- 能用脱敏抓包对比 TLS ClientHello、ALPN、HTTP/2 SETTINGS/窗口/HPACK、WS 握手与分帧。
- sidecar 具备请求取消、超时、连接池、崩溃恢复和 Go transport 一键回滚设计。
- 部署线程接受新增二进制、健康检查和版本协同成本。

重新评估也不采用专项负载或性能测试作为本 ADR 的前置；性能影响继续通过现有生产监控观察。

## 影响

- 可以表述“OAuth Codex 应用层请求与目标 CodexCLI profile 高度一致”。
- 不能表述“TLS/HTTP2/WS 线级请求与真实 CodexCLI 无差异”。
- 本决策不修改用户粘性调度、账号 placement、指纹 seed/epoch 或 turn-state 来源保护。
