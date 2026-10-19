package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type batchRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// batchServer answers every GraphQL POST with respond(request) and counts them.
func batchServer(t *testing.T, respond func(batchRequest) (int, any)) (*Client, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		var req batchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		status, body := respond(req)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return NewClient("tok", WithBaseURL(srv.URL)), &calls
}

func repoField(repo string) batchField {
	owner, name, _ := splitRepo(repo)
	return batchField{
		Key: repo,
		Vars: []batchVar{
			{Name: "owner", Type: "String!", Value: owner},
			{Name: "name", Type: "String!", Value: name},
		},
		Body: `repository(owner: $owner, name: $name) { nameWithOwner }`,
	}
}

// echoData answers each alias with the owner/name variables it was sent.
func echoData(req batchRequest) map[string]any {
	data := map[string]any{}
	for i := 0; ; i++ {
		owner, ok := req.Variables[fmt.Sprintf("owner_%d", i)]
		if !ok {
			break
		}
		data[fmt.Sprintf("b%d", i)] = map[string]any{
			"nameWithOwner": fmt.Sprintf("%v/%v", owner, req.Variables[fmt.Sprintf("name_%d", i)]),
		}
	}
	return data
}

func TestBuildBatchQuery_RenamesVariablesPerAlias(t *testing.T) {
	q, vars := buildBatchQuery([]batchField{repoField("a/x"), repoField("b/y")}, "fragment F on X { id }\n")
	for _, want := range []string{
		"$owner_0: String!", "$name_1: String!",
		"b0: repository(owner: $owner_0, name: $name_0)",
		"b1: repository(owner: $owner_1, name: $name_1)",
		"fragment F on X",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q:\n%s", want, q)
		}
	}
	if vars["owner_1"] != "b" || vars["name_0"] != "x" {
		t.Fatalf("vars = %v", vars)
	}
}

func TestBuildBatchQuery_NoVariables(t *testing.T) {
	q, vars := buildBatchQuery([]batchField{{Key: "v", Body: "viewer { login }"}}, "")
	if !strings.HasPrefix(q, "query {") || len(vars) != 0 {
		t.Fatalf("q=%q vars=%v", q, vars)
	}
}

func TestRunBatch_ChunksAndKeysResults(t *testing.T) {
	c, calls := batchServer(t, func(req batchRequest) (int, any) {
		return 200, map[string]any{"data": echoData(req)}
	})
	c.SetGraphQLBatchSize(2)
	var gates int
	c.SetGraphQLGate(func() error { gates++; return nil })

	repos := []string{"o/a", "o/b", "o/c", "o/d", "o/e"}
	var fields []batchField
	for _, r := range repos {
		fields = append(fields, repoField(r))
	}
	res, failed, err := c.runBatch(graphQLOptions{}, "", fields)
	if err != nil || len(failed) != 0 {
		t.Fatalf("err=%v failed=%v", err, failed)
	}
	if calls.Load() != 3 || gates != 3 {
		t.Fatalf("calls=%d gates=%d, want 3 chunks", calls.Load(), gates)
	}
	for _, r := range repos {
		var got struct{ NameWithOwner string }
		if err := json.Unmarshal(res[r], &got); err != nil || got.NameWithOwner != r {
			t.Errorf("%s: got %s (%v)", r, res[r], err)
		}
	}
}

func TestRunBatch_PerAliasErrorDoesNotSinkBatch(t *testing.T) {
	c, _ := batchServer(t, func(req batchRequest) (int, any) {
		data := echoData(req)
		data["b1"] = nil
		return 200, map[string]any{
			"data":   data,
			"errors": []map[string]any{{"type": "NOT_FOUND", "message": "gone", "path": []any{"b1"}}},
		}
	})
	res, failed, err := c.runBatch(graphQLOptions{}, "", []batchField{repoField("o/a"), repoField("o/gone"), repoField("o/c")})
	if err != nil {
		t.Fatal(err)
	}
	var be *BatchItemError
	if !errors.As(failed["o/gone"], &be) || !be.NotFound() {
		t.Fatalf("failed = %v", failed)
	}
	if _, ok := res["o/a"]; !ok {
		t.Fatal("o/a missing")
	}
	if _, ok := res["o/c"]; !ok {
		t.Fatal("o/c missing")
	}
	if _, ok := res["o/gone"]; ok {
		t.Fatal("failed key must not appear in results")
	}
}

func TestRunBatch_MissingAliasIsNull(t *testing.T) {
	c, _ := batchServer(t, func(req batchRequest) (int, any) {
		return 200, map[string]any{"data": map[string]any{"b0": nil}}
	})
	res, failed, err := c.runBatch(graphQLOptions{}, "", []batchField{repoField("o/a"), repoField("o/b")})
	if err != nil || len(failed) != 0 {
		t.Fatalf("err=%v failed=%v", err, failed)
	}
	if string(res["o/a"]) != "null" || string(res["o/b"]) != "null" {
		t.Fatalf("res = %v", res)
	}
}

