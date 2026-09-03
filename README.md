# roleplay 🎭

Talk to a character who remembers you. `roleplay` is a self-contained character-roleplay chat app: pick a persona, chat in real time over streaming SSE, and the character keeps lightweight memory of you across the session.

Single Go binary (with embedded frontend). No database server, no build pipeline for the UI — everything compiles into one static binary and ships as one Docker container.

[![Deploy to Fly](https://raw.githubusercontent.com/fevnem/roleplay/master/deploy-on-fly.svg)](https://fly.io/)

## ✨ Highlights

- **Multiple characters** (Luna, Max, Aarav, Zara) — each with its own persona, speaking style, temperature, avatar, and accent color defined in `contexts/personas/*.yml`.
- **Streaming replies** — tokens arrive live over SSE; the UI shows a typing indicator and renders in place.
- **Facts memory** — the model extracts up to 2 stable facts per message and the character "remembers" them within a session (TTL-bounded, so nothing lingers forever).
- **Session memory** — the last N turns persist per anonymous session; optionally snapshotted to a JSON file so memory survives a restart.
- **Zero-dep frontend** — vanilla HTML/CSS/JS, embedded via `go:embed`. No npm, no build step.
- **Provider-agnostic** — points at any OpenAI-compatible `/chat/completions` API (default: Hetzner inference).

## 🗂 Structure

```
roleplay/
├── main.go                 # bootstrap: server, timeouts, middleware, graceful shutdown
├── api/                    # HTTP layer (thin handlers, DI via Handler struct)
│   └── chat.go             #   /api/chat, /api/history, /api/characters
├── contexts/               # domain: character personas
│   ├── personality.go      #   load YAML personas, build system prompts
│   └── personas/*.yml      #   per-character definition (name, style, avatar, temp)
├── database/               # persistence: in-memory session + facts store
│   └── temporary_consistency.go
├── model/                  # provider: OpenAI-compatible chat client
│   └── client.go           #   streaming + facts extraction (env-configured, DI)
├── public/                 # embedded single-page frontend
│   ├── index.html
│   ├── style.css
│   └── app.js
├── LICENSE                 # MIT
├── Dockerfile               # multi-stage, static, non-root
├── .dockerignore
└── .env.example
```

Layering is strict and one-directional: **handlers (api) → domain (contexts) → persistence (database) + provider (model)**. No HTTP in the store, no store logic in handlers. Dependencies are injected (the `Handler` struct and `model.Client`) so every package stays decoupled and testable.

## 🚀 Quick start (local)

```bash
# 1. Build (Go 1.22+)
go build -o roleplay .

# 2. Configure the LLM provider (4 env vars)
export PROVIDER_NAME=hetzner
export MODEL_NAME=Qwen/Qwen3.6-35B-A3B-FP8
export PROVIDER_API_ENDPOINT=https://inference.hetzner.com/api/v1
export PROVIDER_API_KEY=your_key

# 3. Run
./roleplay
# → roleplay listening on :2100

# 4. Open
# → http://localhost:2100
```

Then pick a character from the cards and start chatting.

## 🐳 Docker

```bash
docker build -t roleplay .
docker run --rm -p 2100:2100 \
  -e PROVIDER_NAME=hetzner \
  -e MODEL_NAME=Qwen/Qwen3.6-35B-A3B-FP8 \
  -e PROVIDER_API_ENDPOINT=https://inference.hetzner.com/api/v1 \
  -e PROVIDER_API_KEY=your_key \
  roleplay
```

The runtime image runs as a **non-root user** (`appuser`, uid 10001) with a static,
CGO-free binary, a `HEALTHCHECK`, and `SNAPSHOT=/app/data/sessions.json` baked in so
memory survives container restarts.

## 🚀 Deploy to Fly.io

The included `Dockerfile` (static, CGO-free, non-root, `HEALTHCHECK`) deploys to Fly.io as-is.

```bash
# 1. Install flyctl + log in (device-code flow)
curl -L https://fly.io/install.sh | sh
export PATH="$HOME/.fly/bin:$PATH"
flyctl auth login

# 2. Scaffold the app (picks org + region, writes fly.toml)
flyctl launch --auto-confirm --no-deploy --name <app-name>

# 3. Set the provider secrets BEFORE the first deploy (encrypted, never committed)
flyctl secrets set \
  PROVIDER_NAME=hetzner \
  MODEL_NAME=Qwen/Qwen3.6-35B-A3B-FP8 \
  PROVIDER_API_ENDPOINT=https://inference.hetzner.com/api/v1 \
  PROVIDER_API_KEY=your_key

# 4. Deploy
flyctl deploy --remote-only

# 5. Visit https://<app-name>.fly.dev
```

Notes for running on Fly:

- **Probes** — `/healthz` is the liveness probe (always `200` once serving); `/readyz` is the
  readiness probe and returns `503` until the provider is fully configured.
- **SSE** — for long-lived streaming chat connections, keep a machine warm with
  `min_machines_running = 1` in `fly.toml` so a cold start never interrupts a reply.
- **Persistence** — the default Fly machine disk is **ephemeral**. To keep the `SNAPSHOT`
  across redeploys/recreates, add a volume and mount it at `/app/data`
  (`flyctl volumes create <app> --size 1 && add [mounts] source = "data", destination = "/app/data"`).
- **Billing** — a new app needs a paid org (add a card) before a machine can be created.
- **Teardown** — `flyctl destroy <app-name>`.

## ⚙️ Configuration (env vars)

| Variable | Required | Description |
|---|---|---|
| `PROVIDER_NAME` | **yes** | Provider label for logs (e.g. `hetzner`, `openai`). Any value. |
| `MODEL_NAME` | **yes** | Model id to request (e.g. `Qwen/Qwen3.6-35B-A3B-FP8`). |
| `PROVIDER_API_ENDPOINT` | **yes** | OpenAI-compatible base endpoint, no trailing `/chat/completions` (e.g. `https://inference.hetzner.com/api/v1`). |
| `PROVIDER_API_KEY` | **yes** | Provider bearer API key. |
| `PORT` | no | HTTP listen port (default `2100`). |
| `SNAPSHOT` | no | JSON file path to persist session memory on shutdown (default: pure RAM). The Dockerfile sets `/app/data/sessions.json`. |

The four `PROVIDER_*`/`MODEL_*` vars are **provider-agnostic**: point them at any OpenAI-compatible chat-completions endpoint (Hetzner, OpenAI, OpenRouter, a local vLLM, …). There are **no vendor defaults hardcoded** — an unset value surfaces as a clear error naming the missing variable, and `/api/chat` returns `503` until the provider is configured.

> **Security:** the API key is read from the environment at runtime — it is never compiled into the binary or committed. See `.env.example`.

## 🧑‍🤝‍🧑 Adding a character

Drop a YAML file in `contexts/personas/`:

```yaml
name: Nova
language: "English"
avatar: "🪐"
accent: "#22d3ee"
personality: "curious, precise, gently enthusiastic about science"
backstory: "An astronomer who believes every question is worth asking."
style: "clear, warm, excited to explain — short but vivid"
greeting: "Oh hi! I was just charting the outer planets. What are you curious about today? 🔭"
temperature: 0.85
```

It appears in the picker automatically. `avatar` and `accent` are optional (sensible defaults apply).

## 🔌 API

| Endpoint | Method | Purpose |
|---|---|---|
| `/api/characters` | GET | List public persona catalog (name, language, personality, greeting, avatar, accent). |
| `/api/chat` | POST | Send a message; **streams** the reply as SSE `data:` deltas + a final `done`. Max body 32 KB; session ≤64 chars, text ≤4000 chars. |
| `/api/history?session=ID` | GET | Return greeting + recent turns + chosen character for a session. |
| `/healthz` | GET | **Liveness** probe → `{"status":"ok"}` (always 200 once serving). |
| `/readyz` | GET | **Readiness** probe → 200 only when the provider is configured; else 503. |

`/api/chat` request body:

```json
{ "session": "anon-id", "text": "hi", "character": "luna" }
```

## 🧠 Memory model

- **Facts**: the model extracts up to 2 stable facts per user message; the character keeps up to 8 and re-injects them as context on later turns. Auto-forget after TTL.
- **Turns**: only the last ~20 messages are kept per session (keeps prompts bounded).
- **Sessions** idle for TTL (3h) are evicted. Everything is anonymous — no accounts, no PII required.