package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// defaultGraphQLBatchSize bounds how many aliased fields one batched query
// carries. GitHub charges a query by the connection nodes it may return, not
// by its top-level fields, so a batch of scalar-only PR lookups stays at one
// or two points; the bound keeps a single response well inside
// maxBatchBodyBytes and keeps one slow repository from delaying hundreds.
const defaultGraphQLBatchSize = 50

// maxBatchBodyBytes is the response ceiling for one batched query. Fifty PR
// snapshots with their review requests are a few hundred KiB; the generic
// 1 MiB GraphQL ceiling would be cut short by long titles in a full chunk.
const maxBatchBodyBytes = 8 * 1024 * 1024

// batchVar is one variable of a batched field. Name is local to the field
// ("owner"); the executor renames it per alias ("owner_3") so every field of
// the batch can carry its own values without string-building GraphQL
// literals — values always travel as variables, never inside the query text.
type batchVar struct {
	Name  string
	Type  string // GraphQL type, e.g. "String!" or "Int!"
	Value any
}

// batchField is one aliased top-level selection of a batched query.
type batchField struct {
	// Key identifies the item to the caller; results and failures are keyed
	// by it. Aliases are derived from the field's position, so Key may hold
	// any text (an "owner/repo#12" PR key, a login).
	Key string
	// Vars are the variables Body references as $Name.
	Vars []batchVar
	// Body is the selection, e.g.
	// `repository(owner:$owner, name:$name) { nameWithOwner }`.
	Body string
}

// BatchItemError reports that one aliased field of a batched query failed
// while the rest of the batch succeeded.
type BatchItemError struct {
	Key      string
	Types    []string
	Messages []string
}

func (e *BatchItemError) Error() string {
	return fmt.Sprintf("github: graphql batch: %s: %s", e.Key, strings.Join(e.Messages, "; "))
}

// NotFound reports whether every error GitHub returned for the field was a
// NOT_FOUND (a deleted or inaccessible PR or repository).
func (e *BatchItemError) NotFound() bool {
	if e == nil || len(e.Types) == 0 {
		return false
	}
	for _, t := range e.Types {
		if t != "NOT_FOUND" {
			return false
		}
	}
	return true
}

var batchVarRef = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)

// SetGraphQLBatchSize overrides how many items one batched GraphQL query
// carries. Values below one restore the default.
func (c *Client) SetGraphQLBatchSize(n int) {
	c.batchSize.Store(int64(n))
}

func (c *Client) graphQLBatchSize() int {
	if n := int(c.batchSize.Load()); n > 0 {
		return n
	}
	return defaultGraphQLBatchSize
}

