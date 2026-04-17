// Wire-format types mirroring the daemon's JSON responses.
// Field names intentionally use snake_case to match the wire format.
//
// The Dart code-finding type (file/line/severity) is called `Issue` in
// flutter_app/lib/core/models/issue.dart. We rename it to `ReviewFinding`
// here so `Issue` can denote the Fase-2 GitHub-issue domain type without
// collision.

export interface ReviewFinding {
  file: string;
  line: number;
  description: string;
  severity: string;
}

// The daemon stores issues/suggestions as JSON-encoded strings in TEXT
// columns (see daemon/internal/store/reviews.go: `Issues string json:"issues"`),
// so the wire format is `"[{...}]"` — a string, not an array. api.ts is
// responsible for parsing these strings into typed arrays before returning
// Reviews to callers. The Dart api_client does the same in _parseReviewMap.
export interface Review {
  id: number;
  pr_id: number;
  cli_used: string;
  summary: string;
  issues: ReviewFinding[];
  suggestions: string[];
  severity: string;
  created_at: string;
  github_review_id: number; // 0 = not yet published to GitHub
}

export interface PR {
  id: number;
  github_id: number;
  repo: string;
  number: number;
  title: string;
  author: string;
  url: string;
  state: string;
  updated_at: string;
  fetched_at: string;
  dismissed: boolean;
  latest_review?: Review | null;
}

export interface PRDetail {
  pr: PR;
  reviews: Review[];
}

// Fase-2 GitHub issue tracking (daemon endpoints not yet implemented;
// shape follows docs/superpowers/specs/2026-04-16-heimdallm-v2-design.md).
export interface Issue {
  id: number;
  github_id: number;
  repo: string;
  number: number;
  title: string;
  body: string;
  author: string;
  assignees: string[];
  labels: string[];
  state: string;
  created_at: string;
  fetched_at: string;
  dismissed: boolean;
  latest_review?: IssueReview | null;
}

export interface IssueReview {
  id: number;
  issue_id: number;
  cli_used: string;
  summary: string;
  triage: unknown;
  suggestions: unknown[];
  action_taken: 'review_only' | 'auto_implement';
  pr_created: number;
  created_at: string;
}

export interface IssueDetail {
  issue: Issue;
  reviews: IssueReview[];
}

// Agent mirrors daemon/internal/store/agents.go. The daemon's Agent
// includes `cli` (claude | gemini | codex) and `created_at` in addition
// to the fields Dart's ReviewPrompt exposes.
export interface Agent {
  id: string;
  name: string;
  cli: string;
  prompt: string;
  instructions: string;
  cli_flags: string;
  is_default: boolean;
  created_at: string;
}

// Stats mirrors daemon/internal/store/store.go Stats struct.
export interface RepoCount {
  repo: string;
  count: number;
}

export interface DayCount {
  day: string;
  count: number;
}

export interface ReviewTimingStats {
  sample_count: number;
  avg_seconds: number;
  median_seconds: number;
  min_seconds: number;
  max_seconds: number;
  bucket_fast: number; // < 30 s
  bucket_medium: number; // 30–120 s
  bucket_slow: number; // 120–300 s
  bucket_very_slow: number; // > 300 s
}

export interface Stats {
  total_reviews: number;
  by_severity: Record<string, number>;
  by_cli: Record<string, number>;
  top_repos: RepoCount[];
  reviews_last_7_days: DayCount[];
  avg_issues_per_review: number;
  review_timing: ReviewTimingStats;
}

export interface Me {
  login: string;
}

// Config is deliberately loose — the shape is large and the daemon owns
// validation. Typed access happens at the Config-page level in a later PR.
export type Config = Record<string, unknown>;

// SSE event types emitted by the daemon's sse.Broker. Must match the
// constants in daemon/internal/sse/broker.go exactly or listeners in
// sse.ts won't fire.
export type SseEventType = 'pr_detected' | 'review_started' | 'review_completed' | 'review_error';

export interface SseEvent<T = unknown> {
  type: SseEventType;
  data: T;
}
