import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:mix/mix.dart';

import '../../core/models/agent.dart';
import '../../core/models/config_model.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/merge_tracking_override_editor.dart';
import '../../shared/widgets/override_field.dart';
import '../../shared/widgets/toast.dart';
import '../agents/agents_screen.dart' show agentsProvider;
import '../config/config_providers.dart';
import '../dashboard/dashboard_providers.dart';
import '../repositories/widgets/feature_palette.dart';

class OrgDetailScreen extends ConsumerStatefulWidget {
  final String orgName;
  const OrgDetailScreen({super.key, required this.orgName});

  @override
  ConsumerState<OrgDetailScreen> createState() => _OrgDetailScreenState();
}

class _OrgDetailScreenState extends ConsumerState<OrgDetailScreen> {
  OrgConfig _config = const OrgConfig();
  // Last value successfully persisted to the daemon. All diffs are computed
  // against this baseline (not the pre-edit value) so that rapid edits to
  // several fields all reach the server, and so the Save button / dirty state
  // reflect "unsaved since last persist".
  OrgConfig _saved = const OrgConfig();
  bool _initialized = false;
  bool _saving = false;
  Timer? _debounce;

  @override
  void dispose() {
    _debounce?.cancel();
    super.dispose();
  }

  void _initFrom(AppConfig config) {
    if (_initialized) return;
    _initialized = true;
    _config = config.orgConfigs[widget.orgName] ?? const OrgConfig();
    _saved = _config;
  }

  bool get _dirty =>
      _computeOrgDiff(_saved, _config).isNotEmpty ||
      diffMergeTrackingOverrides(
        _saved.mergeTracking,
        _config.mergeTracking,
      ).isNotEmpty;

  void _update(OrgConfig updated) {
    setState(() => _config = updated);
    // Debounced convenience auto-save; the explicit Save button and the
    // save-on-leave guard (PopScope) cover the sub-debounce window so edits
    // are never silently dropped.
    _debounce?.cancel();
    _debounce = Timer(const Duration(milliseconds: 800), _save);
  }