func TestRunBatch_WholeChunkFailureMarksEveryKey(t *testing.T) {
	c, _ := batchServer(t, func(batchRequest) (int, any) {
		return 200, map[string]any{"data": nil, "errors": []map[string]any{{"message": "parse error"}}}
	})
	_, failed, err := c.runBatch(graphQLOptions{}, "", []batchField{repoField("o/a"), repoField("o/b")})
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 2 || !strings.Contains(failed["o/a"].Error(), "parse error") {
		t.Fatalf("failed = %v", failed)
	}
}

func TestRunBatch_HTTPFailureMarksChunkAndContinues(t *testing.T) {
	var n atomic.Int64
	c, _ := batchServer(t, func(req batchRequest) (int, any) {
		if n.Add(1) == 1 {
			return 502, map[string]any{"message": "bad gateway"}
		}
		return 200, map[string]any{"data": echoData(req)}
	})
	c.SetGraphQLBatchSize(1)
	res, failed, err := c.runBatch(graphQLOptions{}, "", []batchField{repoField("o/a"), repoField("o/b")})
	if err != nil {
		t.Fatal(err)
	}
	if _, bad := failed["o/a"]; !bad {
		t.Fatalf("o/a should fail: %v", failed)
	}
	if _, ok := res["o/b"]; !ok {
		t.Fatal("second chunk should still run")
	}
}

func TestRunBatch_UndecodableDataFailsChunk(t *testing.T) {
	c, _ := batchServer(t, func(batchRequest) (int, any) {
		return 200, map[string]any{"data": []int{1}}
	})
	_, failed, err := c.runBatch(graphQLOptions{}, "", []batchField{repoField("o/a")})
	if err != nil || failed["o/a"] == nil {
		t.Fatalf("err=%v failed=%v", err, failed)
	}
}

func TestRunBatch_RateLimitStopsBatch(t *testing.T) {
	c, calls := batchServer(t, func(batchRequest) (int, any) {
		return 200, map[string]any{"data": map[string]any{}}
	})
	c.SetGraphQLBatchSize(1)
	c.pauseRateLimit(30 * 1e9)
	_, _, err := c.runBatch(graphQLOptions{}, "", []batchField{repoField("o/a"), repoField("o/b")})
	var rl *RateLimitError
	if !errors.As(err, &rl) || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestRunBatch_GateRefusalStopsBatch(t *testing.T) {
	c, calls := batchServer(t, func(batchRequest) (int, any) {
		return 200, map[string]any{"data": map[string]any{}}
	})
	c.SetGraphQLGate(func() error { return errors.New("no budget") })
	_, _, err := c.runBatch(graphQLOptions{}, "", []batchField{repoField("o/a")})
	if err == nil || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestRunBatch_Empty(t *testing.T) {
	c := NewClient("tok")
	res, failed, err := c.runBatch(graphQLOptions{}, "", nil)
	if err != nil || len(res) != 0 || len(failed) != 0 {
		t.Fatal("empty batch must be a no-op")
	}
}

func TestBatchAliasIndex(t *testing.T) {
	cases := []struct {
		alias string
		n     int
		idx   int
		ok    bool
	}{
		{"b0", 1, 0, true}, {"b12", 13, 12, true}, {"b12", 12, 0, false},
		{"x1", 5, 0, false}, {"b", 5, 0, false}, {"b1a", 5, 0, false}, {"", 5, 0, false},
	}
	for _, tc := range cases {
		idx, ok := batchAliasIndex(tc.alias, tc.n)
		if idx != tc.idx || ok != tc.ok {
			t.Errorf("batchAliasIndex(%q,%d) = %d,%v", tc.alias, tc.n, idx, ok)
		}
	}
}

func TestBatchItemError(t *testing.T) {
	var nilErr *BatchItemError
	if nilErr.NotFound() {
		t.Fatal("nil is not NotFound")
	}
	e := &BatchItemError{Key: "k", Types: []string{"NOT_FOUND", "FORBIDDEN"}, Messages: []string{"a", "b"}}
	if e.NotFound() || !strings.Contains(e.Error(), "k: a; b") {
		t.Fatalf("got %v / %q", e.NotFound(), e.Error())
	}
	if (&BatchItemError{}).NotFound() {
		t.Fatal("no types is not NotFound")
	}
}

func TestGraphQLBatchSizeDefault(t *testing.T) {
	c := NewClient("tok")
	if c.graphQLBatchSize() != defaultGraphQLBatchSize {
		t.Fatal("default")
	}
	c.SetGraphQLBatchSize(7)
	if c.graphQLBatchSize() != 7 {
		t.Fatal("override")
	}
	c.SetGraphQLBatchSize(0)
	if c.graphQLBatchSize() != defaultGraphQLBatchSize {
		t.Fatal("reset")
	}
}
