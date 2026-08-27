package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/heimdallm/daemon/internal/procgroup"
)

const (
	modelDiscoveryTimeout  = 8 * time.Second
	maxDiscoveredModels    = 500
	maxModelIdentifierSize = 256
	maxModelOutputSize     = 4 << 20
)

var modelDiscoveryCLIs = []string{"claude", "gemini", "codex", "opencode"}

type modelDiscoveryResult struct {
	cli    string
	models []string
	err    error
}

type modelPathResolver func(string) string

// DiscoverModels asks every installed AI CLI for the models available to the
// CLI's current account. Providers are queried concurrently and independently:
// an unavailable, unauthenticated, or slow CLI contributes an empty list
// without hiding successful results from the other providers.
func (e *Executor) DiscoverModels(ctx context.Context) map[string][]string {
	return discoverModels(ctx, resolveCLIPath)
}

func discoverModels(ctx context.Context, resolve modelPathResolver) map[string][]string {
	result := make(map[string][]string, len(modelDiscoveryCLIs))
	paths := make(map[string]string, len(modelDiscoveryCLIs))
	for _, cli := range modelDiscoveryCLIs {
		result[cli] = []string{}
		paths[cli] = resolve(cli)
	}

	results := make(chan modelDiscoveryResult, len(modelDiscoveryCLIs))
	started := 0
	for _, cli := range modelDiscoveryCLIs {
		path := paths[cli]
		if path == "" {
			continue
		}
		started++
		go func(cli, path string) {
			providerCtx, cancel := context.WithTimeout(ctx, modelDiscoveryTimeout)
			defer cancel()
			models, err := discoverModelsForCLI(providerCtx, cli, path)
			results <- modelDiscoveryResult{cli: cli, models: models, err: err}
		}(cli, path)
	}

	for range started {
		discovered := <-results
		if discovered.err != nil {
			slog.Debug("executor: model discovery unavailable", "cli", discovered.cli, "err", discovered.err)
			continue
		}
		result[discovered.cli] = normalizeDiscoveredModels(discovered.models)
	}
	return result
}

func discoverModelsForCLI(ctx context.Context, cli, path string) ([]string, error) {
	switch cli {
	case "claude":
		return discoverClaudeModels(ctx, path)
	case "gemini":
		return discoverGeminiModels(ctx, path)
	case "codex":
		return discoverCodexModels(ctx, path)
	case "opencode":
		return discoverOpenCodeModels(ctx, path)
	default:
		return nil, fmt.Errorf("unsupported CLI %q", cli)
	}
}

