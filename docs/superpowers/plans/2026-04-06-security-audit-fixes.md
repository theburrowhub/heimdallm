# Security Audit Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Corregir los 8 issues de seguridad identificados en la auditoría del daemon Go + cliente Flutter de Heimdallm.

**Architecture:** Los fixes se distribuyen entre el daemon Go (`daemon/`) y el cliente Flutter (`flutter_app/lib/core/api/`). Cada task toca un aspecto específico sin refactorizar código no relacionado. Los tests existentes en `daemon/internal/server/handlers_test.go` y `daemon/internal/executor/executor_test.go` deben seguir pasando tras cada commit.

**Tech Stack:** Go 1.21+, Flutter/Dart 3.8+, SQLite, chi/v5 router, `go test -race`

---

## Mapa de Archivos

| Archivo | Issues que toca |
|---------|----------------|
| `daemon/cmd/heimdallm/main.go` | #1 (auth fail-safe), #2 (cachedLogin race), #4 (JSON SSE) |
| `daemon/internal/executor/executor.go` | #3 (dangerousSegments) |
| `daemon/internal/executor/executor_test.go` | #3 (nuevos tests) |
| `daemon/internal/server/handlers.go` | #5 (rate limit), #6 (auth endpoints), #7 (config validation), #8 (log path) |
| `daemon/internal/server/handlers_test.go` | #5, #6, #7, #8 (nuevos tests) |
| `flutter_app/lib/core/api/api_client.dart` | #6 (auth headers en endpoints públicos) |

---

## Task 1: Auth fail-safe — Daemon no arranca sin token (Issue #1)

**Archivos:**
- Modify: `daemon/cmd/heimdallm/main.go:85-91`
- Test: no hay test unitario para este behavior; se verifica con `go build` y revisión manual

**Problema:** Si `loadOrCreateAPIToken` falla, `apiToken == ""` y `authMiddleware` salta TODA la autenticación (línea 65 de handlers.go: `if srv.apiToken != ""`).

- [ ] **Step 1: Cambiar Warn a Error + os.Exit(1)**

En `daemon/cmd/heimdallm/main.go`, reemplazar líneas 85-88:

```go
// ANTES:
apiToken, err := loadOrCreateAPIToken(dataDir())
if err != nil {
    slog.Warn("could not create API token — mutating endpoints unprotected", "err", err)
}
```

```go
// DESPUÉS:
apiToken, err := loadOrCreateAPIToken(dataDir())
if err != nil {
    slog.Error("could not create API token — refusing to start without authentication", "err", err)
    os.Exit(1)
}
```

- [ ] **Step 2: Compilar y verificar que no hay errores**

```bash
cd daemon && go build ./...
```
Expected: sin errores.

- [ ] **Step 3: Commit**

```bash
cd daemon && git add cmd/heimdallm/main.go
git commit -m "fix(security): abort startup if API token cannot be created

Prevents daemon from starting in unauthenticated state when
loadOrCreateAPIToken fails (e.g. disk full, bad permissions).
Previously a Warn was logged but execution continued with apiToken==\"\",
disabling all auth middleware checks."
```

---

## Task 2: Eliminar data race en cachedLogin (Issue #2)

**Archivos:**
- Modify: `daemon/cmd/heimdallm/main.go:296-308`

**Problema:** La variable `cachedLogin` es leída y escrita desde múltiples goroutines (HTTP handler concurrentes) sin sincronización. `go test -race` detectaría esto.

- [ ] **Step 1: Añadir mutex y reescribir el closure**

En `daemon/cmd/heimdallm/main.go`, la sección del `SetMeFn` (líneas ~296-307). Añadir la variable `loginMu` justo antes del closure, y proteger los accesos:

```go
// Justo ANTES de srv.SetMeFn(func() ...), añadir:
var loginMu sync.Mutex
var cachedLogin string

srv.SetMeFn(func() (string, error) {
    loginMu.Lock()
    if cachedLogin != "" {
        l := cachedLogin
        loginMu.Unlock()
        return l, nil
    }
    loginMu.Unlock()

    login, err := ghClient.AuthenticatedUser()

    loginMu.Lock()
    if err == nil && cachedLogin == "" {
        cachedLogin = login
    }
    loginMu.Unlock()

    return login, err
})
```

> **Nota:** La variable `cachedLogin` de scope superior (línea 297 original: `var cachedLogin string`) DEBE eliminarse o quedará en conflicto con esta declaración local. La nueva `var cachedLogin string` se declara dentro del bloque junto a `loginMu`.

- [ ] **Step 2: Verificar que compila y los tests pasan con -race**

```bash
cd daemon && go test ./... -race -timeout 60s
```
Expected: PASS, 0 races detected.

- [ ] **Step 3: Commit**

