#!/usr/bin/env sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
AGENT="$ROOT/ov-agent"
if [ ! -x "$AGENT" ]; then
  CANDIDATE="$(find "$ROOT" -maxdepth 1 -type f -name 'ov-agent-*' | head -n 1)"
  if [ -n "$CANDIDATE" ]; then
    AGENT="$CANDIDATE"
  fi
fi
if [ ! -f "$AGENT" ]; then
  echo "ov-agent executable not found next to install.sh" >&2
  exit 1
fi

case "$(uname -s)" in
  Darwin)
    INSTALL_DIR="$HOME/Library/Application Support/ov-computeruse/agent"
    PLIST="$HOME/Library/LaunchAgents/com.ov-computeruse.agent.plist"
    ;;
  *)
    INSTALL_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/ov-computeruse/agent"
    SYSTEMD_DIR="$HOME/.config/systemd/user"
    ;;
esac

mkdir -p "$INSTALL_DIR"
"$AGENT" install "$@"

cp "$AGENT" "$INSTALL_DIR/ov-agent"
chmod 700 "$INSTALL_DIR"
chmod 755 "$INSTALL_DIR/ov-agent"

case "$(uname -s)" in
  Darwin)
    mkdir -p "$(dirname "$PLIST")"
    cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.ov-computeruse.agent</string>
  <key>ProgramArguments</key>
  <array><string>$INSTALL_DIR/ov-agent</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict>
</plist>
EOF
    launchctl unload "$PLIST" >/dev/null 2>&1 || true
    launchctl load "$PLIST"
    ;;
  *)
    mkdir -p "$SYSTEMD_DIR"
    cat > "$SYSTEMD_DIR/ov-computeruse-agent.service" <<EOF
[Unit]
Description=ov-computeruse agent

[Service]
ExecStart="$INSTALL_DIR/ov-agent" run
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
EOF
    if command -v systemctl >/dev/null 2>&1; then
      systemctl --user daemon-reload || true
      if ! systemctl --user enable --now ov-computeruse-agent.service; then
        "$INSTALL_DIR/ov-agent" run &
      fi
    else
      "$INSTALL_DIR/ov-agent" run &
    fi
    ;;
esac

echo "ov-computeruse agent installed and started"
