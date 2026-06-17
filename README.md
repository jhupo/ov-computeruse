# ov-computeruse

Remote Codex-style control system with three parts:

- `agent`: local Go executor installed on user machines.
- `server`: Dockerized Postgres + Redis control plane.
- `dash`: web client, to be built after server contracts settle.

Current engineering focus is the agent/server contract and release pipeline. Dash should consume the stable server protocol and render Codex-style conversations from `run.event` messages rather than parsing SDK-private stream shapes.

## Architecture docs

- [agent/ARCHITECTURE.md](agent/ARCHITECTURE.md)
- [server/ARCHITECTURE.md](server/ARCHITECTURE.md)

## Version tags

- `agent-vX.Y.Z`: builds versioned agent binaries/installers/packages.
- `server-vX.Y.Z`: builds and pushes the Docker server image.

Build-time injection only includes public or client-safe metadata. Server private keys, database URLs, Redis URLs, user keys, and runtime tokens are provided as runtime secrets/environment variables.

## Release flow

Each functional change should be committed separately. Pushing `agent-vX.Y.Z` builds raw agent archives plus Windows Inno, macOS pkg, and Linux deb/rpm packages. Pushing `server-vX.Y.Z` builds and pushes the server Docker image to GHCR.
