# Heimdallm v2 — Design Spec

**Fecha:** 2026-04-16
**Estado:** Aprobado

## Visión

Heimdallm (Heimdall + LLM) es la evolución del proyecto Heimdallm. Unifica el repo desktop (`auto-pr`) y el repo Docker (`heimdallm-docker`) en un único monorepo, añade tracking y procesamiento automático de issues con LLM, y ofrece una interfaz web como alternativa a la Flutter app.

---

## Fase 1: Rename + Consolidación de Repos

### Objetivo
Renombrar el repo existente `theburrowhub/auto-pr` → `theburrowhub/heimdallm` (GitHub preserva historial, issues, PRs y redirige URLs) y fusionar `heimdallm-docker` en él como subdirectorio `docker/`.

### Estructura del monorepo

```
heimdallm/
├── daemon/           # Go daemon — binario renombrado a heimdallm
├── flutter_app/      # Desktop UI (macOS/Linux) — sin cambios funcionales
├── web_ui/           # NUEVO: SvelteKit web dashboard
├── docker/           # Contenido fusionado de heimdallm-docker
│   ├── Dockerfile
│   ├── docker-compose.yml
│   └── .env.example
└── docs/
    └── superpowers/
```

### Alcance del rename

| Artefacto | Antes | Después |
|-----------|-------|---------|
| Módulo Go | `github.com/heimdallm/daemon` | `github.com/heimdallm/daemon` |
| Binario | `heimdallm` | `heimdallm` |
| Config dir | `~/.config/heimdallm/` | `~/.config/heimdallm/` |
| Data dir | `~/.local/share/heimdallm/` | `~/.local/share/heimdallm/` |
| LaunchAgent | `com.heimdallm.daemon.plist` | `com.heimdallm.daemon.plist` |
| Token header HTTP | `X-Heimdallm-Token` | `X-Heimdallm-Token` |
| Env vars | `HEIMDALLM_*` | `HEIMDALLM_*` |
| Docker image | `heimdallm/daemon` | `heimdallm/daemon` |
| SSE broker events | sin cambio de formato | sin cambio de formato |

### Migración de heimdallm-docker
- GitHub: `Settings → Rename` en `auto-pr` → `heimdallm`
- El contenido de `heimdallm-docker` se incorpora en `docker/` vía `git subtree add` para preservar su historial de commits
- Los paths de config, Dockerfile, docker-compose y `.env.example` se actualizan al nuevo naming
- Una vez fusionado y verificado, `heimdallm-docker` se archiva (no se borra, para preservar historial de issues/PRs externos)

### Criterios de éxito
- `go build ./...` y `go test ./...` pasan con el nuevo module path
- `flutter analyze` sin errores
- Docker image buildea y arranca correctamente
- Docs y README actualizados

---

## Fase 2: Issue Tracking Pipeline

### Objetivo
Extender el daemon para monitorear issues de GitHub en los repos configurados y procesarlos con el LLM configurado. El pipeline sigue el mismo patrón arquitectónico que el pipeline de PR reviews existente.

### Modelo de datos (SQLite)

```sql
CREATE TABLE issues (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  github_id   INTEGER UNIQUE NOT NULL,
  repo        TEXT NOT NULL,
  number      INTEGER NOT NULL,
  title       TEXT NOT NULL,
  body        TEXT NOT NULL DEFAULT '',
  author      TEXT NOT NULL,
  assignees   TEXT NOT NULL DEFAULT '[]', -- JSON array
  labels      TEXT NOT NULL DEFAULT '[]', -- JSON array
  state       TEXT NOT NULL,              -- open | closed
  created_at  DATETIME NOT NULL,
  fetched_at  DATETIME NOT NULL,
  dismissed   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE issue_reviews (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  issue_id     INTEGER NOT NULL REFERENCES issues(id),
  cli_used     TEXT NOT NULL,
  summary      TEXT NOT NULL,
  triage       TEXT NOT NULL,  -- severity + category + suggested_assignee (JSON)
  suggestions  TEXT NOT NULL DEFAULT '[]', -- JSON array
  action_taken TEXT NOT NULL DEFAULT 'review_only', -- review_only | auto_implement
  pr_created   INTEGER NOT NULL DEFAULT 0, -- GitHub PR number if created, else 0
  created_at   DATETIME NOT NULL
);
```

### Configuración

```toml
[github.issue_tracking]
enabled               = false
process_label         = ""                    # vacío = procesar todos los issues
skip_label            = "no-heimdallm"        # opt-out: ignorar este issue
auto_implement_label  = "heimdallm-implement" # trigger modo auto-implement
review_only_label     = "heimdallm-review"    # trigger modo review-only explícito
```

Env vars equivalentes:
```
HEIMDALLM_ISSUE_TRACKING_ENABLED=true
HEIMDALLM_ISSUE_PROCESS_LABEL=
HEIMDALLM_ISSUE_SKIP_LABEL=no-heimdallm
HEIMDALLM_ISSUE_AUTO_IMPLEMENT_LABEL=heimdallm-implement
HEIMDALLM_ISSUE_REVIEW_ONLY_LABEL=heimdallm-review
```

### Lógica de priorización y filtrado

```
Para cada repo monitorizado:
  1. Fetch issues abiertos
  2. Filtrar: si process_label != "" → solo issues con esa label
  3. Filtrar: excluir issues con skip_label
  4. Excluir: issues asignados a terceros (no al usuario autenticado)
  5. Ordenar:
     a. Asignados al usuario autenticado (primero)
     b. Sin asignar (segundo)
  6. Excluir: ya procesados y sin actividad nueva desde la última review
```

