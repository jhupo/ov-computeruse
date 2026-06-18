# ov-computeruse Agent Architecture

## 目标

agent 是本地 Codex 执行器，不承载业务决策，也不承载 Web UI。它负责读取本机 Codex 配置、项目、历史会话和运行环境，绑定设备后与 server 建立出站 WSS 连接，接收 server 下发的 `new_session/resume/send/stop/refresh_index` 等命令，通过 OpenAI Go SDK 执行，并把结构化输出实时上报给 server。

## 边界

- agent: 本地安装、账号绑定、设备注册、本地 Codex 扫描、SDK 执行、事件采集、心跳、托盘/退出能力。
- server: 用户鉴权、key/设备策略校验、agent 索引存储、命令下发、输出广播、审批。
- dash: 选择设备、项目和历史会话，输入 prompt，查看实时输出和运行状态。

agent 不保存服务端私钥，不接受公网入站连接，不绕过 server 做设备策略。用量和扣费由本地 Codex credential 指向的中转站承担，agent 不采集、不上报、不投影 token usage。

## 安装绑定

安装组件在本地收集用户名和密码，不打开浏览器。agent 从本机 Codex auth/config 提取 `base_url`、`api_key`、模型和来源信息，生成或复用本机 `install_id`，采集设备画像，然后向 server 发起绑定。

绑定明文只在本机内存中组装，包含用户凭据、Codex credential、设备画像、请求时间和随机 nonce。传输前使用 server 公钥混合加密：AES-256-GCM 加密内容，RSA-OAEP-SHA256 加密随机内容密钥。agent 编译时注入 server URL、公钥 key id、公钥和 fingerprint；安装时先校验 fingerprint，防止公钥被替换。

server 解密后校验 `requested_at` 是否在允许时间窗口内，并用 Redis 记录 nonce，拒绝重放的绑定 payload。随后 server 完成用户名密码校验、用户 key 可用性校验、Codex key fingerprint 与 base URL fingerprint 校验、设备策略校验，并返回 `agent_id`、`workspace_id`、`device_id`、`agent_secret`。

agent 将绑定结果写入用户级 config 目录下的 `identity.json`。`agent_secret` 只用于后续 WSS bearer token、envelope 加密和 HMAC 签名。

## 本地目录

agent 明确区分 config、data 和 cache：

- config: `identity.json`、`install_id`、可选 `agent.toml`，存放小而关键的身份和覆盖配置。
- data: `state.db`、`logs/`、`cache/`，存放可增长、可重建或可同步的运行状态。
- logs: `agent.log`，JSON 结构化日志，同时输出到 stdout，便于后台服务排障。

默认路径：

| OS | Config dir | Data dir |
| --- | --- | --- |
| Windows | `%APPDATA%\ov-computeruse\agent` | `%LOCALAPPDATA%\ov-computeruse\agent` |
| macOS | `~/Library/Application Support/ov-computeruse/agent/config` | `~/Library/Application Support/ov-computeruse/agent/data` |
| Linux | `${XDG_CONFIG_HOME:-~/.config}/ov-computeruse/agent` | `${XDG_DATA_HOME:-~/.local/share}/ov-computeruse/agent` |

`state.db` 保存 Codex roots、项目索引、会话索引、runtime session 映射、run 本地状态投影、run message/tool-call 投影、run event 本地事实源 outbox、history chunk 发送确认状态和 server sync cursors。agent 启动时会把未终止的本地 run 收敛为 interrupted 事件并进入 outbox，避免崩溃后永久停留在 running。它不保存 Codex 原始 auth/config 副本，不保存 OpenAI API key 明文。

## 本地扫描

安装时只做轻量发现和 credential 读取：发现 Codex roots、读取本地 Codex credential、完成 server 绑定，然后把 roots 写入 `state.db`。全量项目/会话扫描在 agent 运行后执行。

扫描源包括 `CODEX_HOME`、`~/.codex`、Windows `%APPDATA%/%LOCALAPPDATA%`、macOS Application Support、Linux XDG config/data 中的 Codex 目录。运行时上报项目路径、名称、git branch、`AGENTS.md`、最近活跃时间、历史会话 metadata 和历史内容 chunk。敏感文件默认只做过滤和 metadata，不上传原始内容。

## 连接和协议

agent 只建立出站 WSS：`https://server` 派生为 `wss://server/ws/agent`，不允许明文 `ws/http` 降级。

连接建立后发送：

- `agent.register`: agent、workspace、device、credential fingerprint、capabilities。
- `index.roots`、`index.projects`、`index.sessions`、`history.chunk`、`history.items`、`sync.cursor`。
- `agent.heartbeat`: 在线状态、运行中的 run、最后事件序号。
- `run.event`: SDK stream、工具调用、审批请求、终端输出和运行状态的稳定事件。

server 下发：

- `command.new_session`
- `command.resume`
- `command.send`
- `command.stop`
- `command.refresh_index`
- `command.approval_decision`
- `history.chunk.ack`
- `run.event.ack`

所有 envelope 包含 `message_id`、`agent_id`、`device_id`、`seq`、`type`、`timestamp`、`data`、`signature`。agent 到 server、server 到 agent 都用 `agent_secret` 派生 AES-256-GCM 加密 payload，并用 HMAC-SHA256 签名校验。双方都做 timestamp 和 replay guard。

## 运行模型

一个 agent 可以索引多个本地项目和多个历史会话。agent 可以按 `max_concurrent_runs` 同时执行多个不同项目或不同会话的 run；同一个 `session_id` 始终串行，避免多个远程 prompt 同时续写同一条 Codex 历史上下文。

server 下发的命令带 `deadline_at` 和 `expires_at`。agent 按 `deadline_at` 创建运行上下文，超时后取消 SDK 调用并上报 `run.error`，payload 标记 `deadline_exceeded`；用户主动 stop 则上报 `run.stopped`。这让“超时”和“手动停止”在 server/dash 投影里保持可区分。

运行时通过 OpenAI Go SDK 调用 Responses API，使用本地 Codex credential 的 `base_url` 和 `api_key`。agent 将 SDK stream 转成稳定事件：`assistant.message.delta`、`assistant.message.done`、`tool.call`、`tool.output`、`terminal.output`、`diff.created`、`approval.requested`、`run.status`、`run.completed`、`run.failed`。dash 消费这些事件并按 Codex 桌面版体验渲染。

本地 shell 工具默认关闭。启用 `allow_local_shell` 后，agent 只声明 Responses local shell tool，并且只在 server 审批通过后执行。工作目录必须位于已索引项目根内，路径解析会处理符号链接和 Windows junction，命令有超时、输出限制和进程组清理。

## 发布注入

CI 只注入公开或可给客户端的绑定信息：

- `OV_SERVER_URL`
- `OV_SERVER_KEY_ID`
- `OV_SERVER_PUBLIC_KEY_B64`
- `OV_SERVER_PUBLIC_KEY_FINGERPRINT`

server 私钥、用户 key、agent secret 都不能进入构建产物。Windows job 产出 Inno `.exe`，macOS job 产出 `.pkg`，Linux job 产出 `.deb/.rpm`，同时保留裸二进制归档用于 CLI installer。
