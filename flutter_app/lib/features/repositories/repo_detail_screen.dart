import 'dart:async';
import 'package:file_picker/file_picker.dart';
import 'package:flutter/foundation.dart' show kIsWeb;
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:mix/mix.dart';
import '../../core/models/config_model.dart';
import '../../core/models/agent.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/merge_tracking_override_editor.dart';
import '../../shared/widgets/override_field.dart';
import '../../shared/widgets/review_limits_editor.dart';
import '../../shared/widgets/token_saving_editor.dart';
import '../../shared/widgets/toast.dart';
import '../agents/agents_screen.dart' show agentsProvider;
import '../config/config_providers.dart';
import '../dashboard/dashboard_providers.dart';
import '../flows/flows_screen.dart' show flowsProvider;
import 'repo_diff.dart';
import 'widgets/feature_palette.dart';
import 'widgets/feature_switch.dart';

class RepoDetailScreen extends ConsumerStatefulWidget {
  final String repoName;
  const RepoDetailScreen({super.key, required this.repoName});

  @override
  ConsumerState<RepoDetailScreen> createState() => _RepoDetailScreenState();
}

class _RepoDetailScreenState extends ConsumerState<RepoDetailScreen> {
  RepoConfig _config = const RepoConfig();
  RepoConfig _previousConfig = const RepoConfig();
  bool _initialized = false;
  Timer? _debounce;

  @override
  void dispose() {
    _debounce?.cancel();
    super.dispose();
  }

  void _initFrom(AppConfig config) {
    if (_initialized) return;
    _initialized = true;
    _config = config.repoConfigs[widget.repoName] ?? const RepoConfig();
    _previousConfig = _config;
  }

  void _update(RepoConfig updated) {
    setState(() => _config = updated);
    _debounce?.cancel();
    _debounce = Timer(const Duration(milliseconds: 800), _autoSave);
  }

