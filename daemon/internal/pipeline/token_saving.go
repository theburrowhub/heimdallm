package pipeline

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/store"
)

// TokenSaving is the resolved set of token-saving measures for one review
// (mirrors config.ResolvedTokenSaving; pipeline cannot import config). The
// zero value turns every measure off, which is the pre-feature behaviour.
type TokenSaving struct {
	IncrementalDiff bool
	FilterNoise     bool
	NoiseGlobs      []string
	CompactPrompt   bool
}

// CompareDiffFetcher is the optional capability incremental re-reviews need.
// ok=false means "not a clean linear range, use the full diff".
type CompareDiffFetcher interface {
	FetchCompareDiff(repo, base, head string) (diff string, ok bool, err error)
}

// HeadDiffFetcher is the optional capability to serve a PR diff from a cache
// keyed by the commits it spans, so a re-run on unchanged commits does not
// download the same diff again.
type HeadDiffFetcher interface {
	FetchDiffAt(repo string, number int, baseSHA, headSHA string) (string, error)
}

// Compact-prompt caps. The default path keeps maxCommentsBytes (16KB) and an
// uncapped re-review context.
const (
	compactCommentsBytes      = 8 * 1024
	compactCommentBodyBytes   = 600
	compactReviewContextBytes = 8 * 1024
)

// reviewDiff fetches the diff to review. With incremental_diff on, a re-review
// of a PR whose last reviewed commit is still an ancestor of HEAD gets only
// the changes since that commit; incrementalFrom then names it. Any doubt
// (force-push, merge commits, an API error, a peer instance's review whose
// findings we do not have) falls back to the full PR diff.
func (p *Pipeline) reviewDiff(pr *github.PullRequest, prevReview *store.Review, ts TokenSaving) (diff, incrementalFrom string, err error) {
	if ts.IncrementalDiff && prevReview != nil && prevReview.HeadSHA != "" &&
		prevReview.HeadSHA != pr.Head.SHA && prevReview.CLIUsed != "peer" {
		if fetcher, ok := p.gh.(CompareDiffFetcher); ok {
			d, linear, cmpErr := fetcher.FetchCompareDiff(pr.Repo, prevReview.HeadSHA, pr.Head.SHA)
			switch {
			case cmpErr != nil:
				slog.Warn("pipeline: incremental diff failed, using the full diff",
					"repo", pr.Repo, "pr", pr.Number, "err", cmpErr)
			case linear:
				diff, incrementalFrom = d, prevReview.HeadSHA
			default:
				slog.Info("pipeline: commits since last review are not a linear range, using the full diff",
					"repo", pr.Repo, "pr", pr.Number, "prev_head_sha", prevReview.HeadSHA)
			}
		}
	}
	if incrementalFrom == "" {
		if fetcher, ok := p.gh.(HeadDiffFetcher); ok {
			diff, err = fetcher.FetchDiffAt(pr.Repo, pr.Number, pr.Base.SHA, pr.Head.SHA)
		} else {
			diff, err = p.gh.FetchDiff(pr.Repo, pr.Number)
		}
		if err != nil {
			return "", "", err
		}
	}
	if ts.FilterNoise {
		globs := ts.NoiseGlobs
		if globs == nil {
			globs = DefaultNoiseGlobs
		}
		var omitted []string
		diff, omitted = FilterDiffNoise(diff, globs)
		if len(omitted) > 0 {
			slog.Info("pipeline: dropped noise files from the diff",
				"repo", pr.Repo, "pr", pr.Number, "files", len(omitted))
		}
	}
	return diff, incrementalFrom, nil
}

// checkoutNote warns an agent that explores a repository checkout that the
// checkout is not the PR head: it is the operator's local_dir as it happens to
// be, or the tip of the default branch (#894). Without it an agent reports what
// the PR adds as missing, or reviews the old version of a changed file. The
// "missing" exception names the partial diffs (incremental, truncated, noise
// filtered): each says so in the prompt, and absence from it proves nothing.
const checkoutNote = "The repository checkout in your working directory is context only and may not contain this PR's changes " +
	"(it can be the default branch or an unrelated local branch). The diff is the authoritative version of the changed code: " +
	"do not report a file, function or import as missing because the checkout lacks it. It is missing only if the diff lacks it too " +
	"and the diff is the whole PR: an incremental or truncated diff, or one with files marked not shown, can leave out where the PR defines it.\n"

// reviewHasCheckout reports whether any agent this review may run explores a
// repository checkout: it has a working directory and is not one of the agents
// the executor always runs in an empty workspace (custom agents, Cursor).
func reviewHasCheckout(opts RunOptions) bool {
	if opts.ExecOpts.WorkDir != "" {
		if opts.Primary == "" && opts.Fallback == "" {
			return true // the agent is detected later; assume it explores
		}
		for _, cli := range []string{opts.Primary, opts.Fallback} {
			if cli != "" && executor.ExploresCheckout(cli) {
				return true
			}
		}
	}
	for cli, o := range opts.AgentExecOpts {
		if o.WorkDir != "" && executor.ExploresCheckout(cli) {
			return true
		}
	}
	return false
}

// incrementalNote tells the model the diff is partial, so it neither flags
// "missing" code from earlier commits nor drops still-valid findings.
func incrementalNote(fromSHA string) string {
	short := fromSHA
	if len(short) > 12 {
		short = short[:12]
	}
	return fmt.Sprintf("The diff shows ONLY the commits pushed since your last review (%s). "+
		"Code outside it was already reviewed: keep a previous finding only if this diff "+
		"does not touch it or does not fix it.\n", short)
}

// truncateText cuts s to at most max bytes on a rune boundary, marking the cut.
func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " …(truncated)"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// formatCommentsCompact is the compact_prompt counterpart of formatComments:
// it leaves out the bot's own comments (its previous reviews are summarised in
// the re-review context), comments already listed in that context (made after
// the last review), trims each body and caps the section at half the size.
func formatCommentsCompact(comments []github.Comment, botLogin string, inContextAfter time.Time) string {
	var kept []github.Comment
	for _, c := range comments {
		if botLogin != "" && strings.EqualFold(c.Author, botLogin) {
			continue
		}
		if !inContextAfter.IsZero() && c.CreatedAt.After(inContextAfter) {
			continue
		}
		c.Body = truncateText(strings.TrimSpace(c.Body), compactCommentBodyBytes)
		kept = append(kept, c)
	}
	if len(kept) == 0 {
		return ""
	}
	lines := make([]string, len(kept))
	for i, c := range kept {
		if c.File != "" {
			lines[i] = fmt.Sprintf("@%s (%s:%d): %s", c.Author, c.File, c.Line, c.Body)
		} else {
			lines[i] = fmt.Sprintf("@%s: %s", c.Author, c.Body)
		}
	}
	// Newest discussion matters most: drop from the front until it fits.
	for len(lines) > 1 && len(strings.Join(lines, "\n---\n")) > compactCommentsBytes {
		lines = lines[1:]
	}
	return wrapCommentsSection(truncateText(strings.Join(lines, "\n---\n"), compactCommentsBytes))
}
