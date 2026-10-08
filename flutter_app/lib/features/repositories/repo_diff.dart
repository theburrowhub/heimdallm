import '../../core/models/config_model.dart';

/// Computes the PATCH body diff between the previously-saved [old] repo
/// config and the [updated] one currently held by the repo detail screen.
///
/// Lifted out of `RepoDetailScreen` (was `_computeRepoDiff`) so it can be
/// unit-tested directly without needing to mount the widget.
///
/// Note the intentional asymmetry: string overrides emit '' to clear, but
/// bool overrides only emit when non-null — clearing a bool override is done
/// via _resetField (DELETE), never by diffing a null back into the PATCH body.
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
  if (old.flow != updated.flow) {
    diff['flow'] = updated.flow ?? '';
  }
  if (old.promptId != updated.promptId) {
    diff['prompt'] = updated.promptId ?? '';
  }
  if (old.localDir != updated.localDir) {
    diff['local_dir'] = updated.localDir ?? '';
  }
  if (old.cloneDir != updated.cloneDir) {
    diff['clone_dir'] = updated.cloneDir ?? '';
  }
  if (old.neverApproveWithIssues != updated.neverApproveWithIssues &&
      updated.neverApproveWithIssues != null) {
    diff['never_approve_with_issues'] = updated.neverApproveWithIssues!;
  }
  if (old.neverApproveMinSeverity != updated.neverApproveMinSeverity) {
    diff['never_approve_min_severity'] = updated.neverApproveMinSeverity ?? '';
  }
  // Removing a budget goes through DELETE (review_limits); only a present
  // budget is patched.
  if (old.reviewLimits != updated.reviewLimits &&
      updated.reviewLimits != null) {
    diff['review_limits'] = updated.reviewLimits!.toJson();
  }
  final tokenSaving = diffTokenSavingOverride(old.tokenSaving, updated.tokenSaving);
  if (tokenSaving.isNotEmpty) diff['token_saving'] = tokenSaving;
  return diff;
}