  Future<void> _autoSave() async {
    final api = ref.read(apiClientProvider);
    final previous = _previousConfig;
    final target = _config;
    try {
      final repoDiff = computeRepoDiff(previous, target);
      Map<String, dynamic>? lastResponse;
      var didSave = false;
      if (repoDiff.isNotEmpty) {
        lastResponse = await api.patchRepoConfig(widget.repoName, repoDiff);
        didSave = true;
      }

      final mergeTrackingDiff = diffMergeTrackingOverrides(
        previous.mergeTracking,
        target.mergeTracking,
      );
      if (mergeTrackingDiff.isNotEmpty) {
        lastResponse = await api.patchMergeTrackingRepoConfig(
          widget.repoName,
          mergeTrackingDiff,
        );
        didSave = true;
      }

      final monitoringChanged = previous.isMonitored != target.isMonitored;
      if (monitoringChanged) {
        final current = ref.read(configNotifierProvider).value;
        if (current != null) {
          final updatedRepos = Map<String, RepoConfig>.from(
            current.repoConfigs,
          );
          updatedRepos[widget.repoName] = target;
          final monitored =
              updatedRepos.entries
                  .where((e) => e.value.isMonitored)
                  .map((e) => e.key)
                  .toList()
                ..sort();
          // Preserve any bare org entries (theburrowhub/heimdallm#828) —
          // they aren't represented in repoConfigs, so recomputing
          // non_monitored purely from the per-repo map would silently drop
          // an org-level exclusion every time an unrelated repo is toggled.
          final nonMonitored =
              <String>{
                  ...current.nonMonitoredOrgs,
                  ...updatedRepos.entries
                      .where((e) => !e.value.isMonitored)
                      .map((e) => e.key),
                }.toList()
                ..sort();
          lastResponse = await api.patchConfig({
            'github': {
              'repositories': monitored,
              'non_monitored': nonMonitored,
            },
          });
          didSave = true;
        }
      }

      if (lastResponse != null) {
        ref
            .read(configNotifierProvider.notifier)
            .updateFromServer(lastResponse);
      }
      _previousConfig = target;
      if (mounted && didSave) showToast(context, 'Saved');
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _resetField(String fieldPath) async {
    final api = ref.read(apiClientProvider);
    try {
      final freshJson = await api.deleteRepoField(widget.repoName, fieldPath);
      ref.read(configNotifierProvider.notifier).updateFromServer(freshJson);
      final freshConfig = AppConfig.fromJson(freshJson);
      setState(() {
        _config =
            freshConfig.repoConfigs[widget.repoName] ?? const RepoConfig();
        _previousConfig = _config;
      });
      if (mounted) showToast(context, 'Reset to global');
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  // ── Helpers ──────────────────────────────────────────────────────────────────

  // ── Section card ─────────────────────────────────────────────────────────────

  Widget _sectionCard(String title, List<Widget> children, {Color? accent}) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 12),
      child: Box(
        style: BoxStyler()
            .color(AppColors.surface())
            .borderAll(
              color: accent ?? AppColors.border(),
              width: accent != null ? 2 : 1,
            )
            .borderRadiusAll(AppRadius.lg())
            .padding(EdgeInsetsGeometryMix.value(const EdgeInsets.all(14))),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            AppText.sectionTitle(title, color: accent),
            const SizedBox(height: 12),
            ...children,
          ],
        ),
      ),
    );
  }

  // ── Build ────────────────────────────────────────────────────────────────────

  @override
  Widget build(BuildContext context) {
    final configAsync = ref.watch(configNotifierProvider);

    return Scaffold(
      appBar: AppBar(
        title: Text(widget.repoName),
        leading: IconButton(
          icon: const Icon(Icons.arrow_back),
          onPressed: () => context.canPop() ? context.pop() : context.go('/'),
        ),
      ),
      body: configAsync.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (_, _) => const Center(child: AppText('Could not load config')),
        data: (appConfig) {
          _initFrom(appConfig);
          final prompts = ref.watch(agentsProvider).value ?? <ReviewPrompt>[];
          final orgName = widget.repoName.contains('/')
              ? widget.repoName.split('/').first
              : widget.repoName;
          final orgConfig = appConfig.orgConfigs[orgName];
          final orgTokenSaving = orgConfig?.tokenSaving;
          final flowIds =
              ref.watch(flowsProvider).value?.ids ?? const <String>['default'];
          final orgMergeTracking =
              orgConfig?.mergeTracking ?? const MergeTrackingOverride();
          final inheritedMergeTracking = appConfig.mergeTracking.applyOverride(
            orgMergeTracking,
          );
          final editorInherited = inheritedMergeTracking.copyWith(
            enabled: _config.isEffectivelyMonitored
                ? inheritedMergeTracking.enabled
                : false,
          );
          String source(bool hasOrgValue) =>
              hasOrgValue ? 'org: $orgName' : 'global';

          final warning = AppColors.warning.resolve(context);

          return SingleChildScrollView(
            padding: const EdgeInsets.all(16),
            child: Column(
              children: [
                if (_config.excludedByOrg)
                  Padding(
                    padding: const EdgeInsets.only(bottom: 12),
                    child: Box(
                      style: BoxStyler()
                          .color(warning.withValues(alpha: 0.12))
                          .borderAll(color: warning, width: 1)
                          .borderRadiusAll(AppRadius.lg())
                          .padding(
                            EdgeInsetsGeometryMix.value(
                              const EdgeInsets.all(14),
                            ),
                          ),
                      child: Row(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Icon(
                            Icons.warning_amber_rounded,
                            color: warning,
                            size: 20,
                          ),
                          const SizedBox(width: 10),
                          Expanded(
                            child: Column(
                              crossAxisAlignment: CrossAxisAlignment.start,
                              children: [
                                AppText(
                                  'Every repo in $orgName is excluded from monitoring. '
                                  'The settings below have no effect until the org is re-enabled.',
                                ),
                                const SizedBox(height: 6),
                                GestureDetector(
                                  onTap: () => context.push(
                                    '/orgs/${Uri.encodeComponent(orgName)}',
                                  ),
                                  child: AppText.label(
                                    'Open org settings',
                                    color: warning,
                                  ),
                                ),
                              ],
                            ),
                          ),
                        ],
                      ),
                    ),
                  ),
                // ── Section 1: General ─────────────────────────────────
                _sectionCard('General', [
                  const AppText.label('Local directory'),
                  const SizedBox(height: 4),
                  const AppText.muted(
                    'When set, the AI agent runs inside this directory and can read all project files.',
                  ),
                  const SizedBox(height: 8),
                  _LocalDirField(
                    value: _config.localDir ?? orgConfig?.localDir ?? '',
                    sourceLabel: _config.localDir != null
                        ? 'repo override'
                        : source(orgConfig?.localDir != null),
                    detectedDir: appConfig.localDirsDetected[widget.repoName],
                    onChanged: (dir) => _update(
                      _config.copyWith(localDir: dir.isEmpty ? null : dir),
                    ),
                  ),
                  const SizedBox(height: 10),
                  OverrideTextField(
                    label: 'Clone directory',
                    globalValue:
                        orgConfig?.cloneDir ?? appConfig.globalCloneDir,
                    inheritedLabel: source(orgConfig?.cloneDir != null),
                    overrideValue: _config.cloneDir,
                    onChanged: (v) => _update(_config.copyWith(cloneDir: v)),
                    onReset: () => _resetField('clone_dir'),
                  ),
                ]),

                // ── Section 2: PR Review ───────────────────────────────
                _sectionCard('PR Review', [
                  Row(
                    children: [
                      const Expanded(child: AppText('Auto-review PRs')),
                      FeatureSwitch(
                        feature: Feature.prReview,
                        value: _config.prEnabled ?? false,
                        onChanged: (v) =>
                            _update(_config.copyWith(prEnabled: v)),
                      ),
                    ],
                  ),
                  const SizedBox(height: 6),
                  OverrideDropdown(
                    key: const ValueKey('repo-review-flow'),
                    label: 'Review flow',
                    globalValue:
                        orgConfig?.flow ??
                        (appConfig.aiFlow.isEmpty
                            ? 'default'
                            : appConfig.aiFlow),
                    inheritedLabel: source(orgConfig?.flow != null),
                    overrideValue: _config.flow,
                    options: flowIds,
                    onChanged: (v) => _update(_config.copyWith(flow: v)),
                    onReset: () => _resetField('flow'),
                  ),
                  const SizedBox(height: 10),
                  // Legacy per-repo agent overrides: shown only while set,
                  // so they can be reviewed and removed. They still feed
                  // the default flow when no flow is selected.
                  if (_config.aiPrimary != null ||
                      _config.aiFallback != null) ...[
                    OverrideDropdown(
                      label: 'Primary',
                      globalValue: orgConfig?.aiPrimary ?? appConfig.aiPrimary,
                      inheritedLabel: source(orgConfig?.aiPrimary != null),
                      overrideValue: _config.aiPrimary,
                      options: reviewAgentIds,
                      onChanged: (v) => _update(_config.copyWith(aiPrimary: v)),
                      onReset: () => _resetField('primary'),
                    ),
                    const SizedBox(height: 10),
                    OverrideDropdown(
                      label: 'Fallback',
                      globalValue:
                          (orgConfig?.aiFallback ?? appConfig.aiFallback)
                              .isEmpty
                          ? 'none'
                          : (orgConfig?.aiFallback ?? appConfig.aiFallback),
                      inheritedLabel: source(orgConfig?.aiFallback != null),
                      overrideValue: _config.aiFallback,
                      options: reviewAgentIds,
                      onChanged: (v) =>
                          _update(_config.copyWith(aiFallback: v)),
                      onReset: () => _resetField('fallback'),
                    ),
                    const SizedBox(height: 10),
                  ],
                  OverrideDropdown(
                    label: 'Review mode',
                    globalValue: orgConfig?.reviewMode ?? appConfig.reviewMode,
                    inheritedLabel: source(orgConfig?.reviewMode != null),
                    overrideValue: _config.reviewMode,
                    options: const ['single', 'multi'],
                    onChanged: (v) => _update(_config.copyWith(reviewMode: v)),
                    onReset: () => _resetField('review_mode'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Prompt',
                    globalValue: orgConfig?.promptId ?? 'default',
                    inheritedLabel: source(orgConfig?.promptId != null),
                    overrideValue: _config.promptId,
                    options: prompts.map((p) => p.id).toList(),
                    onChanged: (v) => _update(_config.copyWith(promptId: v)),
                    onReset: () => _resetField('prompt'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Never approve PRs with issues',
                    globalValue:
                        (orgConfig?.neverApproveWithIssues ??
                                appConfig.globalNeverApproveWithIssues)
                            .toString(),
                    inheritedLabel: source(
                      orgConfig?.neverApproveWithIssues != null,
                    ),
                    overrideValue: _config.neverApproveWithIssues?.toString(),
                    options: const ['true', 'false'],
                    onChanged: (v) => _update(
                      _config.copyWith(
                        neverApproveWithIssues: v != null ? v == 'true' : null,
                      ),
                    ),
                    onReset: () => _resetField('never_approve_with_issues'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Never approve — minimum severity',
                    globalValue:
                        orgConfig?.neverApproveMinSeverity ??
                        appConfig.globalNeverApproveMinSeverity,
                    inheritedLabel: source(
                      orgConfig?.neverApproveMinSeverity != null,
                    ),
                    overrideValue: _config.neverApproveMinSeverity,
                    options: neverApproveMinSeverityOptions,
                    onChanged: (v) =>
                        _update(_config.copyWith(neverApproveMinSeverity: v)),
                    onReset: () => _resetField('never_approve_min_severity'),
                  ),
                ], accent: FeaturePalette.prReview),

                // ── Section 3: Merge Tracking ──                // ── Section 6: Merge Tracking ──────────────────────────────
                _sectionCard('Merge Tracking', [
                  MergeTrackingOverrideEditor(
                    scopeKey: 'repo',
                    value: _config.mergeTracking,
                    inherited: editorInherited,
                    parentOverride: orgMergeTracking,
                    parentLabel: 'org: $orgName',
                    enabledInheritedLabel: !_config.isEffectivelyMonitored
                        ? 'repository monitoring'
                        : null,
                    onChanged: (mergeTracking) =>
                        _update(_config.copyWith(mergeTracking: mergeTracking)),
                  ),
                ], accent: FeaturePalette.mergeTracking),
                _sectionCard('Token Saving', [
                  TokenSavingOverrideEditor(
                    value: _config.tokenSaving,
                    inheritedLabelFor: (key) =>
                        orgTokenSaving?.measure(key) == null
                        ? 'global'
                        : 'org: $orgName',
                    inheritedValue: (key) =>
                        orgTokenSaving?.measure(key) ??
                        appConfig.tokenSaving.measure(key),
                    onChanged: (v) =>
                        _update(_config.copyWith(tokenSaving: v)),
                    onReset: (key) => _resetField('token_saving/$key'),
                  ),
                ]),
                _sectionCard('Review Limits', [
                  ReviewLimitsOverrideEditor(
                    keyPrefix: 'repo',
                    scopeLabel: 'repository',
                    value: _config.reviewLimits,
                    onChanged: (v) =>
                        _update(_config.copyWith(reviewLimits: v)),
                    onRemove: () => _resetField('review_limits'),
                  ),
                ]),
              ],
            ),
          );
        },
      ),
    );
  }
}

