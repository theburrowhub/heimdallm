package github

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
)

// mergeFingerprintSelection is everything a merge decision depends on that
// GitHub can change, reduced to scalars: if none of it moved, re-running the
// full merge-status query (reviews, threads, every check, branch protection)
// would produce the same decision. updatedAt covers reviews, comments,
// threads, labels and pushes; the rollup state and context count cover checks,
// which do not bump updatedAt; the base tip covers "behind base".
const mergeFingerprintSelection = `repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      state merged updatedAt headRefOid isDraft
      mergeable mergeStateStatus reviewDecision isInMergeQueue
      autoMergeRequest { enabledAt mergeMethod }
      baseRef { target { oid } }
      commits(last: 1) { nodes { commit { oid statusCheckRollup { state contexts { totalCount } } } } }
    }
  }`

// MergeFingerprints returns an opaque fingerprint per PR (keyed like
// PRKey) of the state its merge decision depends on, for every PR in refs, in
// batched queries. A PR missing from the result (an error, a deleted PR) must
// be evaluated in full.
func (c *Client) MergeFingerprints(refs []PRRef) map[string]string {
	out := make(map[string]string, len(refs))
	fields := make([]batchField, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		owner, name, err := splitRepo(ref.Repo)
		if err != nil || ref.Number <= 0 {
			continue
		}
		key := PRKey(ref.Repo, ref.Number)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		fields = append(fields, batchField{
			Key: key,
			Vars: []batchVar{
				{Name: "owner", Type: "String!", Value: owner},
				{Name: "name", Type: "String!", Value: name},
				{Name: "number", Type: "Int!", Value: ref.Number},
			},
			Body: mergeFingerprintSelection,
		})
	}
	if len(fields) == 0 {
		return out
	}
	results, failed, err := c.runBatch(graphQLOptions{Accept: mergeInfoPreviewAccept}, "", fields)
	if err != nil {
		slog.Warn("github: merge fingerprints unavailable, evaluating in full", "prs", len(fields), "err", err)
	}
	for _, f := range fields {
		if _, bad := failed[f.Key]; bad {
			continue
		}
		raw, ok := results[f.Key]
		if !ok || string(raw) == "null" {
			continue
		}
		// The selection is fixed and GitHub serialises fields in query
		// order, so the raw JSON is a stable representation of the state.
		sum := sha256.Sum256(raw)
		out[f.Key] = hex.EncodeToString(sum[:])
	}
	return out
}

// PRKey is the key batched PR lookups use for repo#number.
func PRKey(repo string, number int) string { return prKey(repo, number) }
