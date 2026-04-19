# Web UI Scaffold — Design Spec

**Fecha:** 2026-04-17
**Estado:** Aprobado
**Issue:** [#30](https://github.com/theburrowhub/heimdallm/issues/30) — `feat: scaffold SvelteKit + API/SSE clients`
**Fase:** 3 (Web UI)
**Autor:** brainstorm session con vbuenog

## Contexto

Este es el primer PR de la Fase 3 del plan [Heimdallm v2](./2026-04-16-heimdallm-v2-design.md). Crea la estructura base del web UI (`web_ui/`) y los clientes HTTP y SSE sobre los que se construirán los PRs siguientes:

- #31 — rutas Dashboard, PRs, Issues
- #32 — rutas Config, Agents, Logs
- #33 — Docker + docker-compose + setup automático de token

El daemon ya existe y expone sus endpoints actuales en `:7842`. Los endpoints de issue-tracking (Fase 2, issues #24–#29) aún no están implementados pero se incluirán en el cliente TS desde el primer día para desbloquear trabajo paralelo.

## Decisiones arquitectónicas

### Server-side proxy vs direct browser → daemon

**Elegido: server-side proxy.** El navegador habla con endpoints de SvelteKit (`/api/*`, `/events`); el servidor SvelteKit guarda el token y reenvía al daemon añadiendo el header `X-Heimdallm-Token`.

Razones:
- El daemon tiene CORS restrictivo (fix #4); un proxy same-origin evita tener que añadir un allowlist en el daemon.
- En docker-compose, `http://daemon:7842` solo es resoluble desde dentro de la red de containers — el navegador no puede alcanzarlo directamente.
- El token nunca llega al navegador, no se almacena en `localStorage`/memoria del cliente.
- `EventSource` nativo del navegador funciona sin polyfill porque la conexión es same-origin (no necesita headers personalizados hacia el servidor SvelteKit; SvelteKit inyecta el token al reenviar al daemon).

Consecuencia de diseño: no necesitamos portar el parser SSE de `sse_client.dart` — `EventSource` nativo parsea y reconecta automáticamente.

### Patrón del proxy: catch-all

**Elegido: un único handler catch-all** en `src/routes/api/[...path]/+server.ts` que reenvía cualquier método+path al daemon, más un handler dedicado `src/routes/events/+server.ts` para el stream SSE.

Razones:
- El proxy no tiene lógica de negocio — auth y validación viven en el daemon.
- `api.ts` sigue siendo la única fuente de verdad sobre el contrato de endpoints.
- Añadir nuevos endpoints del daemon no requiere tocar el proxy.

### Cobertura de endpoints en `api.ts`

**Elegido: incluir los métodos de Fase 2 desde ya**, aunque devuelvan 404 hasta que el daemon los implemente en issues #25–#28.

Razones:
- Desbloquea trabajo paralelo en #31 (ruta Issues) antes de que Fase 2 termine.
- El tipado queda congelado y revisable de una vez.
- Los tests de `api.ts` mockean `fetch`, así que no dependen del estado del daemon.

### Tipos TS

**Elegido: `interface`/`type` escritos a mano** en `src/lib/types.ts`, espejando los modelos Dart. Sin validación en runtime (ni Zod ni similar).

Razones:
- Mantener paridad con cómo maneja tipos el lado Dart.
- Evitar dependencia adicional en el scaffold.
- El daemon ya valida los campos sensibles.

### Layout shell

**Elegido: shell mínimo + landing con indicador de estado.** Nav bar con links a las seis secciones (los que no existan harán 404 hasta #31/#32), comprobación de autenticación vía `/api/me`, conexión SSE en `onMount`, y una landing en `/` que muestra versión + estado de conexión + `fetchStats()`.

Razones:
- `npm run dev` arranca una app navegable que prueba la wiring end-to-end.
- No invade el trabajo de #31/#32 (no crea rutas stub para secciones futuras).
- Satisface los criterios de aceptación de #30 de forma verificable visualmente.

### Testing

**Elegido: vitest con tests unitarios para `api.ts`, `sse.ts` y `lib/server/token.ts`.** Sin Playwright, sin tests de los proxies.

Razones:
- Los proxies son forwarders de bytes sin lógica — no aportan valor al mockear.
- Los clientes y la carga de token son donde pueden meterse bugs sutiles.
- E2E llega naturalmente con los PRs de rutas (#31/#32).

## Arquitectura

```
Browser (Svelte 5 app, same-origin, :5173 dev / :3000 docker)
    │
    │  fetch("/api/…")          new EventSource("/events")
    ▼
SvelteKit server (adapter-node)
    • HEIMDALLM_API_TOKEN en memoria (nunca enviado al navegador)
    • Catch-all proxy: src/routes/api/[...path]/+server.ts
    • SSE passthrough: src/routes/events/+server.ts
    │
    │  http://$HEIMDALLM_API_URL/… con header X-Heimdallm-Token
    ▼
Heimdallm daemon (:7842) — no cambia
```

Propiedades clave:
- Token nunca sale del servidor SvelteKit.
- Todo same-origin desde la óptica del navegador — sin CORS, sin cookies, sin polyfills.
- Proxy fino; el daemon sigue siendo la fuente de verdad para auth, validación y lógica de negocio.
- `adapter-node` porque tenemos server endpoints; corre como HTTP server Node en el container.

## Estructura de archivos

```
web_ui/
├── package.json                # scripts npm, dependencias
├── svelte.config.js            # @sveltejs/adapter-node
├── vite.config.ts              # vite + tailwind v4 plugin + config vitest
├── tsconfig.json
├── .env.example                # HEIMDALLM_API_URL, HEIMDALLM_API_TOKEN
├── Dockerfile                  # multi-stage: build → node:22-alpine runtime
├── .dockerignore
├── README.md                   # dev quickstart
├── static/
│   └── favicon.png
└── src/
    ├── app.d.ts
    ├── app.html
    ├── app.css                 # directivas tailwind + estilos base
    ├── lib/
    │   ├── api.ts              # cliente tipado — todos los métodos de #30
    │   ├── sse.ts              # EventSource nativo → Svelte stores
    │   ├── types.ts            # interfaces TS espejo de los modelos Dart
    │   ├── stores.ts           # auth store ({ login, ready, error })
    │   └── server/
    │       └── token.ts        # server-only: env var o /data/api_token
    ├── routes/
    │   ├── +layout.svelte      # nav, auth check, init SSE
    │   ├── +layout.ts          # carga /api/me en el mount
    │   ├── +page.svelte        # landing: heading + indicador de SSE
    │   ├── api/
    │   │   └── [...path]/+server.ts   # proxy catch-all al daemon
    │   └── events/+server.ts           # passthrough del stream SSE
    └── tests/
        ├── api.test.ts         # fetch mockeado; verifica métodos + errores
        ├── sse.test.ts         # verifica wiring de EventSource y stores
        └── token.test.ts       # precedencia env vs fichero
```

Notas:
- `src/lib/server/**` es una convención de SvelteKit — esos módulos solo pueden importarse desde código server-only, impidiendo que el token llegue al bundle del navegador.
- `stores.ts` expone un store de auth populado por `+layout.ts`; evita prop-drilling de `login` en cada página futura.
- `.env.example` contiene `HEIMDALLM_API_URL=http://127.0.0.1:7842` para que `npm run dev` funcione out-of-the-box contra un daemon local.

## Interfaces clave

### `src/lib/server/token.ts`

```ts
export async function loadToken(): Promise<string | null>
```

Orden de resolución (cacheado en una variable a nivel de módulo tras la primera llamada exitosa):
1. `process.env.HEIMDALLM_API_TOKEN` si está definido y no vacío.
2. Contenido de `/data/api_token` (path sobreescribible via `HEIMDALLM_API_TOKEN_FILE`), trimmed.
3. Si ninguno está disponible → `null`; el proxy responderá 503 con un mensaje claro para que la UI pueda mostrar "daemon token missing".

### `src/routes/api/[...path]/+server.ts` — catch-all proxy

- Exporta handlers `GET`, `POST`, `PUT`, `DELETE`.
- Construye `${HEIMDALLM_API_URL}/${params.path}` preservando la query string.
- Reenvía método, body (streamed, sin buffering) y `Content-Type`.
- Añade `X-Heimdallm-Token` desde `loadToken()`.
- Devuelve la respuesta del daemon verbatim (status + body streamed).
- Sin parseo JSON, sin validación — bytes in, bytes out.

### `src/routes/events/+server.ts` — SSE passthrough

- Abre `fetch(${HEIMDALLM_API_URL}/events, { headers: { Accept: 'text/event-stream', 'X-Heimdallm-Token': token }, signal: request.signal })`.
- Devuelve `new Response(upstream.body, { headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', 'Connection': 'keep-alive' } })`.
- Si el navegador se desconecta, `request.signal` aborta el fetch upstream.
- Si el daemon se desconecta, el `EventSource` nativo del navegador reconecta automáticamente.

### `src/lib/api.ts` — cliente del navegador

- Base URL relativa (`/api`). Sin lógica de token — la inyecta el proxy server-side.
- Helper privado `request(method, path, body?)`: hace `fetch`, lanza `ApiError` en non-2xx, devuelve JSON parseado (o `void` en 202/204).
- Métodos tipados (mínimo listado en #30 más `undismissPR`, `checkHealth` de `api_client.dart` y `undismissIssue` por simetría):

  ```
  checkHealth, fetchPRs, fetchPR, triggerReview, dismissPR, undismissPR,
  fetchIssues, fetchIssue, triggerIssueReview, dismissIssue, undismissIssue,
  fetchConfig, updateConfig, reloadConfig,
  fetchAgents, upsertAgent, deleteAgent,
  fetchMe, fetchStats
  ```

- `ApiError` class con `{ status, statusText, path, message }`, alineado con `ApiException` de Dart.

### `src/lib/sse.ts` — wrapper SSE del navegador

`EventSource` nativo con stores Svelte 5:

```ts
export function connectEvents(): {
  events: Readable<SseEvent>;     // emite en cada evento tipado
  connected: Readable<boolean>;   // refleja EventSource.readyState
  close(): void;
}
```

- Suscribe a tipos de evento del daemon (`pr_updated`, `issue_updated`, `config_reloaded`, `log`, …) via `addEventListener`.
- Estado de conexión derivado de `onopen`/`onerror`.
- `+layout.svelte` llama a `connectEvents()` una vez, en el mount, dentro de un bloque browser-only (`if (browser)`), y guarda el handle para teardown en `onDestroy`.

### `src/lib/types.ts`

Interfaces TS escritas a mano espejando los modelos Dart (`PR`, `Review`, `Issue`, `Agent`, `Config`, `Stats`, `SseEvent`, …). Los nombres de campos coinciden con el wire format JSON (snake_case donde el daemon emite snake_case).

## Comportamiento del layout

### `+layout.ts`

- Carga `/api/me` una vez por sesión (solo en navegación client-side).
- Devuelve `{ login: string | null, authError: string | null }` al resto de páginas.

### `+layout.svelte`

- Shell fino: nav bar arriba + `<slot />`.
- Nav: wordmark "Heimdallm" a la izquierda; seis links a la derecha — Dashboard (`/`), PRs (`/prs`), Issues (`/issues`), Agents (`/agents`), Config (`/config`), Logs (`/logs`). Los no-root harán 404 hasta que #31/#32 los creen; el nav los renderiza igualmente.
- Item más a la derecha: login de GitHub del usuario autenticado (desde el auth store), o "—" mientras carga.
- En el mount (solo en el navegador): abre `connectEvents()` y guarda el handle en un store; cierra en `onDestroy`.
- Si `authError` está seteado, renderiza un banner rojo encima del contenido: "Daemon unreachable — check `HEIMDALLM_API_URL`" (sin modal, sin botón de retry; recargar la página para reintentar).

### `+page.svelte` (landing en `/`)

- Heading grande: **Heimdallm**.
- Una línea con la versión (desde `package.json`).
- Pill pequeña al lado del heading con el estado SSE:
  - verde + "connected" cuando `EventSource.readyState === OPEN`,
  - ámbar + "connecting…" cuando `CONNECTING`,
  - rojo + "disconnected" cuando `CLOSED`.
- Abajo: tabla de cuatro celdas con los resultados de `fetchStats()` — total PRs, open issues (mostrará "—" hasta que Fase 2 esté lista), reviews today, uptime. Esta es la página "prueba de que la wiring funciona"; probablemente será reemplazada por un dashboard más rico en #31.

## Estilos

**TailwindCSS 4** vía el plugin Vite (`@tailwindcss/vite`, más simple que el PostCSS dance de v3). Utility-first sin custom theme en este scaffold — los defaults se ven limpios. `app.css` mínimo: `@import "tailwindcss";` más un par de tweaks base (stack de system font, `color-scheme: dark light`).

Sin librería de componentes, sin tokens personalizados, sin toggle dark-mode. Scope de diseño limitado — #31/#32 pueden introducir eso si lo necesitan.

## Testing

**vitest** con ~10 tests totales:

- `tests/api.test.ts` (fetch mockeado via `vi.fn()`):
  - `fetchPRs` hace GET a `/api/prs`, parsea JSON array, devuelve lista tipada.
  - `triggerReview` POST y resuelve en 202; lanza `ApiError` en 500.
  - `upsertAgent` envía body JSON con `Content-Type` correcto.
  - `ApiError` carga `{ status, path }`.
- `tests/sse.test.ts` (constructor `EventSource` mockeado):
  - Wrapper abre `/events`, registra listeners para cada tipo de evento conocido, empuja al store `events`.
  - Store `connected` alterna `true`/`false` con `onopen`/`onerror`.
  - `close()` llama a `EventSource.close()` y limpia subscripciones.
- `tests/token.test.ts` (fs y `process.env` mockeados):
  - Env var gana sobre fichero.
  - Fichero leído cuando env falta; trim de newline final.
  - Cacheado tras la primera lectura exitosa.

Sin tests de los proxies — son forwarders de bytes sin lógica. La cobertura end-to-end llega con Fase 2/3.

## Tooling

- Package manager: **npm** (la AC de #30 pide explícitamente `npm run build`/`npm run dev`).
- Node: **22.x LTS**, pinneado en `package.json` `engines` y en el Dockerfile.
- Svelte **5** (runes), SvelteKit **2.x**, TypeScript **5.x**, Vite **5.x**, TailwindCSS **4**.
- Lint/format: **prettier + eslint** con la config por defecto de SvelteKit — sin reglas custom en este PR.
- Scripts en `package.json`: `dev`, `build`, `preview`, `test`, `lint`, `format`, `check` (svelte-check).

## Dockerfile

Multi-stage:
1. Stage build (`node:22-alpine`): `npm ci`, `npm run build` → produce `build/` (output de adapter-node).
2. Stage runtime (`node:22-alpine`): copia `build/`, `package.json`, `node_modules` (solo prod), expone `3000`, `CMD ["node", "build"]`.

Usuario no-root (`node`), `USER node`. `HEIMDALLM_API_URL` y `HEIMDALLM_API_TOKEN[_FILE]` leídos del entorno en runtime. Imagen bajo ~200 MB.

El wiring completo de docker-compose (dependencia del servicio daemon, volumen montado para `/data/api_token`, helper `make setup`) vive en #33. Este Dockerfile basta para `docker build` y `docker run` el web UI standalone, apuntando a un daemon existente via `HEIMDALLM_API_URL`.

## Mapeo de criterios de aceptación

| AC de #30 | Satisfecho por |
|---|---|
| `npm run build` sin errores | build de adapter-node en CI; `svelte-check` limpio |
| `npm run dev` levanta el servidor de desarrollo | dev server de Vite en `:5173` proxy-ando al daemon local |
| `api.ts` tipado correctamente y conecta al daemon | `src/lib/api.ts` con métodos tipados + `src/lib/types.ts`; proxy via `/api/*` |
| SSE recibe y parsea eventos correctamente | `EventSource` nativo sobre el proxy `/events`; wrapper `src/lib/sse.ts` + test |
| Token leído automáticamente del volumen o env var | `src/lib/server/token.ts` con precedencia env → fichero + test |

## Fuera de alcance (deferred explícitamente)

- **Rutas más allá de `/`.** Dashboard completo, PRs, Issues, Agents, Config, Logs se ponen en #31/#32.
- **Integración docker-compose + `make setup`.** Issue #33.
- **Endpoints de issue-tracking del daemon.** Fase 2, issues #24–#29. `api.ts` define los métodos; harán 404 hasta que el backend los implemente.
- **UI de autenticación.** El token vive en env/volumen; sin pantalla de login, sin UI de rotación.
- **Workflow de CI para `web_ui/`.** Este PR solo necesita que el scaffold pase build local; añadir un job de GitHub Actions para `web_ui/` puede seguir (trackeado por separado si no se incluye).
- **Dark mode / theming / design tokens personalizados.** Solo Tailwind por defecto.

## Riesgos y trade-offs aceptados

- **Svelte 5 runes** son el default actual pero relativamente nuevos — el equipo los adopta aquí por primera vez. Los docs y el ecosistema están sólidos; sin blockers esperados.
- **Métodos de Fase 2 en `api.ts`** harán 404 en runtime hasta que #25–#28 aterricen. Decisión deliberada: desbloquear trabajo paralelo > preocupación por código muerto.
- **Server proxy añade un hop**; para un dashboard en `localhost`, el coste de latencia es despreciable.

## Orden de implementación sugerido

1. Scaffolding base: `npm create svelte@latest`, configurar adapter-node, tsconfig, package.json, vite.config con plugin de Tailwind.
2. `src/lib/types.ts` — modelos espejo de Dart.
3. `src/lib/server/token.ts` + `tests/token.test.ts`.
4. Proxies: `src/routes/api/[...path]/+server.ts` y `src/routes/events/+server.ts`.
5. `src/lib/api.ts` + `tests/api.test.ts`.
6. `src/lib/sse.ts` + `tests/sse.test.ts`.
7. `src/lib/stores.ts`, `+layout.ts`, `+layout.svelte`.
8. `+page.svelte` con el indicador de estado y tabla de stats.
9. Dockerfile + `.dockerignore`.
10. README (quickstart de dev).
11. Verificación: `npm run build`, `npm run dev`, `npm test`, `npm run check`.

Cada paso es verificable localmente antes de pasar al siguiente.
