# ov-computeruse agent

Local Codex executor for ov-computeruse. The full design is in [ARCHITECTURE.md](ARCHITECTURE.md).

## Install and bind

Windows uses the Inno Setup installer:

```powershell
ov-agent-setup-windows-amd64.exe
```

The installer shows a local login page, validates the user with the server, binds the device, then installs and starts the current-user Scheduled Task. Login secrets are not passed through command-line arguments.

Archive installs use the local script next to the binary:

```powershell
.\install.ps1
```

```sh
./install.sh
```

The script runs `ov-agent install`, prompts locally, binds the device, copies the binary, and registers current-user startup.

macOS `.pkg` and Linux `.deb/.rpm` packages are non-interactive distribution packages. They install the binary and service template; bind as the desktop user with:

```sh
ov-agent install
```

## Local files

The agent keeps identity and durable indexes separate:

- config dir: `identity.json`, `install_id`, optional `agent.toml`.
- data dir: `state.db`, `logs/`, `cache/`.

Default paths:

| OS | Config dir | Data dir |
| --- | --- | --- |
| Windows | `%APPDATA%\ov-computeruse\agent` | `%LOCALAPPDATA%\ov-computeruse\agent` |
| macOS | `~/Library/Application Support/ov-computeruse/agent/config` | `~/Library/Application Support/ov-computeruse/agent/data` |
| Linux | `${XDG_CONFIG_HOME:-~/.config}/ov-computeruse/agent` | `${XDG_DATA_HOME:-~/.local/share}/ov-computeruse/agent` |

`state.db` stores discovered Codex roots, project/session indexes, runtime session mappings, the local run event outbox, history chunk upload state, and sync cursors. It does not store raw API keys or copied Codex auth files.
On Windows, `identity.json` is protected with DPAPI before it is written to disk; other platforms store it with user-only file permissions.

`agent.toml` or `agent.json` can define the same operational settings as environment variables and flags, for example:

```toml
server_url = "https://api.example.com"
codex_home = "C:/Users/me/.codex"
scan_roots = ["C:/Users/me/.codex", "D:/work"]
upload_history = false
allow_sensitive = false
```

## Run

```sh
ov-agent run
```

Runtime behavior:

- scan local Codex config, projects, sessions, history, worktrees, and git metadata.
- connect to server over WSS only.
- encrypt every agent/server envelope payload with AES-256-GCM derived from the per-agent secret, then sign the encrypted envelope.
- send register, project/session index, history chunks, heartbeat, and run events.
- upload displayable user/assistant history messages for dash history views; raw history chunks require explicit `--upload-history`.
- receive server commands for new session, resume, send, stop, and index refresh.
- stream structured events for dash to render like Codex desktop.

## Build

GitHub Actions builds:

- raw archives: Windows zip, macOS/Linux tar.gz.
- Windows Inno Setup `.exe`.
- macOS `.pkg`.
- Linux `.deb` and `.rpm`.

Required repository secrets:

- `OV_SERVER_URL`
- `OV_SERVER_KEY_ID`
- `OV_SERVER_PUBLIC_KEY_B64`
- `OV_SERVER_PUBLIC_KEY_FINGERPRINT`

The build injects only server URL and public-key metadata. Server private keys, user keys, and per-device `agent_secret` are never included in the package.