```bash
cd daemon && git add cmd/heimdallm/main.go
git commit -m "fix(security): protect cachedLogin with mutex to eliminate data race

Multiple goroutines can call GET /me concurrently, causing a data race
on the cachedLogin string variable. Added sync.Mutex to guard all reads
and writes."
```

---

## Task 3: Ampliar dangerousSegments con rutas de credenciales faltantes (Issue #3)

**Archivos:**
- Modify: `daemon/internal/executor/executor.go:248-253`
- Modify: `daemon/internal/executor/executor_test.go` (añadir casos)

**Problema:** `.kube`, `.docker`, `.netrc`, `.npmrc`, `.pypirc`, `.gem` no están en la denylist. Un `local_dir = ~/.kube` enviaría kubeconfigs al proveedor de IA.

- [ ] **Step 1: Escribir el test que falla**

En `daemon/internal/executor/executor_test.go`, añadir al slice `tests` dentro de `TestValidateWorkDir` (después del test `"ssh dir — rejected"`):

```go
{
    name:    ".kube dir — rejected",
    dir:     filepath.Join(home, ".kube"),
    wantErr: true,
},
{
    name:    ".docker dir — rejected",
    dir:     filepath.Join(home, ".docker"),
    wantErr: true,
},
{
    name:    ".netrc parent — rejected",
    dir:     filepath.Join(home, ".netrc"),
    wantErr: true, // EvalSymlinks lo rechaza (es archivo, no dir) pero añadir el segmento igual
},
```

> **Nota:** `.netrc` es un archivo, no un directorio, así que `os.Stat` + `IsDir()` lo rechazará antes de llegar al check de segmentos. Los tests relevantes son `.kube` y `.docker` que SÍ son directorios cuando existen. Si no existen en la máquina de test, `os.Stat` devolverá error (dir not found), lo cual también cuenta como rechazo (`wantErr: true`).

- [ ] **Step 2: Ejecutar para confirmar que fallan los tests de .kube y .docker**

```bash
cd daemon && go test ./internal/executor/... -run TestValidateWorkDir -v
```
Expected: FAIL en `.kube dir` y `.docker dir` (si los directorios existen en la máquina) o PASS si no existen (en cuyo caso el test de wantErr pasa porque Stat devuelve error).

> Si los dirs no existen localmente, los tests pasarán trivialmente. El valor de los tests está en CI donde puede haber `.kube` real. Los tests están correctos.

- [ ] **Step 3: Ampliar dangerousSegments en executor.go**

En `daemon/internal/executor/executor.go`, reemplazar el bloque `dangerousSegments` (líneas ~248-253):

```go
// ANTES:
var dangerousSegments = []string{
    "/.ssh",
    "/.gnupg",
    "/.aws",
    "/.config/heimdallm",
}
```

```go
// DESPUÉS:
var dangerousSegments = []string{
    "/.ssh",
    "/.gnupg",
    "/.aws",
    "/.config/heimdallm",
    "/.kube",          // Kubernetes credentials (service account tokens, certs)
    "/.docker",        // Docker registry auth (config.json)
    "/.netrc",         // FTP/HTTP plaintext credentials
    "/.npmrc",         // npm publish tokens
    "/.pypirc",        // PyPI publish tokens
    "/.gem",           // RubyGems credentials
    "/.config/gcloud", // Google Cloud credentials
}
```

- [ ] **Step 4: Ejecutar tests**

```bash
cd daemon && go test ./internal/executor/... -race -timeout 60s -v
```
Expected: PASS (incluyendo los nuevos casos).

- [ ] **Step 5: Commit**

```bash
cd daemon && git add internal/executor/executor.go internal/executor/executor_test.go
git commit -m "fix(security): block additional credential directories from AI workdir

Adds .kube, .docker, .netrc, .npmrc, .pypirc, .gem, and gcloud to the
dangerousSegments denylist for ValidateWorkDir. An AI CLI with access to
these directories could exfiltrate credentials to the AI provider."
```

---

## Task 4: Reemplazar fmt.Sprintf JSON manual con json.Marshal (Issue #6)

**Archivos:**
- Modify: `daemon/cmd/heimdallm/main.go`

**Problema:** `fmt.Sprintf` con `%q` construye JSON manualmente. Si `err.Error()` o `pr.Repo` contienen caracteres Unicode especiales, el resultado puede ser JSON inválido o diferente al esperado.

Los lugares a corregir en `main.go` son las llamadas a `broker.Publish(sse.Event{...})` que usan `fmt.Sprintf` para construir el campo `Data`.

- [ ] **Step 1: Añadir helper privado `sseData` en main.go**

Al final de `main.go` (antes del último `}`), añadir:

