# GitHub API Efficiency for Polling — Diseño

- **Fecha**: 2026-06-15
- **Estado**: Aprobado para planificación
- **Ámbito**: `daemon/` (Go: `github`, `scheduler`, `config`, wiring en `cmd/heimdallm`), GUI Flutter, CLI TUI.

## 1. Problema

El daemon agota el límite **core REST de GitHub (5.000/h, por usuario y compartido)** y entra en 403-storm. Causa raíz medida (config real: ~75 repos, `poll_interval=1m`, `discovery=1m`, Tier3 30s):

- **Issues = O(repos)**: una llamada `GET /repos/{repo}/issues` por repo y ciclo → ~75 × 60 ≈ hasta 4.500 req/h solo en issues. Los **PRs ya están optimizados** (1 Search agregada). Asimetría central.
- **Sin ETag/304**: cada GET cuenta como llamada completa aunque el recurso no haya cambiado (los 304 NO cuentan contra el límite).
- **Rate-limiter local no GitHub-aware** (`scheduler/ratelimit.go`, 4.500 tokens estáticos): no lee `X-RateLimit-*` ni hace backoff ante 403/secondary → 403-storm.
- **GraphQL sin usar** (budget 5.000 pts/h casi intacto).
- **Tier3** cada 30s hace `GetPRSnapshot` (+`GetPRReviews`) por ítem.

## 2. Objetivo y restricciones

- Recortar ~80-90% del consumo core **sin penalizar la latencia donde importa** (DX).
- **Token personal** (sin GitHub App). **Webhooks fuera de alcance** (requieren GitHub App / endpoint público).
- Cadencia y umbrales **configurables por el usuario desde `config.toml`, GUI y TUI**.

## 3. Componentes

### C1 — Capa de peticiones condicionales (ETag/304)
`daemon/internal/github/` (cliente HTTP).

- **`ConditionalCache`**: mapa concurrente keyed por `method+path+rawquery` → `{etag string, body []byte, storedAt time.Time}`. Interfaz pequeña (`Get(key)`, `Put(key, etag, body)`), thread-safe (RWMutex), con límite de entradas (LRU simple o cap + evicción por antigüedad) para no crecer sin control.
- **Integración en el cliente**: un helper `doConditionalGET(path, accept)` que, si hay ETag cacheado para la key, envía `If-None-Match`. Respuesta:
  - `304 Not Modified` → devuelve el body cacheado y marca `fromCache=true` (no recomputa, no cuenta como consumo).
  - `200` con `ETag` → actualiza la caché y devuelve el body nuevo.
  - `200` sin `ETag` → passthrough sin cachear.
- **Aplicar a** los GET de polling idempotentes: snapshots de PR (`GetPRSnapshot`), reviews (`GetPRReviews`), repo archived/rename probe, labels, comments, y el fetch de issues/PRs (search) cuando proceda.
- **Caché en memoria** en v1 (vive en el proceso). Persistencia a SQLite = futuro (nice-to-have, no v1).
- Métrica: contador de hits 304 vs 200 para observabilidad (log/SSE opcional).

### C2 — Fetch de issues agregado vía Search
`daemon/internal/github/` + `daemon/internal/issues/fetcher.go`.

- Nuevo `SearchIssues(query string) ([]*Issue, error)` sobre `GET /search/issues?q=...&per_page=100` con paginación hasta el cap de 1000 resultados.
- Construir la query desde `issue_tracking` (assignees, organizations, filter_mode exclusive, labels): p.ej. `is:issue is:open assignee:<bot> org:<org1> org:<org2>` o `repo:<org/repo>` para repos sueltos. O(orgs) en lugar de O(repos).
- El fetcher pasa de iterar `FetchIssues` por repo a **una (o pocas) búsquedas agregadas**; mapear resultados a `*Issue` (number, title, labels, assignees, state, repo, updated_at — suficientes para selección/clasificación). Detalle por-issue queda on-demand (con ETag).
- Usa el **budget de search (30/min)**, separado del core. Tolera el lag de indexado (segundos) — aceptable para la DX.
- Mantener `FetchIssues` (REST per-repo) como fallback para casos sin search (p.ej. repos sin org) o tras error de search.

### C3 — Rate-limiter GitHub-aware
`daemon/internal/scheduler/ratelimit.go` (sustituye/extiende el estático).

- **`GitHubLimiter`**: lee de cada respuesta `X-RateLimit-Remaining`, `X-RateLimit-Reset`, `X-RateLimit-Resource`, y `Retry-After`/secondary-limit; mantiene el estado vivo de los budgets (core/search/graphql).
- **Throttle proactivo**: cuando `remaining < safetyThreshold` para un recurso, frena las llamadas de ese recurso hasta `reset`, **priorizando lo crítico** (publicar reviews, review-loop) sobre discovery/polling de bajo valor (tiers con prioridad).
- **Backoff ante 403/secondary**: exponencial + honra `Retry-After`; nunca hammering.
- El cliente HTTP reporta las cabeceras al limiter tras cada respuesta (hook/observer). El limiter expone `Acquire(ctx, tier, resource)` que bloquea/espera según presupuesto.
- `safetyThreshold` y los pesos por tier son configurables (ver C5).

### C4 — GraphQL batching (steady-state)
`daemon/internal/github/graphql.go` (nuevo).

