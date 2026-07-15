import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/api/sse_client.dart';
import '../../core/models/config_model.dart';
import '../../core/platform/platform_services_provider.dart';
import '../dashboard/dashboard_providers.dart';

final daemonHealthProvider = FutureProvider<bool>((ref) async {
  final api = ref.watch(apiClientProvider);
  return api.checkHealth();
});

final configProvider = FutureProvider<AppConfig>((ref) async {
  final api = ref.watch(apiClientProvider);
  final json = await api.fetchConfig();
  return AppConfig.fromJson(json);
});

class ConfigNotifier extends AsyncNotifier<AppConfig> {
  AppConfig? _serverConfig;
  Future<void> _operationQueue = Future<void>.value();
  int _mutationGeneration = 0;
  int _serverGeneration = 0;
  final Map<String, dynamic> _pendingSaveDiff = {};
  final Map<String, RepoConfig?> _pendingRepoMembershipChanges = {};

  @override
  Future<AppConfig> build() async {
    final api = ref.watch(apiClientProvider);
    final json = await api.fetchConfig();
    final config = AppConfig.fromJson(json);
    _serverConfig = config;
    _serverGeneration++;
    _clearPendingSaveIntent();
    return config;
  }

  /// Replaces local state with fresh config from the daemon. Called after
  /// PATCH/DELETE endpoints return the full config.
  void updateFromServer(Map<String, dynamic> json) {
    final config = AppConfig.fromJson(json);
    _serverConfig = config;
    _serverGeneration++;
    _mutationGeneration++;
    _clearPendingSaveIntent();
    state = AsyncValue.data(config);
  }

  /// Save global config changes by computing the diff and sending only
  /// changed fields to the daemon via PATCH.
  /// Optimistic: updates UI state immediately, sends PATCH in background,
  /// then reconciles with the daemon's authoritative response.
  Future<void> save(AppConfig updated) {
    final current = state.value;
    if (current == null) return Future<void>.value();
    _serverConfig ??= current;

    final api = ref.read(apiClientProvider);
    _mergeConfigDiff(_pendingSaveDiff, _computeGlobalDiff(current, updated));
    _pendingRepoMembershipChanges.addAll(
      _computeRepoMembershipChanges(current, updated),
    );
    final requestedDiff = _copyConfigDiff(_pendingSaveDiff);
    final repoMembershipChanges = Map<String, RepoConfig?>.from(
      _pendingRepoMembershipChanges,
    );
    final generation = ++_mutationGeneration;
    // Optimistic update — UI reflects the change immediately.
    state = AsyncValue.data(updated);

    return _enqueue(() async {
      final baseline = _serverConfig;
      if (baseline == null) return;
      final serverGeneration = _serverGeneration;
      final diff = _mergeRepoMembershipChanges(
        requestedDiff,
        baseline,
        repoMembershipChanges,
      );
      if (diff.isEmpty) {
        _clearPendingSaveIntentIfLatest(generation);
        return;
      }

      final freshJson = await api.patchConfig(diff);
      final fresh = AppConfig.fromJson(freshJson);
      if (serverGeneration != _serverGeneration) return;
      _serverConfig = fresh;
      _serverGeneration++;
      _clearPendingSaveIntentIfLatest(generation);
      if (ref.mounted && generation == _mutationGeneration) {
        state = AsyncValue.data(fresh);
      }
    });
  }

  /// Fetches the daemon's latest config. Refreshes share the same queue as
  /// saves so a discovery event cannot race an in-flight PATCH.
  Future<void> refresh() {
    final api = ref.read(apiClientProvider);
    final generation = _mutationGeneration;
    return _enqueue(() async {
      final serverGeneration = _serverGeneration;
      final json = await api.fetchConfig();
      final fresh = AppConfig.fromJson(json);
      if (serverGeneration != _serverGeneration) return;
      _serverConfig = fresh;
      _serverGeneration++;
      if (ref.mounted && generation == _mutationGeneration) {
        _clearPendingSaveIntent();
        state = AsyncValue.data(fresh);
      }
    });
  }