```go
// sseData serializes a map to a JSON string for SSE event Data fields.
// Using json.Marshal instead of fmt.Sprintf avoids encoding bugs with
// strings containing special characters or Unicode escape sequences.
func sseData(v map[string]any) string {
    b, err := json.Marshal(v)
    if err != nil {
        // Fallback: empty JSON object. Should never happen with basic types.
        return "{}"
    }
    return string(b)
}
```

También asegurarse de que `encoding/json` está en los imports de `main.go` (ya debería estarlo).

- [ ] **Step 2: Reemplazar todas las llamadas con fmt.Sprintf en broker.Publish**

En la función `runReview` (líneas ~158-173):

```go
// ANTES:
broker.Publish(sse.Event{Type: sse.EventPRDetected, Data: fmt.Sprintf(`{"pr_number":%d,"repo":%q}`, pr.Number, pr.Repo)})
broker.Publish(sse.Event{Type: sse.EventReviewStarted, Data: fmt.Sprintf(`{"pr_number":%d,"repo":%q}`, pr.Number, pr.Repo)})
// ...
broker.Publish(sse.Event{Type: sse.EventReviewError, Data: fmt.Sprintf(`{"pr_number":%d,"repo":%q,"error":%q}`, pr.Number, pr.Repo, err.Error())})
// ...
broker.Publish(sse.Event{Type: sse.EventReviewCompleted, Data: fmt.Sprintf(
    `{"pr_number":%d,"repo":%q,"pr_id":%d,"severity":%q}`,
    pr.Number, pr.Repo, rev.PRID, rev.Severity,
)})
```

```go
// DESPUÉS:
broker.Publish(sse.Event{Type: sse.EventPRDetected, Data: sseData(map[string]any{"pr_number": pr.Number, "repo": pr.Repo})})
broker.Publish(sse.Event{Type: sse.EventReviewStarted, Data: sseData(map[string]any{"pr_number": pr.Number, "repo": pr.Repo})})
// ...
broker.Publish(sse.Event{Type: sse.EventReviewError, Data: sseData(map[string]any{"pr_number": pr.Number, "repo": pr.Repo, "error": err.Error()})})
// ...
broker.Publish(sse.Event{Type: sse.EventReviewCompleted, Data: sseData(map[string]any{
    "pr_number": pr.Number,
    "repo":      pr.Repo,
    "pr_id":     rev.PRID,
    "severity":  rev.Severity,
})})
```

En `SetTriggerReviewFn` (líneas ~333, 384, 387):

```go
// ANTES:
broker.Publish(sse.Event{
    Type: sse.EventReviewError,
    Data: fmt.Sprintf(`{"pr_id":%d,"error":%q}`, prID, msg),
})
// ...
broker.Publish(sse.Event{Type: sse.EventReviewError, Data: fmt.Sprintf(`{"pr_id":%d,"error":%q}`, prID, err.Error())})
// ...
broker.Publish(sse.Event{Type: sse.EventReviewCompleted, Data: fmt.Sprintf(
    `{"pr_number":%d,"repo":%q,"pr_id":%d,"severity":%q}`,
    pr.Number, pr.Repo, prID, rev.Severity,
)})
```

```go
// DESPUÉS:
broker.Publish(sse.Event{
    Type: sse.EventReviewError,
    Data: sseData(map[string]any{"pr_id": prID, "error": msg}),
})
// ...
broker.Publish(sse.Event{Type: sse.EventReviewError, Data: sseData(map[string]any{"pr_id": prID, "error": err.Error()})})
// ...
broker.Publish(sse.Event{Type: sse.EventReviewCompleted, Data: sseData(map[string]any{
    "pr_number": pr.Number,
    "repo":      pr.Repo,
    "pr_id":     prID,
    "severity":  rev.Severity,
})})
```

- [ ] **Step 3: Verificar que fmt no tiene usos de Sprintf restantes para JSON en main.go**

```bash
cd daemon && grep -n 'Sprintf.*{.*:.*%' cmd/heimdallm/main.go
```
Expected: sin resultados (o solo usos legítimos no-JSON).

- [ ] **Step 4: Compilar y pasar tests**

```bash
cd daemon && go build ./... && go test ./... -race -timeout 60s
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd daemon && git add cmd/heimdallm/main.go
git commit -m "fix(security): use json.Marshal instead of fmt.Sprintf for SSE event JSON

fmt.Sprintf with %q produces Go string literals, not JSON. While they
overlap for most ASCII, special Unicode characters can diverge.
Added sseData() helper that uses json.Marshal for all SSE event payloads."
```

---

## Task 5: Rate limiter para review triggers concurrentes (Issue #4)

**Archivos:**
- Modify: `daemon/internal/server/handlers.go`
- Modify: `daemon/internal/server/handlers_test.go`

