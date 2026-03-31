# PR Auto-Review Desktop App -- Claude Code Prompt

You are a senior software engineer. Your task is to design and implement
a production-ready desktop application with a local daemon architecture.

The goal is to build a GitHub Pull Request auto-review system powered by
local AI CLI tools (Claude Code, Codex, Gemini CLI, etc).

## 🧩 High-Level Architecture

The system MUST be split into two components:

1.  Local Daemon (core system)
2.  Flutter Desktop App (UI client)

Strict separation of concerns is required.

------------------------------------------------------------------------

## ⚙️ 1. DAEMON REQUIREMENTS

Language: Prefer Go (fallback: Python if necessary)

### 1.1 GitHub Integration

-   Poll GitHub API at configurable intervals (1m, 5m, 30m, 1h)
-   Fetch PRs assigned or requesting review
-   Filter by configured repositories

### 1.2 PR Processing Pipeline

1.  Fetch metadata
2.  Fetch diff
3.  Normalize diff
4.  Generate prompt
5.  Select AI CLI
6.  Execute review
7.  Parse result
8.  Store result

### 1.3 CLI Detection & Execution

Detect: - claude - codex - gemini

Use `which <cli>`

### 1.4 Prompt Strategy

Force JSON output: { "summary": "...", "issues": \[...\], "suggestions":
\[...\], "severity": "low\|medium\|high" }

### 1.5 Storage

SQLite: - prs - reviews - configs

### 1.6 API

-   GET /health
-   GET /prs
-   POST /review/{id}

### 1.7 Background Behavior

-   Continuous execution
-   Scheduling
-   State management

### 1.8 Notifications

-   PR detected
-   Review started/completed
-   Errors

### 1.9 Service Mode

-   macOS LaunchAgent
-   Linux systemd user service

------------------------------------------------------------------------

## 🎨 2. FLUTTER APP

-   UI only
-   Dashboard of PRs
-   PR detail
-   Trigger re-review
-   Config management

------------------------------------------------------------------------

## 📦 3. PACKAGING

macOS: - .app bundle

Linux: - AppImage or .deb

------------------------------------------------------------------------

## 🔐 4. SECURITY

-   Secure token storage
-   No sensitive logs

------------------------------------------------------------------------

## 🧪 5. TESTING

-   Unit tests
-   Integration tests

------------------------------------------------------------------------

## 📁 6. STRUCTURE

daemon/ flutter_app/

------------------------------------------------------------------------

## 🚀 7. OUTPUT

-   Full architecture
-   Scaffolding
-   Implementation
-   Run instructions

------------------------------------------------------------------------

## IMPORTANT

-   Do NOT mix UI and backend
-   Build extensible abstractions
