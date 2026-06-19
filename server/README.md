# ov-computeruse server

Postgres + Redis backed multi-user control plane for local ov-computeruse agents. The full design is in [ARCHITECTURE.md](ARCHITECTURE.md).

## Runtime services

- Postgres stores users, user keys, devices, agents, Codex project/session indexes, commands, approvals, audit logs, and run events.
- Redis stores dash sessions, short-lived online agent state, and cross-instance pub/sub for dash broadcasts, agent command routing, and forced agent disconnects.
- The Docker image is stateless. Private keys and database URLs are runtime secrets, not baked into the image.

## Required environment

- `OV_SERVER_ADDR`, default `:8080`
- `OV_SERVER_PUBLIC_URL`
- `OV_SERVER_SUB2API_LOGIN_UPSTREAM`, base URL for the sub2api login upstream used by dash. Server posts `POST <upstream>/api/login`.
- `OV_SERVER_POSTGRES_URL`
- `OV_SERVER_REDIS_URL`
- `OV_SERVER_PRIVATE_KEY_PEM` or `OV_SERVER_PRIVATE_KEY_FILE`
- `OV_SERVER_DASH_TOKEN`, optional internal/admin bearer token; normal users should use `/api/dash/login`
- `OV_SERVER_BIND_USERS_JSON`, optional bootstrap users for local/dev binding

`OV_SERVER_BIND_USERS_JSON` is a JSON array with username/password and allowed Codex key fingerprint records. It is a bootstrap seed path; ongoing user/key management should use the admin API.

## Docker Compose deployment

The server image contains both the Go API service and the embedded dash web UI. Postgres and Redis run as separate containers in the compose stack.

```text
server/docker-compose.example.yml
server/.env.example
```

Deploy flow:

```bash
cd server
cp .env.example .env
# edit .env and copy server_private_key.pem beside docker-compose.example.yml
docker compose --env-file .env -f docker-compose.example.yml up -d
```

Containers:

- `server`: `ghcr.io/jhupo/ov-computeruse/server:<tag>`, exposes API, dash UI, `/ws/agent`, and `/ws/dash`.
- `postgres`: stores users, devices, agents, indexed Codex projects/sessions/history, commands, approvals, and timelines.
- `redis`: stores dash sessions, live agent state, websocket pub/sub, and command routing state.

## sub2api login contract

Dash login sends the user's username/password to `OV_SERVER_SUB2API_LOGIN_UPSTREAM`. On success, server syncs the returned user and key fingerprints into Postgres, issues a dash session token, and never stores plaintext API keys.

Expected response:

```json
{
  "user": {
    "id": "usr_123",
    "username": "alice"
  },
  "keys": [
    {
      "id": "key_123",
      "name": "main",
      "base_url": "https://gateway.example.com/v1",
      "api_key": "sk-...",
      "provider": "openai",
      "model": "gpt-5.1-codex"
    }
  ]
}
```

`key_fingerprint` may be returned instead of `api_key`. `base_url` is required because agent bind validates the local Codex config by matching both `base_url` fingerprint and key fingerprint.

## Endpoints

