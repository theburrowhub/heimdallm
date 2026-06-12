# Modo Autónomo end-to-end — Diseño

- **Fecha**: 2026-06-12
- **Estado**: Aprobado para planificación
- **Ámbito**: `daemon/` (Go). UI Flutter solo recibe eventos SSE nuevos (observabilidad), sin cambios de comportamiento.

## 1. Objetivo

Dar a Heimdallm un modo **100% desatendido** que, por sí solo:

1. **Selecciona** una tarea (issue) según una cascada de preferencia.
2. Ejecuta **triage → refinement → development** sin esperar a que un humano edite labels.
3. **Publica la PR**.
4. **Vigila la PR** hasta que un compañero la revisa.
5. **Reacciona a la review**:
   - `CHANGES_REQUESTED` o `COMMENTED` → corrige.
   - `APPROVED` con comentarios accionables sin resolver → corrige.
   - `APPROVED` limpio → **merge** (construido pero **desactivado** por defecto).

El diseño **orquesta la maquinaria existente**; no reescribe el pipeline de issues, los workers, ni el bucle de review.

## 2. Qué ya existe (se reutiliza tal cual)

| Pieza | Ubicación | Rol en el modo autónomo |
|---|---|---|
| FSM de etapas `triage/refinement/development` | `daemon/internal/issues/stage_transition.go` (`NextStage`, `TransitionIssueStage`) | El orquestador lo invoca para avanzar etapa él mismo |
| Triage / Refinement / Development | `daemon/internal/issues/pipeline.go`, `worker/{triage,refinement,implement}.go` | Etapas ejecutadas, sin cambios de lógica interna |
| Ejecución agéntica del agente | `daemon/internal/executor/executor.go` (`ExecOptions`, `buildArgs`, `ExecuteRaw`) | Ya corre multi-turn con repo como workdir y permisos de escritura |
| Creación de PR + git ops | `daemon/internal/issues/pipeline.go` (`runAutoImplement`), `issues/gitops.go` | Crea rama, commit, push, PR con metadata |
| Vigilancia de PR propia | Tier 3 watch: `bus/watch.go`, `main_pr_review_state.go` (`refreshAutoImplementPRReviewState`) | Detecta `external_review_state` de PRs con `auto_implement_issue_id` |
| Respuesta y corrección por review | `issues/respond.go` (`Responder`), `issues/runfix.go` (`FixRunner`) | `COMMENTED`→responder, `CHANGES_REQUESTED`→fix+push. Con caps por-PR, cooldown y guard `FIX_PUSHED` |
| Generación de texto libre por agente | `issues/pipeline.go` (`generatePRDescription`), `main_pr_review_executors.go` (`GenerateReviewResponse`) | Patrón reutilizado para el comentario de coordinación |
| Resolución config global→org→repo | `config/config.go` (`AIForRepo`, ~681-712, `applyScopedAI`) | Patrón a replicar para los circuit breakers |
| Circuit breakers + denylist de paths | `config/circuit_breaker.go`, `store/{circuitbreaker,issue_circuitbreaker}.go` | Base de seguridad; se extiende (ver §6) |

## 3. Qué es nuevo

1. **Task Selector** con cascada de preferencia + cortesía de reasignación.
2. **Orchestrator** que encadena las etapas single-flight sin gating humano por labels.
3. **Breaker de `implement` por repo·hora** (la dimensión de "amplitud" que hoy no se acota).
4. **Layering global→org→repo de todos los circuit breakers** (hoy solo global).
5. **Heurística "aprobado con issues"** (APPROVED con comentarios accionables → corregir).
6. **Gate de merge** (`auto_merge`, desactivado por defecto).
7. **Comentario de coordinación generado por el agente**.
8. **Eventos SSE / activity** para observabilidad del comportamiento autónomo.

## 4. Arquitectura

Paquete nuevo `daemon/internal/autonomous/` con dos piezas finas montadas **delante** de la maquinaria existente, conducidas por un poller dedicado (análogo a los tiers actuales).

