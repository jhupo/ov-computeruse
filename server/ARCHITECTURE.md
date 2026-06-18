# ov-computeruse Server Architecture

## 目标

server 是多用户控制平面，负责 agent 绑定、设备治理、Codex 索引存储、命令下发、运行事件广播、审批和 key 可用性校验。server 不执行 Codex，不保存本地项目文件系统，也不把私钥、agent secret 或用户 key 打进镜像。

## 分层

- `cmd/server`: 进程入口，加载配置，初始化 logger、Postgres、Redis、store、app，并管理 shutdown。
- `internal/config`: 环境变量解析和启动前校验。公网 URL 必须是 HTTPS；私钥只接受运行时 env/file。
- `internal/platform/*`: 基础设施适配层，包括 JSON slog、HTTP middleware、request id、Postgres pool、Redis client。
- `internal/protocol`: agent/server/dash 之间共享的稳定消息结构。dash 消费稳定事件，不耦合 SDK 私有格式。
- `internal/security`: 绑定 payload 解密、envelope 加密、HMAC 签名和 fingerprint。
- `internal/store`: Postgres repository，封装用户、key、设备、agent、索引、历史、命令、运行事件、审批、审计持久化。
- `internal/app`: 应用服务层，定义 repository ports，组合 bind service、dash session service、websocket hub 和 HTTP handlers。

业务代码只依赖 app ports，不直接穿透到 pgx/redis。平台能力只在 `platform`、`store` 和 `hub` 内部出现。

## 多用户与设备模型

用户通过 `POST /api/dash/login` 使用用户名密码登录，server 使用 bcrypt 校验 `users.password_hash`，在 Redis 写入短期 dash session token。dash 后续 HTTP/WS 请求携带 bearer token，server 解析为 `DashPrincipal`。

`OV_SERVER_BIND_USERS_JSON` 只作为初始种子路径。正式用户和 Codex key fingerprint 管理走 admin API：创建/更新用户、禁用/启用用户、创建/更新 key fingerprint、禁用/启用 key。admin API 只接受 admin principal，普通用户不能管理用户或 key。

agent 绑定时，server 校验：

- 用户名密码是否正确。
- 用户 key 是否可用。
- agent 上传的 Codex key fingerprint 和 base URL fingerprint 是否属于该用户未禁用 key。
- 设备是否允许绑定。

绑定成功后按 `user_id + install_id + machine_hash` 复用设备，按 `device_id` 复用 agent，并滚动 `agent_secret`。这样重装不会制造无限设备记录，多设备也能在同一用户下独立治理。

`users.disabled_at` 禁用整个用户：已有 dash session 不能继续通过鉴权，用户所属 agent 会被跨实例断开，新登录、新绑定、新 agent 连接和命令分发都会失败。`agents.disabled_at` 禁用单个 agent；`devices.disabled_at` 禁用同一安装设备。禁用设备后，同一 install/machine 重新安装绑定会被拒绝。禁用 agent 后，dashboard 仍能看到该 agent 并重新启用，但 agent 不能重连，也不能接收新命令或重放 pending 命令。

dash 命令接口会读取 agent 的 `user_id`，普通用户只能操作自己的 agent；管理 token 仅作为内部/运维路径，不能作为普通用户登录方案。

## 数据存储

Postgres 是事实来源：

- `users` / `user_keys`: 用户、用户禁用状态、允许使用的 Codex `base_url` fingerprint 与 key fingerprint。server 不保存明文 Codex API key。
- `devices` / `agents`: 设备画像、agent secret、workspace 归属、在线更新时间、禁用状态。
- `codex_roots` / `projects` / `codex_sessions` / `history_chunks` / `history_items`: 本地 Codex 索引和历史上传状态。
- `commands`: dash 下发命令、dispatch、ack、deadline、retry 状态。
- `run_events`: agent 上报的模型输出、工具调用、审批、终端输出、状态变化等稳定事件。
- `run_steps` / `run_messages` / `tool_calls`: 从 run events 投影出来的 dash 时间线。
- `heartbeats`: agent 运行状态快照。
- `approval_requests` / `audit_logs`: 审批和审计扩展点。

