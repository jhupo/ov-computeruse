# ov-computeruse server

Postgres + Redis backed multi-user control plane for local ov-computeruse agents. The full design is in [ARCHITECTURE.md](ARCHITECTURE.md).

## Runtime services

- Postgres stores users, user keys, devices, agents, Codex project/session indexes, commands, approvals, audit logs, and run events.
- Redis stores dash sessions, short-lived online agent state, and cross-instance pub/sub for dash broadcasts and agent command routing.
- The Docker image is stateless. Private keys and database URLs are runtime secrets, not baked into the image.

## Required environment

- `OV_SERVER_ADDR`, default `:8080`
- `OV_SERVER_PUBLIC_URL`
- `OV_SERVER_POSTGRES_URL`
- `OV_SERVER_REDIS_URL`
- `OV_SERVER_KEY_ID`
- `OV_SERVER_PRIVATE_KEY_PEM` or `OV_SERVER_PRIVATE_KEY_FILE`
- `OV_SERVER_DASH_TOKEN`, internal/admin bearer token; normal users should use `/api/dash/login`
- `OV_SERVER_BIND_USERS_JSON`, optional bootstrap users for local/dev binding

`OV_SERVER_BIND_USERS_JSON` is a JSON array with username/password and allowed Codex key fingerprint records. It is only a bootstrap path until the real user/key/balance admin service exists.

## Endpoints

- `POST /api/agents/bind`: installer bind flow, decrypts agent payload with the server private key.
- `GET /ws/agent`: outbound agent websocket, bearer token is the per-agent secret.
- `POST /api/dash/login`: username/password login, returns a short-lived dash session token.
- `GET /ws/dash`: dash websocket, bearer token is a dash session token or internal admin token.
- `POST /api/dash/commands`: dash command dispatch to an online agent.
- `GET /healthz`: liveness.

## Tag release

Push a tag like `server-v1.0.0` to build and push:

```text
ghcr.io/<owner>/<repo>/server:server-v1.0.0
ghcr.io/<owner>/<repo>/server:latest
```

The workflow injects only non-secret build metadata: version, server key id, and public key fingerprint.
