import '../../core/models/config_model.dart';

/// Computes the PATCH body diff between the previously-saved [old] repo
/// config and the [updated] one currently held by the repo detail screen.
///
/// Lifted out of `RepoDetailScreen` (was `_computeRepoDiff`) so it can be
/// unit-tested directly without needing to mount the widget.
Map<String, dynamic> computeRepoDiff(RepoConfig old, RepoConfig updated) {
  final diff = <String, dynamic>{};
  if (old.aiPrimary != updated.aiPrimary) {
    diff['primary'] = updated.aiPrimary ?? '';
  }
  if (old.aiFallback != updated.aiFallback) {
    diff['fallback'] = updated.aiFallback ?? '';
  }
  if (old.reviewMode != updated.reviewMode) {
    diff['review_mode'] = updated.reviewMode ?? '';
  }
  if (old.promptId != updated.promptId) {
    diff['prompt'] = updated.promptId ?? '';
  }
  if (old.localDir != updated.localDir) {
    diff['local_dir'] = updated.localDir ?? '';
  }
  if (old.prAssignee != updated.prAssignee) {
    diff['pr_assignee'] = updated.prAssignee ?? '';
  }
  if (old.prDraft != updated.prDraft && updated.prDraft != null) {
    diff['pr_draft'] = updated.prDraft!;
  }
  if (old.neverApproveWithIssues != updated.neverApproveWithIssues &&
      updated.neverApproveWithIssues != null) {
    diff['never_approve_with_issues'] = updated.neverApproveWithIssues!;
  }
  if (old.developPromptId != updated.developPromptId) {
    diff['implement_prompt'] = updated.developPromptId ?? '';
  }
  if (old.issuePromptId != updated.issuePromptId) {
    diff['issue_prompt'] = updated.issuePromptId ?? '';
  }

  if (!_listsEqual(old.prReviewers, updated.prReviewers)) {
    diff['pr_reviewers'] = updated.prReviewers ?? <String>[];
  }
  if (!_listsEqual(old.prLabels, updated.prLabels)) {
    diff['pr_labels'] = updated.prLabels ?? <String>[];
  }

  final itDiff = <String, dynamic>{};
  if (old.itEnabled != updated.itEnabled && updated.itEnabled != null) {
    itDiff['enabled'] = updated.itEnabled!;
  }
  if (old.devEnabled != updated.devEnabled && updated.devEnabled != null) {
    itDiff['develop_enabled'] = updated.devEnabled!;
  }
  if (old.issueFilterMode != updated.issueFilterMode) {
    itDiff['filter_mode'] = updated.issueFilterMode ?? '';
  }
  if (old.issueDefaultAction != updated.issueDefaultAction) {
    itDiff['default_action'] = updated.issueDefaultAction ?? '';
  }
  if (!_listsEqual(old.reviewOnlyLabels, updated.reviewOnlyLabels)) {
    itDiff['review_only_labels'] = updated.reviewOnlyLabels ?? <String>[];
  }
  if (!_listsEqual(old.refinementLabels, updated.refinementLabels)) {
    itDiff['refinement_labels'] = updated.refinementLabels ?? <String>[];
  }
  if (!_listsEqual(old.skipLabels, updated.skipLabels)) {
    itDiff['skip_labels'] = updated.skipLabels ?? <String>[];
  }
  if (!_listsEqual(old.developLabels, updated.developLabels)) {
    itDiff['develop_labels'] = updated.developLabels ?? <String>[];
  }
  if (!_listsEqual(old.issueAssignees, updated.issueAssignees)) {
    itDiff['assignees'] = updated.issueAssignees ?? <String>[];
  }

  if (itDiff.isNotEmpty) diff['issue_tracking'] = itDiff;
  return diff;
}

bool _listsEqual(List<String>? a, List<String>? b) {
  if (a == null && b == null) return true;
  if (a == null || b == null) return false;
  if (a.length != b.length) return false;
  for (var i = 0; i < a.length; i++) {
    if (a[i] != b[i]) return false;
  }
  return true;
}