```
            ┌─────────────────────────────────────────────────────────────┐
            │  autonomous.Selector (ranura triage libre → elige 1 tarea)    │
            │   cascada:  bot-assigned → unassigned → others (reassign+cmt) │
            └───────────────┬───────────────────────────────────────────────┘
                            │ inyecta
            ┌───────────────▼───────────────────────────────────────────────┐
            │  autonomous.Orchestrator — pipeline single-flight (1 por fase) │
            │   triage ─▶ refinement ─▶ development ─▶ (PR creada)            │
            │   (usa workers/pipeline existentes; avanza etapa él mismo)     │
            └───────────────┬───────────────────────────────────────────────┘
                            │ PR creada → enrolada en Tier 3 watch (ya existe)
            ┌───────────────▼───────────────────────────────────────────────┐
            │  Review-loop (existente): Tier3 → Responder / FixRunner        │
            │   + clasificación "approved-with-issues"                        │
            │   + gate de merge (auto_merge, OFF)                             │
            └─────────────────────────────────────────────────────────────────┘
```

### 4.1 Modelo de concurrencia: single-flight por fase

"Una por cada fase" = cinta de montaje de 4 etapas, **una activa por etapa**:

- A lo sumo **1** issue en triage, **1** en refinement, **1** en development y **1** fix-de-review ejecutándose a la vez.
- El **Selector** solo inyecta una tarea nueva cuando la **ranura de triage está libre**.
- El monitoreo de PRs (Tier 3) sigue siendo **asíncrono y sin límite de cantidad** (es barato); el single-flight aplica al **trabajo del agente** (el fix de review se serializa a concurrencia=1 en modo autónomo).

Implementación: cada etapa se protege con un guard de concurrencia=1 (mutex/semáforo de 1) activo solo cuando el modo autónomo está on para ese repo. No altera el comportamiento normal del daemon.

## 5. Task Selector

Archivo nuevo: `daemon/internal/autonomous/selector.go`.

- **Disparo**: cuando la ranura de triage del orquestador está libre.
- **Ámbito**: pool **global** sobre todos los repos monitorizados (respetando el `enabled` por repo/org).
- **Identidad del bot**: `cfg.BotLogin` (resuelto al arranque vía `ghClient.AuthenticatedUser()`).

### 5.1 Cascada de selección (en orden, primer match gana)

Para cada candidato se **excluyen siempre** issues con `skip_labels` o `blocked_labels`, y las **ya empezadas**.

1. Issues **asignadas al bot**, no empezadas.
2. Issues **sin asignar**, no empezadas.
3. Issues **asignadas a otro usuario**, no empezadas — solo si `take_others_tasks = true`.
   - Antes de empezar: **reasignarse + comentar** (ver §5.3).

Orden dentro de cada bucket: **más antigua primero** (por `updated_at`), configurable a futuro.

### 5.2 Definición de "no empezada"

Una issue está **no empezada** si:

- **No** tiene una PR abierta vinculada (sin fila en `prs` con `auto_implement_issue_id = <issue>` y `state = "open"`), **y**
- **No** existe una rama remota activa que referencie la issue (heurística sobre el nombre de rama del patrón que usa `gitops`, vía la API de GitHub).

### 5.3 Cortesía al coger tarea ajena (bucket 3)

- `reassign_on_take = true` → añadir al bot como assignee (GitHub `add-assignees`) **manteniendo** al assignee original.
- Publicar un **comentario de coordinación generado por el agente** (no plantilla), siguiendo el patrón de `GenerateReviewResponse`: prompt con contexto de la issue (con `sanitiseUntrustedFreeText` en el cuerpo) → `ExecuteRaw` en modo review-only → `PostComment`. El comentario explica que Heimdallm va a empezar la tarea y por qué.

## 6. Orchestrator

Archivo nuevo: `daemon/internal/autonomous/orchestrator.go`.

- Conduce las 4 etapas single-flight.
- Al **completar con éxito** una etapa, **avanza la issue él mismo** llamando a `NextStage()` + `TransitionIssueStage()` (mantiene labels y comentario de auditoría como rastro), e inyecta en la siguiente etapa **sin esperar a un humano**.
- **Sobrescribe el gating por labels** cuando el modo autónomo está activo para el repo, pero **respeta** la metadata configurada: `pr_assignee`, `pr_reviewers`, `pr_labels`, `pr_draft` (vía `AIForRepo`).
- Reutiliza los handlers existentes de cada etapa; no duplica su lógica.