  Future<T> _enqueue<T>(Future<T> Function() operation) {
    final completer = Completer<T>();
    _operationQueue = _operationQueue.then((_) async {
      try {
        completer.complete(await operation());
      } catch (error, stackTrace) {
        completer.completeError(error, stackTrace);
      }
    });
    return completer.future;
  }

  void _clearPendingSaveIntentIfLatest(int generation) {
    if (generation == _mutationGeneration) _clearPendingSaveIntent();
  }

  void _clearPendingSaveIntent() {
    _pendingSaveDiff.clear();
    _pendingRepoMembershipChanges.clear();
  }

  /// First-run setup: write config file to disk, store token in Keychain,
  /// then launch the daemon binary and wait for it to become healthy.
  Future<void> saveAndStartDaemon({
    required String token,
    required AppConfig config,
    required String daemonBinaryPath,
  }) async {
    _mutationGeneration++;
    _serverGeneration++;
    _clearPendingSaveIntent();
    state = const AsyncLoading();
    state = await AsyncValue.guard(() async {
      final platform = ref.read(platformServicesProvider);
      // 1. Store token
      await platform.storeGitHubToken(token);
      // Invalidate the cached token so the ApiClient re-reads it.
      ref.read(apiClientProvider).clearTokenCache();

      // 2. Write config
      await platform.writeDaemonConfig(config);

      // 3. Launch daemon
      await platform.spawnDaemon(daemonBinaryPath);

      // 4. Wait up to 8 seconds for the daemon to become healthy
      final api = ref.read(apiClientProvider);
      for (var i = 0; i < 80; i++) {
        await Future.delayed(const Duration(milliseconds: 100));
        if (await api.checkHealth()) break;
      }
      if (!await api.checkHealth()) {
        throw Exception(
          'Heimdallm could not start. Check the app installation.',
        );
      }
      ref.invalidate(daemonHealthProvider);
      return config;
    });
    if (state case AsyncData(value: final savedConfig)) {
      _serverConfig = savedConfig;
    }
  }
}

final configNotifierProvider = AsyncNotifierProvider<ConfigNotifier, AppConfig>(
  ConfigNotifier.new,
);

/// Refreshes repository config after daemon events that change repository
/// membership or identity. This provider is watched by ReposScreen so its SSE
/// subscription exists only while the repository UI is mounted.
class ConfigRefreshNotifier extends Notifier<int> {
  @override
  int build() {
    ref.listen<AsyncValue<SseEvent>>(sseStreamProvider, (_, next) {
      next.whenData((event) {
        if (event.type != 'repo_discovered' && event.type != 'repo_renamed') {
          return;
        }
        state++;
        unawaited(_refreshAfterInitialLoad());
      });
    });
    return 0;
  }

  Future<void> _refreshAfterInitialLoad() async {
    try {
      await ref.read(configNotifierProvider.future);
      if (!ref.mounted) return;
      await ref.read(configNotifierProvider.notifier).refresh();
    } catch (_) {}
  }
}

final configRefreshProvider =
    NotifierProvider.autoDispose<ConfigRefreshNotifier, int>(
      ConfigRefreshNotifier.new,
    );

void _mergeConfigDiff(
  Map<String, dynamic> target,
  Map<String, dynamic> update,
) {
  for (final entry in update.entries) {
    final current = target[entry.key];
    final next = entry.value;
    if (current is Map<String, dynamic> && next is Map<String, dynamic>) {
      _mergeConfigDiff(current, next);
    } else {
      target[entry.key] = _copyConfigValue(next);
    }
  }
}

Map<String, dynamic> _copyConfigDiff(Map<String, dynamic> source) => {
  for (final entry in source.entries) entry.key: _copyConfigValue(entry.value),
};

dynamic _copyConfigValue(dynamic value) {
  if (value is Map<String, dynamic>) return _copyConfigDiff(value);
  if (value is List) return List<dynamic>.from(value);
  return value;
}

Map<String, RepoConfig?> _computeRepoMembershipChanges(
  AppConfig old,
  AppConfig updated,
) {
  final changes = <String, RepoConfig?>{};
  final allRepos = {...old.repoConfigs.keys, ...updated.repoConfigs.keys};
  for (final repo in allRepos) {
    final oldConfig = old.repoConfigs[repo];
    final updatedConfig = updated.repoConfigs[repo];
    if (oldConfig?.isMonitored != updatedConfig?.isMonitored) {
      changes[repo] = updatedConfig;
    }
  }
  return changes;
}

