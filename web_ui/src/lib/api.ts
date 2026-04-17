import type {
  Agent,
  Config,
  Issue,
  IssueDetail,
  IssueReview,
  IssueTriage,
  Me,
  PR,
  PRDetail,
  Review,
  ReviewFinding,
  Stats
} from './types.js';

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly statusText: string,
    public readonly path: string,
    message?: string
  ) {
    super(message ?? `${path} failed: ${status} ${statusText}`);
    this.name = 'ApiError';
  }
}

type Method = 'GET' | 'POST' | 'PUT' | 'DELETE';

async function request<T>(
  method: Method,
  path: string,
  body?: unknown,
  { expectEmpty = false }: { expectEmpty?: boolean } = {}
): Promise<T> {
  const init: RequestInit = { method };
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify(body);
  }
  const resp = await fetch(path, init);
  if (!resp.ok) {
    const text = await resp.text().catch(() => '');
    throw new ApiError(resp.status, resp.statusText, path, text || undefined);
  }
  if (expectEmpty || resp.status === 202 || resp.status === 204) {
    return undefined as T;
  }
  // Defensive: a 200 with empty body would throw on .json() — treat as undefined.
  if (resp.headers.get('content-length') === '0') {
    return undefined as T;
  }
  return (await resp.json()) as T;
}

function safeParse<T>(raw: string, fallback: T): T {
  try {
    return JSON.parse(raw) as T;
  } catch {
    return fallback;
  }
}

// The daemon stores issues/suggestions as JSON-encoded strings; this helper
// parses them so callers always get typed arrays. Mirrors Dart's
// _parseReviewMap in flutter_app/lib/core/api/api_client.dart.
function parseReview(raw: unknown): Review {
  const r = { ...(raw as Record<string, unknown>) };
  if (typeof r.issues === 'string') {
    r.issues = r.issues ? safeParse<ReviewFinding[]>(r.issues, []) : [];
  }
  r.issues ??= [];
  if (typeof r.suggestions === 'string') {
    r.suggestions = r.suggestions ? safeParse<string[]>(r.suggestions, []) : [];
  }
  r.suggestions ??= [];
  return r as unknown as Review;
}

function parsePR(raw: unknown): PR {
  const pr = { ...(raw as Record<string, unknown>) };
  if (pr.latest_review) pr.latest_review = parseReview(pr.latest_review);
  return pr as unknown as PR;
}

function parseIssueReview(raw: unknown): IssueReview {
  const r = { ...(raw as Record<string, unknown>) };
  if (typeof r.triage === 'string') {
    r.triage = r.triage ? safeParse<IssueTriage>(r.triage, {}) : {};
  }
  r.triage ??= {};
  if (typeof r.suggestions === 'string') {
    r.suggestions = r.suggestions ? safeParse<string[]>(r.suggestions, []) : [];
  }
  r.suggestions ??= [];
  return r as unknown as IssueReview;
}

function parseIssue(raw: unknown): Issue {
  const i = { ...(raw as Record<string, unknown>) };
  if (typeof i.assignees === 'string') {
    i.assignees = i.assignees ? safeParse<string[]>(i.assignees, []) : [];
  }
  i.assignees ??= [];
  if (typeof i.labels === 'string') {
    i.labels = i.labels ? safeParse<string[]>(i.labels, []) : [];
  }
  i.labels ??= [];
  if (i.latest_review) i.latest_review = parseIssueReview(i.latest_review);
  return i as unknown as Issue;
}

// ─── Health ─────────────────────────────────────────────────────────────
export function checkHealth(): Promise<boolean> {
  return fetch('/api/health')
    .then((r) => r.ok)
    .catch(() => false);
}

// ─── PRs ────────────────────────────────────────────────────────────────
export async function fetchPRs(): Promise<PR[]> {
  const raws = await request<unknown[]>('GET', '/api/prs');
  return raws.map(parsePR);
}

export async function fetchPR(id: number): Promise<PRDetail> {
  const raw = await request<{ pr: unknown; reviews?: unknown[] }>('GET', `/api/prs/${id}`);
  return {
    pr: parsePR(raw.pr),
    reviews: (raw.reviews ?? []).map(parseReview)
  };
}

export function triggerReview(id: number): Promise<void> {
  return request<void>('POST', `/api/prs/${id}/review`, undefined, { expectEmpty: true });
}

export function dismissPR(id: number): Promise<void> {
  return request<void>('POST', `/api/prs/${id}/dismiss`, undefined, { expectEmpty: true });
}

export function undismissPR(id: number): Promise<void> {
  return request<void>('POST', `/api/prs/${id}/undismiss`, undefined, { expectEmpty: true });
}

// ─── Issues (Fase 2 — daemon endpoints arrive in #25–#28) ───────────────
export async function fetchIssues(): Promise<Issue[]> {
  const raws = await request<unknown[]>('GET', '/api/issues');
  return raws.map(parseIssue);
}

export async function fetchIssue(id: number): Promise<IssueDetail> {
  const raw = await request<{ issue: unknown; reviews?: unknown[] }>('GET', `/api/issues/${id}`);
  return {
    issue: parseIssue(raw.issue),
    reviews: (raw.reviews ?? []).map(parseIssueReview)
  };
}

export function triggerIssueReview(id: number): Promise<void> {
  return request<void>('POST', `/api/issues/${id}/review`, undefined, { expectEmpty: true });
}

export function dismissIssue(id: number): Promise<void> {
  return request<void>('POST', `/api/issues/${id}/dismiss`, undefined, { expectEmpty: true });
}

export function undismissIssue(id: number): Promise<void> {
  return request<void>('POST', `/api/issues/${id}/undismiss`, undefined, { expectEmpty: true });
}

// ─── Config ─────────────────────────────────────────────────────────────
export function fetchConfig(): Promise<Config> {
  return request<Config>('GET', '/api/config');
}

export function updateConfig(config: Config): Promise<void> {
  return request<void>('PUT', '/api/config', config, { expectEmpty: true });
}

export function reloadConfig(): Promise<void> {
  return request<void>('POST', '/api/reload', undefined, { expectEmpty: true });
}

// ─── Agents ─────────────────────────────────────────────────────────────
export function fetchAgents(): Promise<Agent[]> {
  return request<Agent[]>('GET', '/api/agents');
}

export function upsertAgent(agent: Agent): Promise<void> {
  return request<void>('POST', '/api/agents', agent, { expectEmpty: true });
}

export function deleteAgent(id: string): Promise<void> {
  return request<void>('DELETE', `/api/agents/${id}`, undefined, { expectEmpty: true });
}

// ─── Identity & stats ───────────────────────────────────────────────────
export function fetchMe(): Promise<Me> {
  return request<Me>('GET', '/api/me');
}

export function fetchStats(): Promise<Stats> {
  return request<Stats>('GET', '/api/stats');
}