func discoverClaudeModels(ctx context.Context, path string) ([]string, error) {
	workDir, cleanup, err := modelDiscoveryWorkDir()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	args := []string{
		"--print",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
	}
	// Without safe mode, merely initializing Claude can run configured hooks.
	// A CLI too old to support this read-only boundary is left discoverable via
	// the UI's free-text model field instead of being probed unsafely.
	if !cliHelpSupports(path, "--safe-mode") {
		return nil, errors.New("claude safe mode is unavailable")
	}
	args = append(args, "--safe-mode")
	if cliHelpSupports(path, "--no-session-persistence") {
		args = append(args, "--no-session-persistence")
	}

	request := map[string]any{
		"type":       "control_request",
		"request_id": "heimdallm-models",
		"request": map[string]any{
			"subtype": "list_models",
		},
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')

	cmd := newModelCommand(ctx, path, workDir, args...)
	cmd.Stdin = bytes.NewReader(payload)
	out := newCappedBuffer(maxModelOutputSize)
	cmd.Stdout = out
	runErr := procgroup.Run(cmd)
	models, parseErr := parseClaudeModels(out.Bytes())
	if parseErr == nil {
		return models, nil
	}
	if runErr != nil {
		return nil, fmt.Errorf("claude model discovery failed: %w", runErr)
	}
	return nil, parseErr
}

func parseClaudeModels(output []byte) ([]string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 64<<10), maxModelOutputSize)
	for scanner.Scan() {
		var message struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string `json:"request_id"`
				Response  struct {
					Models []struct {
						Value string `json:"value"`
					} `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil ||
			message.Type != "control_response" ||
			message.Response.RequestID != "heimdallm-models" {
			continue
		}
		models := make([]string, 0, len(message.Response.Response.Models))
		for _, model := range message.Response.Response.Models {
			models = append(models, model.Value)
		}
		return models, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read claude model response: %w", err)
	}
	return nil, errors.New("claude returned no model catalog")
}

func discoverCodexModels(ctx context.Context, path string) ([]string, error) {
	workDir, cleanup, err := modelDiscoveryWorkDir()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	client, err := startModelProtocol(ctx, path, workDir, "app-server")
	if err != nil {
		return nil, err
	}
	defer client.close()

	if err := client.send(map[string]any{
		"method": "initialize",
		"id":     0,
		"params": map[string]any{
			"clientInfo": map[string]string{
				"name": "heimdallm", "title": "Heimdallm", "version": "dev",
			},
		},
	}); err != nil {
		return nil, err
	}
	var initialized struct{}
	if err := client.response(0, &initialized); err != nil {
		return nil, err
	}
	if err := client.send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return nil, err
	}

	models := make([]string, 0, 32)
	cursor := ""
	seenCursors := map[string]struct{}{}
	for page, requestID := 0, 1; page < 10 && len(models) < maxDiscoveredModels; page, requestID = page+1, requestID+1 {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := client.send(map[string]any{"method": "model/list", "id": requestID, "params": params}); err != nil {
			return nil, err
		}
		var pageResult struct {
			Data []struct {
				Model string `json:"model"`
			} `json:"data"`
			NextCursor string `json:"nextCursor"`
		}
		if err := client.response(requestID, &pageResult); err != nil {
			return nil, err
		}
		for _, model := range pageResult.Data {
			models = append(models, model.Model)
		}
		cursor = strings.TrimSpace(pageResult.NextCursor)
		if cursor == "" {
			break
		}
		if _, repeated := seenCursors[cursor]; repeated {
			return nil, errors.New("codex returned a repeated model cursor")
		}
		seenCursors[cursor] = struct{}{}
	}
	return models, nil
}

func discoverGeminiModels(ctx context.Context, path string) ([]string, error) {
	workDir, cleanup, err := modelDiscoveryWorkDir()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	client, err := startModelProtocol(ctx, path, workDir, "--acp")
	if err != nil {
		return nil, err
	}
	defer client.close()

	if err := client.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      0,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": 1,
			"clientCapabilities": map[string]any{
				"auth":     map[string]bool{"terminal": false},
				"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
				"terminal": false,
			},
			"clientInfo": map[string]string{
				"name": "heimdallm", "title": "Heimdallm", "version": "dev",
			},
		},
	}); err != nil {
		return nil, err
	}
	var initialized struct{}
	if err := client.response(0, &initialized); err != nil {
		return nil, err
	}
	if err := client.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "session/new",
		"params": map[string]any{
			"cwd": workDir, "mcpServers": []any{},
		},
	}); err != nil {
		return nil, err
	}
	var session struct {
		Models struct {
			Available []struct {
				ModelID string `json:"modelId"`
			} `json:"availableModels"`
		} `json:"models"`
	}
	if err := client.response(1, &session); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(session.Models.Available))
	for _, model := range session.Models.Available {
		models = append(models, model.ModelID)
	}
	return models, nil
}

func discoverOpenCodeModels(ctx context.Context, path string) ([]string, error) {
	workDir, cleanup, err := modelDiscoveryWorkDir()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	cmd := newModelCommand(ctx, path, workDir, "models")
	cmd.Env = upsertModelEnv(cmd.Env, "NO_COLOR", "1")
	cmd.Env = upsertModelEnv(cmd.Env, "TERM", "dumb")
	out := newCappedBuffer(maxModelOutputSize)
	cmd.Stdout = out
	if err := procgroup.Run(cmd); err != nil {
		return nil, fmt.Errorf("opencode model discovery failed: %w", err)
	}
	models := make([]string, 0, 32)
	scanner := bufio.NewScanner(bytes.NewReader(out.Bytes()))
	for scanner.Scan() {
		model := strings.TrimSpace(scanner.Text())
		if strings.Contains(model, "/") {
			models = append(models, model)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return models, nil
}

type modelProtocol struct {
	stdin   io.WriteCloser
	encoder *json.Encoder
	scanner *bufio.Scanner
	process *procgroup.Process
}

func startModelProtocol(ctx context.Context, path, workDir string, args ...string) (*modelProtocol, error) {
	cmd := newModelCommand(ctx, path, workDir, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	process, err := procgroup.Start(cmd)
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), maxModelOutputSize)
	return &modelProtocol{
		stdin: stdin, encoder: json.NewEncoder(stdin), scanner: scanner, process: process,
	}, nil
}

func (client *modelProtocol) send(message any) error {
	if err := client.encoder.Encode(message); err != nil {
		return fmt.Errorf("write model protocol request: %w", err)
	}
	return nil
}

func (client *modelProtocol) response(id int, result any) error {
	wantID := strconv.Itoa(id)
	for client.scanner.Scan() {
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(client.scanner.Bytes(), &response) != nil ||
			strings.TrimSpace(string(response.ID)) != wantID {
			continue
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return fmt.Errorf("model protocol request %d failed", id)
		}
		if len(response.Result) == 0 {
			return fmt.Errorf("model protocol request %d returned no result", id)
		}
		if err := json.Unmarshal(response.Result, result); err != nil {
			return fmt.Errorf("decode model protocol response %d: %w", id, err)
		}
		return nil
	}
	if err := client.scanner.Err(); err != nil {
		return fmt.Errorf("read model protocol response: %w", err)
	}
	return io.ErrUnexpectedEOF
}

func (client *modelProtocol) close() {
	_ = client.stdin.Close()
	_ = client.process.Kill()
	_ = client.process.Wait()
}

func newModelCommand(ctx context.Context, path, workDir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = workDir
	cmd.Stderr = io.Discard
	cmd.Env = appendDirToPath(enrichEnvWithLoginPath(), filepath.Dir(path))
	return cmd
}

func modelDiscoveryWorkDir() (string, func(), error) {
	dir, err := os.MkdirTemp("", "heimdallm-models-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create model discovery directory: %w", err)
	}
	return dir, func() {
		if err := os.RemoveAll(dir); err != nil {
			slog.Debug("executor: remove model discovery directory", "err", err)
		}
	}, nil
}

func normalizeDiscoveredModels(models []string) []string {
	result := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || len(model) > maxModelIdentifierSize ||
			strings.HasPrefix(model, "-") || strings.IndexFunc(model, func(r rune) bool {
			return unicode.IsControl(r) || unicode.IsSpace(r)
		}) >= 0 {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
		if len(result) == maxDiscoveredModels {
			break
		}
	}
	return result
}

type cappedBuffer struct {
	bytes.Buffer
	remaining int
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{remaining: limit}
}

func (buffer *cappedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	if buffer.remaining > 0 {
		keep := len(data)
		if keep > buffer.remaining {
			keep = buffer.remaining
		}
		_, _ = buffer.Buffer.Write(data[:keep])
		buffer.remaining -= keep
	}
	return written, nil
}

func upsertModelEnv(env []string, key, value string) []string {
	prefix := key + "="
	result := append([]string(nil), env...)
	for index, entry := range result {
		if strings.HasPrefix(entry, prefix) {
			result[index] = prefix + value
			return result
		}
	}
	return append(result, prefix+value)
}