### 6.1 Modo agéntico pleno

Todas las fases corren el agente en modo agéntico; **development** es la más "suelta". En modo autónomo, las `ExecOptions` de development se configuran para máxima autonomía:

- `MaxTurns`: alto / sin tope práctico (configurable; ver §8 `dev_max_turns`).
- `Effort`: `high`/`max`.
- `PermissionMode`: `acceptEdits` + permisos de escritura (ya aplicado en auto_implement).
- `WorkDir`: repo clonado completo (`--add-dir`), ya aplicado si `local_dir` está configurado.
- `Timeout`: amplio y configurable (refinement ya usa 30m; development obtiene su propio timeout autónomo).
- **Prompt** explícitamente agéntico: "explora el repo, implementa, ejecuta tests, itera hasta dejar la issue lista para PR".

**Nota de honestidad de diseño**: el daemon corre el CLI **no-interactivo** (`-p`); no hay humano para corregir a mitad de ejecución. El agente corre hasta `MaxTurns`/timeout y se cosechan los cambios del worktree. El **bucle de review (FixRunner)** es el mecanismo de realimentación que sustituye los "nudges" interactivos.

## 7. Review-loop y reacción a la review

Reutiliza Tier 3 + `Responder`/`FixRunner`. Añadidos:

- **Clasificación de resultado de review**:
  - `CHANGES_REQUESTED` → `FixRunner` (existente).
  - `COMMENTED` → `Responder`/`FixRunner` (existente).
  - `APPROVED` **con comentarios de review accionables sin resolver** → tratar como corrección → `FixRunner`. (Heurística nueva: APPROVED con threads de comentarios no resueltos o cuerpo con peticiones de cambio.)
  - `APPROVED` **limpio** → **gate de merge**.
- **Gate de merge** (`auto_merge`, **default `false`**): construido completo (merge vía GitHub API respetando método configurado), pero desactivado. Cuando está off, el bot solo deja la PR aprobada lista y registra el evento.
- **Serialización**: en modo autónomo, `FixRunner` se envuelve con concurrencia=1 (la "ranura de review" del single-flight).

## 8. Configuración

Sección nueva `[autonomous]`, con override **global → org → repo** (mismo patrón que `AIForRepo`).

```toml
[autonomous]
enabled = false              # switch global (kill-switch manual)
auto_merge = false           # gate de merge — construido pero desactivado
merge_method = "squash"      # método usado cuando auto_merge se active (squash|merge|rebase)
take_others_tasks = true     # habilita el bucket 3 de la cascada
reassign_on_take = true      # reasignarse (manteniendo assignee original) al coger tarea ajena
dev_max_turns = 0            # 0 = sin tope práctico para development; >0 = tope de seguridad
dev_effort = "high"          # effort del agente en development
dev_timeout = "45m"          # timeout amplio para la fase de development

[autonomous.orgs."org"]      # override por organización
enabled = true

[autonomous.repos."org/repo"]  # override por repo (gana)
enabled = true
auto_merge = false
```

### 8.1 Circuit breakers: layering global→org→repo + breaker de implement

Se extiende `CircuitBreakerConfig` y se añade el resolver, replicando `AIForRepo`:

```go
type CircuitBreakerConfig struct {
    PerPR24h        int  // existente
    PerRepoHr       int  // existente (reviews)
    PerIssue24h     int  // existente (triages)
    PerIssueRepoHr  int  // existente (triages)
    PerImplRepoHr   int  // NUEVO: implements/development por repo·hora
}
```

- `OrgAI` y `RepoAI` ganan `CircuitBreaker *CircuitBreakerConfig` (nil = heredar).
- Nuevo `func (c *Config) CircuitBreakerForRepo(repo string) CircuitBreakerConfig` con precedencia repo > org > global.
- El enforcement del breaker de implement cuenta filas de `issue_reviews` con `action_taken IN ('auto_implement','auto_implement_failed','auto_implement_no_changes')` en la ventana de 1h por repo (hoy el breaker de issues solo cuenta `review_only`).
- **Default** `PerImplRepoHr`: valor conservador inicial (p.ej. 5/h por repo), elevable por org/repo.