// runBatch executes fields as aliased top-level selections, chunked so no
// query carries more than graphQLBatchSize fields. fragments is appended to
// every chunk verbatim (fragment definitions shared by the bodies).
//
// It returns each field's raw result keyed by Key — JSON null when GitHub
// resolved the field to nothing — and a failure per Key for fields GitHub
// answered with an error, so one deleted PR never sinks the other 49. A chunk
// that fails as a whole (transport error, non-200, an error with no path)
// marks every key of that chunk as failed with the chunk's error, letting the
// caller fall back per item. A rate-limit error or a refused GraphQL gate
// stops the whole batch and is returned as err: the remaining chunks would
// fail the same way, and sending them would only prolong a block.
func (c *Client) runBatch(opts graphQLOptions, fragments string, fields []batchField) (map[string]json.RawMessage, map[string]error, error) {
	results := make(map[string]json.RawMessage, len(fields))
	failed := make(map[string]error)
	if len(fields) == 0 {
		return results, failed, nil
	}
	if opts.MaxBodyBytes == 0 {
		opts.MaxBodyBytes = maxBatchBodyBytes
	}
	size := c.graphQLBatchSize()
	for start := 0; start < len(fields); start += size {
		end := start + size
		if end > len(fields) {
			end = len(fields)
		}
		chunk := fields[start:end]
		if err := c.acquireGraphQL(); err != nil {
			return results, failed, fmt.Errorf("github: graphql batch: acquire: %w", err)
		}
		query, vars := buildBatchQuery(chunk, fragments)
		data, gqlErrs, err := c.graphQLRaw(opts, query, vars)
		if err != nil {
			var rl *RateLimitError
			if errors.As(err, &rl) {
				return results, failed, err
			}
			for _, f := range chunk {
				failed[f.Key] = err
			}
			continue
		}
		perAlias := make(map[string]*BatchItemError)
		var whole []string
		for _, e := range gqlErrs {
			alias := ""
			if len(e.Path) > 0 {
				alias, _ = e.Path[0].(string)
			}
			idx, ok := batchAliasIndex(alias, len(chunk))
			if !ok {
				whole = append(whole, e.Message)
				continue
			}
			key := chunk[idx].Key
			be := perAlias[key]
			if be == nil {
				be = &BatchItemError{Key: key}
				perAlias[key] = be
			}
			be.Types = append(be.Types, strings.ToUpper(e.Type))
			be.Messages = append(be.Messages, e.Message)
		}
		if len(whole) > 0 || data == nil {
			msg := strings.Join(whole, "; ")
			if msg == "" {
				msg = "empty data"
			}
			chunkErr := fmt.Errorf("github: graphql batch: errors: %s", msg)
			for _, f := range chunk {
				failed[f.Key] = chunkErr
			}
			continue
		}
		var byAlias map[string]json.RawMessage
		if err := json.Unmarshal(data, &byAlias); err != nil {
			chunkErr := fmt.Errorf("github: graphql batch: decode data: %w", err)
			for _, f := range chunk {
				failed[f.Key] = chunkErr
			}
			continue
		}
		for i, f := range chunk {
			if be, bad := perAlias[f.Key]; bad {
				failed[f.Key] = be
				continue
			}
			raw, ok := byAlias[batchAlias(i)]
			if !ok {
				raw = json.RawMessage("null")
			}
			results[f.Key] = raw
		}
	}
	return results, failed, nil
}

func batchAlias(i int) string { return fmt.Sprintf("b%d", i) }

func batchAliasIndex(alias string, n int) (int, bool) {
	if len(alias) < 2 || alias[0] != 'b' {
		return 0, false
	}
	idx := 0
	for _, r := range alias[1:] {
		if r < '0' || r > '9' {
			return 0, false
		}
		idx = idx*10 + int(r-'0')
		if idx >= n {
			return 0, false
		}
	}
	return idx, true
}

// buildBatchQuery renders one chunk: every field aliased b<i>, with its
// variables renamed <name>_<i> in both the declarations and the body.
func buildBatchQuery(chunk []batchField, fragments string) (string, map[string]any) {
	vars := make(map[string]any)
	var decls []string
	var body strings.Builder
	for i, f := range chunk {
		local := make(map[string]string, len(f.Vars))
		for _, v := range f.Vars {
			name := fmt.Sprintf("%s_%d", v.Name, i)
			local[v.Name] = name
			decls = append(decls, "$"+name+": "+v.Type)
			vars[name] = v.Value
		}
		renamed := batchVarRef.ReplaceAllStringFunc(f.Body, func(ref string) string {
			if name, ok := local[ref[1:]]; ok {
				return "$" + name
			}
			return ref
		})
		body.WriteString(batchAlias(i))
		body.WriteString(": ")
		body.WriteString(renamed)
		body.WriteString("\n")
	}
	var q strings.Builder
	q.WriteString("query")
	if len(decls) > 0 {
		q.WriteString("(")
		q.WriteString(strings.Join(decls, ", "))
		q.WriteString(")")
	}
	q.WriteString(" {\n")
	q.WriteString(body.String())
	q.WriteString("}\n")
	q.WriteString(fragments)
	return q.String(), vars
}
