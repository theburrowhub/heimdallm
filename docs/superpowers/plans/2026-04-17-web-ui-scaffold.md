# Web UI Scaffold Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Scaffold the SvelteKit `web_ui/` project with a server-side proxy to the daemon, typed API/SSE clients, a minimal layout + landing page, and a Dockerfile — satisfying all acceptance criteria of [issue #30](https://github.com/theburrowhub/heimdallm/issues/30).

**Architecture:** SvelteKit 2.x with `@sveltejs/adapter-node`. Browser talks only to same-origin SvelteKit endpoints; a catch-all proxy `/api/[...path]` and a dedicated `/events` SSE passthrough inject `X-Heimdallm-Token` when forwarding to the daemon. The token lives in `src/lib/server/` — never ships to the browser. Native `EventSource` handles SSE because the connection is same-origin.

**Tech Stack:** SvelteKit 2.x, Svelte 5 (runes), TypeScript 5.x, Vite 6.x, TailwindCSS 4, `@sveltejs/adapter-node`, Vitest 3.x, Node 22 LTS.

**Reference:** [Design spec](../specs/2026-04-17-web-ui-scaffold-design.md)

**Conventions used in this plan:**
- All paths are relative to the repo root (`/home/vbueno/Desarrollo/workspaces/heimdallm-001/`).
- Every step is atomic (2–5 minutes). Run verification after each.
- Commits use conventional-commit prefixes (`feat`, `chore`, etc.) because the repo uses release-please.
- Per `AGENTS.md`, Go tests are not involved here — this is pure Node/TS work.

---

## Task 1: Bootstrap `web_ui/` with package.json, configs, and a placeholder route

**Files:**
- Create: `web_ui/package.json`
- Create: `web_ui/svelte.config.js`
- Create: `web_ui/vite.config.ts`
- Create: `web_ui/tsconfig.json`
- Create: `web_ui/.gitignore`
- Create: `web_ui/.npmrc`
- Create: `web_ui/.prettierrc`
- Create: `web_ui/.prettierignore`
- Create: `web_ui/eslint.config.js`
- Create: `web_ui/src/app.d.ts`
- Create: `web_ui/src/app.html`
- Create: `web_ui/src/routes/+page.svelte`
- Create: `web_ui/static/.gitkeep`

- [ ] **Step 1: Create `web_ui/package.json`**

```json
{
  "name": "heimdallm-web",
  "version": "0.1.0",
  "private": true,
  "type": "module",
  "engines": {
    "node": ">=22"
  },
  "scripts": {
    "dev": "vite dev",
    "build": "vite build",
    "preview": "vite preview",
    "check": "svelte-kit sync && svelte-check --tsconfig ./tsconfig.json",
    "check:watch": "svelte-kit sync && svelte-check --tsconfig ./tsconfig.json --watch",
    "test": "vitest run",
    "test:watch": "vitest",
    "lint": "prettier --check . && eslint .",
    "format": "prettier --write ."
  },
  "devDependencies": {
    "@eslint/js": "^9.15.0",
    "@sveltejs/adapter-node": "^5.2.0",
    "@sveltejs/kit": "^2.15.0",
    "@sveltejs/vite-plugin-svelte": "^5.0.0",
    "@tailwindcss/vite": "^4.0.0",
    "@types/node": "^22.10.0",
    "eslint": "^9.15.0",
    "eslint-config-prettier": "^9.1.0",
    "eslint-plugin-svelte": "^2.46.0",
    "globals": "^15.12.0",
    "jsdom": "^25.0.0",
    "prettier": "^3.4.0",
    "prettier-plugin-svelte": "^3.3.0",
    "svelte": "^5.15.0",
    "svelte-check": "^4.1.0",
    "tailwindcss": "^4.0.0",
    "typescript": "^5.6.0",
    "typescript-eslint": "^8.15.0",
    "vite": "^6.0.0",
    "vitest": "^3.0.0"
  }
}
```

- [ ] **Step 2: Create `web_ui/svelte.config.js`**

```js
import adapter from '@sveltejs/adapter-node';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
const config = {
  preprocess: vitePreprocess(),
  kit: {
    adapter: adapter()
  }
};

export default config;
```

- [ ] **Step 3: Create `web_ui/vite.config.ts`**

```ts
import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  plugins: [tailwindcss(), sveltekit()],
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'jsdom',
    globals: true
  }
});
```

- [ ] **Step 4: Create `web_ui/tsconfig.json`**

```json
{
  "extends": "./.svelte-kit/tsconfig.json",
  "compilerOptions": {
    "allowJs": true,
    "checkJs": true,
    "esModuleInterop": true,
    "forceConsistentCasingInFileNames": true,
    "resolveJsonModule": true,
    "skipLibCheck": true,
    "sourceMap": true,
    "strict": true,
    "moduleResolution": "bundler",
    "types": ["vitest/globals", "node"]
  }
}
```

- [ ] **Step 5: Create `web_ui/.gitignore`**

```
node_modules
/build
/.svelte-kit
/package
.env
.env.*
!.env.example
vite.config.js.timestamp-*
vite.config.ts.timestamp-*
.DS_Store
```

- [ ] **Step 6: Create `web_ui/.npmrc`**

```
engine-strict=true
```

- [ ] **Step 7: Create `web_ui/.prettierrc`**

```json
{
  "useTabs": false,
  "tabWidth": 2,
  "singleQuote": true,
  "trailingComma": "none",
  "printWidth": 100,
  "plugins": ["prettier-plugin-svelte"],
  "overrides": [{ "files": "*.svelte", "options": { "parser": "svelte" } }]
}
```

- [ ] **Step 8: Create `web_ui/.prettierignore`**

```
.svelte-kit/
build/
node_modules/
package-lock.json
```

- [ ] **Step 9: Create `web_ui/eslint.config.js`**