### 8.2 Seguridad resultante (sin daily cap arbitrario)

- **Single-flight** → acota concurrencia.
- **`PerImplRepoHr`** → acota amplitud (cuántas tareas nuevas se desarrollan por repo·hora).
- **Breakers de fix por-PR + cooldown + `FIX_PUSHED`** → acotan profundidad (no machacar una PR).
- **Denylist de paths sensibles** (`sanitiseUntrustedFreeText` / git ops) → seguridad de contenido.
- **`enabled`** (global/org/repo) → kill-switch manual.
- Se respetan **siempre** `skip_labels` / `blocked_labels`.

## 9. Estado y persistencia

Reutiliza `issues`, `issue_reviews`, `prs`. Añadidos mínimos:

- Marca `claimed_by_autonomous` (bool) en `issues` para distinguir tareas tomadas por el modo autónomo (auditoría / UI).
- La etapa actual ya vive en el FSM (`IssueStage`) y la relación issue↔PR en `prs.auto_implement_issue_id`. No se duplica.

## 10. Observabilidad

Eventos SSE nuevos (broker existente `sse/broker.go`) + activity log:

- `autonomous_task_selected` (issue, bucket de la cascada).
- `autonomous_task_reassigned` (issue, assignee añadido).
- `autonomous_stage_advanced` (issue, from→to).
- `autonomous_review_classified` (PR, clasificación: changes/commented/approved-with-issues/approved-clean).
- `autonomous_merge_skipped` (PR, motivo: `auto_merge=false`).

La UI Flutter consume estos eventos sin cambios de comportamiento.

## 11. Componentes y límites (diseño para aislamiento)

| Unidad | Qué hace | Cómo se usa | De qué depende |
|---|---|---|---|
| `autonomous.Selector` | Elige 1 issue según la cascada; aplica cortesía | El poller la llama cuando triage está libre | `github` client, `store` (prs/issues), `executor` (comentario), `config` |
| `autonomous.Orchestrator` | Encadena etapas single-flight, avanza FSM | El poller la conduce | `issues` pipeline/workers, `stage_transition`, guards de concurrencia |
| `config.CircuitBreakerForRepo` | Resuelve breakers global→org→repo | Llamado donde hoy se leen breakers | `config` |
| Clasificador de review | Decide fix vs merge-gate | Dentro del review-state refresh de Tier 3 | `github` (reviews/comments), `store` |
| Poller autónomo | Tick que orquesta selector+orchestrator | Wired en `cmd/heimdallm/main.go` junto a tiers | scheduler abstractions existentes |

Cada unidad tiene una responsabilidad clara, interfaz definida y es testeable en aislamiento (mocks de `github`/`store`/`executor` como en los tests existentes).

## 12. Testing

- **Selector**: tests de la cascada (bot-assigned / unassigned / others), exclusión por skip/blocked labels, definición de "no empezada" (con/ sin PR vinculada, con/sin rama remota), cortesía de reasignación.
- **CircuitBreakerForRepo**: precedencia repo>org>global, herencia con nil, breaker de implement contando los `action_taken` correctos.
- **Orchestrator**: single-flight (no arranca etapa N+1 con la N ocupada), avance de FSM sin labels manuales, respeto de `pr_assignee`/`pr_reviewers`.
- **Clasificador de review**: approved-clean vs approved-with-issues, mapeo a fix/merge-gate.
- **Gate de merge**: con `auto_merge=false` no mergea y emite `autonomous_merge_skipped`.
- Todo con `-race`, siguiendo la convención del repo (ver `Makefile`).

## 13. Fuera de alcance (YAGNI)

- Activación del auto-merge real (se construye pero queda OFF; activarlo es una decisión futura).
- Priorización avanzada (milestones, peso por complejidad): de momento solo "más antigua primero".
- Handoff de tareas entre daemons de distintos operadores.
- Cambios de comportamiento en la UI Flutter más allá de consumir los eventos nuevos.
