# never_approve_with_issues Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a global setting (with per-org and per-repo override) that publishes a PR review as `COMMENT` instead of `APPROVE` whenever the review found any issue.

**Architecture:** Mirror the existing `generate_pr_description` bool-with-override pattern in the config layer (`AIConfig` global + `*bool` in `RepoAI`/`OrgAI`, resolved by `AIForRepo`/`applyScopedAI`). A new pure function `ReviewEvent` maps `(finalSeverity, hasIssues, flag)` → GitHub event, building on the existing `SeverityToEvent`. The decided event is **persisted** on the `store.Review` row (new `event` column) so retry paths reproduce it verbatim (Enfoque A). The global toggle is edited via the existing generic `PATCH /config` route (writes `[ai].never_approve_with_issues` in config.toml); the per-repo/org override via the existing generic `PATCH /config/repos/{repo}` and `.../orgs/{org}` routes.

**Tech Stack:** Go 1.x (daemon), Flutter/Dart (GUI), SQLite (BurntSushi TOML + database/sql), Riverpod.

## Global Constraints

- Canonical severity values are exactly `low` | `medium` | `high` (empty string treated as `low`). No other values exist.
- TOML/JSON key everywhere: `never_approve_with_issues`. Go field: `NeverApproveWithIssues`. Dart global field: `globalNeverApproveWithIssues`; Dart repo/org override field: `neverApproveWithIssues` (`bool?`, null = inherit).
- Default is `false` at the global level → the change must be 100% backward-compatible (OFF = today's behavior).
- "Contains issues" is defined as `len(result.Issues) > 0`.
- `REQUEST_CHANGES` (severity `high`) is NEVER altered by this setting. Only the `APPROVE`-with-issues case becomes `COMMENT`.
- Retry/publish paths must publish the persisted `Review.Event`; legacy rows with empty `Event` fall back to `SeverityToEvent(Review.Severity)`.
- Test commands: daemon → `cd daemon && make test` (`go test ./... -timeout 60s`); Flutter → `cd flutter_app && flutter test`. Build check: `cd daemon && make build`.
- TDD, small commits, one deliverable per task.

---

### Task 1: Config field + resolution (repo > org > global)

**Files:**
- Modify: `daemon/internal/config/config.go` (AIConfig ~458, RepoAI ~547, OrgAI ~609, AIForRepo 743-775, applyOrgAI 777-798, applyRepoAI 800-821, scopedAIFields 823-842, applyScopedAI 844+)
- Test: `daemon/internal/config/config_test.go`

**Interfaces:**
- Produces: `Config.AIForRepo(repo string) RepoAI` where the returned `RepoAI.NeverApproveWithIssues *bool` is always non-nil (global seeds it), resolved repo > org > global.

- [ ] **Step 1: Write the failing test**

Add to `daemon/internal/config/config_test.go`:

```go
func TestNeverApproveWithIssues_Resolution(t *testing.T) {
	tru := true
	fal := false
	cases := []struct {
		name   string
		global bool
		org    *bool
		repo   *bool
		want   bool
	}{
		{"global off, no overrides", false, nil, nil, false},
		{"global on, no overrides", true, nil, nil, true},
		{"org on over global off", false, &tru, nil, true},
		{"repo off over org on", false, &tru, &fal, false},
		{"repo on over global off", false, nil, &tru, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{}
			c.AI.NeverApproveWithIssues = tc.global
			c.AI.Orgs = map[string]OrgAI{"acme": {NeverApproveWithIssues: tc.org}}
			c.AI.Repos = map[string]RepoAI{"acme/widget": {NeverApproveWithIssues: tc.repo}}
			got := c.AIForRepo("acme/widget").NeverApproveWithIssues
			if got == nil {
				t.Fatalf("NeverApproveWithIssues is nil, want non-nil")
			}
			if *got != tc.want {
				t.Errorf("NeverApproveWithIssues = %v, want %v", *got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/config/ -run TestNeverApproveWithIssues_Resolution -v`
Expected: FAIL (compile error — `NeverApproveWithIssues` field does not exist).

- [ ] **Step 3: Add the struct fields and resolution wiring**

In `AIConfig` (right after the `GeneratePRDescription bool` field, ~line 458):

```go
	// NeverApproveWithIssues, when true, downgrades an otherwise-APPROVE review
	// to COMMENT whenever the review found any issue. REQUEST_CHANGES (high
	// severity) is unaffected. Default: false (backwards compat). Overridable
	// per-org and per-repo.
	NeverApproveWithIssues bool `toml:"never_approve_with_issues"`
```

In `RepoAI` (right after its `GeneratePRDescription *bool` field, ~line 547):

```go
	// NeverApproveWithIssues overrides ai.never_approve_with_issues for this
	// repo. nil = inherit from org/global.
	NeverApproveWithIssues *bool `toml:"never_approve_with_issues,omitempty"`
```

In `OrgAI` (right after its `GeneratePRDescription *bool` field, ~line 609):

```go
	NeverApproveWithIssues *bool `toml:"never_approve_with_issues,omitempty"`
```

In `AIForRepo` — seed the global value. After `gGenDesc := c.AI.GeneratePRDescription` (~745):

```go
	gNever := c.AI.NeverApproveWithIssues
```

And in the `out := RepoAI{...}` literal, after `GeneratePRDescription: &gGenDesc,` (~757):

```go
		NeverApproveWithIssues: &gNever,
```

In `applyOrgAI` — inside the `scopedAIFields{...}` literal, after `GeneratePRDescription: o.GeneratePRDescription,` (~795):

```go
		NeverApproveWithIssues: o.NeverApproveWithIssues,
```

In `applyRepoAI` — inside the `scopedAIFields{...}` literal, after `GeneratePRDescription: r.GeneratePRDescription,` (~818):

```go
		NeverApproveWithIssues: r.NeverApproveWithIssues,
```

In `scopedAIFields` struct, after `GeneratePRDescription *bool` (~840):

```go
	NeverApproveWithIssues *bool
```

In `applyScopedAI`, after the `GeneratePRDescription` block (~893-895):

```go
	if fields.NeverApproveWithIssues != nil {
		out.NeverApproveWithIssues = fields.NeverApproveWithIssues
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd daemon && go test ./internal/config/ -run TestNeverApproveWithIssues_Resolution -v`
Expected: PASS (all 5 subtests).

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/config/config.go daemon/internal/config/config_test.go
git commit -m "feat(config): add never_approve_with_issues with repo/org/global merge"
```

---

### Task 2: `ReviewEvent` pure function

**Files:**
- Modify: `daemon/internal/pipeline/pipeline.go` (add function next to `SeverityToEvent`, ~1007)
- Test: `daemon/internal/pipeline/pipeline_test.go` (create if absent, else append)

**Interfaces:**
- Produces: `func ReviewEvent(finalSeverity string, hasIssues bool, neverApproveWithIssues bool) string` returning one of `"APPROVE"`, `"COMMENT"`, `"REQUEST_CHANGES"`.
- Consumes: existing `func SeverityToEvent(severity string) string`.

- [ ] **Step 1: Write the failing test**

Append to `daemon/internal/pipeline/pipeline_test.go` (create the file with `package pipeline` + imports `testing` if it does not exist):

```go
func TestReviewEvent(t *testing.T) {
	cases := []struct {
		sev       string
		hasIssues bool
		never     bool
		want      string
	}{
		// flag OFF → identical to SeverityToEvent
		{"low", true, false, "APPROVE"},
		{"medium", true, false, "APPROVE"},
		{"high", true, false, "REQUEST_CHANGES"},
		{"", false, false, "APPROVE"},
		// flag ON
		{"low", true, true, "COMMENT"},
		{"medium", true, true, "COMMENT"},
		{"", true, true, "COMMENT"},
		{"high", true, true, "REQUEST_CHANGES"}, // high never downgraded
		{"low", false, true, "APPROVE"},         // clean review still approves
		{"medium", false, true, "APPROVE"},
	}
	for _, tc := range cases {
		got := ReviewEvent(tc.sev, tc.hasIssues, tc.never)
		if got != tc.want {
			t.Errorf("ReviewEvent(%q, %v, %v) = %q, want %q",
				tc.sev, tc.hasIssues, tc.never, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/pipeline/ -run TestReviewEvent -v`
Expected: FAIL (compile error — `ReviewEvent` undefined).

- [ ] **Step 3: Implement `ReviewEvent`**

Add directly below `SeverityToEvent` (after line ~1007) in `daemon/internal/pipeline/pipeline.go`:

```go
// ReviewEvent decides the GitHub review event, honoring the
// never-approve-with-issues setting. It builds on SeverityToEvent: when the
// base decision would be APPROVE, the setting is on, and the review found at
// least one issue, it downgrades APPROVE to COMMENT. REQUEST_CHANGES is never
// altered, and a clean review (no issues) still approves.
func ReviewEvent(finalSeverity string, hasIssues bool, neverApproveWithIssues bool) string {
	event := SeverityToEvent(finalSeverity)
	if event == "APPROVE" && neverApproveWithIssues && hasIssues {
		return "COMMENT"
	}
	return event
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd daemon && go test ./internal/pipeline/ -run TestReviewEvent -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/pipeline/pipeline.go daemon/internal/pipeline/pipeline_test.go
git commit -m "feat(pipeline): add ReviewEvent mapping issues+flag to review event"
```

---

### Task 3: Persist the decided event on `store.Review`

**Files:**
- Modify: `daemon/internal/store/reviews.go` (Review struct 16-38, InsertReview 41-57, the 4 SELECT queries at 62/82/122/143, scanReview 166+)
- Modify: `daemon/internal/store/store.go` (schema CREATE TABLE reviews 52-65, migrations block ~195-203)
- Test: `daemon/internal/store/store_test.go`

**Interfaces:**
- Produces: `store.Review.Event string` (JSON tag `event`) round-tripped through insert/select. Empty string on legacy rows.

- [ ] **Step 1: Write the failing test**

Append to `daemon/internal/store/store_test.go`:

```go
func TestReview_EventRoundTrip(t *testing.T) {
	s := newTestStore(t) // existing helper used by other tests in this file
	prID := insertTestPR(t, s) // existing helper; if named differently, reuse the file's PR-insert helper
	rev := &Review{
		PRID: prID, CLIUsed: "claude",
		Issues: "[]", Suggestions: "[]", Severity: "low",
		Event:     "COMMENT",
		CreatedAt: time.Now().UTC(),
		HeadSHA:   "abc123",
	}
	id, err := s.InsertReview(rev)
	if err != nil {
		t.Fatalf("InsertReview: %v", err)
	}
	got, err := s.GetReview(id)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if got.Event != "COMMENT" {
		t.Errorf("Event = %q, want %q", got.Event, "COMMENT")
	}
}
```

> If `newTestStore`/`insertTestPR` helper names differ, match the exact helpers already used in `store_test.go` (see `TestReview_HeadSHARoundTrip`, ~line 185, for the canonical setup in this file).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/store/ -run TestReview_EventRoundTrip -v`
Expected: FAIL (compile error — `Event` field does not exist).

- [ ] **Step 3: Add field, column, migration, and wire all queries**

In `daemon/internal/store/reviews.go`, add to the `Review` struct after `HeadSHA` (line 37):

```go
	// Event is the GitHub review event the daemon decided to submit
	// (APPROVE | COMMENT | REQUEST_CHANGES). Persisted so retry/publish paths
	// reproduce the exact decision even if config changes afterwards. Empty on
	// legacy rows — callers fall back to SeverityToEvent(Severity).
	Event string `json:"event"`
```

In `InsertReview`, update the SQL to add the column and placeholder, and pass `r.Event`:

```go
	res, err := s.db.Exec(`
		INSERT INTO reviews (pr_id, cli_used, summary, issues, suggestions, severity, created_at, published_at, github_review_id, github_review_state, head_sha, event)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.PRID, r.CLIUsed, r.Summary, r.Issues, r.Suggestions, r.Severity,
		r.CreatedAt.UTC().Format(sqliteTimeFormat), publishedAt,
		r.GitHubReviewID, r.GitHubReviewState, r.HeadSHA, r.Event,
	)
```

Append `, event` to the column list of ALL FOUR SELECT statements (lines 62, 82, 122, 143) so each ends with `... head_sha, event FROM reviews ...`. Example for line 62:

```go
		"SELECT id, pr_id, cli_used, summary, issues, suggestions, severity, created_at, published_at, github_review_id, github_review_state, head_sha, event FROM reviews WHERE github_review_id=0 ORDER BY created_at ASC",
```

Do the same for the queries at lines 82, 122, and 143 (keep their existing WHERE/ORDER clauses).

In `scanReview`, add `&rev.Event` as the final scan target:

```go
	if err = s.Scan(&rev.ID, &rev.PRID, &rev.CLIUsed, &rev.Summary,
		&rev.Issues, &rev.Suggestions, &rev.Severity, &createdAt, &publishedAt,
		&rev.GitHubReviewID, &rev.GitHubReviewState, &rev.HeadSHA, &rev.Event); err != nil {
		return nil, fmt.Errorf("store: scan review: %w", err)
	}
```

In `daemon/internal/store/store.go`, add the column to the fresh-install schema (inside `CREATE TABLE IF NOT EXISTS reviews`, after `head_sha ... DEFAULT ''` on line 64 — add a comma to that line):

```sql
  head_sha            TEXT NOT NULL DEFAULT '',
  event               TEXT NOT NULL DEFAULT ''
```

And add the idempotent migration next to the other reviews ALTERs (after line 203):

```go
	// event stores the decided review event (APPROVE|COMMENT|REQUEST_CHANGES)
	// so retry/publish paths reproduce the decision. Empty default => legacy
	// rows fall back to SeverityToEvent(severity).
	db.Exec("ALTER TABLE reviews ADD COLUMN event TEXT NOT NULL DEFAULT ''")
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd daemon && go test ./internal/store/ -run TestReview_EventRoundTrip -v`
Then run the whole store package to catch any missed query: `cd daemon && go test ./internal/store/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/store/reviews.go daemon/internal/store/store.go daemon/internal/store/store_test.go
git commit -m "feat(store): persist decided review event on reviews rows"
```

---

### Task 4: Wire the decision + persistence into the pipeline

**Files:**
- Modify: `daemon/internal/pipeline/pipeline.go` (RunOptions 284-300, Run submit block ~600-648, PublishPending submit ~776-780)
- Modify: `daemon/cmd/heimdallm/main.go` (buildRunOpts RunOptions literal ~445-451)
- Test: `daemon/internal/pipeline/pipeline_test.go`

**Interfaces:**
- Consumes: `ReviewEvent` (Task 2), `store.Review.Event` (Task 3), `Config.AIForRepo(...).NeverApproveWithIssues` (Task 1).
- Produces: `RunOptions.NeverApproveWithIssues bool`.

- [ ] **Step 1: Add the RunOptions field**

In `daemon/internal/pipeline/pipeline.go`, add to the `RunOptions` struct (after `InstructionAuthors []string`, ~line 299):

```go
	// NeverApproveWithIssues, when true, publishes the review as COMMENT
	// instead of APPROVE whenever the review found any issue (see ReviewEvent).
	NeverApproveWithIssues bool
```

- [ ] **Step 2: Write the failing test**

Append to `daemon/internal/pipeline/pipeline_test.go`. This pins the two contract points that Task 4 wires — the decision helper and the "use persisted event, else fall back" rule — without standing up a full `Run`:

```go
func TestReviewEvent_PersistedAndReused(t *testing.T) {
	// Unit-level guard: the decision helper + persistence contract.
	// Run path: COMMENT chosen when flag on and issues present.
	ev := ReviewEvent("medium", true, true)
	if ev != "COMMENT" {
		t.Fatalf("decision = %q, want COMMENT", ev)
	}
	// Publish reproduction: a stored event is used verbatim; empty falls back.
	if got := publishEventFor(&store.Review{Event: "COMMENT", Severity: "low"}); got != "COMMENT" {
		t.Errorf("publishEventFor(stored COMMENT) = %q, want COMMENT", got)
	}
	if got := publishEventFor(&store.Review{Event: "", Severity: "high"}); got != "REQUEST_CHANGES" {
		t.Errorf("publishEventFor(legacy high) = %q, want REQUEST_CHANGES", got)
	}
}
```

This test references a small helper `publishEventFor` that Step 3 introduces to centralize the "use persisted event, else fall back" rule (so both `Run` and `PublishPending` share it and the test can pin it).

- [ ] **Step 3: Run test to verify it fails**

Run: `cd daemon && go test ./internal/pipeline/ -run TestReviewEvent_PersistedAndReused -v`
Expected: FAIL (compile error — `publishEventFor` undefined).

- [ ] **Step 4: Implement the helper and wire both call sites**

Add the helper near `ReviewEvent` in `pipeline.go`:

```go
// publishEventFor returns the GitHub event to submit for a stored review:
// the decided event persisted at review time, or — for legacy rows written
// before the event column existed — the severity-derived fallback.
func publishEventFor(rev *store.Review) string {
	if rev.Event != "" {
		return rev.Event
	}
	return SeverityToEvent(rev.Severity)
}
```

In `Run`, compute the event before building the `rev` struct. Immediately after `finalSeverity := ApplySignalEscalation(...)` (~line 600), add:

```go
	reviewEvent := ReviewEvent(finalSeverity, len(result.Issues) > 0, opts.NeverApproveWithIssues)
```

Add `Event: reviewEvent,` to the `rev := &store.Review{...}` literal (~line 613, e.g. right after `Severity: finalSeverity,`):

```go
		Severity:       finalSeverity,
		Event:          reviewEvent,
```

Change the submit call (~line 644-648) from `SeverityToEvent(finalSeverity)` to `reviewEvent`:

```go
	ghReviewID, ghReviewState, publishErr := p.gh.SubmitReview(
		pr.Repo, pr.Number,
		reviewBody,
		reviewEvent,
	)
```

In `PublishPending` (~line 776-780), change `SeverityToEvent(rev.Severity)` to `publishEventFor(rev)`:

```go
	ghID, ghState, err := p.gh.SubmitReview(
		pr.Repo, pr.Number,
		BuildGitHubBody(result),
		publishEventFor(rev),
	)
```

In `daemon/cmd/heimdallm/main.go`, set the RunOptions field in `buildRunOpts` (in the `pipeline.RunOptions{...}` literal ~445-451, after `InstructionAuthors: aiCfg.InstructionAuthors,`):

```go
			NeverApproveWithIssues: aiCfg.NeverApproveWithIssues != nil && *aiCfg.NeverApproveWithIssues,
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/pipeline/ -run TestReviewEvent -v`
Then build: `cd daemon && make build`
Expected: tests PASS; binary builds.

- [ ] **Step 6: Commit**

```bash
git add daemon/internal/pipeline/pipeline.go daemon/cmd/heimdallm/main.go daemon/internal/pipeline/pipeline_test.go
git commit -m "feat(pipeline): publish COMMENT for reviews with issues when flag on"
```

---

### Task 5: Expose the setting over the HTTP config API

**Files:**
- Modify: `daemon/cmd/heimdallm/main.go` (GET /config result map ~1616; `aiOverrideFields` struct 4466-4480; `repoAIOverrideMap` 4410-4423 & `orgAIOverrideMap` 4445-4458; `addCommonAIOverrideFields` ~4519; AI-clearing snapshot ~2226)
- Test: `daemon/internal/server/handlers_test.go`

**Interfaces:**
- Produces: GET `/config` includes top-level `never_approve_with_issues` (global) and, inside each `repo_overrides[repo]` / `org_overrides[org]`, a `never_approve_with_issues` bool when set.
- Note: the write paths need NO code — `PATCH /config` (global, merges into `[ai]`) and `PATCH /config/repos/{repo}` / `.../orgs/{org}` (merge into `[ai.repos."…"]` / `[ai.orgs."…"]`) are generic TOML merges keyed by the `toml:"never_approve_with_issues"` tag added in Task 1. `DELETE /config/repos/{repo}/never_approve_with_issues` likewise works generically.

- [ ] **Step 1: Write the failing test**

Append to `daemon/internal/server/handlers_test.go` (follow the existing GET /config test helper style; look at the test around line 1634 that asserts on `gh["repositories"]` for the canonical request/decode pattern):

```go
func TestGetConfig_ExposesNeverApproveWithIssues(t *testing.T) {
	srv := newTestServerWithConfig(t, &config.Config{
		AI: config.AIConfig{
			NeverApproveWithIssues: true,
			Repos: map[string]config.RepoAI{
				"org/repo1": {NeverApproveWithIssues: boolPtr(false)},
			},
		},
	}) // reuse whatever constructor the other GET /config tests use
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/config", nil)
	withAuth(req) // reuse existing auth helper if GET /config requires token
	srv.Router().ServeHTTP(rec, req)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["never_approve_with_issues"] != true {
		t.Errorf("global never_approve_with_issues = %v, want true", body["never_approve_with_issues"])
	}
	ro := body["repo_overrides"].(map[string]any)["org/repo1"].(map[string]any)
	if ro["never_approve_with_issues"] != false {
		t.Errorf("repo override = %v, want false", ro["never_approve_with_issues"])
	}
}
```

> Match the exact server constructor / auth helper / `boolPtr` used by neighboring tests in `handlers_test.go`. If a `boolPtr` helper does not exist, add `func boolPtr(b bool) *bool { return &b }` once.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd daemon && go test ./internal/server/ -run TestGetConfig_ExposesNeverApproveWithIssues -v`
Expected: FAIL (`never_approve_with_issues` absent from the response).

- [ ] **Step 3: Add serialization**

In `daemon/cmd/heimdallm/main.go`, add to the GET /config result map (after `"generate_pr_description": c.AI.GeneratePRDescription,` ~line 1616):

```go
			"never_approve_with_issues":   c.AI.NeverApproveWithIssues,
```

Add the field to `aiOverrideFields` (after `GeneratePRDescription *bool` ~line 4479):

```go
	NeverApproveWithIssues *bool
```

Pass it in BOTH `repoAIOverrideMap` (after `GeneratePRDescription: ai.GeneratePRDescription,` ~line 4423) and `orgAIOverrideMap` (~line 4458):

```go
		NeverApproveWithIssues: ai.NeverApproveWithIssues,
```

Emit it in `addCommonAIOverrideFields` (after the `GeneratePRDescription` block ~line 4519-4521):

```go
	if fields.NeverApproveWithIssues != nil {
		out["never_approve_with_issues"] = *fields.NeverApproveWithIssues
	}
```

In the AI-clearing snapshot helper, add the reset next to `snap.AI.GeneratePRDescription = false` (~line 2226) for consistency:

```go
	snap.AI.NeverApproveWithIssues = false
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd daemon && go test ./internal/server/ -run TestGetConfig_ExposesNeverApproveWithIssues -v`
Then the full daemon suite: `cd daemon && make test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add daemon/cmd/heimdallm/main.go daemon/internal/server/handlers_test.go
git commit -m "feat(api): expose never_approve_with_issues in GET /config and overrides"
```

---

### Task 6: Flutter model support

**Files:**
- Modify: `flutter_app/lib/core/models/config_model.dart` (RepoConfig field 151/constructor 180/hasAiOverride 229/copyWith 287+357/fromJson repo-override parse ~953; OrgConfig field 392/constructor 420/copyWith 477+540/parse ~991; AppConfig field/constructor, copyWith 842+868, toJson 901, fromJson ~1063)
- Test: `flutter_app/test/features/config_test.dart`

**Interfaces:**
- Produces: `AppConfig.globalNeverApproveWithIssues` (bool, default false); `RepoConfig.neverApproveWithIssues` and `OrgConfig.neverApproveWithIssues` (`bool?`, null = inherit). JSON key `never_approve_with_issues` (global at top level; overrides inside repo/org override maps).

- [ ] **Step 1: Write the failing test**

Append to `flutter_app/test/features/config_test.dart`:

```dart
test('never_approve_with_issues round-trips (global + repo override)', () {
  final json = {
    'never_approve_with_issues': true,
    'repositories': <String>[],
    'repo_overrides': {
      'org/repo1': {'never_approve_with_issues': false},
    },
  };
  final cfg = AppConfig.fromJson(json);
  expect(cfg.globalNeverApproveWithIssues, isTrue);
  expect(cfg.repoConfigs['org/repo1']!.neverApproveWithIssues, isFalse);

  // Global survives toJson round-trip.
  expect(cfg.toJson()['never_approve_with_issues'], isTrue);

  // copyWith sentinel: repo override can be cleared to inherit (null).
  final cleared = cfg.repoConfigs['org/repo1']!.copyWith(
    neverApproveWithIssues: null,
  );
  expect(cleared.neverApproveWithIssues, isNull);
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd flutter_app && flutter test test/features/config_test.dart --plain-name "never_approve_with_issues"`
Expected: FAIL (getters/fields do not exist).

- [ ] **Step 3: Add the fields, mirroring `prDraft` (repo/org) and a global bool**

RepoConfig — declare after `final bool? prDraft;` (line 151):

```dart
  final bool? neverApproveWithIssues;
```

Add `this.neverApproveWithIssues,` to the `const RepoConfig({...})` constructor (after `this.prDraft,` ~line 180). Add `|| neverApproveWithIssues != null` to the `hasAiOverride` getter chain (after `prDraft != null` ~line 229 — turn that line into `prDraft != null ||` and append the new clause, keeping the final `;`). In `copyWith`, add the sentinel param `Object? neverApproveWithIssues = _sentinel,` (near `Object? prDraft = _sentinel,` ~line 287) and the resolution line (near ~357):

```dart
      neverApproveWithIssues: neverApproveWithIssues == _sentinel
          ? this.neverApproveWithIssues
          : neverApproveWithIssues as bool?,
```

OrgConfig — mirror the same four edits (field after `final bool? prDraft;` line 392; constructor ~420; copyWith param ~477 and resolution ~540). OrgConfig has no `hasAiOverride`; skip that part.

Repo-override parse (the `RepoConfig(...)` built from `ov` inside `AppConfig.fromJson`, ~line 953 where `generatePRDescription: ov['generate_pr_description'] as bool?` lives) — add:

```dart
          neverApproveWithIssues: ov['never_approve_with_issues'] as bool?,
```

Org-override parse (the `OrgConfig(...)` built from `ov`, ~line 991 near `prDraft: ov['pr_draft'] as bool?`) — add the same line.

AppConfig — declare the field (place it next to `globalGeneratePRDescription`), default false:

```dart
  final bool globalNeverApproveWithIssues;
```

Add it to the `const AppConfig({...})` constructor with a default: `this.globalNeverApproveWithIssues = false,`. Add to `copyWith`: parameter `bool? globalNeverApproveWithIssues,` (near line 849) and body `globalNeverApproveWithIssues: globalNeverApproveWithIssues ?? this.globalNeverApproveWithIssues,` (near line 880). Add to `toJson` (after `'generate_pr_description': globalGeneratePRDescription,` ~line 901):

```dart
    'never_approve_with_issues': globalNeverApproveWithIssues,
```

Add to `AppConfig.fromJson` (near where `globalGeneratePRDescription` is read, ~line 1063):

```dart
      globalNeverApproveWithIssues:
          (json['never_approve_with_issues'] as bool?) ?? false,
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd flutter_app && flutter test test/features/config_test.dart --plain-name "never_approve_with_issues"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add flutter_app/lib/core/models/config_model.dart flutter_app/test/features/config_test.dart
git commit -m "feat(gui): model support for never_approve_with_issues"
```

---

### Task 7: Global settings toggle (config screen)

**Files:**
- Modify: `flutter_app/lib/features/config/config_screen.dart` (state field near 635, init near 644, a `SwitchListTile` near the draft one 726-738, `_buildConfig` 947-959)
- Modify: `flutter_app/lib/features/config/config_providers.dart` (`_computeGlobalDiff` ~149-151)
- Test: covered by Task 6 model tests + a diff test below (optional; the UI widget itself is thin).

**Interfaces:**
- Consumes: `AppConfig.globalNeverApproveWithIssues` (Task 6).
- Produces: PATCH `/config` body `{"ai": {"never_approve_with_issues": <bool>}}` when the toggle changes.

- [ ] **Step 1: Add state + init**

In `config_screen.dart`, near `bool _globalPRDraft = false;` (line 635):

```dart
  bool _globalNeverApproveWithIssues = false;
```

In the same init method that sets `_globalPRDraft = config.globalPRDraft;` (line 644):

```dart
    _globalNeverApproveWithIssues = config.globalNeverApproveWithIssues;
```

- [ ] **Step 2: Add the SwitchListTile**

Near the existing draft `SwitchListTile` (726-738), add (match the surrounding `Material`/wrapper the sibling switches use — see the commit that wrapped config switches in `Material`):

```dart
        SwitchListTile(
          title: const Text('No aprobar PRs con issues'),
          subtitle: const Text(
            'Si la review encuentra issues (de cualquier severidad), se publica '
            'como comentario en la PR en vez de una aprobación. Los casos que '
            'bloquean (severidad alta) siguen siendo "cambios solicitados".',
          ),
          value: _globalNeverApproveWithIssues,
          onChanged: (v) =>
              setState(() => _globalNeverApproveWithIssues = v),
        ),
```

- [ ] **Step 3: Include it in `_buildConfig`**

In `_buildConfig` (947-959), add to the `base.copyWith(...)` call (after `globalPRDraft: _globalPRDraft,` ~line 955):

```dart
    globalNeverApproveWithIssues: _globalNeverApproveWithIssues,
```

- [ ] **Step 4: Add it to the global diff**

In `config_providers.dart`, in `_computeGlobalDiff`, after the `generate_pr_description` block (149-151):

```dart
  if (old.globalNeverApproveWithIssues != updated.globalNeverApproveWithIssues) {
    aiDiff['never_approve_with_issues'] = updated.globalNeverApproveWithIssues;
  }
```

- [ ] **Step 5: Verify the app analyzes and tests pass**

Run: `cd flutter_app && flutter analyze && flutter test`
Expected: no analyzer errors; all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add flutter_app/lib/features/config/config_screen.dart flutter_app/lib/features/config/config_providers.dart
git commit -m "feat(gui): global toggle for never_approve_with_issues"
```

---

### Task 8: Per-repo override (repo detail screen)

**Files:**
- Modify: `flutter_app/lib/features/repositories/repo_detail_screen.dart` (`_computeRepoDiff` ~144-165; an `OverrideDropdown` near the `pr_draft` one 631-639; reset via `_resetField`)
- Test: `flutter_app/test/features/config_test.dart` (or the repo-detail test file if one exists)

**Interfaces:**
- Consumes: `RepoConfig.neverApproveWithIssues`, `OrgConfig.neverApproveWithIssues`, `AppConfig.globalNeverApproveWithIssues` (Task 6).
- Produces: PATCH `/config/repos/{repo}` body `{"never_approve_with_issues": <bool>}` when set; `DELETE /config/repos/{repo}/never_approve_with_issues` on reset.

- [ ] **Step 1: Write the failing diff test**

Append to `flutter_app/test/features/config_test.dart`:

```dart
test('repo diff includes never_approve_with_issues when set', () {
  const oldCfg = RepoConfig();
  final updated = oldCfg.copyWith(neverApproveWithIssues: true);
  final diff = computeRepoDiff(oldCfg, updated); // expose the same fn used by the screen
  expect(diff['never_approve_with_issues'], isTrue);
});
```

> If `_computeRepoDiff` is private to the screen, either (a) lift it to a top-level `computeRepoDiff` in a small `repo_diff.dart` helper imported by the screen and the test, or (b) mirror the existing repo-detail test's approach for exercising the diff. Prefer (a) only if no test currently reaches `_computeRepoDiff`; otherwise follow the existing test pattern.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd flutter_app && flutter test test/features/config_test.dart --plain-name "repo diff includes never_approve_with_issues"`
Expected: FAIL.

- [ ] **Step 3: Add to `_computeRepoDiff`**

In `repo_detail_screen.dart`, next to the `pr_draft` diff block (164-165):

```dart
    if (old.neverApproveWithIssues != updated.neverApproveWithIssues &&
        updated.neverApproveWithIssues != null) {
      diff['never_approve_with_issues'] = updated.neverApproveWithIssues!;
    }
```

- [ ] **Step 4: Add the override control**

Near the `pr_draft` `OverrideDropdown` (631-639), add (mirror it exactly, swapping field + reset key):

```dart
        OverrideDropdown(
          label: 'No aprobar PRs con issues',
          globalValue:
              (orgConfig?.neverApproveWithIssues ??
                      appConfig.globalNeverApproveWithIssues)
                  .toString(),
          inheritedLabel: source(orgConfig?.neverApproveWithIssues != null),
          overrideValue: _config.neverApproveWithIssues?.toString(),
          options: const ['true', 'false'],
          onChanged: (v) => _update(
            _config.copyWith(
              neverApproveWithIssues: v != null ? v == 'true' : null,
            ),
          ),
          onReset: () => _resetField('never_approve_with_issues'),
        ),
```

> Match the exact named parameters of `OverrideDropdown` as used by the `pr_draft` instance in this file (label/globalValue/inheritedLabel/overrideValue/onChanged/onReset). If the constructor differs, copy the sibling verbatim and change only the field, label, and reset key.

- [ ] **Step 5: Run tests + analyze**

Run: `cd flutter_app && flutter analyze && flutter test`
Expected: no analyzer errors; all tests PASS (including the new diff test).

- [ ] **Step 6: Commit**

```bash
git add flutter_app/lib/features/repositories/repo_detail_screen.dart flutter_app/test/features/config_test.dart
git commit -m "feat(gui): per-repo override for never_approve_with_issues"
```

---

### Task 9: Full-suite verification

**Files:** none (verification only)

- [ ] **Step 1: Run the whole daemon suite (with race)**

Run: `cd daemon && make test-race`
Expected: PASS.

- [ ] **Step 2: Build the daemon binary**

Run: `cd daemon && make build`
Expected: `bin/heimdallm` builds without error.

- [ ] **Step 3: Run the whole Flutter suite + analyzer**

Run: `cd flutter_app && flutter analyze && flutter test`
Expected: no analyzer errors; all tests PASS.

- [ ] **Step 4: Manual end-to-end sanity (config round-trip)**

With a scratch config, confirm the setting flows through the daemon:
```bash
cd daemon && make build
# Point at a scratch config dir; enable per-repo override via PATCH and read it back.
```
Verify: GET `/config` shows `never_approve_with_issues`, PATCH `/config/repos/{repo}` persists it into `[ai.repos."…"]`, and DELETE removes it. (This is the same generic path exercised earlier when adding a repo to config.toml.)

---

## Notes for the implementer

- The global write path is `PATCH /config` (generic TOML merge into `[ai]`), NOT `PUT /config` (which has an allow-list). Do not add the key to `validConfigKeys`/`ApplyStore` — that path is unused by the GUI for this family of settings and would be dead code.
- `AIForRepo(repo).NeverApproveWithIssues` is always non-nil (global seeds it), but consumers should still nil-check defensively; `buildRunOpts` uses `!= nil && *ptr`.
- `SeverityToEvent` is intentionally left unchanged and remains the single source for the base mapping; `ReviewEvent` only layers the downgrade on top.
