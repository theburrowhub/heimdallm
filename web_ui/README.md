# Heimdallm Web UI

SvelteKit dashboard — a browser-based alternative to the Flutter desktop app.

See the [design spec](../docs/superpowers/specs/2026-04-17-web-ui-scaffold-design.md) for the architecture.

## Local development

Prereqs: Node 22 LTS, a running heimdallm daemon on `127.0.0.1:7842` with a token at `~/.local/share/heimdallm/api_token`.

```bash
cd web_ui
cp .env.example .env
# point HEIMDALLM_API_TOKEN_FILE at your local token file, e.g.:
echo "HEIMDALLM_API_TOKEN_FILE=$HOME/.local/share/heimdallm/api_token" >> .env

npm install
npm run dev
```

Open http://127.0.0.1:5173.

## Scripts

- `npm run dev` — Vite dev server with HMR.
- `npm run build` — production build (adapter-node output in `build/`).
- `npm run preview` — preview the production build.
- `npm run check` — type-check with svelte-check.
- `npm test` — unit tests (Vitest).
- `npm run lint` / `npm run format` — prettier + eslint.

## Architecture at a glance

```
Browser ── /api/* ── SvelteKit server ── daemon :7842
        └─ /events ─┘                    (X-Heimdallm-Token injected server-side)
```

The token lives in `src/lib/server/token.ts` and never ships to the browser. Same-origin SSE means we use native `EventSource` without polyfills.
