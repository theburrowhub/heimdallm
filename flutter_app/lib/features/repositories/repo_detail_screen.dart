import 'dart:async';
import 'package:file_picker/file_picker.dart';
import 'package:flutter/foundation.dart' show kIsWeb;
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/models/config_model.dart';
import '../../core/models/agent.dart';
import '../../shared/widgets/autocomplete_chip_field.dart';
import '../../shared/widgets/override_field.dart';
import '../../shared/widgets/toast.dart';
import '../agents/agents_screen.dart' show agentsProvider;
import '../config/config_providers.dart';
import '../dashboard/dashboard_providers.dart';
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
  // ignore: unused_field
  RepoConfig _previousConfig = const RepoConfig();
  bool _initialized = false;
  Timer? _debounce;
  List<String> _repoLabels = [];
  List<String> _repoCollaborators = [];

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
    _loadRepoMeta();
  }

  Future<void> _loadRepoMeta() async {
    final api = ref.read(apiClientProvider);
    try {
      final results = await Future.wait([
        api.fetchRepoLabels(widget.repoName),
        api.fetchRepoCollaborators(widget.repoName),
      ]);
      if (mounted) {
        setState(() {
          _repoLabels = results[0];
          _repoCollaborators = results[1];
        });
      }
    } catch (_) {
      // Non-fatal — autocomplete just won't have suggestions
    }
  }

  void _update(RepoConfig updated) {
    final previous = _config;
    setState(() => _config = updated);
    _debounce?.cancel();
    _debounce = Timer(
      const Duration(milliseconds: 800),
      () => _autoSave(previous),
    );
  }

  Future<void> _autoSave(RepoConfig previous) async {
    final api = ref.read(apiClientProvider);
    try {
      final repoDiff = computeRepoDiff(previous, _config);
      Map<String, dynamic>? lastResponse;
      if (repoDiff.isNotEmpty) {
        lastResponse = await api.patchRepoConfig(widget.repoName, repoDiff);
      }

      final monitoringChanged = previous.isMonitored != _config.isMonitored;
      if (monitoringChanged) {
        final current = ref.read(configNotifierProvider).value;
        if (current != null) {
          final updatedRepos = Map<String, RepoConfig>.from(
            current.repoConfigs,
          );
          updatedRepos[widget.repoName] = _config;
          final monitored =
              updatedRepos.entries
                  .where((e) => e.value.isMonitored)
                  .map((e) => e.key)
                  .toList()
                ..sort();
          final nonMonitored =
              updatedRepos.entries
                  .where((e) => !e.value.isMonitored)
                  .map((e) => e.key)
                  .toList()
                ..sort();
          lastResponse = await api.patchConfig({
            'github': {
              'repositories': monitored,
              'non_monitored': nonMonitored,
            },
          });
        }
      }

      if (lastResponse != null) {
        ref
            .read(configNotifierProvider.notifier)
            .updateFromServer(lastResponse);
      }
      _previousConfig = _config;
      if (mounted) showToast(context, 'Saved');
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

  String _joinList(List<String>? list) => list?.join(', ') ?? '';

  List<String> _mergeOptions(List<String> first, List<String> second) =>
      ({...first, ...second}.where((v) => v.trim().isNotEmpty).toList()
        ..sort());

  // ── Section card ─────────────────────────────────────────────────────────────

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
              style: TextStyle(
                fontWeight: FontWeight.w600,
                fontSize: 15,
                color: accent,
              ),
            ),
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
        error: (_, _) => const Center(child: Text('Could not load config')),
        data: (appConfig) {
          _initFrom(appConfig);
          final prompts =
              ref.watch(agentsProvider).value ?? <ReviewPrompt>[];
          final orgName = widget.repoName.contains('/')
              ? widget.repoName.split('/').first
              : widget.repoName;
          final orgConfig = appConfig.orgConfigs[orgName];
          String source(bool hasOrgValue) =>
              hasOrgValue ? 'org: $orgName' : 'global';

          return SingleChildScrollView(
            padding: const EdgeInsets.all(16),
            child: Column(
              children: [
                // ── Section 1: General ─────────────────────────────────
                _sectionCard('General', [
                  const Text(
                    'Local directory',
                    style: TextStyle(fontSize: 12, color: Colors.grey),
                  ),
                  const SizedBox(height: 4),
                  Text(
                    'When set, the AI agent runs inside this directory and can read all project files.',
                    style: TextStyle(fontSize: 11, color: Colors.grey.shade600),
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
                ]),

                // ── Section 2: PR Review ───────────────────────────────
                _sectionCard('PR Review', [
                  Row(
                    children: [
                      const Expanded(
                        child: Text(
                          'Auto-review PRs',
                          style: TextStyle(fontSize: 13),
                        ),
                      ),
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
                    label: 'Primary',
                    globalValue: orgConfig?.aiPrimary ?? appConfig.aiPrimary,
                    inheritedLabel: source(orgConfig?.aiPrimary != null),
                    overrideValue: _config.aiPrimary,
                    options: const ['claude', 'gemini', 'codex'],
                    onChanged: (v) => _update(_config.copyWith(aiPrimary: v)),
                    onReset: () => _resetField('primary'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Fallback',
                    globalValue:
                        (orgConfig?.aiFallback ?? appConfig.aiFallback).isEmpty
                        ? 'none'
                        : (orgConfig?.aiFallback ?? appConfig.aiFallback),
                    inheritedLabel: source(orgConfig?.aiFallback != null),
                    overrideValue: _config.aiFallback,
                    options: const ['claude', 'gemini', 'codex'],
                    onChanged: (v) => _update(_config.copyWith(aiFallback: v)),
                    onReset: () => _resetField('fallback'),
                  ),
                  const SizedBox(height: 10),
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
                ], accent: FeaturePalette.prReview),

                // ── Section: Organizations (org-inherited review policy) ─
                _sectionCard('Organizations', [
                  OverrideDropdown(
                    label: 'No aprobar PRs con issues',
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
                ]),

                // ── Section 3: Issue Tracking ──────────────────────────
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
                            orgConfig?.itEnabled ??
                            appConfig.issueTracking.enabled,
                        onChanged: (v) =>
                            _update(_config.copyWith(itEnabled: v)),
                      ),
                    ],
                  ),
                  const SizedBox(height: 6),
                  AutocompleteChipField(
                    label: 'Review-only labels',
                    helper:
                        'Issues with these labels get a review comment only',
                    selectedValues:
                        _config.reviewOnlyLabels ??
                        orgConfig?.reviewOnlyLabels ??
                        appConfig.issueTracking.reviewOnlyLabels,
                    availableOptions: _repoLabels,
                    isOverridden: _config.reviewOnlyLabels != null,
                    inheritedLabel: source(orgConfig?.reviewOnlyLabels != null),
                    globalHint: _joinList(
                      orgConfig?.reviewOnlyLabels ??
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
                        orgConfig?.refinementLabels ??
                        appConfig.issueTracking.refinementLabels,
                    availableOptions: _repoLabels,
                    isOverridden: _config.refinementLabels != null,
                    inheritedLabel: source(orgConfig?.refinementLabels != null),
                    globalHint: _joinList(
                      orgConfig?.refinementLabels ??
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
                    helper: 'Issues with these labels are ignored',
                    selectedValues:
                        _config.skipLabels ??
                        orgConfig?.skipLabels ??
                        appConfig.issueTracking.skipLabels,
                    availableOptions: _repoLabels,
                    isOverridden: _config.skipLabels != null,
                    inheritedLabel: source(orgConfig?.skipLabels != null),
                    globalHint: _joinList(
                      orgConfig?.skipLabels ??
                          appConfig.issueTracking.skipLabels,
                    ),
                    onChanged: (v) => _update(_config.copyWith(skipLabels: v)),
                    onReset: () => _resetField('issue_tracking/skip_labels'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Filter mode',
                    globalValue:
                        orgConfig?.issueFilterMode ??
                        appConfig.issueTracking.filterMode,
                    inheritedLabel: source(orgConfig?.issueFilterMode != null),
                    overrideValue: _config.issueFilterMode,
                    options: const ['exclusive', 'inclusive'],
                    onChanged: (v) =>
                        _update(_config.copyWith(issueFilterMode: v)),
                    onReset: () => _resetField('issue_tracking/filter_mode'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Default action',
                    globalValue:
                        orgConfig?.issueDefaultAction ??
                        appConfig.issueTracking.defaultAction,
                    inheritedLabel: source(
                      orgConfig?.issueDefaultAction != null,
                    ),
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
                        orgConfig?.issueOrganizations ??
                        appConfig.issueTracking.organizations,
                    availableOptions: appConfig.knownOrganizations,
                    isOverridden: _config.issueOrganizations != null,
                    inheritedLabel: source(
                      orgConfig?.issueOrganizations != null,
                    ),
                    globalHint: _joinList(
                      orgConfig?.issueOrganizations ??
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
                        orgConfig?.issueAssignees ??
                        appConfig.issueTracking.assignees,
                    availableOptions: _mergeOptions(
                      _repoCollaborators,
                      appConfig.knownGitHubUsers,
                    ),
                    isOverridden: _config.issueAssignees != null,
                    inheritedLabel: source(orgConfig?.issueAssignees != null),
                    globalHint: _joinList(
                      orgConfig?.issueAssignees ??
                          appConfig.issueTracking.assignees,
                    ),
                    onChanged: (v) =>
                        _update(_config.copyWith(issueAssignees: v)),
                    onReset: () => _resetField('issue_tracking/assignees'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Prompt',
                    globalValue:
                        orgConfig?.issuePromptId ??
                        (appConfig.globalIssuePrompt.isEmpty
                            ? 'default'
                            : appConfig.globalIssuePrompt),
                    inheritedLabel: source(orgConfig?.issuePromptId != null),
                    overrideValue: _config.issuePromptId,
                    options: prompts.map((p) => p.id).toList(),
                    onChanged: (v) =>
                        _update(_config.copyWith(issuePromptId: v)),
                    onReset: () => _resetField('issue_prompt'),
                  ),
                ], accent: FeaturePalette.issueTracking),

                // ── Section: Pipeline ──────────────────────────────────
                _sectionCard('Pipeline', [
                  OverrideTextField(
                    label: 'Triage owner',
                    globalValue:
                        orgConfig?.triageOwner ?? appConfig.globalTriageOwner,
                    inheritedLabel: source(orgConfig?.triageOwner != null),
                    overrideValue: _config.triageOwner,
                    onChanged: (v) => _update(_config.copyWith(triageOwner: v)),
                    onReset: () => _resetField('triage_owner'),
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
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Auto-promote triage',
                    globalValue:
                        (orgConfig?.autoPromoteTriage ??
                                appConfig.globalAutoPromoteTriage ??
                                false)
                            .toString(),
                    inheritedLabel: source(orgConfig?.autoPromoteTriage != null),
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
                        (orgConfig?.autoPromoteRefinement ??
                                appConfig.globalAutoPromoteRefinement ??
                                false)
                            .toString(),
                    inheritedLabel: source(
                      orgConfig?.autoPromoteRefinement != null,
                    ),
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
                    globalValue:
                        (orgConfig?.generatePRDescription ??
                                appConfig.globalGeneratePRDescription)
                            .toString(),
                    inheritedLabel: source(
                      orgConfig?.generatePRDescription != null,
                    ),
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

                // ── Section 4: Develop ─────────────────────────────────
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
                        value:
                            _config.devEnabled ??
                            orgConfig?.devEnabled ??
                            false,
                        onChanged: (v) =>
                            _update(_config.copyWith(devEnabled: v)),
                      ),
                    ],
                  ),
                  const SizedBox(height: 6),
                  AutocompleteChipField(
                    label: 'Develop labels',
                    helper: 'Issues with these labels get a branch + PR',
                    selectedValues:
                        _config.developLabels ??
                        orgConfig?.developLabels ??
                        appConfig.issueTracking.developLabels,
                    availableOptions: _repoLabels,
                    isOverridden: _config.developLabels != null,
                    inheritedLabel: source(orgConfig?.developLabels != null),
                    globalHint: _joinList(
                      orgConfig?.developLabels ??
                          appConfig.issueTracking.developLabels,
                    ),
                    onChanged: (v) =>
                        _update(_config.copyWith(developLabels: v)),
                    onReset: () => _resetField('issue_tracking/develop_labels'),
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'PR Reviewers',
                    helper: 'GitHub usernames to request review',
                    selectedValues:
                        _config.prReviewers ??
                        orgConfig?.prReviewers ??
                        appConfig.globalPRReviewers,
                    availableOptions: _repoCollaborators,
                    isOverridden: _config.prReviewers != null,
                    inheritedLabel: source(orgConfig?.prReviewers != null),
                    globalHint: _joinList(
                      orgConfig?.prReviewers ?? appConfig.globalPRReviewers,
                    ),
                    onChanged: (v) => _update(_config.copyWith(prReviewers: v)),
                    onReset: () => _resetField('pr_reviewers'),
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'PR Assignee',
                    helper: 'GitHub username to assign to PRs',
                    selectedValues: _config.prAssignee != null
                        ? [_config.prAssignee!]
                        : (orgConfig?.prAssignee != null
                              ? [orgConfig!.prAssignee!]
                              : (appConfig.globalPRAssignee.isNotEmpty
                                    ? [appConfig.globalPRAssignee]
                                    : [])),
                    availableOptions: _repoCollaborators,
                    isOverridden: _config.prAssignee != null,
                    inheritedLabel: source(orgConfig?.prAssignee != null),
                    globalHint:
                        orgConfig?.prAssignee ?? appConfig.globalPRAssignee,
                    onChanged: (v) => _update(
                      _config.copyWith(
                        prAssignee: v != null && v.isNotEmpty ? v.first : null,
                      ),
                    ),
                    onReset: () => _resetField('pr_assignee'),
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'PR Labels',
                    helper: 'Labels to add to PRs',
                    selectedValues:
                        _config.prLabels ??
                        orgConfig?.prLabels ??
                        appConfig.globalPRLabels,
                    availableOptions: _repoLabels,
                    isOverridden: _config.prLabels != null,
                    inheritedLabel: source(orgConfig?.prLabels != null),
                    globalHint: _joinList(
                      orgConfig?.prLabels ?? appConfig.globalPRLabels,
                    ),
                    onChanged: (v) => _update(_config.copyWith(prLabels: v)),
                    onReset: () => _resetField('pr_labels'),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Draft',
                    globalValue: (orgConfig?.prDraft ?? appConfig.globalPRDraft)
                        .toString(),
                    inheritedLabel: source(orgConfig?.prDraft != null),
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
                    globalValue:
                        orgConfig?.developPromptId ??
                        (appConfig.globalImplementPrompt.isEmpty
                            ? 'default'
                            : appConfig.globalImplementPrompt),
                    inheritedLabel: source(orgConfig?.developPromptId != null),
                    overrideValue: _config.developPromptId,
                    options: prompts.map((p) => p.id).toList(),
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
          child: Text(
            widget.sourceLabel,
            style: TextStyle(fontSize: 10, color: Colors.grey.shade600),
          ),
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
                child: Text(
                  'Leave empty to use the auto-detected path above. '
                  'Type a different path to override.',
                  style: TextStyle(fontSize: 11, color: Colors.blue.shade400),
                ),
              ),
            ],
          ),
        ],
      ],
    );
  }
}