### Modos de output

**Modo `review_only` (default)**
El agente recibe: título + body del issue + últimos comentarios + diff del repo (si `local_dir` configurado).
Produce: triage (severidad, categoría), resumen técnico, sugerencias de implementación.
Acción: postea comentario en el issue de GitHub.

**Modo `auto_implement`** (requiere label `auto_implement_label` en el issue)
Igual que `review_only` + el agente tiene acceso de escritura al `local_dir` del repo.
Produce: análisis + código implementado.
Acción: crea rama `heimdallm/issue-{number}`, hace commit, abre PR linkeado al issue.

**Degradación:** si `auto_implement` se activa pero el repo no tiene `local_dir` configurado, el pipeline degrada a `review_only` y loguea un warning.

### Nuevos paquetes

```
daemon/internal/issues/
├── fetcher.go    # FetchIssuesToProcess() — llama a GitHub Search API
└── pipeline.go   # Run(issue, opts) — procesa un issue y actúa según modo
```

Nuevo método en `daemon/internal/github/client.go`:
```go
// FetchIssues returns open issues for a repo, filtered by labels.
// Priority: assigned to authenticatedUser first, then unassigned.
func (c *Client) FetchIssues(repo, processLabel string) ([]*Issue, error)
```

El scheduler principal (`makePollFn`) incluye el ciclo de issues si `issue_tracking.enabled = true`.

### Endpoints HTTP nuevos

| Método | Path | Auth | Descripción |
|--------|------|------|-------------|
| GET | `/issues` | Token | Lista issues con su última review |
| GET | `/issues/{id}` | Token | Issue + todas sus reviews |
| POST | `/issues/{id}/review` | Token | Dispara review manual |
| POST | `/issues/{id}/dismiss` | Token | Descarta issue |
| POST | `/issues/{id}/undismiss` | Token | Restaura issue |

### Criterios de éxito
- Issues se detectan y procesan en el ciclo de polling
- Comentario aparece en GitHub para modo `review_only`
- Rama + PR creados correctamente para modo `auto_implement`
- Degradación a `review_only` loguea warning cuando falta `local_dir`
- Tests unitarios para `fetcher.go`, `pipeline.go` y los nuevos endpoints

---

## Fase 3: Web UI (SvelteKit)

### Objetivo
Dashboard web como alternativa a la Flutter desktop app. Feature parity completa. Se ejecuta como contenedor Docker independiente.

### Stack
- **Framework:** SvelteKit + Vite
- **Estilos:** TailwindCSS
- **Tiempo real:** EventSource nativo (SSE) — igual que la Flutter app
- **Sin base de datos propia** — todo viene de la API del daemon

### Estructura

```
web_ui/
├── src/
│   ├── lib/
│   │   ├── api.ts       # cliente HTTP — mismo contrato que api_client.dart
│   │   └── sse.ts       # SSE stream de eventos en tiempo real
│   └── routes/
│       ├── +layout.svelte         # shell con nav y auth check
│       ├── +page.svelte           # Dashboard unificado PRs + Issues
│       ├── issues/+page.svelte    # Issue tracking
│       ├── prs/+page.svelte       # PR reviews
│       ├── agents/+page.svelte    # Gestión de agentes
│       ├── config/+page.svelte    # Configuración
│       └── logs/+page.svelte      # Logs en vivo
├── Dockerfile
├── package.json
└── .env.example
```

### Configuración del servicio

```yaml
# docker/docker-compose.yml
services:
  daemon:
    image: heimdallm/daemon:latest
    volumes:
      - heimdallm_data:/data
    ports:
      - "7842:7842"

  web:
    image: heimdallm/web:latest
    volumes:
      - heimdallm_data:/data:ro  # solo lectura para leer el token
    ports:
      - "3000:3000"
    environment:
      HEIMDALLM_API_URL: http://daemon:7842
      # HEIMDALLM_API_TOKEN se puede sobreescribir manualmente; si no,
      # el servicio lo lee de /data/api_token al arrancar
    depends_on:
      - daemon

volumes:
  heimdallm_data:
```

### Setup automático del token

**Mecanismo principal — volumen compartido:**
El daemon escribe el token en `/data/api_token` (mismo archivo que hoy, montado en volumen compartido).
El servicio web lee `/data/api_token` al arrancar si `HEIMDALLM_API_TOKEN` no está definido.

**Mecanismo fallback — script:**
```bash
make setup
# equivalente a:
# docker compose exec daemon cat /data/api_token > .token
# y lo inyecta en .env del servicio web
```

### Autenticación
Token estático `X-Heimdallm-Token` en cada petición HTTP desde el cliente SvelteKit al daemon. Sin OAuth en esta versión.

### Criterios de éxito
- Dashboard accesible en `http://localhost:3000`
- Token leído automáticamente del volumen compartido sin config manual
- SSE actualizaciones en tiempo real funcionan
- `make setup` configura el entorno desde cero en un solo comando
- Todas las vistas de la Flutter app replicadas

---

## Orden de implementación recomendado

```
1. Fase 1 — Rename + Consolidación   (prerequisito para todo lo demás)
2. Fase 2 — Issue Tracking Pipeline  (core de la nueva propuesta de valor)
3. Fase 3 — Web UI                   (presentación alternativa, independiente)
```

Cada fase es mergeable y funcional por sí sola.