server 不做 token usage 投影、用量账本或扣费；本地 agent 实际调用的中转站负责这部分计费。server 只在绑定和命令下发前确认用户与 key 仍被允许使用。

Redis 是低延迟协调层：

- `dash:session:*`: dash 登录会话，默认 12 小时滑动续期。
- `agent:online:*`: agent 在线租约，由连接和 heartbeat 刷新。
- `ov:dash:broadcast`: 多实例 dash 事件广播。
- `ov:agent:commands`: 多实例 agent 命令路由。
- `ov:agent:disconnects`: 多实例 agent 或用户级强制断连广播。

单实例时命令直接进本机 websocket 队列；agent 在其他实例时，命令通过 Redis pub/sub 路由到持有连接的实例。pub/sub envelope 带 origin，避免本实例重复处理。

## 安全模型

安装绑定 payload 使用 `RSA-OAEP-SHA256 + AES-256-GCM` 混合加密，server 私钥只在运行时通过 `OV_SERVER_PRIVATE_KEY_PEM` 或 `OV_SERVER_PRIVATE_KEY_FILE` 注入。agent 包内只包含 server URL、公钥、key id 和公钥 fingerprint。

agent websocket 使用 per-agent `agent_secret`：

- bearer token 认证连接。
- 每个 envelope 使用 AES-256-GCM 加密 `data`。
- 每个 envelope 使用 HMAC-SHA256 签名。
- replay guard 拒绝过期、未来时间和重复 message id。

被禁用的 agent/device 在 `AgentBySecret` 入口被拒绝，不能升级 websocket。禁用操作会关闭本实例连接，并通过 Redis 广播让其他实例关闭对应连接。

被禁用用户不能继续使用旧 dash session，用户下所有在线 agent 会被强制断开。被禁用 key 不会影响历史展示，但会阻止新的 `new_session/resume/send` 执行类命令通过 credential 校验。

HTTP 请求统一经过 request id、panic recovery 和结构化日志 middleware。API 错误返回稳定 `{error:{code,message}}`，内部错误写日志，不把数据库、私钥或 agent secret 细节透给 dash。

## 命令生命周期

dash 创建命令后，server 先校验 agent 归属、agent/device access 状态、命令 payload、目标 project/session/run、agent capabilities 和 credential 可用性，再写入 `commands` 并尝试分发。

后台 dispatcher 会扫描 queued/dispatch_failed/dispatched 命令，处理过期、重新校验目标和 access 状态，然后分发。agent 重连后也会 replay pending commands。禁用 agent/device 后，这些路径统一把命令标记为 failed，不会让命令长期停留在 pending 状态。

审批决定也是普通命令：dash 更新 approval decision 时，server 生成 `command.approval_decision`，复用同一套 capability、access 和 dispatch 校验。

## 事件模型

agent 上报 `run.event`，server 按稳定事件模型持久化、投影，并按 `user_id` 广播给该用户 dash。dash 应按事件种类渲染：

- `assistant.message.delta` / `assistant.message.done`
- `tool.call` / `tool.output`
- `terminal.output`
- `diff.created`
- `approval.requested`
- `session.created` / `session.resumed` / `session.updated`
- `run.started` / `run.done` / `run.error` / `run.stopped`

这层稳定事件是“像 Codex 桌面版展示”的关键。server 不要求 dash 解析 SDK 内部 stream，也不让 agent 直接决定 UI 形态。

## 发布

server 作为 Docker 镜像发布。Git tag `server-vX.Y.Z` 触发 GitHub Actions：

1. `go test ./...`
2. buildx 构建 distroless nonroot 镜像。
3. 推送 `ghcr.io/<owner>/<repo>/server:<tag>` 和 `latest`。

构建参数只注入版本、server key id 和公钥 fingerprint。Postgres URL、Redis URL、dash token、私钥、用户 key 都是运行时 secret。
