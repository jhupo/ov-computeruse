#!/usr/bin/env sh
set -eu

if [ "$(id -u)" -eq 0 ]; then
  echo "Run ov-agent-install as the desktop user, not root. The agent binds the current user's local Codex config." >&2
  exit 1
fi

if ! command -v ov-agent >/dev/null 2>&1; then
  echo "ov-agent executable not found in PATH" >&2
  exit 1
fi

ov-agent install "$@"

case "$(uname -s)" in
  Darwin)
    INSTALL_DIR="/usr/local/ov-computeruse/agent"
    PLIST="$HOME/Library/LaunchAgents/com.ov-computeruse.agent.plist"
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
    if command -v systemctl >/dev/null 2>&1; then
      systemctl --user daemon-reload || true
      systemctl --user enable --now ov-computeruse-agent.service
    else
      nohup ov-agent run >/dev/null 2>&1 &
    fi
    ;;
esac

echo "ov-computeruse agent bound and started"
