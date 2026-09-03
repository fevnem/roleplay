# roleplay 🎭

Talk to a character who remembers you. `roleplay` is a self-contained character-roleplay chat app: pick a persona, chat in real time over streaming SSE, and the character keeps lightweight memory of you across the session.

Single Go binary (with embedded frontend). No database server, no build pipeline for the UI — everything compiles into one static binary and ships as one Docker container.

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
├── main.go                 # bootstrap: server, routes, graceful shutdown
├── api/                    # HTTP layer (thin handlers)
│   └── chat.go             #   /api/chat, /api/history, /api/characters
├── contexts/               # domain: character personas
│   ├── personality.go      #   load YAML personas, build system prompts
│   └── personas/*.yml      #   per-character definition (name, style, avatar, temp)
├── database/               # persistence: in-memory session + facts store
│   └── temporary_consistency.go
├── model/                  # provider: OpenAI-compatible chat client
│   └── client.go           #   streaming + facts extraction (env-configured)
└── public/                 # embedded single-page frontend
    ├── index.html
    ├── style.css
    └── app.js
```

Layering is strict and one-directional: **handlers (api) → domain (contexts) → persistence (database) + provider (model)**. No HTTP in the store, no store logic in handlers.

## 🚀 Quick start (local)

```bash
# 1. Build (Go 1.22+)
go build -o roleplay .

# 2. Provide a model API key
export MODEL_API_KEY=your_key

# 3. Run
./roleplay
# → roleplay listening on :3000

# 4. Open
# → http://localhost:3000
```

Then pick a character from the cards and start chatting.

## 🐳 Docker

```bash
docker build -t roleplay .
docker run --rm -p 3000:3000 \
  -e MODEL_API_KEY=your_key \
  roleplay
```

`PORT` defaults to 3000; `SNAPSHOT=/app/data/sessions.json` is baked in so memory survives container restarts.

## ⚙️ Configuration (env vars)

| Variable | Required | Default | Description |
|---|---|---|---|
| `MODEL_API_KEY` | **yes** | – | Provider bearer token. Without it, `/api/chat` returns errors. |
| `MODEL_BASE_URL` | no | `https://inference.hetzner.com/api/v1` | Any OpenAI-compatible base URL. |
| `MODEL_NAME` | no | `Qwen/Qwen3.6-35B-A3B-FP8` | Model id to request. |
| `PORT` | no | `3000` | HTTP listen port. |
| `SNAPSHOT` | no | (RAM only) | JSON file path to persist session memory on shutdown. |

> **Security:** the API token is read from the environment at runtime — it is never compiled into the binary or committed. See `.env.example`.

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
| `/api/chat` | POST | Send a message; **streams** the reply as SSE `data:` deltas + a final `done`. |
| `/api/history?session=ID` | GET | Return greeting + recent turns + chosen character for a session. |
| `/health` | GET | Liveness probe → `{"status":"ok"}`. |

`/api/chat` request body:

```json
{ "session": "anon-id", "text": "hi", "character": "luna" }
```

## 🧠 Memory model

- **Facts**: the model extracts up to 2 stable facts per user message; the character keeps up to 8 and re-injects them as context on later turns. Auto-forget after TTL.
- **Turns**: only the last ~20 messages are kept per session (keeps prompts bounded).
- **Sessions** idle for TTL (3h) are evicted. Everything is anonymous — no accounts, no PII required.