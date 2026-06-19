# ov-computeruse Agent Architecture

## 目标

agent 是本地 Codex 执行器，不承载业务决策，也不承载 Web UI。它负责读取本机 Codex 配置、项目、历史会话和运行环境，绑定设备后与 server 建立出站 WSS 连接，接收 server 下发的 `new_session`、`resume`、`send`、`stop`、`refresh_index` 等命令，通过本机 Codex CLI 执行，并把结构化输出实时上报给 server。

当前唯一运行时是 `codex.cli`。agent 不保留 OpenAI SDK runtime，不兼容 `openai.responses` 旧语义，也不写假历史；真实历史由本机 Codex CLI/桌面版/CLI runtime 统一写入 `CODEX_HOME`。

## 边界

- agent: 本地安装、账号绑定、设备注册、本地 Codex 扫描、Codex CLI 执行、事件采集、心跳、托盘/退出能力。
- server: 用户鉴权、key/设备策略校验、agent 索引存储、命令下发、输出广播、审批、审计。
- dash: 选择设备、项目和历史会话，输入 prompt，查看实时输出和运行状态。

agent 不接受公网入站连接，不绕过 server 做设备策略。用量和扣费由本地 Codex credential 指向的中转站承担，agent 不采集、不上报、不投影 token usage。

## 安装绑定

安装组件在本地收集用户名和密码，不打开浏览器。agent 从本机 Codex auth/config 提取 `base_url`、`api_key`、模型和来源信息，生成或复用本机 `install_id`，采集设备画像，然后向 server 发起绑定。

绑定明文只在本机内存中组装，包含用户凭据、Codex credential、设备画像、请求时间和随机 nonce。传输前使用部署级 token 派生 AES-256-GCM key 加密。agent 编译时注入 server URL 和 `OV_COMPUTERUSE_TOKEN`，server 运行时使用同一个 token 解密绑定请求。

server 解密后校验 `requested_at` 是否在允许时间窗口内，并用 Redis 记录 nonce，拒绝重放的绑定 payload。随后 server 完成用户名密码校验、用户 key 可用性校验、Codex key fingerprint 与 base URL fingerprint 校验、设备策略校验，并返回 `agent_id`、`workspace_id`、`device_id`、`agent_secret`。

agent 将绑定结果写入用户级 config 目录下的 `identity.json`。`agent_secret` 只用于后续 WSS bearer token、envelope 加密和 HMAC 签名。

## 本地目录

agent 明确区分 config、data 和 cache：

| OS | Config dir | Data dir |
| --- | --- | --- |
| Windows | `%APPDATA%\ov-computeruse\agent` | `%LOCALAPPDATA%\ov-computeruse\agent` |
| macOS | `~/Library/Application Support/ov-computeruse/agent/config` | `~/Library/Application Support/ov-computeruse/agent/data` |
| Linux | `${XDG_CONFIG_HOME:-~/.config}/ov-computeruse/agent` | `${XDG_DATA_HOME:-~/.local/share}/ov-computeruse/agent` |

config 目录保存 `identity.json`、`install_id`、可选 `agent.toml`。data 目录保存 `state.db`、`logs/`、`cache/`。`state.db` 保存 Codex roots、项目索引、会话索引、runtime session 映射、run 本地状态、run event 本地事实源、history chunk 发送确认状态和 server sync cursors。agent 不保存 Codex 原始 auth/config 副本，不保存 OpenAI API key 明文。

## 本地扫描

安装时只做轻量发现和 credential 读取：发现 Codex roots、读取本地 Codex credential、完成 server 绑定，然后把 roots 写入 `state.db`。全量项目/会话扫描在 agent 运行后执行。

扫描源包括 `CODEX_HOME`、`~/.codex`、Windows `%APPDATA%/%LOCALAPPDATA%`、macOS Application Support、Linux XDG config/data 中的 Codex 目录。运行时上报项目路径、名称、git branch、`AGENTS.md`、最近活跃时间、历史会话 metadata、历史内容 chunk 和解析后的 history items。敏感文件默认只做过滤和 metadata，不上传原始内容。

## 连接和协议

agent 只建立出站 WSS：`https://server` 派生为 `wss://server/ws/agent`，不允许明文 `ws/http` 降级。

连接建立后发送：

- `agent.register`: agent、workspace、device、credential fingerprint、capabilities。
- `index.roots`、`index.projects`、`index.sessions`、`index.runtime_sessions`、`history.chunk`、`history.items`、`sync.cursor`。
- `agent.heartbeat`: 在线状态、运行中的 run、最后事件序号。
- `run.event`: Codex CLI JSONL、工具调用、审批请求、终端输出和运行状态的稳定事件。

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

server 下发的命令带 `deadline_at` 和 `expires_at`。agent 按 `deadline_at` 创建运行上下文，超时后取消 Codex CLI 进程树并上报 `run.error`，payload 标记 `deadline_exceeded`；用户主动 stop 则上报 `run.stopped`。这让“超时”和“手动停止”在 server/dash 投影里保持可区分。

运行时调用本机 `codex exec --json --skip-git-repo-check -C <project> -`。续接和发送都调用 `codex exec resume --json --all <native_session_id> -`。agent 从 stdout JSONL 解析 `thread.started`、`turn.started`、`item.started`、`item.updated`、`item.completed`、`turn.completed`、`turn.failed`、`error`，转换为稳定事件：`session.updated`、`assistant.message.delta`、`assistant.message.done`、`tool.call.started`、`tool.call.delta`、`tool.call.done`、`tool.output`、`terminal.output`、`run.status`。最终运行结果由 runs manager 上报 `run.done`、`run.error` 或 `run.stopped`。

Windows 上优先解析 native `codex.exe`，其次才是 `codex.cmd`。所有 Codex CLI 进程都通过进程树/job object 管理；如果 CLI 已经输出 `turn.completed` 但外层包装进程没有及时退出，agent 给它短暂收尾窗口后清理进程树，并按已完成事件结束。

Codex CLI 负责写真实本地历史。run 完成、失败或停止后，agent 触发索引刷新，把新生成的 session metadata、runtime session、history chunks 和 history items 上传给 server。dash 的历史展示来自这些真实本地历史，不来自 agent 自造消息。

## 发布注入

CI 从 secret 注入安装包需要的公开连接地址和部署级安装密钥：

- `OV_COMPUTERUSE_SERVER_URL`
- `OV_COMPUTERUSE_TOKEN`

用户 key、agent secret 都不能进入构建产物。Windows job 产出 Inno `.exe`，macOS job 产出 `.pkg`，Linux job 产出 `.deb/.rpm`，同时保留裸二进制归档用于 CLI installer。