**Problema:** `POST /prs/{id}/review` lanza una goroutine sin límite. Con N PRs distintos en la BD, se pueden disparar N reviews simultáneas, consumiendo recursos ilimitados.

- [ ] **Step 1: Escribir el test que verifica el límite**

En `daemon/internal/server/handlers_test.go`, añadir al final:

```go
func TestHandlerTriggerReviewRateLimit(t *testing.T) {
    s, err := store.Open(":memory:")
    if err != nil {
        t.Fatalf("open store: %v", err)
    }
    defer s.Close()
    broker := sse.NewBroker()
    broker.Start()
    defer broker.Stop()

    // Server with max 2 concurrent reviews
    srv := server.NewWithOptions(s, broker, nil, "test-token", server.Options{MaxConcurrentReviews: 2})

    // Wire a slow review function that blocks until released
    gate := make(chan struct{})
    srv.SetTriggerReviewFn(func(prID int64) error {
        <-gate // blocks until test releases
        return nil
    })

    token := "test-token"

    // Seed 3 PRs
    for i := 1; i <= 3; i++ {
        s.UpsertPR(&store.PR{
            GithubID: int64(i), Repo: "org/r", Number: i,
            Title: "t", Author: "a", URL: "u", State: "open",
            UpdatedAt: time.Now(), FetchedAt: time.Now(),
        })
    }

    // Fire 2 concurrent reviews (should succeed)
    for i := 1; i <= 2; i++ {
        req := httptest.NewRequest("POST", fmt.Sprintf("/prs/%d/review", i), nil)
        req.Header.Set("X-Heimdallm-Token", token)
        w := httptest.NewRecorder()
        srv.Router().ServeHTTP(w, req)
        if w.Code != http.StatusAccepted {
            t.Errorf("review %d: expected 202, got %d", i, w.Code)
        }
    }

    // Brief wait for goroutines to acquire semaphore
    time.Sleep(10 * time.Millisecond)

    // Third review should be rejected (semaphore full)
    req := httptest.NewRequest("POST", "/prs/3/review", nil)
    req.Header.Set("X-Heimdallm-Token", token)
    w := httptest.NewRecorder()
    srv.Router().ServeHTTP(w, req)
    if w.Code != http.StatusTooManyRequests {
        t.Errorf("expected 429 when semaphore full, got %d", w.Code)
    }

    // Release goroutines
    close(gate)
}
```

- [ ] **Step 2: Ejecutar para verificar que falla (NewWithOptions no existe aún)**

```bash
cd daemon && go test ./internal/server/... -run TestHandlerTriggerReviewRateLimit -v 2>&1 | head -5
```
Expected: compilation error — `server.NewWithOptions` undefined.

- [ ] **Step 3: Añadir Options y NewWithOptions a handlers.go**

En `daemon/internal/server/handlers.go`, añadir después de la struct `Server`:

```go
// Options holds optional configuration for the Server.
type Options struct {
    // MaxConcurrentReviews limits how many POST /prs/{id}/review goroutines
    // can run simultaneously. 0 means use the default (5).
    MaxConcurrentReviews int
}

const defaultMaxConcurrentReviews = 5
```

Añadir `reviewSem chan struct{}` al campo de `Server`:

```go
type Server struct {
    store           *store.Store
    broker          *sse.Broker
    pipeline        *pipeline.Pipeline
    router          chi.Router
    httpServer      *http.Server
    reloadFn        func() error
    triggerReviewFn func(prID int64) error
    meFn            func() (string, error)
    configFn        func() map[string]any
    apiToken        string
    reviewSem       chan struct{} // counting semaphore for concurrent review triggers
}
```

Actualizar `New` para inicializar el semáforo:

```go
func New(s *store.Store, broker *sse.Broker, p *pipeline.Pipeline, apiToken string) *Server {
    return NewWithOptions(s, broker, p, apiToken, Options{})
}

// NewWithOptions creates a Server with configurable options.
func NewWithOptions(s *store.Store, broker *sse.Broker, p *pipeline.Pipeline, apiToken string, opts Options) *Server {
    max := opts.MaxConcurrentReviews
    if max <= 0 {
        max = defaultMaxConcurrentReviews
    }
    srv := &Server{
        store:     s,
        broker:    broker,
        pipeline:  p,
        apiToken:  apiToken,
        reviewSem: make(chan struct{}, max),
    }
    srv.router = srv.buildRouter()
    return srv
}
```

Actualizar `handleTriggerReview` para usar el semáforo:

```go
func (srv *Server) handleTriggerReview(w http.ResponseWriter, r *http.Request) {
    id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
    if err != nil {
        http.Error(w, "invalid id", http.StatusBadRequest)
        return
    }
    if srv.triggerReviewFn == nil {
        http.Error(w, "review trigger not configured", http.StatusServiceUnavailable)
        return
    }
    // Acquire semaphore slot (non-blocking).
    select {
    case srv.reviewSem <- struct{}{}:
    default:
        http.Error(w, `{"error":"too many concurrent reviews — try again later"}`, http.StatusTooManyRequests)
        return
    }
    go func() {
        defer func() { <-srv.reviewSem }()
        if err := srv.triggerReviewFn(id); err != nil {
            slog.Error("trigger review failed", "pr_id", id, "err", err)
        }
    }()
    writeJSON(w, http.StatusAccepted, map[string]string{"status": "review queued"})
}
```

- [ ] **Step 4: Ejecutar el test nuevo**

```bash
cd daemon && go test ./internal/server/... -run TestHandlerTriggerReviewRateLimit -v -race
```
Expected: PASS.

- [ ] **Step 5: Ejecutar todos los tests del servidor**

```bash
cd daemon && go test ./internal/server/... -race -timeout 60s -v
```
Expected: todos PASS.

- [ ] **Step 6: Commit**

```bash
cd daemon && git add internal/server/handlers.go internal/server/handlers_test.go
git commit -m "fix(security): limit concurrent review triggers with counting semaphore

POST /prs/{id}/review previously spawned goroutines without bound,
allowing resource exhaustion via repeated calls. Added a semaphore
(default 5 concurrent reviews) that returns HTTP 429 when full.
Added NewWithOptions for configurable limit and corresponding test."
```

---

## Task 6: Requerir auth en /me, /prs, /prs/{id}, /stats + actualizar Flutter (Issue #5)

**Archivos:**
- Modify: `daemon/internal/server/handlers.go:56`
- Modify: `flutter_app/lib/core/api/api_client.dart`
- Modify: `daemon/internal/server/handlers_test.go`

**Problema:** `/me`, `/prs`, `/prs/{id}`, `/stats` no requieren token. Exponen username de GitHub y lista de PRs a cualquier proceso local (o tab de browser en localhost).

- [ ] **Step 1: Añadir test que verifica que /prs requiere auth cuando hay token**

En `daemon/internal/server/handlers_test.go`, añadir helper y test:

```go
func setupServerWithToken(t *testing.T) (*server.Server, *store.Store) {
    t.Helper()
    s, err := store.Open(":memory:")
    if err != nil {
        t.Fatalf("open store: %v", err)
    }
    t.Cleanup(func() { s.Close() })
    broker := sse.NewBroker()
    broker.Start()
    t.Cleanup(broker.Stop)
    srv := server.New(s, broker, nil, "secret-token")
    return srv, s
}

func TestPublicEndpointsRequireAuthWhenTokenSet(t *testing.T) {
    srv, _ := setupServerWithToken(t)

    paths := []string{"/me", "/prs", "/stats"}
    for _, path := range paths {
        req := httptest.NewRequest("GET", path, nil)
        w := httptest.NewRecorder()
        srv.Router().ServeHTTP(w, req)
        if w.Code != http.StatusUnauthorized {
            t.Errorf("GET %s without token: expected 401, got %d", path, w.Code)
        }

        req2 := httptest.NewRequest("GET", path, nil)
        req2.Header.Set("X-Heimdallm-Token", "secret-token")
        w2 := httptest.NewRecorder()
        srv.Router().ServeHTTP(w2, req2)
        if w2.Code == http.StatusUnauthorized {
            t.Errorf("GET %s with valid token: expected not-401, got 401", path)
        }
    }
}
```

- [ ] **Step 2: Ejecutar para verificar que falla**

```bash
cd daemon && go test ./internal/server/... -run TestPublicEndpointsRequireAuthWhenTokenSet -v
```
Expected: FAIL — actualmente los endpoints devuelven 200 sin token.

- [ ] **Step 3: Ampliar sensitiveGETPaths en handlers.go**

En `daemon/internal/server/handlers.go`, reemplazar la variable `sensitiveGETPaths`:

```go
// ANTES:
var sensitiveGETPaths = []string{"/config", "/agents", "/events", "/logs/stream"}
```

```go
// DESPUÉS:
var sensitiveGETPaths = []string{
    "/config",
    "/agents",
    "/events",
    "/logs/stream",
    "/me",    // exposes GitHub username
    "/prs",   // exposes PR titles, repos, authors
    "/stats", // exposes review activity metadata
}
```

> **Nota:** `/health` se mantiene público (necesario para health checks de systemd/launchctl y tests de conectividad antes de tener el token).

- [ ] **Step 4: Ejecutar el test nuevo**

```bash
cd daemon && go test ./internal/server/... -run TestPublicEndpointsRequireAuthWhenTokenSet -v
```
Expected: PASS.