/// Repository membership is encoded as two aggregate arrays in the daemon
/// config. Rebase per-repo user changes onto the latest server snapshot so an
/// unrelated discovery or rename is not removed by a stale save snapshot.
Map<String, dynamic> _mergeRepoMembershipChanges(
  Map<String, dynamic> requestedDiff,
  AppConfig baseline,
  Map<String, RepoConfig?> changes,
) {
  if (changes.isEmpty) return requestedDiff;

  final mergedRepos = Map<String, RepoConfig>.from(baseline.repoConfigs);
  for (final entry in changes.entries) {
    final config = entry.value;
    if (config == null) {
      mergedRepos.remove(entry.key);
    } else {
      mergedRepos[entry.key] = config;
    }
  }

  final membershipDiff = _computeGlobalDiff(
    baseline,
    baseline.copyWith(repoConfigs: mergedRepos),
  );
  final membershipGithub = membershipDiff['github'] as Map<String, dynamic>?;
  final github =
      requestedDiff['github'] as Map<String, dynamic>? ?? <String, dynamic>{};
  github.remove('repositories');
  github.remove('non_monitored');
  if (membershipGithub != null) {
    if (membershipGithub.containsKey('repositories')) {
      github['repositories'] = membershipGithub['repositories'];
    }
    if (membershipGithub.containsKey('non_monitored')) {
      github['non_monitored'] = membershipGithub['non_monitored'];
    }
  }
  if (github.isEmpty) {
    requestedDiff.remove('github');
  } else {
    requestedDiff['github'] = github;
  }
  return requestedDiff;
}