// ── Local directory picker ─────────────────────────────────────────────────

class _LocalDirField extends StatefulWidget {
  final String value;
  final String sourceLabel;
  final ValueChanged<String> onChanged;

  /// Non-null when the daemon detected a `/home/heimdallm/repos/<name>` path
  /// for this repo (HEIMDALLM_LOCAL_DIR_BASE is mounted and the repo is
  /// visible there). Shown
  /// as the field's placeholder + a small hint below the row so the operator
  /// knows the fallback will kick in if they leave the field empty.
  final String? detectedDir;
  const _LocalDirField({
    required this.value,
    required this.sourceLabel,
    required this.onChanged,
    this.detectedDir,
  });

  @override
  State<_LocalDirField> createState() => _LocalDirFieldState();
}

class _LocalDirFieldState extends State<_LocalDirField> {
  late final TextEditingController _ctrl;

  @override
  void initState() {
    super.initState();
    _ctrl = TextEditingController(text: widget.value);
  }

  @override
  void didUpdateWidget(_LocalDirField oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (widget.value != oldWidget.value && _ctrl.text != widget.value) {
      _ctrl.text = widget.value;
    }
  }

  @override
  void dispose() {
    _ctrl.dispose();
    super.dispose();
  }

  Future<void> _pick() async {
    final dir = await FilePicker.getDirectoryPath(
      dialogTitle: 'Select local repository directory',
      lockParentWindow: true,
    );
    if (dir == null) return;
    setState(() => _ctrl.text = dir);
    widget.onChanged(dir);
  }