- Cliente GraphQL mínimo (`POST /graphql`) con una query que trae, para el set de repos monitorizados: issues abiertos (assignee-scoped) + PRs review-requested, en **una sola petición** (usando `search()` connections o `repository(...)` por nodo con cursores).
- Usa el **budget graphql ocioso** (5.000 pts/h). Coste por puntos proporcional a nodos; una query para N repos ≪ N REST.
- Reemplaza el fetch agregado del steady-state (issues+PRs) cuando GraphQL está disponible; REST+ETag (C1) y Search (C2) quedan como fallback y para detalle/on-demand.
- Manejo de errores GraphQL (errors array, rate-limit en la respuesta), paginación por cursor.
- **Feature-flag** (`polling.use_graphql`, default según validación) para poder caer a REST/Search si algo falla.

### C5 — Intervalos adaptativos y configurables (3 capas)
`daemon/internal/scheduler/` + `config` + GUI + TUI.

- **Adaptativo por-repo**: repos con actividad reciente → intervalo mínimo; ociosos → backoff hasta un máximo. Señal de actividad: cambios detectados (200 vs 304) o eventos recientes.
- **Config nueva** sección `[polling]` (+ `[rate_limit]`): `poll_interval` (base), `min_interval`, `max_interval`, `adaptive` (bool), `discovery_interval`, `tier3_interval`, `rate_limit_safety_threshold`, `use_graphql`, `use_etag` (kill-switches). Resueltos con defaults sanos.
- **Exposición en las 3 capas** (mismo patrón que la config-UI de autonomous): `GET/PATCH /config` (DTO + handlers), sección editable en GUI Flutter (`config_screen.dart` + modelo + diff provider), display read-only en TUI (`buildConfigLines`).

### C6 — Tier3 condicional + backoff adaptativo
`daemon/cmd/heimdallm` (wiring Tier3).

- `GetPRSnapshot`/`GetPRReviews` pasan por C1 (ETag) → la mayoría de ticks devuelven 304 (gratis).
- Backoff adaptativo de los ítems vigilados (C5): ítems sin cambios amplían su intervalo; los activos se mantienen rápidos. `tier3_interval` configurable.

## 4. Fases (orden de construcción)

- **Fase 1 — parón de hemorragia, cero coste DX**: C1 (ETag/304) + C3 (rate-limiter GH-aware) + C2 (issues vía Search). Tests por componente.
- **Fase 2 — adaptativo + configurable**: C5 (intervalos adaptativos + config `[polling]`/`[rate_limit]` en las 3 capas) + C6 (Tier3). 
- **Fase 3 — GraphQL**: C4 (batching steady-state) tras feature-flag, con fallback a Fase 1/2.

## 5. Flujo de datos (steady-state objetivo)

```
ciclo de poll
  → GitHubLimiter.Acquire (comprueba budget vivo; throttle si bajo)
  → Fase3: 1 query GraphQL (issues+PRs de N repos)  [o]
    Fase1/2: 1 Search issues + 1 Search PRs (+ detalle on-demand con ETag)
  → respuestas → ConditionalCache actualiza ETags; 304 = gratis
  → limiter actualiza budgets desde X-RateLimit-*
  → Tier3: snapshots/reviews con ETag (304 mayoritario)
  → cadencia siguiente = adaptativa por actividad + config del usuario
```

## 6. Manejo de errores

- 304 → ruta normal (cache hit). 403/secondary → backoff + Retry-After (C3). Search lag/cap → fallback REST (C2). GraphQL error → fallback Search/REST (C4). ETag cache miss → GET normal. Todo degrada con seguridad; nada bloquea el review-loop crítico.

## 7. Testing

- **C1**: tests con `httptest` — segunda petición con ETag → servidor responde 304 → cliente devuelve cuerpo cacheado; 200 con ETag nuevo actualiza caché; sin ETag no cachea. Concurrencia (-race).
- **C2**: `SearchIssues` parsea/pagina/mapea; construcción de query desde `issue_tracking`; fallback a REST.
- **C3**: parseo de cabeceras; throttle cuando remaining<threshold; backoff y Retry-After; prioridad por tier. `-race`.
- **C4**: query GraphQL construida y parseada (httptest con payload GraphQL); fallback ante error; paginación por cursor.
- **C5**: resolución de config `[polling]`/`[rate_limit]` con defaults + overrides; lógica adaptativa (activo→min, ocioso→max); exposición en GET/PATCH /config; GUI (flutter test) y TUI (display).
- **C6**: Tier3 usa ETag (304) y backoff adaptativo.
- Gate por capa: `cd daemon && go build ./... && go vet ./... && go test ./... -race`; `cd flutter_app && flutter analyze && flutter test`; `make -C cli test`.

## 8. Fuera de alcance (YAGNI)

- **GitHub App** (token de instalación) y **webhooks** (push) — descartados por el usuario; north-star futuro.
- Persistencia de la caché de ETags a disco (v1 = memoria).
- Overrides per-org/repo de la config `[polling]` por UI (la config global basta para v1; el daemon puede soportar overrides por TOML si hiciera falta).

## 9. Observabilidad

Contadores/SSE opcionales: ratio de 304 vs 200, budget remaining por recurso, throttle activado, fallbacks GraphQL→REST. Para que el usuario vea el ahorro y el estado del rate-limit en GUI/TUI.
