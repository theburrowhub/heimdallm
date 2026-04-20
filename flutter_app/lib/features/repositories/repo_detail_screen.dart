import 'dart:async';
import 'package:file_picker/file_picker.dart';
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

class RepoDetailScreen extends ConsumerStatefulWidget {
  final String repoName;
  const RepoDetailScreen({super.key, required this.repoName});

  @override
  ConsumerState<RepoDetailScreen> createState() => _RepoDetailScreenState();
}

class _RepoDetailScreenState extends ConsumerState<RepoDetailScreen> {
  RepoConfig _config = const RepoConfig();
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
    setState(() => _config = updated);
    _debounce?.cancel();
    _debounce = Timer(const Duration(milliseconds: 800), _autoSave);
  }

  Future<void> _autoSave() async {
    final current = ref.read(configNotifierProvider).valueOrNull;
    if (current == null) return;
    final updatedRepos = Map<String, RepoConfig>.from(current.repoConfigs);
    updatedRepos[widget.repoName] = _config;
    final updated = current.copyWith(repoConfigs: updatedRepos);
    try {
      await ref.read(configNotifierProvider.notifier).save(updated);
      if (mounted) showToast(context, 'Saved');
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  // ── Helpers ──────────────────────────────────────────────────────────────────

  String _joinList(List<String>? list) => list?.join(', ') ?? '';

  List<String>? _parseList(String? value) {
    if (value == null) return null;
    final parsed = value
        .split(',')
        .map((s) => s.trim())
        .where((s) => s.isNotEmpty)
        .toList();
    return parsed.isEmpty ? null : parsed;
  }

  // ── Section card ─────────────────────────────────────────────────────────────

  Widget _sectionCard(String title, List<Widget> children) {
    return Card(
      margin: const EdgeInsets.only(bottom: 12),
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(title,
                style: const TextStyle(
                    fontWeight: FontWeight.w600, fontSize: 15)),
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
          onPressed: () =>
              context.canPop() ? context.pop() : context.go('/'),
        ),
      ),
      body: configAsync.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (_, __) =>
            const Center(child: Text('Could not load config')),
        data: (appConfig) {
          _initFrom(appConfig);
          final prompts =
              ref.watch(agentsProvider).valueOrNull ?? <ReviewPrompt>[];

          return SingleChildScrollView(
            padding: const EdgeInsets.all(16),
            child: Column(
              children: [
                // ── Section 1: General ─────────────────────────────────
                _sectionCard('General', [
                  const Text('Local directory',
                      style: TextStyle(fontSize: 12, color: Colors.grey)),
                  const SizedBox(height: 4),
                  Text(
                    'When set, the AI agent runs inside this directory and can read all project files.',
                    style: TextStyle(
                        fontSize: 11, color: Colors.grey.shade600),
                  ),
                  const SizedBox(height: 8),
                  _LocalDirField(
                    value: _config.localDir ?? '',
                    onChanged: (dir) => _update(_config.copyWith(
                        localDir: dir.isEmpty ? null : dir)),
                  ),
                ]),

                // ── Section 2: PR Review ───────────────────────────────
                _sectionCard('PR Review', [
                  SwitchListTile(
                    title: const Text('Auto-review PRs',
                        style: TextStyle(fontSize: 13)),
                    dense: true,
                    contentPadding: EdgeInsets.zero,
                    value: _config.prEnabled ?? false,
                    onChanged: (v) =>
                        _update(_config.copyWith(prEnabled: v)),
                  ),
                  const SizedBox(height: 6),
                  OverrideDropdown(
                    label: 'Primary',
                    globalValue: appConfig.aiPrimary,
                    overrideValue: _config.aiPrimary,
                    options: const ['claude', 'gemini', 'codex'],
                    onChanged: (v) =>
                        _update(_config.copyWith(aiPrimary: v)),
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
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Review mode',
                    globalValue: appConfig.reviewMode,
                    overrideValue: _config.reviewMode,
                    options: const ['single', 'multi'],
                    onChanged: (v) =>
                        _update(_config.copyWith(reviewMode: v)),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Prompt',
                    globalValue: 'default',
                    overrideValue: _config.promptId,
                    options: prompts.map((p) => p.id).toList(),
                    onChanged: (v) =>
                        _update(_config.copyWith(promptId: v)),
                  ),
                ]),

                // ── Section 3: Issue Tracking ──────────────────────────
                _sectionCard('Issue Tracking', [
                  SwitchListTile(
                    title: const Text('Triage issues',
                        style: TextStyle(fontSize: 13)),
                    dense: true,
                    contentPadding: EdgeInsets.zero,
                    value: _config.itEnabled ?? false,
                    onChanged: (v) =>
                        _update(_config.copyWith(itEnabled: v)),
                  ),
                  const SizedBox(height: 6),
                  AutocompleteChipField(
                    label: 'Review-only labels',
                    helper: 'Issues with these labels get a review comment only',
                    selectedValues: _config.reviewOnlyLabels ?? appConfig.issueTracking.reviewOnlyLabels,
                    availableOptions: _repoLabels,
                    isOverridden: _config.reviewOnlyLabels != null,
                    globalHint: _joinList(appConfig.issueTracking.reviewOnlyLabels),
                    onChanged: (v) => _update(_config.copyWith(reviewOnlyLabels: v)),
                    onReset: () => _update(_config.copyWith(reviewOnlyLabels: null)),
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'Skip labels',
                    helper: 'Issues with these labels are ignored',
                    selectedValues: _config.skipLabels ?? appConfig.issueTracking.skipLabels,
                    availableOptions: _repoLabels,
                    isOverridden: _config.skipLabels != null,
                    globalHint: _joinList(appConfig.issueTracking.skipLabels),
                    onChanged: (v) => _update(_config.copyWith(skipLabels: v)),
                    onReset: () => _update(_config.copyWith(skipLabels: null)),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Filter mode',
                    globalValue:
                        appConfig.issueTracking.filterMode,
                    overrideValue: _config.issueFilterMode,
                    options: const ['exclusive', 'inclusive'],
                    onChanged: (v) => _update(
                        _config.copyWith(issueFilterMode: v)),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Default action',
                    globalValue:
                        appConfig.issueTracking.defaultAction,
                    overrideValue: _config.issueDefaultAction,
                    options: const ['ignore', 'review_only'],
                    onChanged: (v) => _update(
                        _config.copyWith(issueDefaultAction: v)),
                  ),
                  const SizedBox(height: 10),
                  OverrideTextField(
                    label: 'Organizations',
                    helper: 'GitHub org names to filter issues',
                    globalValue: _joinList(appConfig.issueTracking.organizations),
                    overrideValue: _config.issueOrganizations?.join(', '),
                    onChanged: (v) => _update(_config.copyWith(
                        issueOrganizations: v != null ? _parseList(v) : null)),
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'Assignees',
                    helper: 'Only process issues assigned to these users',
                    selectedValues: _config.issueAssignees ?? appConfig.issueTracking.assignees,
                    availableOptions: _repoCollaborators,
                    isOverridden: _config.issueAssignees != null,
                    globalHint: _joinList(appConfig.issueTracking.assignees),
                    onChanged: (v) => _update(_config.copyWith(issueAssignees: v)),
                    onReset: () => _update(_config.copyWith(issueAssignees: null)),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Prompt',
                    globalValue: 'default',
                    overrideValue: _config.issuePromptId,
                    options: prompts.map((p) => p.id).toList(),
                    onChanged: (v) =>
                        _update(_config.copyWith(issuePromptId: v)),
                  ),
                ]),

                // ── Section 4: Develop ─────────────────────────────────
                _sectionCard('Develop', [
                  SwitchListTile(
                    title: const Text('Auto-implement issues',
                        style: TextStyle(fontSize: 13)),
                    dense: true,
                    contentPadding: EdgeInsets.zero,
                    value: _config.devEnabled ?? false,
                    onChanged: (v) =>
                        _update(_config.copyWith(devEnabled: v)),
                  ),
                  const SizedBox(height: 6),
                  AutocompleteChipField(
                    label: 'Develop labels',
                    helper: 'Issues with these labels get a branch + PR',
                    selectedValues: _config.developLabels ?? appConfig.issueTracking.developLabels,
                    availableOptions: _repoLabels,
                    isOverridden: _config.developLabels != null,
                    globalHint: _joinList(appConfig.issueTracking.developLabels),
                    onChanged: (v) => _update(_config.copyWith(developLabels: v)),
                    onReset: () => _update(_config.copyWith(developLabels: null)),
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'PR Reviewers',
                    helper: 'GitHub usernames to request review',
                    selectedValues: _config.prReviewers ?? [],
                    availableOptions: _repoCollaborators,
                    isOverridden: _config.prReviewers != null,
                    onChanged: (v) => _update(_config.copyWith(prReviewers: v)),
                    onReset: () => _update(_config.copyWith(prReviewers: null)),
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'PR Assignee',
                    helper: 'GitHub username to assign to PRs',
                    selectedValues: _config.prAssignee != null ? [_config.prAssignee!] : [],
                    availableOptions: _repoCollaborators,
                    isOverridden: _config.prAssignee != null,
                    onChanged: (v) => _update(_config.copyWith(
                        prAssignee: v != null && v.isNotEmpty ? v.first : null)),
                    onReset: () => _update(_config.copyWith(prAssignee: null)),
                  ),
                  const SizedBox(height: 10),
                  AutocompleteChipField(
                    label: 'PR Labels',
                    helper: 'Labels to add to PRs',
                    selectedValues: _config.prLabels ?? [],
                    availableOptions: _repoLabels,
                    isOverridden: _config.prLabels != null,
                    onChanged: (v) => _update(_config.copyWith(prLabels: v)),
                    onReset: () => _update(_config.copyWith(prLabels: null)),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Draft',
                    globalValue: 'false',
                    overrideValue: _config.prDraft?.toString(),
                    options: const ['true', 'false'],
                    onChanged: (v) => _update(_config.copyWith(
                        prDraft: v != null ? v == 'true' : null)),
                  ),
                  const SizedBox(height: 10),
                  OverrideDropdown(
                    label: 'Prompt',
                    globalValue: 'default',
                    overrideValue: _config.developPromptId,
                    options: prompts.map((p) => p.id).toList(),
                    onChanged: (v) =>
                        _update(_config.copyWith(developPromptId: v)),
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
  final ValueChanged<String> onChanged;
  const _LocalDirField({required this.value, required this.onChanged});

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
  void dispose() {
    _ctrl.dispose();
    super.dispose();
  }

  Future<void> _pick() async {
    final dir = await FilePicker.platform.getDirectoryPath(
      dialogTitle: 'Select local repository directory',
      lockParentWindow: true,
    );
    if (dir == null) return;
    setState(() => _ctrl.text = dir);
    widget.onChanged(dir);
  }

  @override
  Widget build(BuildContext context) {
    return Row(children: [
      Expanded(
        child: TextFormField(
          controller: _ctrl,
          decoration: const InputDecoration(
            hintText: '/path/to/local/repo',
            border: OutlineInputBorder(),
            isDense: true,
          ),
          onChanged: widget.onChanged,
        ),
      ),
      const SizedBox(width: 8),
      OutlinedButton.icon(
        icon: const Icon(Icons.folder_open, size: 16),
        label: const Text('Browse'),
        onPressed: _pick,
        style: OutlinedButton.styleFrom(
          padding:
              const EdgeInsets.symmetric(horizontal: 12, vertical: 12),
        ),
      ),
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
    ]);
  }
}