```js
import prettier from 'eslint-config-prettier';
import js from '@eslint/js';
import svelte from 'eslint-plugin-svelte';
import globals from 'globals';
import ts from 'typescript-eslint';

export default ts.config(
  js.configs.recommended,
  ...ts.configs.recommended,
  ...svelte.configs['flat/recommended'],
  prettier,
  ...svelte.configs['flat/prettier'],
  {
    languageOptions: {
      globals: { ...globals.browser, ...globals.node }
    }
  },
  {
    files: ['**/*.svelte'],
    languageOptions: { parserOptions: { parser: ts.parser } }
  },
  { ignores: ['build/', '.svelte-kit/', 'node_modules/'] }
);
```

- [ ] **Step 10: Create `web_ui/src/app.d.ts`**

```ts
// See https://kit.svelte.dev/docs/types#app
declare global {
  namespace App {
    interface Locals {
      login: string | null;
    }
    // interface PageData {}
    // interface PageState {}
    // interface Platform {}
    interface Error {
      message: string;
    }
  }
}

export {};
```

- [ ] **Step 11: Create `web_ui/src/app.html`**

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <link rel="icon" href="%sveltekit.assets%/favicon.ico" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Heimdallm</title>
    %sveltekit.head%
  </head>
  <body data-sveltekit-preload-data="hover" class="min-h-screen bg-white text-gray-900 antialiased">
    <div style="display: contents">%sveltekit.body%</div>
  </body>
</html>
```

- [ ] **Step 12: Create `web_ui/src/routes/+page.svelte` (placeholder)**

```svelte
<h1>Heimdallm scaffold OK</h1>
```

- [ ] **Step 13: Create `web_ui/static/.gitkeep`** (empty file — just `touch web_ui/static/.gitkeep`)

- [ ] **Step 14: Install dependencies**

```bash
cd web_ui && npm install
```

Expected: downloads packages, creates `node_modules/` and `package-lock.json`. If it fails on `engine-strict` it means the local Node is <22 — install Node 22 LTS via nvm/fnm first.

- [ ] **Step 15: Verify dev server boots**

```bash
cd web_ui && npm run dev -- --port 5173 &
sleep 3
curl -s http://127.0.0.1:5173/ | grep -q "Heimdallm scaffold OK" && echo OK || echo FAIL
kill %1
```

Expected: `OK`.

- [ ] **Step 16: Verify production build**

```bash
cd web_ui && npm run build
```

Expected: exits 0, produces `web_ui/build/` directory with a `handler.js`.

- [ ] **Step 17: Commit**

```bash
git add web_ui/
git commit -m "chore(web_ui): bootstrap SvelteKit project scaffold"
```

---

## Task 2: Add TailwindCSS v4 and verify dev server renders styled content

**Files:**
- Create: `web_ui/src/app.css`
- Modify: `web_ui/src/routes/+page.svelte`

Tailwind CSS v4 is already installed as part of Task 1 and wired as a Vite plugin. This task adds the stylesheet import and confirms utility classes work end-to-end.

- [ ] **Step 1: Create `web_ui/src/app.css`**

```css
@import 'tailwindcss';

:root {
  color-scheme: light dark;
}

html,
body {
  font-family:
    system-ui,
    -apple-system,
    'Segoe UI',
    Roboto,
    sans-serif;
}
```

- [ ] **Step 2: Update `web_ui/src/routes/+page.svelte` to import the stylesheet and use utility classes**

```svelte
<script lang="ts">
  import '../app.css';
</script>

<main class="mx-auto max-w-3xl p-8">
  <h1 class="text-3xl font-bold text-indigo-600">Heimdallm scaffold OK</h1>
  <p class="mt-2 text-sm text-gray-500">Tailwind is wired.</p>
