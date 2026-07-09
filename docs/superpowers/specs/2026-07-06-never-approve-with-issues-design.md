# Diseño: "No aprobar PRs con issues" (never_approve_with_issues)

- **Fecha:** 2026-07-06
- **Estado:** Aprobado (diseño), pendiente de plan de implementación
- **Autor:** jamuriano

## Resumen

Añadir un setting booleano, global y con override por org y por repo (jerarquía
**repo > org > global**), que cuando resuelve a `true` cambia el veredicto de las
reviews propias de la app: cualquier review que contenga issues deja de publicarse
como **APPROVE** y pasa a publicarse como **COMMENT**. Las reviews limpias (sin
issues) siguen aprobando, y los casos de severidad `high` siguen siendo
**REQUEST_CHANGES**.

Por defecto **OFF** a nivel global → cambio 100% backward-compatible.

## Motivación

Hoy la app aprueba PRs aunque haya encontrado issues de severidad `low`/`medium`.
Algunos repos/equipos prefieren que la app **nunca** ponga un Approval cuando
detectó algo que comentar, dejando la aprobación final a un humano. Este setting
lo permite sin forzarlo globalmente.

## Comportamiento (contrato)

Con el setting resuelto a `true` para un repo, en el momento de decidir el evento
de una review propia:

| Situación | Hoy | Con setting ON |
|---|---|---|
| 0 issues | APPROVE | APPROVE (sin cambio) |
| Issues presentes, severidad final `high` | REQUEST_CHANGES | REQUEST_CHANGES (sin cambio) |
| Issues presentes, severidad final `low`/`medium`/vacía | **APPROVE** | **COMMENT** |

Definición de "contiene issues": `len(ReviewResult.Issues) > 0`.

Con el setting OFF (default), el comportamiento es idéntico al actual.

## Estado actual del código (referencia)

- Mapeo severidad → evento: `SeverityToEvent` en `daemon/internal/pipeline/pipeline.go:998-1007`
  (`high → REQUEST_CHANGES`, resto → `APPROVE`; **no existe camino a `COMMENT`**).
- Submit a GitHub: `Client.SubmitReview(repo, number, body, event)` en
  `daemon/internal/github/client.go:464-503` — **ya acepta `COMMENT`**.
- Callers de `SubmitReview(..., SeverityToEvent(...))`:
  - Camino principal: `pipeline.go:644-648`.
  - Reintento de publicación (`PublishPending`): `pipeline.go:776-780`.
  - Camino en `main`: `daemon/cmd/heimdallm/main.go:1063-1066`.
- Modelo persistido: `store.Review` en `daemon/internal/store/reviews.go:16`.
  Migraciones de la tabla `reviews` = `ALTER TABLE reviews ADD COLUMN ...`
  idempotentes en `daemon/internal/store/store.go:195-203`.
- Precedente de setting global-con-override (bool tri-estado): `GeneratePRDescription`
  — global `AIConfig` (`config.go:459`), `*bool` en `RepoAI` (`config.go:548`) y
  `OrgAI` (`config.go:610`), resuelto por `AIForRepo`/`applyScopedAI`
  (`config.go:746,796,819,894`).

## Decisión de arquitectura: reintentos (Enfoque A)

El nuevo veredicto depende de "¿hay issues?" además de la severidad. El `Review`
persistido es la fuente de verdad para reproducir la decisión en reintentos sin
re-analizar la PR. Por tanto:

**Enfoque A (elegido): persistir el evento ya decidido.** El evento
(`APPROVE`/`COMMENT`/`REQUEST_CHANGES`) se calcula una sola vez en el momento de la
review resolviendo el setting del repo, y se guarda en el `Review`. Los reintentos
publican exactamente lo decidido, aunque el setting cambie entremedias. Determinista
y auditable.

Descartado el Enfoque B (recalcular en cada publicación): un cambio de config entre
el análisis y la publicación produciría un veredicto sorpresa.

## Diseño detallado

### 1. Config (Go) — `daemon/internal/config/config.go`

Reutilizar la maquinaria de merge existente de `AIConfig`/`AIForRepo` (misma que
`GeneratePRDescription`), en lugar de un resolver ad-hoc:

- **Global:** nuevo campo `NeverApproveWithIssues bool` en `AIConfig`, TOML key
  `never_approve_with_issues`. Default `false` (en `applyDefaults`; también en
  `applyEnvOverrides` si se sigue el patrón de `GeneratePRDescription`, opcional).
- **Override org:** `NeverApproveWithIssues *bool` en `OrgAI` (`toml:"...,omitempty"`;
  `nil` = heredar).
- **Override repo:** `NeverApproveWithIssues *bool` en `RepoAI` (`toml:"...,omitempty"`;
  `nil` = heredar).
- **Resolución:** extender `AIForRepo`/`applyScopedAI` para incluir el nuevo campo
  (igual que `GeneratePRDescription`: string vacío no aplica; `*bool` no-nil gana).
  El valor efectivo se lee como `cfg.AIForRepo(repo).NeverApproveWithIssues`.

### 2. Pipeline — decisión y persistencia del evento