/// Computes a nested diff between two AppConfig instances, returning only
/// the fields that changed in the structure expected by PATCH /config
/// (mirrors TOML layout).
Map<String, dynamic> _computeGlobalDiff(AppConfig old, AppConfig updated) {
  final diff = <String, dynamic>{};
  final aiDiff = <String, dynamic>{};
  final githubDiff = <String, dynamic>{};
  final retentionDiff = <String, dynamic>{};

  // AI section
  if (old.aiPrimary != updated.aiPrimary) {
    aiDiff['primary'] = updated.aiPrimary;
  }
  if (old.aiFallback != updated.aiFallback) {
    aiDiff['fallback'] = updated.aiFallback;
  }
  if (old.reviewMode != updated.reviewMode) {
    aiDiff['review_mode'] = updated.reviewMode;
  }

  // PR metadata
  final prMeta = <String, dynamic>{};
  if (_listsDiffer(old.globalPRReviewers, updated.globalPRReviewers)) {
    prMeta['reviewers'] = updated.globalPRReviewers;
  }
  if (_listsDiffer(old.globalPRLabels, updated.globalPRLabels)) {
    prMeta['labels'] = updated.globalPRLabels;
  }
  if (old.globalPRAssignee != updated.globalPRAssignee) {
    prMeta['pr_assignee'] = updated.globalPRAssignee;
  }
  if (old.globalPRDraft != updated.globalPRDraft) {
    prMeta['pr_draft'] = updated.globalPRDraft;
  }
  if (prMeta.isNotEmpty) aiDiff['pr_metadata'] = prMeta;

  if (old.globalIssuePrompt != updated.globalIssuePrompt) {
    aiDiff['issue_prompt'] = updated.globalIssuePrompt;
  }
  if (old.globalImplementPrompt != updated.globalImplementPrompt) {
    aiDiff['implement_prompt'] = updated.globalImplementPrompt;
  }
  if (old.globalTriageOwner != updated.globalTriageOwner) {
    aiDiff['triage_owner'] = updated.globalTriageOwner;
  }
  if (old.globalCloneDir != updated.globalCloneDir) {
    aiDiff['clone_dir'] = updated.globalCloneDir;
  }
  if (old.globalAutoPromoteTriage != updated.globalAutoPromoteTriage &&
      updated.globalAutoPromoteTriage != null) {
    aiDiff['auto_promote_triage'] = updated.globalAutoPromoteTriage;
  }
  if (old.globalAutoPromoteRefinement != updated.globalAutoPromoteRefinement &&
      updated.globalAutoPromoteRefinement != null) {
    aiDiff['auto_promote_refinement'] = updated.globalAutoPromoteRefinement;
  }
  if (old.globalGeneratePRDescription != updated.globalGeneratePRDescription) {
    aiDiff['generate_pr_description'] = updated.globalGeneratePRDescription;
  }
  if (old.globalNeverApproveWithIssues !=
      updated.globalNeverApproveWithIssues) {
    aiDiff['never_approve_with_issues'] = updated.globalNeverApproveWithIssues;
  }

  // Agent configs — diff each CLI agent's settings individually.
  final agentsDiff = <String, dynamic>{};
  final allAgentNames = {
    ...old.agentConfigs.keys,
    ...updated.agentConfigs.keys,
  };
  for (final name in allAgentNames) {
    final o = old.agentConfigs[name] ?? const CLIAgentConfig();
    final n = updated.agentConfigs[name] ?? const CLIAgentConfig();
    final ad = <String, dynamic>{};
    if (o.model != n.model) ad['model'] = n.model;
    if (o.maxTurns != n.maxTurns) ad['max_turns'] = n.maxTurns;
    if (o.approvalMode != n.approvalMode) ad['approval_mode'] = n.approvalMode;
    if (o.extraFlags != n.extraFlags) ad['extra_flags'] = n.extraFlags;
    if (o.promptId != n.promptId) ad['prompt'] = n.promptId ?? '';
    if (o.effort != n.effort) ad['effort'] = n.effort;
    if (o.permissionMode != n.permissionMode) {
      ad['permission_mode'] = n.permissionMode;
    }
    if (o.bare != n.bare) ad['bare'] = n.bare;
    if (o.dangerouslySkipPerms != n.dangerouslySkipPerms) {
      ad['dangerously_skip_perms'] = n.dangerouslySkipPerms;
    }
    if (o.noSessionPersistence != n.noSessionPersistence) {
      ad['no_session_persistence'] = n.noSessionPersistence;
    }
    if (ad.isNotEmpty) agentsDiff[name] = ad;
  }
  if (agentsDiff.isNotEmpty) aiDiff['agents'] = agentsDiff;

  if (aiDiff.isNotEmpty) {
    diff['ai'] = aiDiff;
  }

  // GitHub section
  if (old.pollInterval != updated.pollInterval) {
    githubDiff['poll_interval'] = updated.pollInterval;
  }
  if (_listsDiffer(old.repositories, updated.repositories)) {
    githubDiff['repositories'] = updated.repositories;
  }
  final oldNonMon =
      old.repoConfigs.entries
          .where((e) => !e.value.isMonitored)
          .map((e) => e.key)
          .toList()
        ..sort();
  final newNonMon =
      updated.repoConfigs.entries
          .where((e) => !e.value.isMonitored)
          .map((e) => e.key)
          .toList()
        ..sort();
  if (_listsDiffer(oldNonMon, newNonMon)) {
    githubDiff['non_monitored'] = newNonMon;
  }

  // Issue tracking (global)
  final itDiff = _computeIssueTrackingDiff(
    old.issueTracking,
    updated.issueTracking,
  );
  if (itDiff.isNotEmpty) {
    githubDiff['issue_tracking'] = itDiff;
  }

  if (githubDiff.isNotEmpty) {
    diff['github'] = githubDiff;
  }

  // Retention
  if (old.retentionDays != updated.retentionDays) {
    retentionDiff['max_days'] = updated.retentionDays;
  }
  if (retentionDiff.isNotEmpty) {
    diff['retention'] = retentionDiff;
  }

  // Autonomous mode
  final autonomousDiff = <String, dynamic>{};
  if (old.autonomous.enabled != updated.autonomous.enabled) {
    autonomousDiff['enabled'] = updated.autonomous.enabled;
  }
  if (old.autonomous.autoMerge != updated.autonomous.autoMerge) {
    autonomousDiff['auto_merge'] = updated.autonomous.autoMerge;
  }
  if (old.autonomous.mergeMethod != updated.autonomous.mergeMethod) {
    autonomousDiff['merge_method'] = updated.autonomous.mergeMethod;
  }
  if (old.autonomous.takeOthersTasks != updated.autonomous.takeOthersTasks) {
    autonomousDiff['take_others_tasks'] = updated.autonomous.takeOthersTasks;
  }
  if (old.autonomous.reassignOnTake != updated.autonomous.reassignOnTake) {
    autonomousDiff['reassign_on_take'] = updated.autonomous.reassignOnTake;
  }
  if (old.autonomous.devMaxTurns != updated.autonomous.devMaxTurns) {
    autonomousDiff['dev_max_turns'] = updated.autonomous.devMaxTurns;
  }
  if (old.autonomous.devEffort != updated.autonomous.devEffort) {
    autonomousDiff['dev_effort'] = updated.autonomous.devEffort;
  }
  if (old.autonomous.devTimeout != updated.autonomous.devTimeout) {
    autonomousDiff['dev_timeout'] = updated.autonomous.devTimeout;
  }
  if (old.autonomous.claimLease != updated.autonomous.claimLease) {
    autonomousDiff['claim_lease'] = updated.autonomous.claimLease;
  }
  if (autonomousDiff.isNotEmpty) diff['autonomous'] = autonomousDiff;

  // Circuit breaker
  final cbDiff = <String, dynamic>{};
  if (old.circuitBreaker.perPr24h != updated.circuitBreaker.perPr24h) {
    cbDiff['per_pr_24h'] = updated.circuitBreaker.perPr24h;
  }
  if (old.circuitBreaker.perRepoHr != updated.circuitBreaker.perRepoHr) {
    cbDiff['per_repo_hr'] = updated.circuitBreaker.perRepoHr;
  }
  if (old.circuitBreaker.perIssue24h != updated.circuitBreaker.perIssue24h) {
    cbDiff['per_issue_24h'] = updated.circuitBreaker.perIssue24h;
  }
  if (old.circuitBreaker.perIssueRepoHr !=
      updated.circuitBreaker.perIssueRepoHr) {
    cbDiff['per_issue_repo_hr'] = updated.circuitBreaker.perIssueRepoHr;
  }
  if (old.circuitBreaker.perImplRepoHr !=
      updated.circuitBreaker.perImplRepoHr) {
    cbDiff['per_impl_repo_hr'] = updated.circuitBreaker.perImplRepoHr;
  }
  if (cbDiff.isNotEmpty) diff['circuit_breaker'] = cbDiff;

  return diff;
}

