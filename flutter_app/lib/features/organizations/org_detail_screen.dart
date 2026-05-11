import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../core/models/agent.dart';
import '../../core/models/config_model.dart';
import '../../shared/widgets/autocomplete_chip_field.dart';
import '../../shared/widgets/override_field.dart';
import '../../shared/widgets/toast.dart';
import '../agents/agents_screen.dart' show agentsProvider;
import '../config/config_providers.dart';
import '../dashboard/dashboard_providers.dart';
import '../repositories/widgets/feature_palette.dart';
import '../repositories/widgets/feature_switch.dart';

class OrgDetailScreen extends ConsumerStatefulWidget {
  final String orgName;
  const OrgDetailScreen({super.key, required this.orgName});

  @override
  ConsumerState<OrgDetailScreen> createState() => _OrgDetailScreenState();
}

class _OrgDetailScreenState extends ConsumerState<OrgDetailScreen> {
  OrgConfig _config = const OrgConfig();
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
    _config = config.orgConfigs[widget.orgName] ?? const OrgConfig();
  }

  void _update(OrgConfig updated) {
    final previous = _config;
    setState(() => _config = updated);
    _debounce?.cancel();
    _debounce = Timer(
      const Duration(milliseconds: 800),
      () => _autoSave(previous),
    );
  }

  Future<void> _autoSave(OrgConfig previous) async {
    final diff = _computeOrgDiff(previous, _config);
    if (diff.isEmpty) return;
    try {
      final freshJson = await ref
          .read(apiClientProvider)
          .patchOrgConfig(widget.orgName, diff);
      ref.read(configNotifierProvider.notifier).updateFromServer(freshJson);
      if (mounted) showToast(context, 'Saved');
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
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
    if (old.triageOwner != updated.triageOwner) {
      diff['triage_owner'] = updated.triageOwner ?? '';
    }
    if (old.cloneDir != updated.cloneDir) {
      diff['clone_dir'] = updated.cloneDir ?? '';
    }
    if (old.autoPromoteTriage != updated.autoPromoteTriage &&
        updated.autoPromoteTriage != null) {
      diff['auto_promote_triage'] = updated.autoPromoteTriage!;
    }
    if (old.autoPromoteRefinement != updated.autoPromoteRefinement &&
        updated.autoPromoteRefinement != null) {
      diff['auto_promote_refinement'] = updated.autoPromoteRefinement!;
    }
    if (old.generatePRDescription != updated.generatePRDescription &&
        updated.generatePRDescription != null) {
      diff['generate_pr_description'] = updated.generatePRDescription!;
    }
    if (old.issuePromptId != updated.issuePromptId) {
      diff['issue_prompt'] = updated.issuePromptId ?? '';
    }
    if (old.developPromptId != updated.developPromptId) {
      diff['implement_prompt'] = updated.developPromptId ?? '';
    }
    if (old.prAssignee != updated.prAssignee) {
      diff['pr_assignee'] = updated.prAssignee ?? '';
    }
    if (old.prDraft != updated.prDraft && updated.prDraft != null) {
      diff['pr_draft'] = updated.prDraft!;
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
    if (!_listsEqual(old.developLabels, updated.developLabels)) {
      itDiff['develop_labels'] = updated.developLabels ?? <String>[];
    }
    if (!_listsEqual(old.skipLabels, updated.skipLabels)) {
      itDiff['skip_labels'] = updated.skipLabels ?? <String>[];
    }
    if (!_listsEqual(old.issueOrganizations, updated.issueOrganizations)) {
      itDiff['organizations'] = updated.issueOrganizations ?? <String>[];
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

  String _joinList(List<String>? list) => list?.join(', ') ?? '';

  @override
  Widget build(BuildContext context) {
    final configAsync = ref.watch(configNotifierProvider);
    return Scaffold(
      appBar: AppBar(
        title: Text(widget.orgName),
        leading: IconButton(
          icon: const Icon(Icons.arrow_back),
          onPressed: () => context.canPop() ? context.pop() : context.go('/'),
        ),
      ),
      body: configAsync.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (_, _) => const Center(child: Text('Could not load config')),
        data: (appConfig) {
          _initFrom(appConfig);
          final prompts =
              ref.watch(agentsProvider).value ?? <ReviewPrompt>[];
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
                    onChanged: (v) => _update(_config.copyWith(localDir: v)),
                    onReset: () => _resetField('local_dir'),
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
                    onChanged: (v) => _update(_config.copyWith(aiFallback: v)),
                    onReset: () => _resetField('fallback'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Review mode',
                    globalValue: appConfig.reviewMode,
                    overrideValue: _config.reviewMode,
                    options: const ['single', 'multi'],
                    onChanged: (v) => _update(_config.copyWith(reviewMode: v)),
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
                ], accent: FeaturePalette.prReview),
                _sectionCard('Issue Tracking', [
                  Row(
                    children: [
                      const Expanded(
                        child: Text(
                          'Triage issues',
                          style: TextStyle(fontSize: 13),
                        ),
                      ),
                      FeatureSwitch(
                        feature: Feature.issueTracking,
                        value:
                            _config.itEnabled ??
                            appConfig.issueTracking.enabled,
                        onChanged: (v) =>
                            _update(_config.copyWith(itEnabled: v)),
                      ),
                    ],
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'Review-only labels',
                    selectedValues:
                        _config.reviewOnlyLabels ??
                        appConfig.issueTracking.reviewOnlyLabels,
                    availableOptions: const <String>[],
                    isOverridden: _config.reviewOnlyLabels != null,
                    globalHint: _joinList(
                      appConfig.issueTracking.reviewOnlyLabels,
                    ),
                    onChanged: (v) =>
                        _update(_config.copyWith(reviewOnlyLabels: v)),
                    onReset: () =>
                        _resetField('issue_tracking/review_only_labels'),
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'Refinement labels',
                    helper:
                        'Issues with these labels get a deep implementation plan',
                    selectedValues:
                        _config.refinementLabels ??
                        appConfig.issueTracking.refinementLabels,
                    availableOptions: const <String>[],
                    isOverridden: _config.refinementLabels != null,
                    globalHint: _joinList(
                      appConfig.issueTracking.refinementLabels,
                    ),
                    onChanged: (v) =>
                        _update(_config.copyWith(refinementLabels: v)),
                    onReset: () =>
                        _resetField('issue_tracking/refinement_labels'),
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'Skip labels',
                    selectedValues:
                        _config.skipLabels ??
                        appConfig.issueTracking.skipLabels,
                    availableOptions: const <String>[],
                    isOverridden: _config.skipLabels != null,
                    globalHint: _joinList(appConfig.issueTracking.skipLabels),
                    onChanged: (v) => _update(_config.copyWith(skipLabels: v)),
                    onReset: () => _resetField('issue_tracking/skip_labels'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Filter mode',
                    globalValue: appConfig.issueTracking.filterMode,
                    overrideValue: _config.issueFilterMode,
                    options: const ['exclusive', 'inclusive'],
                    onChanged: (v) =>
                        _update(_config.copyWith(issueFilterMode: v)),
                    onReset: () => _resetField('issue_tracking/filter_mode'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Default action',
                    globalValue: appConfig.issueTracking.defaultAction,
                    overrideValue: _config.issueDefaultAction,
                    options: const ['ignore', 'review_only'],
                    onChanged: (v) =>
                        _update(_config.copyWith(issueDefaultAction: v)),
                    onReset: () => _resetField('issue_tracking/default_action'),
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'Organizations',
                    helper: 'GitHub org names to filter issues',
                    selectedValues:
                        _config.issueOrganizations ??
                        appConfig.issueTracking.organizations,
                    availableOptions: appConfig.knownOrganizations,
                    isOverridden: _config.issueOrganizations != null,
                    globalHint: _joinList(
                      appConfig.issueTracking.organizations,
                    ),
                    onChanged: (v) =>
                        _update(_config.copyWith(issueOrganizations: v)),
                    onReset: () => _resetField('issue_tracking/organizations'),
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'Assignees',
                    helper: 'Only process issues assigned to these users',
                    selectedValues:
                        _config.issueAssignees ??
                        appConfig.issueTracking.assignees,
                    availableOptions: appConfig.knownGitHubUsers,
                    isOverridden: _config.issueAssignees != null,
                    globalHint: _joinList(appConfig.issueTracking.assignees),
                    onChanged: (v) =>
                        _update(_config.copyWith(issueAssignees: v)),
                    onReset: () => _resetField('issue_tracking/assignees'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Prompt',
                    globalValue: appConfig.globalIssuePrompt.isEmpty
                        ? 'default'
                        : appConfig.globalIssuePrompt,
                    overrideValue: _config.issuePromptId,
                    options: promptOptions,
                    onChanged: (v) =>
                        _update(_config.copyWith(issuePromptId: v)),
                    onReset: () => _resetField('issue_prompt'),
                  ),
                ], accent: FeaturePalette.issueTracking),
                _sectionCard('Pipeline', [
                  OverrideTextField(
                    label: 'Triage owner',
                    globalValue: appConfig.globalTriageOwner,
                    overrideValue: _config.triageOwner,
                    onChanged: (v) => _update(_config.copyWith(triageOwner: v)),
                    onReset: () => _resetField('triage_owner'),
                  ),
                  const SizedBox(height: 10),
                  OverrideTextField(
                    label: 'Clone directory',
                    globalValue: appConfig.globalCloneDir,
                    overrideValue: _config.cloneDir,
                    onChanged: (v) => _update(_config.copyWith(cloneDir: v)),
                    onReset: () => _resetField('clone_dir'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Auto-promote triage',
                    globalValue: (appConfig.globalAutoPromoteTriage ?? false)
                        .toString(),
                    overrideValue: _config.autoPromoteTriage?.toString(),
                    options: const ['true', 'false'],
                    onChanged: (v) => _update(
                      _config.copyWith(
                        autoPromoteTriage: v != null ? v == 'true' : null,
                      ),
                    ),
                    onReset: () => _resetField('auto_promote_triage'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Auto-promote refinement',
                    globalValue:
                        (appConfig.globalAutoPromoteRefinement ?? false)
                            .toString(),
                    overrideValue: _config.autoPromoteRefinement?.toString(),
                    options: const ['true', 'false'],
                    onChanged: (v) => _update(
                      _config.copyWith(
                        autoPromoteRefinement: v != null ? v == 'true' : null,
                      ),
                    ),
                    onReset: () => _resetField('auto_promote_refinement'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Generate PR description',
                    globalValue: appConfig.globalGeneratePRDescription
                        .toString(),
                    overrideValue: _config.generatePRDescription?.toString(),
                    options: const ['true', 'false'],
                    onChanged: (v) => _update(
                      _config.copyWith(
                        generatePRDescription: v != null ? v == 'true' : null,
                      ),
                    ),
                    onReset: () => _resetField('generate_pr_description'),
                  ),
                ]),
                _sectionCard('Develop', [
                  Row(
                    children: [
                      const Expanded(
                        child: Text(
                          'Auto-implement issues',
                          style: TextStyle(fontSize: 13),
                        ),
                      ),
                      FeatureSwitch(
                        feature: Feature.develop,
                        value: _config.devEnabled ?? false,
                        onChanged: (v) =>
                            _update(_config.copyWith(devEnabled: v)),
                      ),
                    ],
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'Develop labels',
                    selectedValues:
                        _config.developLabels ??
                        appConfig.issueTracking.developLabels,
                    availableOptions: const <String>[],
                    isOverridden: _config.developLabels != null,
                    globalHint: _joinList(
                      appConfig.issueTracking.developLabels,
                    ),
                    onChanged: (v) =>
                        _update(_config.copyWith(developLabels: v)),
                    onReset: () => _resetField('issue_tracking/develop_labels'),
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'PR Reviewers',
                    selectedValues:
                        _config.prReviewers ?? appConfig.globalPRReviewers,
                    availableOptions: const <String>[],
                    isOverridden: _config.prReviewers != null,
                    globalHint: _joinList(appConfig.globalPRReviewers),
                    onChanged: (v) => _update(_config.copyWith(prReviewers: v)),
                    onReset: () => _resetField('pr_reviewers'),
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'PR Labels',
                    selectedValues:
                        _config.prLabels ?? appConfig.globalPRLabels,
                    availableOptions: const <String>[],
                    isOverridden: _config.prLabels != null,
                    globalHint: _joinList(appConfig.globalPRLabels),
                    onChanged: (v) => _update(_config.copyWith(prLabels: v)),
                    onReset: () => _resetField('pr_labels'),
                  ),
                  const SizedBox(height: 10),
                  OverrideTextField(
                    label: 'PR Assignee',
                    globalValue: appConfig.globalPRAssignee,
                    overrideValue: _config.prAssignee,
                    onChanged: (v) => _update(_config.copyWith(prAssignee: v)),
                    onReset: () => _resetField('pr_assignee'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Draft',
                    globalValue: appConfig.globalPRDraft.toString(),
                    overrideValue: _config.prDraft?.toString(),
                    options: const ['true', 'false'],
                    onChanged: (v) => _update(
                      _config.copyWith(prDraft: v != null ? v == 'true' : null),
                    ),
                    onReset: () => _resetField('pr_draft'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Prompt',
                    globalValue: appConfig.globalImplementPrompt.isEmpty
                        ? 'default'
                        : appConfig.globalImplementPrompt,
                    overrideValue: _config.developPromptId,
                    options: promptOptions,
                    onChanged: (v) =>
                        _update(_config.copyWith(developPromptId: v)),
                    onReset: () => _resetField('implement_prompt'),
                  ),
                ], accent: FeaturePalette.develop),
              ],
            ),
          );
        },
      ),
    );
  }

  Widget _sectionCard(String title, List<Widget> children, {Color? accent}) {
    return Card(
      margin: const EdgeInsets.only(bottom: 12),
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(10),
        side: accent != null
            ? BorderSide(color: accent, width: 2)
            : BorderSide.none,
      ),
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              title,
              style: const TextStyle(fontSize: 14, fontWeight: FontWeight.w600),
            ),
            const SizedBox(height: 12),
            ...children,
          ],
        ),
      ),
    );
  }
}
