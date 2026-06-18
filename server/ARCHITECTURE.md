# ov-computeruse Server Architecture

## 目标

server 是多用户控制平面，负责 agent 绑定、设备管理、Codex 索引存储、命令下发、运行事件广播、审计和 key 可用性校验入口。server 不执行 Codex，不保存本地项目文件系统，也不把私钥或运行密钥打进镜像。

## 分层

- `cmd/server`：进程入口，加载配置，初始化 logger、Postgres、Redis、store、app，管理 shutdown。
- `internal/config`：环境变量解析和启动前校验。公网 URL 必须是 HTTPS；私钥只接受运行时 env/file。
- `internal/platform/*`：基础设施适配层，包括 JSON slog、HTTP middleware、request id、Postgres pool、Redis client。
- `internal/protocol`：agent/server/dash 之间共享的稳定消息结构。dash 渲染应消费这些稳定事件，而不是耦合 SDK 私有格式。
- `internal/security`：绑定 payload 解密和 envelope HMAC 签名/验签。
- `internal/store`：Postgres repository，封装用户、key、设备、agent、索引、历史 chunk、命令、运行事件和心跳持久化。
- `internal/app`：应用服务层，定义 repository ports，组合 bind service、dash session service、websocket hub、HTTP handlers。

业务代码只依赖 app ports，不直接穿透到 pgx/redis。平台能力只在 `platform` 和 `store/hub` 内部出现。

## 多用户模型

用户通过 `POST /api/dash/login` 使用用户名密码登录，server 使用 bcrypt 校验 `users.password_hash`，在 Redis 写入短期 dash session token。dash 后续 HTTP/WS 请求携带 bearer token，server 解析为 `DashPrincipal`。

agent 绑定时，server 校验：

- 用户名密码是否正确。
- 用户 key 是否可用。
- agent 上传的 Codex key fingerprint 是否属于该用户未禁用 key。
- 设备是否允许绑定。

绑定成功后按 `user_id + install_id + machine_hash` 复用设备，按 `device_id` 复用 agent，并滚动 `agent_secret`。这样重装不会制造无限设备垃圾，多设备也能在同一用户下独立管理。

dash 命令接口会读取 agent 的 `user_id`，普通用户只能操作自己的 agent；管理 token 仅作为内部/运维路径，不能作为普通用户登录方案。

## 数据存储

Postgres 是事实来源：

- `users` / `user_keys`：用户和允许使用的 Codex `base_url` fingerprint 与 key fingerprint。
- `devices` / `agents`：设备画像、agent secret、workspace 归属、在线更新时间。
- `codex_roots` / `projects` / `codex_sessions` / `history_chunks`：本地 Codex 索引和历史上传状态。
- `commands`：dash 下发命令和 ack 状态。
- `run_events`：agent 上报的模型输出、工具调用、审批、状态变化等稳定事件。
- `heartbeats`：agent 运行状态快照。
- `approval_requests` / `audit_logs`：审批和审计扩展点。

server 不做 token usage 投影、用量账本或扣费；本地 agent 实际调用的中转站负责这部分计费。server 只在绑定和命令下发前确认用户与 key 仍被允许使用。

Redis 是低延迟协调层：

- `dash:session:*`：dash 登录会话，默认 12 小时滑动续期。
- `agent:online:*`：agent 在线租约，由连接和 heartbeat 刷新。
- `ov:dash:broadcast`：多实例 dash 事件广播。
- `ov:agent:commands`：多实例 agent 命令路由。

单实例时命令直接进本机 websocket 队列；agent 在其他实例时，命令通过 Redis pub/sub 路由到持有连接的实例。pub/sub envelope 带 origin，避免本实例重复广播。

## 安全模型

安装绑定 payload 使用 `RSA-OAEP-SHA256 + AES-256-GCM` 混合加密，server 私钥只在运行时通过 `OV_SERVER_PRIVATE_KEY_PEM` 或 `OV_SERVER_PRIVATE_KEY_FILE` 注入。agent 包内只包含 server URL、公钥、key id 和公钥 fingerprint。

agent websocket 使用 per-agent `agent_secret`：

- bearer token 认证连接。
- 每个 envelope 用 HMAC-SHA256 签名。
- server 到 agent 的命令同样签名，agent 拒绝无签名或签名错误消息。

HTTP 请求统一经过 request id、panic recovery 和结构化日志 middleware。API 错误返回稳定 `{error:{code,message}}`，内部错误写日志，不把数据库/密钥细节透给 dash。

## 事件模型

agent 上报 `run.event`，server 原样持久化并按 `user_id` 广播给该用户 dash。dash 应按事件种类渲染：

- `assistant.message.delta` / `assistant.message.done`
- `tool.call` / `tool.output`
- `terminal.output`
- `diff.created`
- `approval.requested`
- `run.started` / `run.done` / `run.error` / `run.stopped`

这层稳定事件是“像 Codex 桌面版展示”的关键。server 不要求 dash 解析 SDK 内部 stream，也不让 agent 直接决定 UI 形态。

## 发布

server 作为 Docker 镜像发布。Git tag `server-vX.Y.Z` 触发 GitHub Actions：

1. `go test ./...`
2. buildx 构建 distroless nonroot 镜像。
3. 推送 `ghcr.io/<owner>/<repo>/server:<tag>` 和 `latest`。

构建参数只注入版本、server key id 和公钥 fingerprint。Postgres URL、Redis URL、dash token、私钥、用户 key 都是运行时 secret。