Map<String, dynamic> _computeIssueTrackingDiff(
  IssueTrackingConfig old,
  IssueTrackingConfig updated,
) {
  final diff = <String, dynamic>{};
  if (old.enabled != updated.enabled) {
    diff['enabled'] = updated.enabled;
  }
  if (old.filterMode != updated.filterMode) {
    diff['filter_mode'] = updated.filterMode;
  }
  if (old.defaultAction != updated.defaultAction) {
    diff['default_action'] = updated.defaultAction;
  }
  if (_listsDiffer(old.developLabels, updated.developLabels)) {
    diff['develop_labels'] = updated.developLabels;
  }
  if (_listsDiffer(old.refinementLabels, updated.refinementLabels)) {
    diff['refinement_labels'] = updated.refinementLabels;
  }
  if (_listsDiffer(old.reviewOnlyLabels, updated.reviewOnlyLabels)) {
    diff['review_only_labels'] = updated.reviewOnlyLabels;
  }
  if (_listsDiffer(old.skipLabels, updated.skipLabels)) {
    diff['skip_labels'] = updated.skipLabels;
  }
  if (_listsDiffer(old.organizations, updated.organizations)) {
    diff['organizations'] = updated.organizations;
  }
  if (_listsDiffer(old.assignees, updated.assignees)) {
    diff['assignees'] = updated.assignees;
  }
  return diff;
}

bool _listsDiffer(List<String> a, List<String> b) {
  if (a.length != b.length) return true;
  for (var i = 0; i < a.length; i++) {
    if (a[i] != b[i]) return true;
  }
  return false;
}