- **Función pura nueva** en `pipeline.go`:
  ```go
  // ReviewEvent decides the GitHub review event, honoring the
  // never-approve-with-issues setting. It builds on SeverityToEvent: when the
  // base decision would be APPROVE, the setting is on, and the review found
  // issues, it downgrades APPROVE to COMMENT. REQUEST_CHANGES is never altered.
  func ReviewEvent(finalSeverity string, hasIssues bool, neverApproveWithIssues bool) string {
      event := SeverityToEvent(finalSeverity)
      if event == "APPROVE" && neverApproveWithIssues && hasIssues {
          return "COMMENT"
      }
      return event
  }
  ```
  `SeverityToEvent` se mantiene intacta como building block.

- **`Review(...)`** (`pipeline.go`, tras calcular `finalSeverity` en `:600` y antes
  del submit en `:644-648`): resolver el setting del repo y calcular
  `event := ReviewEvent(finalSeverity, len(result.Issues) > 0, neverApprove)`.
  Guardar el evento en el `Review` (`rev.Event = event`, ver §3) **antes** de
  `InsertReview`, y pasar `event` a `SubmitReview` en lugar de
  `SeverityToEvent(finalSeverity)`.

- **`PublishPending`** (`pipeline.go:776-780`) y **`main.go:1063-1066`:** publicar
  usando `rev.Event` persistido. Fallback para filas antiguas sin evento:
  `if rev.Event == "" { event = SeverityToEvent(rev.Severity) }`. Esto respeta el
  Enfoque A y no rompe reviews pendientes ya guardadas antes de la migración.

### 3. Store — `daemon/internal/store/reviews.go` + `store.go`

- Añadir campo `Event string` al struct `store.Review` (`json:"event"`).
- Migración idempotente en `store.go` (junto a `:195-203`):
  `db.Exec("ALTER TABLE reviews ADD COLUMN event TEXT NOT NULL DEFAULT ''")`.
- Incluir `event` en el `INSERT` de `InsertReview` y en los `SELECT`/scan de las
  lecturas (`GetLatestReview`, y la query de `PublishPending`), manteniendo el orden
  de columnas coherente.

### 4. GUI Flutter

- **Modelo** `flutter_app/lib/core/models/config_model.dart`:
  - `AppConfig`: `bool neverApproveWithIssues` (default `false`) + (de)serialización.
  - `RepoConfig`: `bool? neverApproveWithIssues` (nullable = heredar) con centinela
    en `copyWith` (igual que los demás overrides) + (de)serialización.
  - `OrgConfig`: `bool? neverApproveWithIssues` + (de)serialización.
- **Settings global** `flutter_app/lib/features/config/config_screen.dart`: un
  `SwitchListTile` junto a los toggles de comportamiento de review; label
  *"No aprobar PRs con issues"*, subtítulo *"Si la review encuentra issues (de
  cualquier severidad), se publica como comentario en la PR en vez de una
  aprobación."* Serializar en `_buildConfig` (`:1117`).
- **Settings por-repo** `flutter_app/lib/features/repositories/repo_detail_screen.dart`:
  control de override tri-estado reutilizando el patrón existente
  (`feature_switch`/`OverrideDropdown` con badge "overridden" + reset vía
  `deleteRepoField`), mostrando el valor heredado repo→org→global (p.ej.
  `repoConfig?.neverApproveWithIssues ?? orgConfig?.neverApproveWithIssues ?? appConfig.neverApproveWithIssues`).
- **PATCH parcial:** añadir la key al diff `_computeRepoDiff` (`:144`) y verificar
  que el writer TOML parcial (`daemon/internal/config/writer.go`) la escribe/borra.

## Tests

### Go (unit)
- `ReviewEvent`: tabla severidad × `hasIssues` × `flag`:
  - flag off → idéntico a `SeverityToEvent` en todos los casos.
  - flag on + `high` + issues → `REQUEST_CHANGES`.
  - flag on + `low`/`medium`/`""` + issues → `COMMENT`.
  - flag on + sin issues → `APPROVE`.
- `AIForRepo`/`NeverApproveWithIssues`: merge tri-estado repo > org > global
  (nil hereda; no-nil gana en cada nivel; combinaciones repo-nil/org-set, etc.).
- Persistencia + reintento (Enfoque A): un `Review` con `Event="COMMENT"` se
  republica como `COMMENT` aunque el flag se apague después; fila legacy con
  `Event=""` cae al fallback `SeverityToEvent(Severity)`.
- Round-trip del nuevo campo `event` en el store (insert → select).

### Flutter
- (De)serialización del nuevo campo en `AppConfig`, `RepoConfig`, `OrgConfig`
  (incluyendo `null` = heredar en repo/org).
- Diff PATCH del override por repo (set y reset/borrado).

## Fuera de alcance (YAGNI)

- No se añade granularidad por severidad (p.ej. "comentar solo si medium"): el
  contrato es binario (hay issues / no hay issues).
- No se modifica el comportamiento de `REQUEST_CHANGES`.
- No se toca la clasificación de reviews de humanos externos
  (`autonomous/review_class.go`, `issues/reviewstate.go`).