</main>
```

- [ ] **Step 3: Verify dev server shows styled content**

```bash
cd web_ui && npm run dev -- --port 5173 &
sleep 3
curl -s http://127.0.0.1:5173/ | grep -q 'text-indigo-600' && echo OK || echo FAIL
kill %1
```

Expected: `OK`.

- [ ] **Step 4: Commit**

```bash
git add web_ui/src/app.css web_ui/src/routes/+page.svelte
git commit -m "chore(web_ui): integrate TailwindCSS v4"
```

---

## Task 3: Define TypeScript types in `src/lib/types.ts`

**Files:**
- Create: `web_ui/src/lib/types.ts`

The types below mirror the **daemon's wire format** (Go structs in `daemon/internal/store/*.go` and `daemon/internal/sse/broker.go`), not the Dart models. Most fields match Dart's models, but a few don't — see inline comments. Specifically:
- The Dart codebase uses `Issue` for "code finding" (file/line/severity). In TS we rename that to **`ReviewFinding`** so `Issue` can mean "GitHub issue" per the Fase-2 design spec.
- `Review.issues` and `Review.suggestions` arrive from the daemon as **JSON-encoded strings** (stored in TEXT columns), not arrays. `api.ts` parses them — see Task 7.
- Field names keep snake_case to match the wire format. UI components convert to camelCase only at display time if needed.

- [ ] **Step 1: Create `web_ui/src/lib/types.ts`**

```ts
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
  median_seconds: number;
  min_seconds: number;
  max_seconds: number;
  bucket_fast: number;       // < 30 s
  bucket_medium: number;     // 30–120 s
  bucket_slow: number;       // 120–300 s
  bucket_very_slow: number;  // > 300 s
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
export type SseEventType =
  | 'pr_detected'
  | 'review_started'
  | 'review_completed'
  | 'review_error';

export interface SseEvent<T = unknown> {
  type: SseEventType;
  data: T;
}
```

- [ ] **Step 2: Verify types compile**

```bash
cd web_ui && npm run check
```

Expected: `0 errors, 0 warnings`.

- [ ] **Step 3: Commit**

```bash
git add web_ui/src/lib/types.ts
git commit -m "feat(web_ui): define TypeScript types mirroring daemon wire format"
```

---

## Task 4: Server-side token loader with tests (TDD)

**Files:**
- Create: `web_ui/src/lib/server/token.ts`
- Create: `web_ui/src/tests/token.test.ts`

Precedence: `HEIMDALLM_API_TOKEN` env var wins over `HEIMDALLM_API_TOKEN_FILE` (default `/data/api_token`). Cached after first successful read so we don't hit the filesystem on every request.

- [ ] **Step 1: Write failing tests at `web_ui/src/tests/token.test.ts`**

```ts
/**
 * @vitest-environment node
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';

const readFileMock = vi.fn();

vi.mock('node:fs/promises', () => ({
  readFile: (...args: unknown[]) => readFileMock(...args)
}));

const originalEnv = { ...process.env };

beforeEach(async () => {
  vi.resetModules();
  readFileMock.mockReset();
  process.env = { ...originalEnv };
  delete process.env.HEIMDALLM_API_TOKEN;
  delete process.env.HEIMDALLM_API_TOKEN_FILE;
});

describe('loadToken', () => {
  it('returns env var when set and non-empty', async () => {
    process.env.HEIMDALLM_API_TOKEN = 'env-token';
    readFileMock.mockResolvedValue('file-token\n');
    const { loadToken } = await import('../lib/server/token.js');
    expect(await loadToken()).toBe('env-token');
    expect(readFileMock).not.toHaveBeenCalled();
  });

  it('falls back to file when env var missing, trimming trailing newline', async () => {
    readFileMock.mockResolvedValue('file-token\n');
    const { loadToken } = await import('../lib/server/token.js');
    expect(await loadToken()).toBe('file-token');
    expect(readFileMock).toHaveBeenCalledWith('/data/api_token', 'utf-8');
  });

  it('caches the resolved token across calls', async () => {
    readFileMock.mockResolvedValue('file-token\n');
    const { loadToken } = await import('../lib/server/token.js');
    expect(await loadToken()).toBe('file-token');
    expect(await loadToken()).toBe('file-token');
    expect(readFileMock).toHaveBeenCalledTimes(1);
  });

  it('returns null when neither env nor file yields a token', async () => {
    readFileMock.mockRejectedValue(new Error('ENOENT'));
    const { loadToken } = await import('../lib/server/token.js');
    expect(await loadToken()).toBeNull();
  });

  it('uses HEIMDALLM_API_TOKEN_FILE override when set', async () => {
    process.env.HEIMDALLM_API_TOKEN_FILE = '/run/secrets/token';
    readFileMock.mockResolvedValue('secret\n');
    const { loadToken } = await import('../lib/server/token.js');
    expect(await loadToken()).toBe('secret');
    expect(readFileMock).toHaveBeenCalledWith('/run/secrets/token', 'utf-8');
  });
});
```

- [ ] **Step 2: Run tests — confirm they fail because the module doesn't exist**

```bash
cd web_ui && npm test -- src/tests/token.test.ts
```

Expected: FAIL with `Cannot find module '../lib/server/token.js'` (or similar).

- [ ] **Step 3: Implement `web_ui/src/lib/server/token.ts`**

```ts
import { readFile } from 'node:fs/promises';

let cached: string | null | undefined;

export async function loadToken(): Promise<string | null> {
  if (cached !== undefined) return cached;

  const envToken = process.env.HEIMDALLM_API_TOKEN;
  if (envToken && envToken.trim().length > 0) {
    cached = envToken.trim();
    return cached;
  }

  const path = process.env.HEIMDALLM_API_TOKEN_FILE ?? '/data/api_token';
  try {
    const contents = await readFile(path, 'utf-8');
    const trimmed = contents.trim();
    cached = trimmed.length > 0 ? trimmed : null;
  } catch {
    cached = null;
  }
  return cached;
}
```

- [ ] **Step 4: Run tests — confirm they pass**

```bash
cd web_ui && npm test -- src/tests/token.test.ts
```

Expected: 5 passing tests.

- [ ] **Step 5: Run typecheck**

```bash
cd web_ui && npm run check
```

Expected: `0 errors`.

- [ ] **Step 6: Commit**

```bash
git add web_ui/src/lib/server/ web_ui/src/tests/token.test.ts
git commit -m "feat(web_ui): add server-side token loader with env/file precedence"
```

---

## Task 5: API catch-all proxy (`/api/[...path]`)

**Files:**
- Create: `web_ui/src/routes/api/[...path]/+server.ts`

Forwards any HTTP method + path to the daemon, injects `X-Heimdallm-Token`, streams the response body back unchanged. No JSON parsing, no validation. If the token is missing, returns 503 with a clear JSON error.

- [ ] **Step 1: Create `web_ui/src/routes/api/[...path]/+server.ts`**

```ts
import { loadToken } from '$lib/server/token.js';
import { error, type RequestHandler } from '@sveltejs/kit';

const DAEMON_URL = (process.env.HEIMDALLM_API_URL ?? 'http://127.0.0.1:7842').replace(/\/+$/, '');

const handle: RequestHandler = async ({ params, request, url, fetch: _fetch }) => {
  const token = await loadToken();
  if (!token) {
    error(503, {
      message: 'daemon token missing: set HEIMDALLM_API_TOKEN or mount /data/api_token'
    });
  }

  const target = new URL(`${DAEMON_URL}/${params.path ?? ''}`);
  target.search = url.search;

  const headers = new Headers();
  const contentType = request.headers.get('content-type');
  if (contentType) headers.set('content-type', contentType);
  headers.set('X-Heimdallm-Token', token);

  const init: RequestInit = {
    method: request.method,
    headers,
    signal: request.signal,
    // @ts-expect-error duplex is required by Node fetch when streaming a body
    duplex: 'half'
  };
  if (request.method !== 'GET' && request.method !== 'HEAD') {
    init.body = request.body;
  }

  const upstream = await fetch(target, init);

  const respHeaders = new Headers();
  const upstreamCt = upstream.headers.get('content-type');
  if (upstreamCt) respHeaders.set('content-type', upstreamCt);

  return new Response(upstream.body, {
    status: upstream.status,
    statusText: upstream.statusText,
    headers: respHeaders
  });
};

export const GET = handle;
export const POST = handle;
export const PUT = handle;
export const DELETE = handle;
export const PATCH = handle;
```

- [ ] **Step 2: Verify typecheck**

```bash
cd web_ui && npm run check
```

Expected: `0 errors`. (The `@ts-expect-error` on `duplex` is required because `RequestInit` lib types don't include `duplex` yet.)

- [ ] **Step 3: Verify build still passes**

```bash
cd web_ui && npm run build
```

Expected: success.

- [ ] **Step 4: Commit**

```bash
git add web_ui/src/routes/api/
git commit -m "feat(web_ui): add catch-all /api proxy to daemon"
```

---

## Task 6: SSE passthrough proxy (`/events`)

**Files:**
- Create: `web_ui/src/routes/events/+server.ts`

Opens an upstream streaming fetch to the daemon's `/events`, pipes the `ReadableStream` back to the browser with `text/event-stream` headers. Browser disconnect aborts the upstream via `request.signal`.

- [ ] **Step 1: Create `web_ui/src/routes/events/+server.ts`**

```ts
import { loadToken } from '$lib/server/token.js';
import { error, type RequestHandler } from '@sveltejs/kit';

const DAEMON_URL = (process.env.HEIMDALLM_API_URL ?? 'http://127.0.0.1:7842').replace(/\/+$/, '');

export const GET: RequestHandler = async ({ request }) => {
  const token = await loadToken();
  if (!token) {
    error(503, {
      message: 'daemon token missing: set HEIMDALLM_API_TOKEN or mount /data/api_token'
    });
  }

  const upstream = await fetch(`${DAEMON_URL}/events`, {
    headers: {
      Accept: 'text/event-stream',
      'X-Heimdallm-Token': token
    },
    signal: request.signal
  });

  if (!upstream.ok || !upstream.body) {
    error(upstream.status ?? 502, { message: `daemon /events failed: ${upstream.status}` });
  }

  return new Response(upstream.body, {
    status: 200,
    headers: {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive'
    }
  });
};
```

- [ ] **Step 2: Verify typecheck**

```bash
cd web_ui && npm run check
```

Expected: `0 errors`.

- [ ] **Step 3: Verify build passes**

```bash
cd web_ui && npm run build
```

Expected: success.

- [ ] **Step 4: Commit**

```bash
git add web_ui/src/routes/events/
git commit -m "feat(web_ui): add /events SSE passthrough to daemon"
```

---

## Task 7: HTTP client `src/lib/api.ts` with tests (TDD)

**Files:**
- Create: `web_ui/src/lib/api.ts`
- Create: `web_ui/src/tests/api.test.ts`

One `request()` helper does all HTTP work. Each typed method delegates with the right verb/path. `ApiError` exposes `{ status, statusText, path, message }`. All browser requests hit `/api/<daemon-path>` — same-origin, token injected by the proxy.

The daemon serialises `Review.issues` / `Review.suggestions` as JSON-encoded strings (not arrays). `api.ts` parses them transparently via a private `parseReview` helper so callers always get typed arrays — same behaviour as Dart's `_parseReviewMap`.

- [ ] **Step 1: Write failing tests at `web_ui/src/tests/api.test.ts`**

```ts
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, fetchPR, fetchPRs, fetchStats, triggerReview, upsertAgent } from '../lib/api.js';

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function okJson(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' }
  });
}

describe('api.ts', () => {
  it('fetchPRs GETs /api/prs and returns a typed array', async () => {
    fetchMock.mockResolvedValue(okJson([{ id: 1, number: 42, title: 't' }]));
    const prs = await fetchPRs();
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/prs',
      expect.objectContaining({ method: 'GET' })
    );
    expect(prs).toHaveLength(1);
    expect(prs[0].id).toBe(1);
  });

  it('fetchPR parses stringified issues/suggestions in latest_review', async () => {
    fetchMock.mockResolvedValue(
      okJson({
        pr: {
          id: 1,
          latest_review: {
            id: 9,
            issues: '[{"file":"a.go","line":1,"description":"x","severity":"LOW"}]',
            suggestions: '["do the thing"]'
          }
        },
        reviews: [
          { id: 9, issues: '[]', suggestions: '[]' }
        ]
      })
    );
    const detail = await fetchPR(1);
    expect(detail.pr.latest_review!.issues).toEqual([
      { file: 'a.go', line: 1, description: 'x', severity: 'LOW' }
    ]);
    expect(detail.pr.latest_review!.suggestions).toEqual(['do the thing']);
    expect(detail.reviews[0].issues).toEqual([]);
    expect(detail.reviews[0].suggestions).toEqual([]);
  });

  it('triggerReview POSTs and resolves on 202', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 202 }));
    await expect(triggerReview(7)).resolves.toBeUndefined();
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/prs/7/review',
      expect.objectContaining({ method: 'POST' })
    );
  });

  it('triggerReview throws ApiError on 500', async () => {
    const make500 = () =>
      new Response('boom', { status: 500, statusText: 'Server Error' });
    fetchMock.mockResolvedValueOnce(make500());
    await expect(triggerReview(7)).rejects.toBeInstanceOf(ApiError);
    fetchMock.mockResolvedValueOnce(make500());
    await expect(triggerReview(7)).rejects.toMatchObject({
      status: 500,
      path: '/api/prs/7/review'
    });
  });

  it('upsertAgent POSTs JSON body with correct content-type', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 200 }));
    await upsertAgent({
      id: 'a',
      name: 'A',
      cli: 'claude',
      prompt: '',
      instructions: 'Review carefully.',
      cli_flags: '',
      is_default: false,
      created_at: '2026-04-17T00:00:00Z'
    });
    const [, init] = fetchMock.mock.calls[0];
    expect(init.method).toBe('POST');
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json');
    expect(JSON.parse(init.body as string)).toMatchObject({ id: 'a', name: 'A', cli: 'claude' });
  });

  it('fetchStats returns parsed JSON object', async () => {
    fetchMock.mockResolvedValue(
      okJson({
        total_reviews: 3,
        by_severity: { HIGH: 1 },
        by_cli: {},
        top_repos: [],
        reviews_last_7_days: [],
        avg_issues_per_review: 1.5,
        review_timing: {
          median_seconds: 42,
          min_seconds: 10,
          max_seconds: 90,
          bucket_fast: 1,
          bucket_medium: 1,
          bucket_slow: 1,
          bucket_very_slow: 0
        }
      })
    );
    const stats = await fetchStats();
    expect(stats.total_reviews).toBe(3);
    expect(stats.avg_issues_per_review).toBe(1.5);
    expect(stats.review_timing.median_seconds).toBe(42);
  });
});
```

- [ ] **Step 2: Run tests — confirm they fail**

```bash
cd web_ui && npm test -- src/tests/api.test.ts
```

Expected: FAIL with `Cannot find module '../lib/api.js'`.

- [ ] **Step 3: Implement `web_ui/src/lib/api.ts`**

```ts
import type {
  Agent,
  Config,
  Issue,
  IssueDetail,
  Me,
  PR,
  PRDetail,
  Review,
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
  return (await resp.json()) as T;
}

// The daemon stores issues/suggestions as JSON-encoded strings; this helper
// parses them so callers always get typed arrays. Mirrors Dart's
// _parseReviewMap in flutter_app/lib/core/api/api_client.dart.
function parseReview(raw: unknown): Review {
  const r = { ...(raw as Record<string, unknown>) };
  if (typeof r.issues === 'string') {
    r.issues = r.issues ? JSON.parse(r.issues as string) : [];
  }
  r.issues ??= [];
  if (typeof r.suggestions === 'string') {
    r.suggestions = r.suggestions ? JSON.parse(r.suggestions as string) : [];
  }
  r.suggestions ??= [];
  return r as unknown as Review;
}

function parsePR(raw: unknown): PR {
  const pr = { ...(raw as Record<string, unknown>) };
  if (pr.latest_review) pr.latest_review = parseReview(pr.latest_review);
  return pr as unknown as PR;
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
export function fetchIssues(): Promise<Issue[]> {
  return request<Issue[]>('GET', '/api/issues');
}

export function fetchIssue(id: number): Promise<IssueDetail> {
  return request<IssueDetail>('GET', `/api/issues/${id}`);
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
```

- [ ] **Step 4: Run tests — confirm they pass**

```bash
cd web_ui && npm test -- src/tests/api.test.ts
```

Expected: 5 passing tests.

- [ ] **Step 5: Run typecheck**

```bash
cd web_ui && npm run check
```

Expected: `0 errors`.

- [ ] **Step 6: Commit**

```bash
git add web_ui/src/lib/api.ts web_ui/src/tests/api.test.ts
git commit -m "feat(web_ui): typed API client mirroring api_client.dart"
```

---

## Task 8: SSE wrapper `src/lib/sse.ts` with tests (TDD)

**Files:**
- Create: `web_ui/src/lib/sse.ts`
- Create: `web_ui/src/tests/sse.test.ts`

Wraps native `EventSource` into two Svelte stores: `events` (a readable of every parsed event) and `connected` (a readable boolean reflecting readyState). Exposes `close()` for teardown.

- [ ] **Step 1: Write failing tests at `web_ui/src/tests/sse.test.ts`**

```ts
import { get } from 'svelte/store';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { connectEvents } from '../lib/sse.js';

type Listener = (e: MessageEvent) => void;

class MockEventSource {
  static instances: MockEventSource[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;

  url: string;
  readyState = MockEventSource.CONNECTING;
  onopen: ((e: Event) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  closed = false;
  listeners: Record<string, Listener[]> = {};

  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }

  addEventListener(type: string, cb: Listener): void {
    (this.listeners[type] ||= []).push(cb);
  }

  close(): void {
    this.closed = true;
    this.readyState = MockEventSource.CLOSED;
  }

  // test helpers
  emit(type: string, data: string): void {
    const ev = new MessageEvent(type, { data });
    this.listeners[type]?.forEach((cb) => cb(ev));
  }
  fireOpen(): void {
    this.readyState = MockEventSource.OPEN;
    this.onopen?.(new Event('open'));
  }
  fireError(): void {
    this.onerror?.(new Event('error'));
  }
}

beforeEach(() => {
  MockEventSource.instances = [];
  vi.stubGlobal('EventSource', MockEventSource);
});

describe('connectEvents', () => {
  it('opens EventSource at /events', () => {
    connectEvents();
    expect(MockEventSource.instances).toHaveLength(1);
    expect(MockEventSource.instances[0].url).toBe('/events');
  });

  it('pushes typed events into the events store', () => {
    const handle = connectEvents();
    const es = MockEventSource.instances[0];
    const received: unknown[] = [];
    const unsub = handle.events.subscribe((e) => {
      if (e) received.push(e);
    });
    es.emit('pr_detected', JSON.stringify({ id: 1 }));
    es.emit('review_completed', '{"pr_id":1}');
    unsub();
    expect(received).toEqual([
      { type: 'pr_detected', data: { id: 1 } },
      { type: 'review_completed', data: { pr_id: 1 } }
    ]);
  });

  it('connected store reflects open/error transitions', () => {
    const handle = connectEvents();
    const es = MockEventSource.instances[0];
    expect(get(handle.connected)).toBe(false);
    es.fireOpen();
    expect(get(handle.connected)).toBe(true);
    es.fireError();
    expect(get(handle.connected)).toBe(false);
  });

  it('close() calls EventSource.close()', () => {
    const handle = connectEvents();
    const es = MockEventSource.instances[0];
    handle.close();
    expect(es.closed).toBe(true);
  });
});
```

- [ ] **Step 2: Run tests — confirm they fail**

```bash
cd web_ui && npm test -- src/tests/sse.test.ts
```

Expected: FAIL with `Cannot find module '../lib/sse.js'`.

- [ ] **Step 3: Implement `web_ui/src/lib/sse.ts`**

```ts
import { readable, writable, type Readable } from 'svelte/store';
import type { SseEvent, SseEventType } from './types.js';

const KNOWN_EVENT_TYPES: SseEventType[] = [
  'pr_detected',
  'review_started',
  'review_completed',
  'review_error'
];

export interface EventsHandle {
  events: Readable<SseEvent | null>;
  connected: Readable<boolean>;
  close: () => void;
}

function parse(data: string): unknown {
  try {
    return JSON.parse(data);
  } catch {
    return data;
  }
}

export function connectEvents(path = '/events'): EventsHandle {
  const connected = writable(false);
  let emit: ((e: SseEvent) => void) | undefined;
  const events = readable<SseEvent | null>(null, (set) => {
    emit = (e) => set(e);
    return () => {
      emit = undefined;
    };
  });

  const source = new EventSource(path);

  source.onopen = () => connected.set(true);
  source.onerror = () => connected.set(false);

  for (const type of KNOWN_EVENT_TYPES) {
    source.addEventListener(type, (ev) => {
      const msg = ev as MessageEvent;
      emit?.({ type, data: parse(msg.data) });
    });
  }

  return {
    events,
    connected,
    close: () => {
      source.close();
      connected.set(false);
    }
  };
}
```

- [ ] **Step 4: Run tests — confirm they pass**

```bash
cd web_ui && npm test -- src/tests/sse.test.ts
```

Expected: 4 passing tests.

- [ ] **Step 5: Run typecheck**

```bash
cd web_ui && npm run check
```

Expected: `0 errors`.

- [ ] **Step 6: Commit**

```bash
git add web_ui/src/lib/sse.ts web_ui/src/tests/sse.test.ts
git commit -m "feat(web_ui): SSE wrapper exposing events and connected stores"
```

---

## Task 9: Auth store and `+layout.ts` loader

**Files:**
- Create: `web_ui/src/lib/stores.ts`
- Create: `web_ui/src/routes/+layout.ts`

Auth is best-effort: if `/api/me` succeeds we store the login; if it fails we surface a friendly message via `authError`. The landing page reads from this store; future pages (#31/#32) will too.

- [ ] **Step 1: Create `web_ui/src/lib/stores.ts`**

```ts
import { writable } from 'svelte/store';

export interface AuthState {
  login: string | null;
  authError: string | null;
  ready: boolean;
}

export const auth = writable<AuthState>({ login: null, authError: null, ready: false });
```

- [ ] **Step 2: Create `web_ui/src/routes/+layout.ts`**

```ts
import { fetchMe } from '$lib/api.js';
import { auth } from '$lib/stores.js';
import type { LayoutLoad } from './$types.js';

export const ssr = false; // browser-only — auth state + SSE live in the client

export const load: LayoutLoad = async () => {
  try {
    const me = await fetchMe();
    auth.set({ login: me.login ?? null, authError: null, ready: true });
  } catch (err) {
    const message = err instanceof Error ? err.message : 'daemon unreachable';
    auth.set({ login: null, authError: message, ready: true });
  }
  return {};
};
```

- [ ] **Step 3: Verify typecheck**

```bash
cd web_ui && npm run check
```

Expected: `0 errors`. (SvelteKit generates `./$types.js` during `svelte-kit sync` — the `check` script runs that first.)

- [ ] **Step 4: Commit**

```bash
git add web_ui/src/lib/stores.ts web_ui/src/routes/+layout.ts
git commit -m "feat(web_ui): auth store + layout loader calling /api/me"
```

---

## Task 10: Layout shell (`+layout.svelte`)

**Files:**
- Create: `web_ui/src/routes/+layout.svelte`

Top nav bar, login display, auth-error banner, SSE connection initiated on mount and torn down on destroy. Nav links point at routes #31/#32 will add — they'll 404 until then, which is intentional.

- [ ] **Step 1: Create `web_ui/src/routes/+layout.svelte`**

```svelte
<script lang="ts">
  import '../app.css';
  import { browser } from '$app/environment';
  import { page } from '$app/stores';
  import { connectEvents, type EventsHandle } from '$lib/sse.js';
  import { auth } from '$lib/stores.js';
  import { onDestroy, onMount } from 'svelte';

  let { children } = $props();

  let sse: EventsHandle | undefined;

  onMount(() => {
    if (browser) sse = connectEvents();
  });

  onDestroy(() => {
    sse?.close();
  });

  const navItems = [
    { href: '/', label: 'Dashboard' },
    { href: '/prs', label: 'PRs' },
    { href: '/issues', label: 'Issues' },
    { href: '/agents', label: 'Agents' },
    { href: '/config', label: 'Config' },
    { href: '/logs', label: 'Logs' }
  ];
</script>

<header class="border-b border-gray-200 bg-white">
  <nav class="mx-auto flex max-w-6xl items-center justify-between gap-4 px-6 py-3">
    <a href="/" class="text-lg font-semibold tracking-tight">Heimdallm</a>
    <ul class="flex items-center gap-4 text-sm">
      {#each navItems as item (item.href)}
        <li>
          <a
            href={item.href}
            class="hover:text-indigo-600 {$page.url.pathname === item.href
              ? 'text-indigo-600 font-medium'
              : 'text-gray-600'}"
          >
            {item.label}
          </a>
        </li>
      {/each}
    </ul>
    <span class="text-sm text-gray-500" data-testid="login">
      {$auth.login ?? '—'}
    </span>
  </nav>
</header>

{#if $auth.authError}
  <div class="border-b border-red-200 bg-red-50 px-6 py-2 text-sm text-red-700">
    Daemon unreachable — check <code>HEIMDALLM_API_URL</code>. ({$auth.authError})
  </div>
{/if}

<main class="mx-auto max-w-6xl px-6 py-8">
  {@render children()}
</main>
```

- [ ] **Step 2: Run typecheck**

```bash
cd web_ui && npm run check
```

Expected: `0 errors`.

- [ ] **Step 3: Run build**

```bash
cd web_ui && npm run build
```

Expected: success.

- [ ] **Step 4: Smoke-test in dev (without a daemon, auth banner should appear)**

```bash
cd web_ui && npm run dev -- --port 5173 &
sleep 3
curl -s http://127.0.0.1:5173/ | grep -q 'Heimdallm' && echo OK || echo FAIL
kill %1
```

Expected: `OK` (the banner will be absent in SSR because `ssr = false`, but the nav shell will render).

- [ ] **Step 5: Commit**

```bash
git add web_ui/src/routes/+layout.svelte
git commit -m "feat(web_ui): layout shell with nav, auth banner, and SSE init"
```

---

## Task 11: Landing page (`+page.svelte`) with SSE indicator and stats

**Files:**
- Replace contents of: `web_ui/src/routes/+page.svelte`

Demonstrates end-to-end wiring: hits `/api/stats` and shows the resulting key numbers, plus a live connection pill driven by the SSE `connected` store.

- [ ] **Step 1: Replace `web_ui/src/routes/+page.svelte`**

```svelte
<script lang="ts">
  import { browser } from '$app/environment';
  import { fetchStats } from '$lib/api.js';
  import { connectEvents } from '$lib/sse.js';
  import type { Stats } from '$lib/types.js';
  import { onDestroy, onMount } from 'svelte';

  let stats = $state<Stats | null>(null);
  let statsError = $state<string | null>(null);
  let sseHandle = $state<ReturnType<typeof connectEvents> | null>(null);
  let connected = $state(false);
  let connUnsub: (() => void) | undefined;

  onMount(async () => {
    if (!browser) return;
    sseHandle = connectEvents();
    connUnsub = sseHandle.connected.subscribe((v) => (connected = v));
    try {
      stats = await fetchStats();
    } catch (e) {
      statsError = e instanceof Error ? e.message : String(e);
    }
  });

  onDestroy(() => {
    connUnsub?.();
    sseHandle?.close();
  });

  const pillClass = $derived(
    connected
      ? 'bg-green-100 text-green-700'
      : 'bg-amber-100 text-amber-700'
  );
  const pillLabel = $derived(connected ? 'connected' : 'disconnected');
</script>

<section class="flex items-center gap-3">
  <h1 class="text-3xl font-bold">Heimdallm</h1>
  <span class="rounded-full px-2 py-0.5 text-xs font-medium {pillClass}" data-testid="sse-pill">
    {pillLabel}
  </span>
</section>

<p class="mt-1 text-sm text-gray-500">v0.1 scaffold — dashboard lands in #31.</p>

<section class="mt-8 grid grid-cols-2 gap-4 md:grid-cols-4">
  {#each [
    { label: 'Total reviews', value: stats?.total_reviews },
    { label: 'Avg findings / review', value: stats?.avg_issues_per_review?.toFixed(1) },
    { label: 'Median time (s)', value: stats?.review_timing?.median_seconds?.toFixed(1) },
    { label: 'High-severity', value: stats?.by_severity?.HIGH ?? 0 }
  ] as cell (cell.label)}
    <div class="rounded-lg border border-gray-200 bg-white p-4">
      <dt class="text-xs uppercase tracking-wide text-gray-500">{cell.label}</dt>
      <dd class="mt-1 text-2xl font-semibold">
        {cell.value ?? '—'}
      </dd>
    </div>
  {/each}
</section>

{#if statsError}
  <p class="mt-4 text-sm text-red-600">Could not load stats: {statsError}</p>
{/if}
```

- [ ] **Step 2: Run typecheck**

```bash
cd web_ui && npm run check
```

Expected: `0 errors`.

- [ ] **Step 3: Run build**

```bash
cd web_ui && npm run build
```

Expected: success.

- [ ] **Step 4: Commit**

```bash
git add web_ui/src/routes/+page.svelte
git commit -m "feat(web_ui): landing page with SSE indicator and stats cards"
```

---

## Task 12: `.env.example` and `README.md`

**Files:**
- Create: `web_ui/.env.example`
- Create: `web_ui/README.md`

- [ ] **Step 1: Create `web_ui/.env.example`**

```
# URL of the running heimdallm daemon. In docker-compose, set to
# http://daemon:7842 (internal service hostname).
HEIMDALLM_API_URL=http://127.0.0.1:7842

# API token. Leave unset to read from HEIMDALLM_API_TOKEN_FILE instead.
# HEIMDALLM_API_TOKEN=

# Path to the token file. Defaults to /data/api_token. For local dev,
# point at ~/.local/share/heimdallm/api_token.
# HEIMDALLM_API_TOKEN_FILE=/data/api_token
```

- [ ] **Step 2: Create `web_ui/README.md`**

```markdown
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
```

- [ ] **Step 3: Commit**

```bash
git add web_ui/.env.example web_ui/README.md
git commit -m "docs(web_ui): add .env.example and README quickstart"
```

---

## Task 13: Dockerfile + `.dockerignore`

**Files:**
- Create: `web_ui/Dockerfile`
- Create: `web_ui/.dockerignore`

Multi-stage build: `node:22-alpine` build stage runs `npm ci` + `npm run build`; runtime stage copies only `build/`, `package.json`, and production `node_modules`. Final image runs as the unprivileged `node` user on port 3000. docker-compose wiring lives in issue #33.

- [ ] **Step 1: Create `web_ui/.dockerignore`**

```
node_modules
build
.svelte-kit
.git
.env
.env.*
!.env.example
*.log
.DS_Store
```

- [ ] **Step 2: Create `web_ui/Dockerfile`**

```dockerfile
# syntax=docker/dockerfile:1.7

# ─── Build stage ────────────────────────────────────────────────────────
FROM node:22-alpine AS build
WORKDIR /app

COPY package.json package-lock.json ./
RUN npm ci

COPY . .
RUN npm run build && npm prune --omit=dev

# ─── Runtime stage ──────────────────────────────────────────────────────
FROM node:22-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production
ENV PORT=3000
ENV HOST=0.0.0.0

COPY --from=build /app/build ./build
COPY --from=build /app/node_modules ./node_modules
COPY --from=build /app/package.json ./package.json

USER node
EXPOSE 3000
CMD ["node", "build"]
```

- [ ] **Step 3: Verify the image builds**

```bash
cd web_ui && docker build -t heimdallm-web:dev .
```

Expected: image builds successfully.

- [ ] **Step 4: Smoke-run the image (no daemon required for boot — the app serves the shell; `/api/me` will 503 without a token)**

```bash
docker run --rm -d --name heimdallm-web-smoke -p 3000:3000 heimdallm-web:dev
sleep 3
curl -s http://127.0.0.1:3000/ | grep -q 'Heimdallm' && echo OK || echo FAIL
docker stop heimdallm-web-smoke
```

Expected: `OK`.

- [ ] **Step 5: Commit**

```bash
git add web_ui/Dockerfile web_ui/.dockerignore
git commit -m "feat(web_ui): add Dockerfile for standalone container build"
```

---

## Task 14: Final verification against acceptance criteria

- [ ] **Step 1: Run the full check suite**

```bash
cd web_ui && npm run check && npm test && npm run build
```

Expected:
- `svelte-check` reports 0 errors, 0 warnings.
- Vitest: all tests pass (5 in `token.test.ts` + 6 in `api.test.ts` + 4 in `sse.test.ts` = 15).
- Build succeeds and produces `build/`.

- [ ] **Step 2: Confirm the dev server works end-to-end with a real daemon**

Only runnable if you have the daemon running locally.

```bash
cd web_ui
echo "HEIMDALLM_API_TOKEN_FILE=$HOME/.local/share/heimdallm/api_token" > .env
npm run dev -- --port 5173 &
sleep 3
# expect the login name of the authenticated user in the response
curl -s http://127.0.0.1:5173/api/me
# expect an event-stream response (will hang — use timeout)
timeout 3 curl -sN http://127.0.0.1:5173/events | head -n 5 || true
kill %1
```

Expected: `/api/me` returns `{"login":"..."}`; `/events` emits at least a `:` comment line or an actual event.

- [ ] **Step 3: Map each AC in issue #30 to evidence**

Paste into the PR description:

| AC | Evidence |
|---|---|
| `npm run build` sin errores | Task 14 step 1 build passes |
| `npm run dev` levanta el servidor | Task 14 step 2 dev server serves `/` |
| `api.ts` tipado correctamente y conecta al daemon | `src/lib/api.ts` + `src/tests/api.test.ts` + Task 14 step 2 `/api/me` call |
| SSE recibe y parsea eventos correctamente | `src/lib/sse.ts` + `src/tests/sse.test.ts` + Task 14 step 2 `/events` curl |
| Token leído automáticamente | `src/lib/server/token.ts` + `src/tests/token.test.ts` (5 tests) |

- [ ] **Step 4: Push branch and open PR**

```bash
# branch name convention from existing repo: <scope>/<short-slug>
git checkout -b feat/web-ui-scaffold
git push -u origin feat/web-ui-scaffold
gh pr create --title "feat(web_ui): scaffold SvelteKit + API/SSE clients (#30)" \
  --body "$(cat <<'EOF'
Closes #30. Implements the foundational scaffold from
`docs/superpowers/specs/2026-04-17-web-ui-scaffold-design.md`.

## Summary

- SvelteKit 2 + Svelte 5 + Tailwind v4 + adapter-node.
- Server-side proxy: `/api/[...path]` catch-all + `/events` SSE passthrough.
- `src/lib/server/token.ts` reads `HEIMDALLM_API_TOKEN` env var or `/data/api_token`.
- `src/lib/api.ts` — typed client mirroring `api_client.dart` plus Fase-2 methods.
- `src/lib/sse.ts` — native-EventSource wrapper exposing Svelte stores.
- Landing page at `/` with connection indicator + stats cards.
- Dockerfile (standalone; docker-compose wiring is #33).

## Test plan

- [x] `npm run check` — 0 errors
- [x] `npm test` — 15 passing
- [x] `npm run build` — produces `build/`
- [x] `docker build` — succeeds
- [ ] Manual: `/api/me` returns login with daemon running
- [ ] Manual: `/events` streams events

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: PR opened; URL returned by `gh pr create`.

- [ ] **Step 5: Mark #30 complete**

Once the PR merges, #30 closes automatically via the `Closes #30` in the PR body.

---

## Self-review checklist (plan author)

1. **Spec coverage** — every section of `2026-04-17-web-ui-scaffold-design.md` has a task:
   - Architecture → Tasks 5, 6
   - File layout → Task 1 (skeleton), Tasks 3–11 (files)
   - Key interfaces → Tasks 3, 4, 5, 6, 7, 8
   - Layout behaviour → Tasks 9, 10, 11
   - TailwindCSS → Task 2
   - Testing → Tasks 4, 7, 8
   - Tooling → Task 1
   - Dockerfile → Task 13
   - AC mapping → Task 14
   - Out-of-scope items are deliberately absent (no Issues route, no docker-compose).
2. **No placeholders** — no TBD/TODO; every code block is concrete.
3. **Type consistency** — `Agent`, `Stats`, `Me`, `PR`, `Review`, `Issue`, `SseEvent` defined in Task 3 are used verbatim in Tasks 7, 8, 10, 11.
4. **Ambiguity** — `ReviewFinding` vs `Issue` naming divergence from Dart is called out explicitly in Task 3.