  @override
  Widget build(BuildContext context) {
    final detected = widget.detectedDir;
    final hintText = detected != null && detected.isNotEmpty
        ? 'Auto-detected: $detected'
        : '/path/to/local/repo';
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Padding(
          padding: const EdgeInsets.only(bottom: 6),
          child: AppText.label(widget.sourceLabel, color: Colors.grey.shade600),
        ),
        Row(
          children: [
            Expanded(
              child: TextFormField(
                controller: _ctrl,
                decoration: InputDecoration(
                  hintText: hintText,
                  hintStyle: detected != null && detected.isNotEmpty
                      ? TextStyle(
                          color: Colors.blue.shade400,
                          fontStyle: FontStyle.italic,
                        )
                      : null,
                  border: const OutlineInputBorder(),
                  isDense: true,
                ),
                onChanged: widget.onChanged,
              ),
            ),
            // Browse button is desktop-only — browsers can't expose native
            // filesystem paths to the daemon. On web the operator types a
            // path that exists inside the daemon container (e.g.
            // /home/heimdallm/repos/foo if they've bind-mounted their host
            // repos root via HEIMDALLM_LOCAL_DIR_BASE).
            if (!kIsWeb) ...[
              const SizedBox(width: 8),
              OutlinedButton.icon(
                icon: const Icon(Icons.folder_open, size: 16),
                label: const Text('Browse'),
                onPressed: _pick,
                style: OutlinedButton.styleFrom(
                  padding: const EdgeInsets.symmetric(
                    horizontal: 12,
                    vertical: 12,
                  ),
                ),
              ),
            ] else ...[
              const SizedBox(width: 8),
              Tooltip(
                message:
                    'The daemon runs in a container, so paths here refer to '
                    'directories inside that container — typically a bind-mount '
                    'like /home/heimdallm/repos/<name>. Enter the path manually.',
                child: Icon(
                  Icons.info_outline,
                  size: 16,
                  color: Colors.grey.shade500,
                ),
              ),
            ],
            if (_ctrl.text.isNotEmpty) ...[
              const SizedBox(width: 4),
              IconButton(
                icon: const Icon(Icons.clear, size: 16),
                tooltip: 'Clear',
                onPressed: () {
                  setState(() => _ctrl.clear());
                  widget.onChanged('');
                },
              ),
            ],
          ],
        ),
        if (detected != null && detected.isNotEmpty && _ctrl.text.isEmpty) ...[
          const SizedBox(height: 6),
          Row(
            children: [
              Icon(Icons.auto_awesome, size: 12, color: Colors.blue.shade400),
              const SizedBox(width: 4),
              Expanded(
                child: AppText(
                  'Leave empty to use the auto-detected path above. '
                  'Type a different path to override.',
                  role: AppTextRole.bodyMuted,
                  color: Colors.blue.shade400,
                ),
              ),
            ],
          ),
        ],
      ],
    );
  }
}