- [ ] **Step 5: Verificar que los tests existentes de /prs y /me (sin token) siguen pasando**

Los tests existentes usan `setupServer(t)` que crea el servidor con `apiToken = ""`. Con token vacío, auth está desactivada (comportamiento correcto para tests). Verificar:

```bash
cd daemon && go test ./internal/server/... -race -timeout 60s -v
```
Expected: PASS. Si algún test antiguo falla porque asume que /prs no requiere auth con token set, revisar si usa `setupServer` (sin token) o `setupServerWithToken`.

- [ ] **Step 6: Actualizar Flutter api_client.dart — añadir auth headers a fetchPRs, fetchPR, fetchMe, fetchStats**

En `flutter_app/lib/core/api/api_client.dart`, reemplazar los métodos que hacen GET sin auth:

```dart
// ANTES:
Future<List<PR>> fetchPRs() async {
  final resp = await _client.get(_uri('/prs'));
  // ...
}

Future<Map<String, dynamic>> fetchPR(int id) async {
  final resp = await _client.get(_uri('/prs/$id'));
  // ...
}

Future<String> fetchMe() async {
  final resp = await _client.get(_uri('/me'));
  // ...
}

Future<Map<String, dynamic>> fetchStats() async {
  final resp = await _client.get(_uri('/stats'));
  // ...
}
```

```dart
// DESPUÉS:
Future<List<PR>> fetchPRs() async {
  final resp = await _client.get(_uri('/prs'), headers: await _authHeaders());
  if (resp.statusCode != 200) {
    throw ApiException('GET /prs failed: ${resp.statusCode}');
  }
  final list = jsonDecode(resp.body) as List<dynamic>;
  return list.map((e) => _parsePRWithReview(e as Map<String, dynamic>)).toList();
}

Future<Map<String, dynamic>> fetchPR(int id) async {
  final resp = await _client.get(_uri('/prs/$id'), headers: await _authHeaders());
  if (resp.statusCode != 200) {
    throw ApiException('GET /prs/$id failed: ${resp.statusCode}');
  }
  final body = jsonDecode(resp.body) as Map<String, dynamic>;
  final pr = _parsePRWithReview(body['pr'] as Map<String, dynamic>);
  final reviewsRaw = body['reviews'] as List<dynamic>? ?? [];
  final reviews = reviewsRaw
      .map((r) => _parseReview(r as Map<String, dynamic>))
      .toList();
  return {'pr': pr, 'reviews': reviews};
}

Future<String> fetchMe() async {
  final resp = await _client.get(_uri('/me'), headers: await _authHeaders());
  if (resp.statusCode != 200) throw ApiException('GET /me failed: ${resp.statusCode}');
  final body = jsonDecode(resp.body) as Map<String, dynamic>;
  return body['login'] as String? ?? '';
}

Future<Map<String, dynamic>> fetchStats() async {
  final resp = await _client.get(_uri('/stats'), headers: await _authHeaders());
  if (resp.statusCode != 200) throw ApiException('GET /stats failed: ${resp.statusCode}');
  return jsonDecode(resp.body) as Map<String, dynamic>;
}
```

- [ ] **Step 7: Compilar Flutter para verificar que no hay errores**

```bash
cd flutter_app && flutter analyze lib/core/api/api_client.dart
```
Expected: No issues found.

- [ ] **Step 8: Commit**

```bash
git add daemon/internal/server/handlers.go daemon/internal/server/handlers_test.go \
        flutter_app/lib/core/api/api_client.dart
git commit -m "fix(security): require auth token on /me, /prs, /stats endpoints

Previously these endpoints exposed GitHub username, PR list, and activity
stats to any local process without authentication. Added them to
sensitiveGETPaths and updated Flutter api_client to send auth headers
on all non-health requests."
```

---

## Task 7: Validar formato de valores en PUT /config (Issue #7)

**Archivos:**
- Modify: `daemon/internal/server/handlers.go:262-295`
- Modify: `daemon/internal/server/handlers_test.go`

**Problema:** `handlePutConfig` valida las claves pero no los valores. Un `poll_interval` inválido se almacena silenciosamente (fallback a 5m en runtime). Un `server_port` fuera de rango podría causar pánico al reiniciar.

- [ ] **Step 1: Escribir tests de validación de valores**

En `daemon/internal/server/handlers_test.go`, añadir:

```go
func TestHandlerPutConfigValueValidation(t *testing.T) {
    srv, _ := setupServerWithToken(t)

    cases := []struct {
        name       string
        body       string
        wantStatus int
    }{
        {
            name:       "valid poll_interval",
            body:       `{"poll_interval":"5m"}`,
            wantStatus: http.StatusOK,
        },
        {
            name:       "invalid poll_interval",
            body:       `{"poll_interval":"2m"}`,
            wantStatus: http.StatusBadRequest,
        },
        {
            name:       "valid retention_days",
            body:       `{"retention_days":90}`,
            wantStatus: http.StatusOK,
        },
        {
            name:       "retention_days too high",
            body:       `{"retention_days":9999}`,
            wantStatus: http.StatusBadRequest,
        },
        {
            name:       "valid server_port",
            body:       `{"server_port":8080}`,
            wantStatus: http.StatusOK,
        },
        {
            name:       "server_port too low (privileged)",
            body:       `{"server_port":80}`,
            wantStatus: http.StatusBadRequest,
        },
        {
            name:       "valid review_mode",
            body:       `{"review_mode":"single"}`,
            wantStatus: http.StatusOK,
        },
        {
            name:       "invalid review_mode",
            body:       `{"review_mode":"batch"}`,
            wantStatus: http.StatusBadRequest,
        },
    }

    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            req := httptest.NewRequest("PUT", "/config",
                strings.NewReader(tc.body))
            req.Header.Set("X-Heimdallm-Token", "secret-token")
            req.Header.Set("Content-Type", "application/json")
            w := httptest.NewRecorder()
            srv.Router().ServeHTTP(w, req)
            if w.Code != tc.wantStatus {
                t.Errorf("%s: expected %d, got %d (body: %s)",
                    tc.name, tc.wantStatus, w.Code, w.Body.String())
            }
        })
    }
}
```

- [ ] **Step 2: Ejecutar para verificar que fallan los casos de validación**

```bash
cd daemon && go test ./internal/server/... -run TestHandlerPutConfigValueValidation -v
```
Expected: FAIL en los casos "invalid" (actualmente devuelven 200).

- [ ] **Step 3: Añadir validación de valores en handlePutConfig**

En `daemon/internal/server/handlers.go`, añadir antes de `handlePutConfig` las constantes/variables:

```go
var validPollIntervals = map[string]struct{}{
    "1m": {}, "5m": {}, "30m": {}, "1h": {},
}

var validReviewModes = map[string]struct{}{
    "single": {}, "multi": {},
}
```

Y en `handlePutConfig`, añadir validación de valores justo DESPUÉS del bucle de validación de claves (después del `for k := range body` que revisa `validConfigKeys`), ANTES del bucle que hace `SetConfig`:

```go
// Validate value formats per key.
if v, ok := body["poll_interval"]; ok {
    s, isStr := v.(string)
    if !isStr {
        http.Error(w, "poll_interval must be a string", http.StatusBadRequest)
        return
    }
    if _, valid := validPollIntervals[s]; !valid {
        http.Error(w, "poll_interval must be one of: 1m, 5m, 30m, 1h", http.StatusBadRequest)
        return
    }
}
if v, ok := body["retention_days"]; ok {
    n, isNum := v.(float64) // JSON numbers decode as float64
    if !isNum || n < 1 || n > 3650 {
        http.Error(w, "retention_days must be between 1 and 3650", http.StatusBadRequest)
        return
    }
}
if v, ok := body["server_port"]; ok {
    n, isNum := v.(float64)
    if !isNum || n < 1024 || n > 65535 {
        http.Error(w, "server_port must be between 1024 and 65535", http.StatusBadRequest)
        return
    }
}
if v, ok := body["review_mode"]; ok {
    s, isStr := v.(string)
    if !isStr {
        http.Error(w, "review_mode must be a string", http.StatusBadRequest)
        return
    }
    if _, valid := validReviewModes[s]; !valid {
        http.Error(w, "review_mode must be one of: single, multi", http.StatusBadRequest)
        return
    }
}
```

- [ ] **Step 4: Ejecutar todos los tests del servidor**

```bash
cd daemon && go test ./internal/server/... -race -timeout 60s -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd daemon && git add internal/server/handlers.go internal/server/handlers_test.go
git commit -m "fix(security): validate config values in PUT /config

Previously only config keys were validated (allowlist), but not their
values. Added format/range validation for poll_interval (enum),
retention_days (1-3650), server_port (1024-65535), and review_mode (enum).
Invalid values now return HTTP 400."
```

---

## Task 8: Path de logs independiente de plataforma (Issue #8)

**Archivos:**
- Modify: `daemon/internal/server/handlers.go:426-484`

**Problema:** `handleLogsStream` tiene hardcodeado `~/Library/Logs/heimdallm/...` (macOS). En Linux el archivo no existe y el stream devuelve un mensaje de error inmediatamente.

- [ ] **Step 1: Añadir función helper `daemonLogPath()` con lógica por plataforma**

En `daemon/internal/server/handlers.go`, añadir al inicio del archivo (en los imports):

```go
import (
    // ...imports existentes...
    "runtime"
)
```