- `POST /api/agents/bind`: installer bind flow, decrypts agent payload with the server private key.
- `GET /ws/agent`: outbound agent websocket, bearer token is the per-agent secret.
- `POST /api/dash/login`: username/password login, returns a short-lived dash session token.
- `GET /api/dash/me`: return the current dash principal.
- `GET /api/dash/config`: return dash runtime configuration, including the sub2api login upstream domain.
- `GET /api/admin/users`: admin-only list of users.
- `POST /api/admin/users`: admin-only create/update user. Body includes `username`, optional `id`, and `password`.
- `POST /api/admin/users/{user_id}/disable`: admin-only disable user, invalidating sessions and disconnecting agents.
- `POST /api/admin/users/{user_id}/enable`: admin-only enable user.
- `GET /api/admin/users/{user_id}/keys`: admin-only list of a user's Codex credential fingerprints.
- `POST /api/admin/users/{user_id}/keys`: admin-only create/update Codex key fingerprint record. Body includes `base_url`, `key_fingerprint`, optional `id`, `name`, `provider`, `model`.
- `POST /api/admin/users/{user_id}/keys/{key_id}/disable`: admin-only disable one Codex key fingerprint.
- `POST /api/admin/users/{user_id}/keys/{key_id}/enable`: admin-only enable one Codex key fingerprint.
- `GET /api/admin/audit-logs`: admin-only audit log query. Supports `user_id`, `agent_id`, `action`, `since`, `until`, and `limit`.
- `GET /api/dash/agents`: list the current user's agents and device heartbeat snapshots.
- `POST /api/dash/agents/{agent_id}/disable`: disable one agent or its device. JSON body accepts `scope` as `agent` or `device` and optional `reason`.
- `POST /api/dash/agents/{agent_id}/enable`: enable one agent or its device. JSON body accepts `scope` as `agent` or `device`.
- `GET /api/dash/commands?agent_id=...&status=...`: list persisted command lifecycle records.
- `GET /api/dash/commands/{command_id}?agent_id=...`: load one command record with dispatch/ack/deadline metadata.
- `POST /api/dash/commands/{command_id}/retry?agent_id=...`: retry a queued, dispatched, dispatch-failed, expired, or failed command.
- `GET /api/dash/projects?agent_id=...`: list projects indexed from an agent.
- `GET /api/dash/sessions?agent_id=...&project_id=...`: list Codex sessions for an agent or project.
- `GET /api/dash/runs?agent_id=...&session_id=...`: list persisted runs.
- `GET /api/dash/runs/events?agent_id=...&run_id=...&after_seq=...`: replay run events for dash refresh/resume.
- `GET /api/dash/runs/timeline?agent_id=...&run_id=...`: load projected run timeline, messages, and tool calls.
- `GET /api/dash/runtime-sessions?agent_id=...&session_id=...`: list runtime/native session mappings.
- `GET /api/dash/history/items?agent_id=...&session_id=...`: load projected history items for a Codex session.
- `GET /api/dash/approvals?status=pending`: list approval requests.
- `POST /api/dash/approvals/{approval_id}/decision`: approve or reject a pending request and forward the decision to the agent.
- `GET /ws/dash`: dash websocket, bearer token is a dash session token or internal admin token.
- `POST /api/dash/commands`: create a durable command intent and dispatch it when the agent is online. Returns the command record plus `command_id` and `run_id`.
- `GET /api/dash/history/messages?agent_id=...&session_id=...`: load stored displayable history messages for a session.
- `GET /healthz`: liveness.
- `GET /readyz`: readiness; pings Postgres and Redis and returns dependency status.

The image serves the embedded dash build at `/`. Unknown non-API GET/HEAD paths fall back to `index.html` for SPA routing. GitHub Actions and the Dockerfile build `dash/dist` first, copy it into the server source tree during the image build, and embed it into the `ov-server` binary.

Agent websocket envelopes encrypt `data` with AES-256-GCM derived from the per-agent secret and then sign the encrypted envelope with HMAC-SHA256. Bind requests still use the server public key because they happen before an agent secret exists.

## Dash websocket

Dash connects to `GET /ws/dash` with a dash bearer token. The socket is a control channel plus filtered event stream.

Client messages:

- `{"type":"run.subscribe","agent_id":"agt_...","run_id":"run_...","after_seq":0,"limit":300}`: authorize the agent, subscribe this socket to the run, and receive `run.snapshot`.
- `{"type":"run.unsubscribe","agent_id":"agt_...","run_id":"run_..."}`: remove the run subscription.
- `{"type":"ping"}`: receive `pong`.

Server messages:

- `run.snapshot`: projected timeline/messages/tool calls plus raw run events after `after_seq`.
- `run.event`: live agent run event, delivered to subscribed sockets for that run.
- `agent.*`, `index.*`, `history.*`, `command.*`, `approval.*`: account-level updates.
- `error`: stable `{code,message}` payload for invalid socket messages.

## Tag release

Push a tag like `server-v1.0.0` to build and push:

```text
ghcr.io/<owner>/<repo>/server:server-v1.0.0
ghcr.io/<owner>/<repo>/server:latest
```

The workflow injects only non-secret build metadata: version, server key id, and public key fingerprint.

Runtime secrets are set on the deployed container, not at image build time:

- `OV_SERVER_PUBLIC_URL`: public HTTPS service URL used by agent installers.
- `OV_SERVER_SUB2API_LOGIN_UPSTREAM`: sub2api login upstream base URL.
- `OV_SERVER_PRIVATE_KEY_PEM` or `OV_SERVER_PRIVATE_KEY_FILE`: server private key for installer bind decrypt.
- `OV_SERVER_DASH_TOKEN`: optional internal/admin bearer token.
