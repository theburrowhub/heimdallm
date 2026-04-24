# ─── Stage 1: Build Heimdallm daemon ──────────────────────────────────────────
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /build
COPY daemon/go.mod daemon/go.sum ./
RUN go mod download

COPY daemon/ .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /heimdallm ./cmd/heimdallm

# ─── Stage 2: Runtime with AI CLIs ───────────────────────────────────────────
FROM node:20-alpine

RUN apk add --no-cache ca-certificates tzdata bash curl coreutils git openssh-client

# ── Install AI CLI tools ──────────────────────────────────────────────────────
# Claude Code CLI (Anthropic) — requires ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN at runtime
RUN npm install -g @anthropic-ai/claude-code 2>/dev/null || true

# Gemini CLI (Google) — requires GEMINI_API_KEY at runtime
RUN npm install -g @google/gemini-cli 2>/dev/null || true

# Codex CLI (OpenAI) — requires OPENAI_API_KEY or CODEX_API_KEY at runtime
RUN npm install -g @openai/codex 2>/dev/null || true

# OpenCode CLI — requires provider API key (e.g. ANTHROPIC_API_KEY) at runtime
# Install the musl-specific binary package alongside the main package (Alpine uses musl).
# Then symlink the musl binary into the wrapper's expected location.
RUN npm install -g opencode-ai opencode-linux-x64-musl 2>/dev/null || true \
    && MUSL_BIN="/usr/local/lib/node_modules/opencode-linux-x64-musl/bin/opencode" \
    && WRAPPER_BIN="/usr/local/lib/node_modules/opencode-ai/bin/.opencode" \
    && if [ -f "$MUSL_BIN" ]; then cp "$MUSL_BIN" "$WRAPPER_BIN" && chmod +x "$WRAPPER_BIN"; fi

# Clean npm cache to reduce image size
RUN npm cache clean --force 2>/dev/null || true

# ── Create non-root user ─────────────────────────────────────────────────────
RUN addgroup -S heimdallm && adduser -S heimdallm -G heimdallm

# ── Create directories ───────────────────────────────────────────────────────
# Heimdallm dirs
RUN mkdir -p /config /data && chown -R heimdallm:heimdallm /config /data

# AI CLI config/cache directories (must be writable by the runtime user)
RUN mkdir -p /home/heimdallm/.claude \
             /home/heimdallm/.gemini \
             /home/heimdallm/.codex \
             /home/heimdallm/.config/opencode \
             /home/heimdallm/.local/share/opencode \
    && chown -R heimdallm:heimdallm /home/heimdallm

# ── SSH setup for git operations (auto_implement) ───────────────────────────
# Pre-seed GitHub's host key so git fetch/push over SSH never prompts.
# The host's SSH agent socket is forwarded at runtime via docker-compose.
RUN mkdir -p /home/heimdallm/.ssh \
    && ssh-keyscan github.com >> /home/heimdallm/.ssh/known_hosts 2>/dev/null \
    && chmod 700 /home/heimdallm/.ssh \
    && chmod 644 /home/heimdallm/.ssh/known_hosts \
    && chown -R heimdallm:heimdallm /home/heimdallm/.ssh

# ── Copy Heimdallm binary ────────────────────────────────────────────────────
COPY --from=builder /heimdallm /usr/local/bin/heimdallm

# ── Default environment ──────────────────────────────────────────────────────
ENV HEIMDALLM_DATA_DIR=/data
ENV HEIMDALLM_CONFIG_PATH=/config/config.toml
ENV HEIMDALLM_BIND_ADDR=0.0.0.0
# Ensure npm global binaries are in PATH
ENV PATH="/usr/local/lib/node_modules/.bin:${PATH}"
# Disable interactive prompts / auto-updates in AI CLIs
ENV CI=true
ENV OPENCODE_DISABLE_AUTOUPDATE=true

EXPOSE 7842

VOLUME ["/data", "/config"]

USER heimdallm
WORKDIR /home/heimdallm

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:7842/health || exit 1

ENTRYPOINT ["/usr/local/bin/heimdallm"]
