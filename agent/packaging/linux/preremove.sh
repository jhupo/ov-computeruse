#!/usr/bin/env sh
set -eu

if command -v systemctl >/dev/null 2>&1; then
  systemctl --user disable --now ov-computeruse-agent.service >/dev/null 2>&1 || true
fi
