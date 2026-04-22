import 'package:flutter_riverpod/flutter_riverpod.dart';
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
  @override
  Future<AppConfig> build() async {
    final api = ref.watch(apiClientProvider);
    final json = await api.fetchConfig();
    return AppConfig.fromJson(json);
  }

  /// Replaces local state with fresh config from the daemon. Called after
  /// PATCH/DELETE endpoints return the full config.
  void updateFromServer(Map<String, dynamic> json) {
    state = AsyncValue.data(AppConfig.fromJson(json));
  }

  /// Save global config changes by computing the diff and sending only
  /// changed fields to the daemon via PATCH.
  /// Optimistic: updates UI state immediately, sends PATCH in background,
  /// then reconciles with the daemon's authoritative response.
  Future<void> save(AppConfig updated) async {
    final current = state.valueOrNull;
    if (current == null) return;
    final api = ref.read(apiClientProvider);
    final diff = _computeGlobalDiff(current, updated);
    if (diff.isEmpty) {
      state = AsyncValue.data(updated);
      return;
    }
    // Optimistic update — UI reflects the change immediately
    state = AsyncValue.data(updated);
    // Reconcile with daemon response in background
    final freshJson = await api.patchConfig(diff);
    state = AsyncValue.data(AppConfig.fromJson(freshJson));
  }

  /// First-run setup: write config file to disk, store token in Keychain,
  /// then launch the daemon binary and wait for it to become healthy.
  Future<void> saveAndStartDaemon({
    required String token,
    required AppConfig config,
    required String daemonBinaryPath,
  }) async {
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
  }
}

final configNotifierProvider =
    AsyncNotifierProvider<ConfigNotifier, AppConfig>(ConfigNotifier.new);

/// Computes a nested diff between two AppConfig instances, returning only
/// the fields that changed in the structure expected by PATCH /config
/// (mirrors TOML layout).
Map<String, dynamic> _computeGlobalDiff(AppConfig old, AppConfig updated) {
  final diff = <String, dynamic>{};
  final aiDiff = <String, dynamic>{};
  final githubDiff = <String, dynamic>{};
  final retentionDiff = <String, dynamic>{};

  // AI section
  if (old.aiPrimary != updated.aiPrimary) aiDiff['primary'] = updated.aiPrimary;
  if (old.aiFallback != updated.aiFallback) aiDiff['fallback'] = updated.aiFallback;
  if (old.reviewMode != updated.reviewMode) aiDiff['review_mode'] = updated.reviewMode;

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

  if (aiDiff.isNotEmpty) diff['ai'] = aiDiff;

  // GitHub section
  if (old.pollInterval != updated.pollInterval) {
    githubDiff['poll_interval'] = updated.pollInterval;
  }
  if (_listsDiffer(old.repositories, updated.repositories)) {
    githubDiff['repositories'] = updated.repositories;
  }
  final oldNonMon = old.repoConfigs.entries
      .where((e) => !e.value.isMonitored).map((e) => e.key).toList()..sort();
  final newNonMon = updated.repoConfigs.entries
      .where((e) => !e.value.isMonitored).map((e) => e.key).toList()..sort();
  if (_listsDiffer(oldNonMon, newNonMon)) {
    githubDiff['non_monitored'] = newNonMon;
  }

  // Issue tracking (global)
  final itDiff = _computeIssueTrackingDiff(old.issueTracking, updated.issueTracking);
  if (itDiff.isNotEmpty) githubDiff['issue_tracking'] = itDiff;

  if (githubDiff.isNotEmpty) diff['github'] = githubDiff;

  // Retention
  if (old.retentionDays != updated.retentionDays) {
    retentionDiff['max_days'] = updated.retentionDays;
  }
  if (retentionDiff.isNotEmpty) diff['retention'] = retentionDiff;

  return diff;
}

Map<String, dynamic> _computeIssueTrackingDiff(
    IssueTrackingConfig old, IssueTrackingConfig updated) {
  final diff = <String, dynamic>{};
  if (old.enabled != updated.enabled) diff['enabled'] = updated.enabled;
  if (old.filterMode != updated.filterMode) diff['filter_mode'] = updated.filterMode;
  if (old.defaultAction != updated.defaultAction) diff['default_action'] = updated.defaultAction;
  if (_listsDiffer(old.developLabels, updated.developLabels)) diff['develop_labels'] = updated.developLabels;
  if (_listsDiffer(old.reviewOnlyLabels, updated.reviewOnlyLabels)) diff['review_only_labels'] = updated.reviewOnlyLabels;
  if (_listsDiffer(old.skipLabels, updated.skipLabels)) diff['skip_labels'] = updated.skipLabels;
  if (_listsDiffer(old.organizations, updated.organizations)) diff['organizations'] = updated.organizations;
  if (_listsDiffer(old.assignees, updated.assignees)) diff['assignees'] = updated.assignees;
  return diff;
}

bool _listsDiffer(List<String> a, List<String> b) {
  if (a.length != b.length) return true;
  for (var i = 0; i < a.length; i++) {
    if (a[i] != b[i]) return true;
  }
  return false;
}