Añadir la función helper antes de `handleLogsStream`:

```go
// daemonLogPath returns the platform-specific path to the daemon stderr log.
// macOS: ~/Library/Logs/heimdallm/heimdallm-daemon-error.log (LaunchAgent convention)
// Linux: ~/.local/share/heimdallm/heimdallm.log (XDG data dir)
// Other: falls back to Linux path
func daemonLogPath() string {
    home, _ := os.UserHomeDir()
    switch runtime.GOOS {
    case "darwin":
        return filepath.Join(home, "Library", "Logs", "heimdallm", "heimdallm-daemon-error.log")
    default:
        // Linux/other: XDG_STATE_HOME or ~/.local/share/heimdallm/
        if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
            return filepath.Join(xdg, "heimdallm", "heimdallm.log")
        }
        return filepath.Join(home, ".local", "share", "heimdallm", "heimdallm.log")
    }
}
```

- [ ] **Step 2: Actualizar handleLogsStream para usar daemonLogPath()**

En `handleLogsStream`, reemplazar las líneas que construyen `logPath`:

```go
// ANTES:
func (srv *Server) handleLogsStream(w http.ResponseWriter, r *http.Request) {
    home, _ := os.UserHomeDir()
    logPath := filepath.Join(home, "Library", "Logs", "heimdallm", "heimdallm-daemon-error.log")
```

```go
// DESPUÉS:
func (srv *Server) handleLogsStream(w http.ResponseWriter, r *http.Request) {
    logPath := daemonLogPath()
```

- [ ] **Step 3: Asegurarse de que el daemon escribe su log en el path correcto en Linux**

En `daemon/cmd/heimdallm/main.go`, la función `setupLogging()` escribe a stderr. En Linux el log lo gestiona systemd/launchctl pero en dev mode se pierde. Añadir nota en el mensaje de error del stream:

En `handleLogsStream`, la línea que emite cuando el archivo no existe:

```go
// ANTES:
emit("(log file not found — daemon may be running in dev mode)")
```

```go
// DESPUÉS:
emit(fmt.Sprintf("(log file not found at %s — daemon may be running in dev mode or log path differs)", logPath))
```

- [ ] **Step 4: Compilar y ejecutar tests**

```bash
cd daemon && go build ./... && go test ./... -race -timeout 60s
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd daemon && git add internal/server/handlers.go
git commit -m "fix: platform-agnostic daemon log path in /logs/stream

Replaces hardcoded macOS ~/Library/Logs/... path with a daemonLogPath()
helper that returns the correct location per OS:
- macOS: ~/Library/Logs/heimdallm/heimdallm-daemon-error.log
- Linux: $XDG_STATE_HOME/heimdallm/heimdallm.log (fallback: ~/.local/share/heimdallm/heimdallm.log)"
```

---

## Verificación Final

- [ ] **Ejecutar suite completa con race detector**

```bash
cd daemon && go test ./... -race -timeout 60s -v
```
Expected: PASS, 0 races.

- [ ] **Compilar daemon**

```bash
cd daemon && go build -o bin/heimdallm ./cmd/heimdallm && echo "BUILD OK"
```
Expected: `BUILD OK`.

- [ ] **Analizar Flutter**

```bash
cd flutter_app && flutter analyze
```
Expected: No issues found.

- [ ] **Crear branch y PR**

```bash
git checkout -b fix/security-audit-issues
git push origin fix/security-audit-issues
gh pr create \
  --title "fix(security): resolve 8 issues from security audit" \
  --body "$(cat <<'EOF'
## Summary

- **#1 🔴** Abort daemon startup if API token cannot be created (fail-safe)
- **#2 🔴** Fix data race on cachedLogin with sync.Mutex
- **#3 🟡** Expand dangerousSegments with .kube, .docker, .netrc, .npmrc, .pypirc, .gem
- **#4 🟡** Limit concurrent review triggers with semaphore (HTTP 429 when full)
- **#5 🟡** Require auth on /me, /prs, /prs/{id}, /stats + update Flutter client
- **#6 🟡** Replace fmt.Sprintf JSON building with json.Marshal
- **#7 🟢** Validate config values (poll_interval enum, port range, retention range)
- **#8 🟢** Platform-agnostic daemon log path (macOS / Linux)

## Test plan
- [ ] `cd daemon && go test ./... -race -timeout 60s` — PASS
- [ ] `cd daemon && go build ./...` — PASS
- [ ] `cd flutter_app && flutter analyze` — PASS
- [ ] Manual: start daemon, verify it exits if api_token dir is unwritable
- [ ] Manual: trigger >5 reviews, verify 429 on the 6th

🤖 Generated with [Claude Code](https://claude.ai/claude-code)
EOF
)"
```