  Future<void> _save() async {
    _debounce?.cancel();
    final diff = _computeOrgDiff(_saved, _config);
    final mergeTrackingDiff = diffMergeTrackingOverrides(
      _saved.mergeTracking,
      _config.mergeTracking,
    );
    if (diff.isEmpty && mergeTrackingDiff.isEmpty) return;
    final target = _config;
    if (mounted) setState(() => _saving = true);
    try {
      final api = ref.read(apiClientProvider);
      Map<String, dynamic>? freshJson;
      if (diff.isNotEmpty) {
        freshJson = await api.patchOrgConfig(widget.orgName, diff);
      }
      if (mergeTrackingDiff.isNotEmpty) {
        freshJson = await api.patchMergeTrackingOrgConfig(
          widget.orgName,
          mergeTrackingDiff,
        );
      }
      if (freshJson != null) {
        ref.read(configNotifierProvider.notifier).updateFromServer(freshJson);
      }
      _saved = target;
      if (mounted) showToast(context, 'Saved');
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  Future<void> _resetField(String fieldPath) async {
    try {
      final freshJson = await ref
          .read(apiClientProvider)
          .deleteOrgField(widget.orgName, fieldPath);
      ref.read(configNotifierProvider.notifier).updateFromServer(freshJson);
      final freshConfig = AppConfig.fromJson(freshJson);
      setState(() {
        _config = freshConfig.orgConfigs[widget.orgName] ?? const OrgConfig();
        _saved = _config;
      });
      if (mounted) showToast(context, 'Reset to global');
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Map<String, dynamic> _computeOrgDiff(OrgConfig old, OrgConfig updated) {
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
    if (old.cloneDir != updated.cloneDir) {
      diff['clone_dir'] = updated.cloneDir ?? '';
    }
    if (old.neverApproveWithIssues != updated.neverApproveWithIssues &&
        updated.neverApproveWithIssues != null) {
      diff['never_approve_with_issues'] = updated.neverApproveWithIssues!;
    }
    if (old.neverApproveMinSeverity != updated.neverApproveMinSeverity) {
      diff['never_approve_min_severity'] =
          updated.neverApproveMinSeverity ?? '';
    }
    return diff;
  }

  @override
  Widget build(BuildContext context) {
    final configAsync = ref.watch(configNotifierProvider);
    return PopScope(
      // Block the pop while there are unsaved edits so the sub-debounce window
      // can't drop them: flush the save, then leave. When nothing is pending
      // (canPop true) navigation proceeds normally.
      canPop: !_dirty,
      onPopInvokedWithResult: (didPop, _) async {
        if (didPop) return;
        await _save();
        if (!context.mounted) return;
        if (context.canPop()) context.pop();
      },
      child: Scaffold(
        appBar: AppBar(
          title: Text(widget.orgName),
          leading: IconButton(
            icon: const Icon(Icons.arrow_back),
            onPressed: () => context.canPop() ? context.pop() : context.go('/'),
          ),
          actions: [
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 8),
              child: _saving
                  ? const Center(
                      child: SizedBox(
                        width: 18,
                        height: 18,
                        child: CircularProgressIndicator(strokeWidth: 2),
                      ),
                    )
                  : TextButton.icon(
                      onPressed: _dirty ? _save : null,
                      icon: const Icon(Icons.save, size: 18),
                      label: const Text('Save'),
                    ),
            ),
          ],
        ),
        body: configAsync.when(
          loading: () => const Center(child: CircularProgressIndicator()),
          error: (_, _) =>
              const Center(child: AppText('Could not load config')),
          data: (appConfig) {
            _initFrom(appConfig);
            final prompts = ref.watch(agentsProvider).value ?? <ReviewPrompt>[];
            final promptOptions = prompts.map((p) => p.id).toList();

            return SingleChildScrollView(
              padding: const EdgeInsets.all(16),
              child: Column(
                children: [
                  _sectionCard('General', [
                    OverrideTextField(
                      label: 'Local directory',
                      globalValue: '',
                      overrideValue: _config.localDir,
                      isDirectory: true,
                      onChanged: (v) => _update(_config.copyWith(localDir: v)),
                      onReset: () => _resetField('local_dir'),
                    ),
                    const SizedBox(height: 10),
                    OverrideTextField(
                      label: 'Clone directory',
                      globalValue: appConfig.globalCloneDir,
                      overrideValue: _config.cloneDir,
                      onChanged: (v) => _update(_config.copyWith(cloneDir: v)),
                      onReset: () => _resetField('clone_dir'),
                    ),
                  ]),
                  _sectionCard('PR Review', [
                    OverrideDropdown(
                      label: 'Primary',
                      globalValue: appConfig.aiPrimary,
                      overrideValue: _config.aiPrimary,
                      options: const ['claude', 'gemini', 'codex'],
                      onChanged: (v) => _update(_config.copyWith(aiPrimary: v)),
                      onReset: () => _resetField('primary'),
                    ),
                    const SizedBox(height: 10),
                    OverrideDropdown(
                      label: 'Fallback',
                      globalValue: appConfig.aiFallback.isEmpty
                          ? 'none'
                          : appConfig.aiFallback,
                      overrideValue: _config.aiFallback,
                      options: const ['claude', 'gemini', 'codex'],
                      onChanged: (v) =>
                          _update(_config.copyWith(aiFallback: v)),
                      onReset: () => _resetField('fallback'),
                    ),
                    const SizedBox(height: 10),
                    OverrideDropdown(
                      label: 'Review mode',
                      globalValue: appConfig.reviewMode,
                      overrideValue: _config.reviewMode,
                      options: const ['single', 'multi'],
                      onChanged: (v) =>
                          _update(_config.copyWith(reviewMode: v)),
                      onReset: () => _resetField('review_mode'),
                    ),
                    const SizedBox(height: 10),
                    OverrideDropdown(
                      label: 'Prompt',
                      globalValue: 'default',
                      overrideValue: _config.promptId,
                      options: promptOptions,
                      onChanged: (v) => _update(_config.copyWith(promptId: v)),
                      onReset: () => _resetField('prompt'),
                    ),
                    const SizedBox(height: 10),
                    OverrideDropdown(
                      label: 'Never approve PRs with issues',
                      globalValue: appConfig.globalNeverApproveWithIssues
                          .toString(),
                      overrideValue: _config.neverApproveWithIssues?.toString(),
                      options: const ['true', 'false'],
                      onChanged: (v) => _update(
                        _config.copyWith(
                          neverApproveWithIssues: v != null
                              ? v == 'true'
                              : null,
                        ),
                      ),
                      onReset: () => _resetField('never_approve_with_issues'),
                    ),
                    const SizedBox(height: 10),
                    OverrideDropdown(
                      label: 'Never approve — minimum severity',
                      globalValue: appConfig.globalNeverApproveMinSeverity,
                      overrideValue: _config.neverApproveMinSeverity,
                      options: neverApproveMinSeverityOptions,
                      onChanged: (v) =>
                          _update(_config.copyWith(neverApproveMinSeverity: v)),
                      onReset: () => _resetField('never_approve_min_severity'),
                    ),
                  ], accent: FeaturePalette.prReview),
                  _sectionCard('Merge Tracking', [
                    MergeTrackingOverrideEditor(
                      scopeKey: 'org',
                      value: _config.mergeTracking,
                      inherited: appConfig.mergeTracking,
                      onChanged: (mergeTracking) => _update(
                        _config.copyWith(mergeTracking: mergeTracking),
                      ),
                    ),
                  ], accent: FeaturePalette.mergeTracking),
                ],
              ),
            );
          },
        ),
      ),
    );
  }

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
}
