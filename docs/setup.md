# Setup

[← README](../README.md)

- [Models](#models)
- [Docker](#docker)
- [Without Docker](#without-docker)
- [Browser assets](#browser-assets)
- [Reaching HERMES from other devices](#reaching-hermes-from-other-devices)
- [Everyday commands (Taskfile)](#everyday-commands-taskfile)

## Models

You need a local [Ollama](https://ollama.com) with the chat model and the embedding model pulled:

```bash
ollama pull qwen3.5:latest     # chat (Settings → Models picks from what you have)
ollama pull embeddinggemma     # memory and document search; without it HERMES uses keyword search
```

`task models` pulls both.

## Docker

```bash
mkdir -p data   # so Docker doesn't create it as root
docker compose up -d --build
```

and open http://localhost:8080. After code changes, `task rebuild` rebuilds the image, restarts the container and waits until it is healthy.

The image build downloads the [browser assets](#browser-assets). Building offline? `docker compose build --build-arg FETCH_ASSETS=0`; everything except voice ID still works.

**User and files.** The container runs as an unprivileged user (uid 1000) and keeps all its state in `./data`, which is mounted into the container. That is also where `hermes.conf` goes. Compose runs it as `${HERMES_UID:-1000}:${HERMES_GID:-1000}`. If your host user isn't uid 1000, put `HERMES_UID` / `HERMES_GID` in `.env`, start it with `HERMES_UID=$(id -u) HERMES_GID=$(id -g) docker compose up -d --build`, or run `sudo chown -R 1000:1000 data`. Otherwise saving fails with *permission denied*. (`UID` itself is read-only in bash, which is why the variables have their own names.) The container's root filesystem is read-only, it drops all Linux capabilities and cannot gain new privileges; it only writes to `/app/data` and `/tmp`.

**Ollama on Linux.** The container reaches your host Ollama via `host.docker.internal` (wired up in `docker-compose.yml`). On macOS and Windows (Docker Desktop) that just works. On Linux a stock Ollama only listens on `127.0.0.1`, which the container can't reach, so the Brain pill stays red. Make Ollama listen on the Docker bridge:

```bash
sudo systemctl edit ollama
# add:
#   [Service]
#   Environment="OLLAMA_HOST=0.0.0.0"
sudo systemctl restart ollama
```

`0.0.0.0` exposes Ollama on every interface, so firewall port 11434 from your LAN, or use the bridge IP instead (e.g. `OLLAMA_HOST=172.17.0.1`). The alternative is host networking: add `network_mode: host` to the service, drop the `ports:` block, and set `ADDR=127.0.0.1` and `DOCKER_OLLAMA_BASE=http://127.0.0.1:11434` (also remove `HERMES_BEHIND_LOOPBACK`, which isn't needed then). `DOCKER_OLLAMA_BASE` in `.env` points the container somewhere else entirely. It is separate from `OLLAMA_BASE` so that a `localhost` value meant for `go run` doesn't end up inside the container.

**Docker and your LAN.** Inside the container HERMES can't see which networks your host is on. Set `HERMES_BLOCK_CIDRS` to your LAN prefixes (e.g. `192.168.1.0/24`) so that read-a-link stays away from them, and use the LAN IP of Home Assistant, not `homeassistant.local` (mDNS names do not resolve inside the container).

## Without Docker

```bash
sh scripts/fetch-assets.sh   # once: browser assets into web/static/vendor/
go run ./cmd/hermes
# open http://localhost:8080
```

You need Go 1.22+ and, for the HERMES voice, the `piper` binary on your `PATH` (or `PIPER_BIN`). Without Piper he falls back to the browser voice and says so. Without the fetched assets he still runs; only voice ID, PDF text extraction and the article extractor for read-a-link are unavailable. `task run` does the same as `go run`.

Speech recognition needs **Chrome or Edge**. Other browsers can type. Brave exposes the API without a speech service, so HERMES tells you to switch.

## Browser assets

`scripts/fetch-assets.sh` (or `task assets`) downloads onnxruntime-web and the CAM++ speaker model for voice ID, pdf.js and Readability.js into `web/static/vendor/`, about 44 MB. Each file is checked against a pinned sha256 and skipped when it is already there. The Docker image does this in its build. Licenses are in [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md).

## Reaching HERMES from other devices

The server binds `127.0.0.1` by default, and the Compose setup publishes the port on `127.0.0.1` only, so it's local-only out of the box. Anything that can reach the API can rewrite the system prompt and read personal data, so network access needs a token. There are two Docker layouts:

1. **Loopback only** (`docker compose up -d --build`, the default `docker-compose.yml`): port `127.0.0.1:8080`, `HERMES_BEHIND_LOOPBACK=1`. Everything works on this machine, nothing on the LAN.
2. **LAN + tokens** (`docker compose -f docker-compose.yml -f docker-compose.lan.yml up -d --build` or `task rebuild:lan`; needs Compose 2.24+ for `!override`): port `8080` on every interface, `HERMES_BEHIND_LOOPBACK=0`, and `HERMES_TOKEN` is required (Compose refuses to start without it). Optional: `HERMES_LISTS_TOKEN`, `ALLOWED_HOSTS`.

Then:

1. Set `HERMES_TOKEN` in `.env` (`task token` or `openssl rand -hex 32`). For `go run`, also set `ADDR=0.0.0.0`. Add any hostnames you use to `ALLOWED_HOSTS` (e.g. `hermes.lan`).
2. On each device, open `http://<host>:8080/?token=<token>` once. HERMES stores it in an HttpOnly cookie and drops it from the address bar. Scripts send `Authorization: Bearer <token>` instead. On the host itself, open `http://localhost:8080/?token=<token>` once in the LAN layout: admin actions (calendar connect, integrations, creating `hermes.conf`) need "this machine", which in Docker means a `localhost` address **plus** the master token.
3. **Phones for the shopping list:** set `HERMES_LISTS_TOKEN` and give family members `http://<host>:8080/lists.html?token=<lists token>`. That token opens only the list page, the list API and list change events; the chat, settings, memories and everything else answer 401. Give partners and kids the lists token, never the master token.

Requests to `localhost`, `127.0.0.1` or `::1` from the machine itself never need the token. Without `HERMES_TOKEN`, every other request is refused (403), and the server logs an error at startup if it's configured to be reachable anyway.

HERMES refuses to start when `HERMES_BEHIND_LOOPBACK=1` is combined with `HERMES_LISTS_TOKEN` or a non-loopback `ALLOWED_HOSTS` entry: with that flag any LAN client could pass as local by sending `Host: localhost`. Use `docker-compose.lan.yml` instead. If a request with a LAN `Host` ever arrives while the flag is on, the log shows an ERROR once (the port is published wider than it should be). Even with the flag, a `localhost` request only counts as local when it comes from the container's default gateway, which is where Docker's port proxy connects from. Other containers on the same network, or a LAN host that routes to the Docker bridge, need the token (the log shows a WARNING once). For an unusual setup, `HERMES_TRUSTED_PEER` lists extra peer IPs. `HERMES_INSECURE_NO_TOKEN=1` turns the token check off entirely; use it only behind a reverse proxy that authenticates on its own.

Outside the home, use Tailscale (or another VPN) rather than forwarding the port, and TLS (e.g. Tailscale Serve), because on plain HTTP the token can be sniffed. See [Security](security.md).

## Everyday commands (Taskfile)

The repo has a [Taskfile](https://taskfile.dev) for the usual chores. Install it once with `brew install go-task` (Linux: see taskfile.dev), then `task` lists everything. The most useful ones:

| Command | Does |
| --- | --- |
| `task rebuild` | Rebuild the image and restart the container after code changes, then wait until it is healthy |
| `task rebuild:lan` | The same in the LAN layout (phones; needs `HERMES_TOKEN` in `.env`) |
| `task logs` | Follow the container logs (health checks hidden; Ctrl+C to stop) |
| `task run` | Run HERMES directly with Go on http://localhost:8080 (needs Piper on `PATH` for the voice) |
| `task assets` | Download the voice-ID model, ONNX runtime, pdf.js and Readability (sha256-checked, skipped when present) |
| `task models` | Pull the Ollama models HERMES uses (chat + embeddings) |
| `task test` | All tests: Go with the race detector, plus the frontend tests |
| `task check` | Everything CI checks: gofmt, go vet, all tests, JS and shell syntax |
| `task backup` | Archive `data/` into `backups/` (folder 0700, archive 0600 — it holds voiceprints and tokens) |
| `task config:export` | Save the current settings as a fully commented `data/hermes.conf.export` |
| `task token` | Print a random token for `HERMES_TOKEN` / `HERMES_LISTS_TOKEN` |

Also there: `up`, `down`, `restart`, `ps`, `status`, `open`, `shell`, `build`, `fmt`, `rebuild:clean`. The `status` and `config*` tasks call `http://localhost:8080` without a token, so in the LAN layout they need the loopback port (or call the API yourself with `Authorization: Bearer`).
