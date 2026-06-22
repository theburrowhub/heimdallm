import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/models/config_model.dart';
import '../../core/platform/platform_services_provider.dart';
import '../../shared/widgets/autocomplete_chip_field.dart';
import '../../shared/widgets/toast.dart';
import '../agents/agents_screen.dart' show agentsProvider;
import '../dashboard/dashboard_providers.dart';
import 'config_providers.dart';

const _aiOptions = ['claude', 'gemini', 'codex'];

class ConfigScreen extends ConsumerStatefulWidget {
  const ConfigScreen({super.key});

  @override
  ConsumerState<ConfigScreen> createState() => _ConfigScreenState();
}

class _ConfigScreenState extends ConsumerState<ConfigScreen> {
  final _tokenController = TextEditingController();
  bool _obscureToken = true;
  bool _tokenFromGh = false; // true = auto-detected from gh CLI

  String _pollInterval = '5m';
  int _retentionDays = 90;
  IssueTrackingConfig _issueTracking = const IssueTrackingConfig();
  String? _issuePromptId;
  String? _developPromptId;

  // All known repos. Key = "org/repo", Value = per-repo settings.
  Map<String, RepoConfig> _repoConfigs = {};

  // Autonomous mode
  AutonomousConfig _autonomous = const AutonomousConfig();
  CircuitBreakerConfig _circuitBreaker = const CircuitBreakerConfig();
  late TextEditingController _devMaxTurnsController;
  late TextEditingController _devTimeoutController;
  late TextEditingController _claimLeaseController;
  late TextEditingController _perPr24hController;
  late TextEditingController _perRepoHrController;
  late TextEditingController _perIssue24hController;
  late TextEditingController _perIssueRepoHrController;
  late TextEditingController _perImplRepoHrController;
  bool _autonomousControllersInitialized = false;

  bool _initialized = false;
  bool _discovering = false;
  String? _discoverError;

  @override
  void initState() {
    super.initState();
    _devMaxTurnsController = TextEditingController();
    _devTimeoutController = TextEditingController();
    _claimLeaseController = TextEditingController();
    _perPr24hController = TextEditingController();
    _perRepoHrController = TextEditingController();
    _perIssue24hController = TextEditingController();
    _perIssueRepoHrController = TextEditingController();
    _perImplRepoHrController = TextEditingController();
    _detectToken().then((_) => _autoDiscoverRepos());
  }

  @override
  void dispose() {
    _tokenController.dispose();
    _devMaxTurnsController.dispose();
    _devTimeoutController.dispose();
    _claimLeaseController.dispose();
    _perPr24hController.dispose();
    _perRepoHrController.dispose();
    _perIssue24hController.dispose();
    _perIssueRepoHrController.dispose();
    _perImplRepoHrController.dispose();
    super.dispose();
  }

  Future<void> _detectToken() async {
    final platform = ref.read(platformServicesProvider);

    // 1. Try the full platform detection first (gh CLI on desktop, nothing on web).
    final detected = await platform.detectGitHubToken();
    if (!mounted) return;
    if (detected != null && detected.isNotEmpty) {
      setState(() {
        _tokenController.text = detected;
        _tokenFromGh = true; // detectGitHubToken prefers gh CLI
      });
      return;
    }

    // 2. Fall back to stored token / env var
    final stored =
        await platform.getStoredGitHubToken() ??
        platform.readEnv('GITHUB_TOKEN');
    if (!mounted || stored == null || stored.isEmpty) return;
    setState(() => _tokenController.text = stored);
  }

  void _initFromConfig(AppConfig config) {
    if (_initialized) return;
    _initialized = true;
    _pollInterval = config.pollInterval;
    _retentionDays = config.retentionDays;
    _repoConfigs = Map.from(config.repoConfigs);
    _issueTracking = config.issueTracking;
    _issuePromptId = config.globalIssuePrompt.isEmpty
        ? null
        : config.globalIssuePrompt;
    _developPromptId = config.globalImplementPrompt.isEmpty
        ? null
        : config.globalImplementPrompt;
    _initAutonomousFromConfig(config);
  }

  void _initAutonomousFromConfig(AppConfig config) {
    if (_autonomousControllersInitialized) return;
    _autonomousControllersInitialized = true;
    _autonomous = config.autonomous;
    _circuitBreaker = config.circuitBreaker;
    _devMaxTurnsController.text = config.autonomous.devMaxTurns.toString();
    _devTimeoutController.text = config.autonomous.devTimeout;
    _claimLeaseController.text = config.autonomous.claimLease;
    _perPr24hController.text = config.circuitBreaker.perPr24h.toString();
    _perRepoHrController.text = config.circuitBreaker.perRepoHr.toString();
    _perIssue24hController.text = config.circuitBreaker.perIssue24h.toString();
    _perIssueRepoHrController.text = config.circuitBreaker.perIssueRepoHr.toString();
    _perImplRepoHrController.text = config.circuitBreaker.perImplRepoHr.toString();
  }

  /// Auto-discovers repos from the user's PRs. Runs silently on init.
  Future<void> _autoDiscoverRepos() async {
    final token = _tokenController.text.trim();
    if (token.isEmpty) return;
    if (!mounted) return;
    setState(() {
      _discovering = true;
      _discoverError = null;
    });
    try {
      final discovered = await ref
          .read(platformServicesProvider)
          .discoverReposFromPRs(token);
      if (!mounted) return;
      setState(() {
        for (final repo in discovered) {
          // Keep existing toggle state; default new ones to monitored
          _repoConfigs.putIfAbsent(
            repo,
            () => const RepoConfig(prEnabled: true),
          );
        }
        _discovering = false;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _discovering = false;
        _discoverError = 'Could not discover repos: $e';
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final configAsync = ref.watch(configNotifierProvider);
    final daemonRunning = ref.watch(daemonHealthProvider).value ?? false;

    return Scaffold(
      appBar: AppBar(
        title: const Text('Settings'),
        leading: IconButton(
          icon: const Icon(Icons.arrow_back),
          onPressed: () => context.canPop() ? context.pop() : context.go('/'),
        ),
      ),
      body: configAsync.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (e, _) => Center(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Text(
                'Could not load config: $e',
                style: TextStyle(color: Colors.red.shade400, fontSize: 13),
              ),
              const SizedBox(height: 12),
              ElevatedButton(
                onPressed: () => ref.invalidate(configNotifierProvider),
                child: const Text('Retry'),
              ),
            ],
          ),
        ),
        data: (config) {
          _initFromConfig(config);
          return _buildForm(context, config, daemonRunning);
        },
      ),
    );
  }

  Widget _buildForm(
    BuildContext context,
    AppConfig config,
    bool daemonRunning,
  ) {
    return SingleChildScrollView(
      padding: const EdgeInsets.all(24),
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: double.infinity),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            if (!daemonRunning) _setupBanner(),
            _tokenSection(),
            const SizedBox(height: 20),
            if (!daemonRunning) ...[_repoSection(), const SizedBox(height: 20)],
            _pollSection(),
            const SizedBox(height: 20),
            _retentionSection(),
            const SizedBox(height: 20),
            _issueTrackingSection(config),
            const SizedBox(height: 20),
            _developSection(config),
            const SizedBox(height: 20),
            _autonomousSection(),
            const SizedBox(height: 20),
            _circuitBreakerSection(),
            const SizedBox(height: 28),
            _saveButton(context, config, daemonRunning),
          ],
        ),
      ),
    );
  }

  // ── Token ───────────────────────────────────────────────────────────────

  Widget _tokenSection() {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _sectionHeader('GitHub Token'),
        if (_tokenFromGh)
          _infoChip(
            Icons.check_circle,
            'Auto-detected from gh CLI',
            Colors.green,
          )
        else
          TextFormField(
            controller: _tokenController,
            obscureText: _obscureToken,
            decoration: InputDecoration(
              labelText: 'Personal Access Token',
              hintText: 'ghp_...',
              helperText: 'Required scopes: repo, read:org',
              border: const OutlineInputBorder(),
              suffixIcon: IconButton(
                icon: Icon(
                  _obscureToken ? Icons.visibility : Icons.visibility_off,
                ),
                onPressed: () => setState(() => _obscureToken = !_obscureToken),
              ),
            ),
          ),
        if (_tokenFromGh)
          TextButton.icon(
            icon: const Icon(Icons.edit, size: 14),
            label: const Text('Use a different token'),
            onPressed: () => setState(() {
              _tokenFromGh = false;
              _tokenController.clear();
            }),
          ),
      ],
    );
  }

  // ── Repos ───────────────────────────────────────────────────────────────

  Widget _repoSection() {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            _sectionHeaderInline('Repos with active PRs'),
            if (_discovering) ...[
              const SizedBox(width: 10),
              const SizedBox(
                width: 14,
                height: 14,
                child: CircularProgressIndicator(strokeWidth: 2),
              ),
            ],
          ],
        ),
        if (_discoverError != null) ...[
          const SizedBox(height: 6),
          _infoChip(Icons.warning_amber, _discoverError!, Colors.orange),
        ],
        const SizedBox(height: 8),
        if (_repoConfigs.isEmpty && !_discovering)
          const Padding(
            padding: EdgeInsets.symmetric(vertical: 8),
            child: Text(
              'No active PRs found assigned to you.',
              style: TextStyle(color: Colors.grey),
            ),
          )
        else
          _repoList(),
      ],
    );
  }

  Widget _repoList() {
    final sorted = _repoConfigs.keys.toList()..sort();
    return Column(children: sorted.map((repo) => _repoTile(repo)).toList());
  }

  Widget _repoTile(String repo) {
    final rc = _repoConfigs[repo]!;
    return Card(
      margin: const EdgeInsets.only(bottom: 4),
      child: ExpansionTile(
        leading: Switch(
          value: rc.isMonitored,
          onChanged: (v) => setState(() {
            _repoConfigs[repo] = rc.copyWith(prEnabled: v);
          }),
        ),
        title: Text(
          repo,
          style: TextStyle(
            color: rc.isMonitored ? null : Colors.grey,
            fontWeight: rc.isMonitored ? FontWeight.w600 : FontWeight.normal,
          ),
        ),
        subtitle: rc.hasAiOverride
            ? Text(
                'AI: ${rc.aiPrimary ?? "global"}',
                style: const TextStyle(fontSize: 12),
              )
            : null,
        childrenPadding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
        children: [
          const Divider(height: 1),
          const SizedBox(height: 10),
          const Text(
            'AI overrides for this repo',
            style: TextStyle(fontSize: 12, color: Colors.grey),
          ),
          const SizedBox(height: 8),
          Row(
            children: [
              Expanded(
                child: _overrideDropdown(
                  label: 'Primary agent',
                  value: rc.aiPrimary,
                  onChanged: (v) => setState(() {
                    _repoConfigs[repo] = rc.copyWith(aiPrimary: v);
                  }),
                ),
              ),
              const SizedBox(width: 12),
              Expanded(
                child: _overrideDropdown(
                  label: 'Fallback',
                  value: rc.aiFallback,
                  onChanged: (v) => setState(() {
                    _repoConfigs[repo] = rc.copyWith(aiFallback: v);
                  }),
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }

  Widget _overrideDropdown({
    required String label,
    required String? value,
    required ValueChanged<String?> onChanged,
  }) {
    return DropdownButtonFormField<String?>(
      // ignore: deprecated_member_use
      value: value,
      decoration: InputDecoration(
        labelText: label,
        border: const OutlineInputBorder(),
        isDense: true,
      ),
      items: [
        const DropdownMenuItem<String?>(
          value: null,
          child: Text('Global (no override)'),
        ),
        ..._aiOptions.map(
          (v) => DropdownMenuItem<String?>(value: v, child: Text(v)),
        ),
      ],
      onChanged: onChanged,
    );
  }

  // ── Poll interval ─────────────────────────────────────────────────────────

  Widget _pollSection() {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _sectionHeader('Polling'),
        DropdownButtonFormField<String>(
          // ignore: deprecated_member_use
          value: _pollInterval,
          decoration: const InputDecoration(
            labelText: 'Poll interval',
            helperText: 'How often to check GitHub for new review requests',
            border: OutlineInputBorder(),
          ),
          items: [
            '1m',
            '5m',
            '30m',
            '1h',
          ].map((v) => DropdownMenuItem(value: v, child: Text(v))).toList(),
          onChanged: (v) => setState(() => _pollInterval = v!),
        ),
      ],
    );
  }

  // ── Retention ────────────────────────────────────────────────────────────

  Widget _retentionSection() {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _sectionHeader('Retention'),
        TextFormField(
          initialValue: _retentionDays.toString(),
          decoration: const InputDecoration(
            labelText: 'Keep reviews for (days, 0 = forever)',
            border: OutlineInputBorder(),
          ),
          keyboardType: TextInputType.number,
          onChanged: (v) =>
              setState(() => _retentionDays = int.tryParse(v) ?? 90),
        ),
      ],
    );
  }

  // ── Issue tracking ──────────────────────────────────────────────────────

  Widget _issueTrackingSection(AppConfig config) {
    return _settingsCard('Issue Tracking', [
      SwitchListTile(
        title: const Text('Triage issues', style: TextStyle(fontSize: 13)),
        subtitle: const Text(
          'AI reviews and triages GitHub issues',
          style: TextStyle(fontSize: 11),
        ),
        dense: true,
        contentPadding: EdgeInsets.zero,
        value: _issueTracking.enabled,
        onChanged: (v) => setState(() {
          _issueTracking = _issueTracking.copyWith(enabled: v);
        }),
      ),
      if (_issueTracking.enabled) ...[
        const SizedBox(height: 8),
        AutocompleteChipField(
          label: 'Review-only labels',
          helper: 'Issues with these labels get an AI triage comment',
          selectedValues: _issueTracking.reviewOnlyLabels,
          availableOptions: const [],
          onChanged: (v) => setState(() {
            _issueTracking = _issueTracking.copyWith(reviewOnlyLabels: v ?? []);
          }),
        ),
        const SizedBox(height: 10),
        AutocompleteChipField(
          label: 'Refinement labels',
          helper: 'Issues with these labels get a deep implementation plan',
          selectedValues: _issueTracking.refinementLabels,
          availableOptions: const [],
          onChanged: (v) => setState(() {
            _issueTracking = _issueTracking.copyWith(refinementLabels: v ?? []);
          }),
        ),
        const SizedBox(height: 10),
        AutocompleteChipField(
          label: 'Skip labels',
          helper: 'Issues with these labels are ignored (highest priority)',
          selectedValues: _issueTracking.skipLabels,
          availableOptions: const [],
          onChanged: (v) => setState(() {
            _issueTracking = _issueTracking.copyWith(skipLabels: v ?? []);
          }),
        ),
        const SizedBox(height: 10),
        Row(
          children: [
            Expanded(
              child: DropdownButtonFormField<String>(
                initialValue: _issueTracking.filterMode,
                decoration: const InputDecoration(
                  labelText: 'Filter mode',
                  helperText: 'exclusive = AND, inclusive = OR',
                  border: OutlineInputBorder(),
                  isDense: true,
                ),
                items: ['exclusive', 'inclusive']
                    .map((v) => DropdownMenuItem(value: v, child: Text(v)))
                    .toList(),
                onChanged: (v) => setState(() {
                  _issueTracking = _issueTracking.copyWith(filterMode: v);
                }),
              ),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: DropdownButtonFormField<String>(
                initialValue: _issueTracking.defaultAction,
                decoration: const InputDecoration(
                  labelText: 'Default action',
                  helperText: 'When no label matches',
                  border: OutlineInputBorder(),
                  isDense: true,
                ),
                items: ['ignore', 'review_only']
                    .map((v) => DropdownMenuItem(value: v, child: Text(v)))
                    .toList(),
                onChanged: (v) => setState(() {
                  _issueTracking = _issueTracking.copyWith(defaultAction: v);
                }),
              ),
            ),
          ],
        ),
        const SizedBox(height: 10),
        AutocompleteChipField(
          label: 'Organizations',
          helper: 'Limit to issues from these orgs (empty = all monitored)',
          selectedValues: _issueTracking.organizations,
          availableOptions: _knownOrganizationOptions(config),
          onChanged: (v) => setState(() {
            _issueTracking = _issueTracking.copyWith(organizations: v ?? []);
          }),
        ),
        const SizedBox(height: 10),
        AutocompleteChipField(
          label: 'Assignees',
          helper: 'Only process issues assigned to these users',
          selectedValues: _issueTracking.assignees,
          availableOptions: config.knownGitHubUsers,
          onChanged: (v) => setState(() {
            _issueTracking = _issueTracking.copyWith(assignees: v ?? []);
          }),
        ),
        const SizedBox(height: 10),
        _agentDropdown(
          label: 'Issue Prompt',
          helper: 'Agent profile for issue triage',
          value: _issuePromptId,
          onChanged: (v) => setState(() => _issuePromptId = v),
        ),
      ],
    ]);
  }

  List<String> _knownOrganizationOptions(AppConfig config) {
    final orgs = <String>{...config.knownOrganizations};
    for (final repo in _repoConfigs.keys) {
      final slash = repo.indexOf('/');
      if (slash > 0) orgs.add(repo.substring(0, slash));
    }
    return orgs.where((o) => o.trim().isNotEmpty).toList()..sort();
  }

  List<String> _globalPRReviewers = [];
  List<String> _globalPRLabels = [];
  String _globalPRAssignee = '';
  bool _globalPRDraft = false;
  bool _developInitialized = false;

  void _initDevelopFromConfig(AppConfig config) {
    if (_developInitialized) return;
    _developInitialized = true;
    _globalPRReviewers = List.from(config.globalPRReviewers);
    _globalPRLabels = List.from(config.globalPRLabels);
    _globalPRAssignee = config.globalPRAssignee;
    _globalPRDraft = config.globalPRDraft;
  }

  Widget _developSection(AppConfig config) {
    _initDevelopFromConfig(config);
    final hasLabels = _issueTracking.developLabels.isNotEmpty;
    return _settingsCard('Develop', [
      SwitchListTile(
        title: const Text(
          'Auto-implement issues',
          style: TextStyle(fontSize: 13),
        ),
        subtitle: const Text(
          'Issues with develop labels get a branch + PR',
          style: TextStyle(fontSize: 11),
        ),
        dense: true,
        contentPadding: EdgeInsets.zero,
        value: hasLabels,
        onChanged: (v) => setState(() {
          if (v) {
            // Give it a default label so the section stays enabled
            if (_issueTracking.developLabels.isEmpty) {
              _issueTracking = _issueTracking.copyWith(
                developLabels: ['develop'],
              );
            }
          } else {
            _issueTracking = _issueTracking.copyWith(developLabels: []);
          }
        }),
      ),
      if (hasLabels) ...[
        const SizedBox(height: 6),
        AutocompleteChipField(
          label: 'Develop labels',
          helper: 'Issues with these labels get a branch + PR',
          selectedValues: _issueTracking.developLabels,
          availableOptions: const [],
          onChanged: (v) => setState(() {
            _issueTracking = _issueTracking.copyWith(developLabels: v ?? []);
          }),
        ),
        const SizedBox(height: 10),
        AutocompleteChipField(
          label: 'PR Reviewers',
          helper: 'GitHub usernames to request review',
          selectedValues: _globalPRReviewers,
          availableOptions: const [],
          onChanged: (v) => setState(() {
            _globalPRReviewers = v ?? [];
          }),
        ),
        const SizedBox(height: 10),
        AutocompleteChipField(
          label: 'PR Assignee',
          helper: 'GitHub username to assign PRs to',
          selectedValues: _globalPRAssignee.isEmpty ? [] : [_globalPRAssignee],
          availableOptions: const [],
          onChanged: (v) => setState(() {
            _globalPRAssignee = (v != null && v.isNotEmpty) ? v.first : '';
          }),
        ),
        const SizedBox(height: 10),
        AutocompleteChipField(
          label: 'PR Labels',
          helper: 'Labels to add to PRs',
          selectedValues: _globalPRLabels,
          availableOptions: const [],
          onChanged: (v) => setState(() {
            _globalPRLabels = v ?? [];
          }),
        ),
        const SizedBox(height: 10),
        Container(
          decoration: BoxDecoration(
            color: Theme.of(
              context,
            ).colorScheme.surfaceContainerHighest.withValues(alpha: 0.3),
            borderRadius: BorderRadius.circular(6),
          ),
          padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 2),
          child: SwitchListTile(
            title: const Text(
              'Create as draft',
              style: TextStyle(fontSize: 11),
            ),
            subtitle: Text(
              'PRs are created as drafts by default',
              style: TextStyle(fontSize: 10, color: Colors.grey.shade600),
            ),
            dense: true,
            contentPadding: EdgeInsets.zero,
            value: _globalPRDraft,
            onChanged: (v) => setState(() => _globalPRDraft = v),
          ),
        ),
        const SizedBox(height: 10),
        _agentDropdown(
          label: 'Develop Prompt',
          helper: 'Agent profile for auto-implementation',
          value: _developPromptId,
          onChanged: (v) => setState(() => _developPromptId = v),
        ),
      ],
    ]);
  }

  // ── Autonomous mode ────────────────────────────────────────────────────────

  Widget _autonomousSection() {
    return _settingsCard('Autonomous Mode', [
      SwitchListTile(
        title: const Text('Enable autonomous mode', style: TextStyle(fontSize: 13)),
        subtitle: const Text(
          'Allow Heimdallm to act autonomously on PRs and issues',
          style: TextStyle(fontSize: 11),
        ),
        dense: true,
        contentPadding: EdgeInsets.zero,
        value: _autonomous.enabled,
        onChanged: (v) => setState(() {
          _autonomous = _autonomous.copyWith(enabled: v);
        }),
      ),
      if (_autonomous.enabled) ...[
        const SizedBox(height: 8),
        SwitchListTile(
          title: const Text('Auto-merge approved PRs', style: TextStyle(fontSize: 13)),
          subtitle: const Text(
            'Automatically merge PRs that pass all checks',
            style: TextStyle(fontSize: 11),
          ),
          dense: true,
          contentPadding: EdgeInsets.zero,
          value: _autonomous.autoMerge,
          onChanged: (v) => setState(() {
            _autonomous = _autonomous.copyWith(autoMerge: v);
          }),
        ),
        const SizedBox(height: 8),
        DropdownButtonFormField<String>(
          // ignore: deprecated_member_use
          value: _autonomous.mergeMethod,
          decoration: const InputDecoration(
            labelText: 'Merge method',
            border: OutlineInputBorder(),
            isDense: true,
          ),
          items: ['squash', 'merge', 'rebase']
              .map((v) => DropdownMenuItem(value: v, child: Text(v)))
              .toList(),
          onChanged: (v) => setState(() {
            _autonomous = _autonomous.copyWith(mergeMethod: v);
          }),
        ),
        const SizedBox(height: 8),
        DropdownButtonFormField<String>(
          // ignore: deprecated_member_use
          value: _autonomous.devEffort,
          decoration: const InputDecoration(
            labelText: 'Dev effort',
            helperText: 'Effort level for autonomous development tasks',
            border: OutlineInputBorder(),
            isDense: true,
          ),
          items: ['low', 'medium', 'high', 'max']
              .map((v) => DropdownMenuItem(value: v, child: Text(v)))
              .toList(),
          onChanged: (v) => setState(() {
            _autonomous = _autonomous.copyWith(devEffort: v);
          }),
        ),
        const SizedBox(height: 8),
        TextFormField(
          controller: _devMaxTurnsController,
          decoration: const InputDecoration(
            labelText: 'Dev max turns (0 = unlimited)',
            border: OutlineInputBorder(),
            isDense: true,
          ),
          keyboardType: TextInputType.number,
          inputFormatters: [FilteringTextInputFormatter.digitsOnly],
          onChanged: (v) => setState(() {
            _autonomous = _autonomous.copyWith(devMaxTurns: int.tryParse(v) ?? 0);
          }),
        ),
        const SizedBox(height: 8),
        TextFormField(
          controller: _devTimeoutController,
          decoration: const InputDecoration(
            labelText: 'Dev timeout (e.g. 45m)',
            border: OutlineInputBorder(),
            isDense: true,
          ),
          onChanged: (v) => setState(() {
            _autonomous = _autonomous.copyWith(devTimeout: v);
          }),
        ),
        const SizedBox(height: 8),
        TextFormField(
          controller: _claimLeaseController,
          decoration: const InputDecoration(
            labelText: 'Claim lease (e.g. 2h)',
            helperText: 'How long to hold a claim on an issue before releasing',
            border: OutlineInputBorder(),
            isDense: true,
          ),
          onChanged: (v) => setState(() {
            _autonomous = _autonomous.copyWith(claimLease: v);
          }),
        ),
        const SizedBox(height: 8),
        SwitchListTile(
          title: const Text("Take others' tasks", style: TextStyle(fontSize: 13)),
          subtitle: const Text(
            'Claim issues assigned to other users',
            style: TextStyle(fontSize: 11),
          ),
          dense: true,
          contentPadding: EdgeInsets.zero,
          value: _autonomous.takeOthersTasks,
          onChanged: (v) => setState(() {
            _autonomous = _autonomous.copyWith(
              takeOthersTasks: v,
              reassignOnTake: v ? _autonomous.reassignOnTake : false,
            );
          }),
        ),
        if (_autonomous.takeOthersTasks) ...[
          const SizedBox(height: 4),
          Container(
            decoration: BoxDecoration(
              color: Theme.of(context)
                  .colorScheme
                  .surfaceContainerHighest
                  .withValues(alpha: 0.3),
              borderRadius: BorderRadius.circular(6),
            ),
            padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 2),
            child: SwitchListTile(
              title: const Text('Reassign on take', style: TextStyle(fontSize: 11)),
              subtitle: Text(
                'Reassign issue to the bot user when claiming',
                style: TextStyle(fontSize: 10, color: Colors.grey.shade600),
              ),
              dense: true,
              contentPadding: EdgeInsets.zero,
              value: _autonomous.reassignOnTake,
              onChanged: (v) => setState(() {
                _autonomous = _autonomous.copyWith(reassignOnTake: v);
              }),
            ),
          ),
        ],
      ],
    ]);
  }

  // ── Circuit breaker ────────────────────────────────────────────────────────

  Widget _circuitBreakerSection() {
    return _settingsCard('Circuit Breaker', [
      TextFormField(
        controller: _perPr24hController,
        decoration: const InputDecoration(
          labelText: 'PRs per 24h',
          border: OutlineInputBorder(),
          isDense: true,
        ),
        keyboardType: TextInputType.number,
        inputFormatters: [FilteringTextInputFormatter.digitsOnly],
        onChanged: (v) => setState(() {
          _circuitBreaker = _circuitBreaker.copyWith(
            perPr24h: int.tryParse(v) ?? _circuitBreaker.perPr24h,
          );
        }),
      ),
      const SizedBox(height: 8),
      TextFormField(
        controller: _perRepoHrController,
        decoration: const InputDecoration(
          labelText: 'PRs per repo per hour',
          border: OutlineInputBorder(),
          isDense: true,
        ),
        keyboardType: TextInputType.number,
        inputFormatters: [FilteringTextInputFormatter.digitsOnly],
        onChanged: (v) => setState(() {
          _circuitBreaker = _circuitBreaker.copyWith(
            perRepoHr: int.tryParse(v) ?? _circuitBreaker.perRepoHr,
          );
        }),
      ),
      const SizedBox(height: 8),
      TextFormField(
        controller: _perIssue24hController,
        decoration: const InputDecoration(
          labelText: 'Issues per 24h',
          border: OutlineInputBorder(),
          isDense: true,
        ),
        keyboardType: TextInputType.number,
        inputFormatters: [FilteringTextInputFormatter.digitsOnly],
        onChanged: (v) => setState(() {
          _circuitBreaker = _circuitBreaker.copyWith(
            perIssue24h: int.tryParse(v) ?? _circuitBreaker.perIssue24h,
          );
        }),
      ),
      const SizedBox(height: 8),
      TextFormField(
        controller: _perIssueRepoHrController,
        decoration: const InputDecoration(
          labelText: 'Issues per repo per hour',
          border: OutlineInputBorder(),
          isDense: true,
        ),
        keyboardType: TextInputType.number,
        inputFormatters: [FilteringTextInputFormatter.digitsOnly],
        onChanged: (v) => setState(() {
          _circuitBreaker = _circuitBreaker.copyWith(
            perIssueRepoHr: int.tryParse(v) ?? _circuitBreaker.perIssueRepoHr,
          );
        }),
      ),
      const SizedBox(height: 8),
      TextFormField(
        controller: _perImplRepoHrController,
        decoration: const InputDecoration(
          labelText: 'Implementations per repo per hour (autonomous dev)',
          border: OutlineInputBorder(),
          isDense: true,
        ),
        keyboardType: TextInputType.number,
        inputFormatters: [FilteringTextInputFormatter.digitsOnly],
        onChanged: (v) => setState(() {
          _circuitBreaker = _circuitBreaker.copyWith(
            perImplRepoHr: int.tryParse(v) ?? _circuitBreaker.perImplRepoHr,
          );
        }),
      ),
    ]);
  }

  Widget _agentDropdown({
    required String label,
    required String helper,
    required String? value,
    required ValueChanged<String?> onChanged,
  }) {
    final agents = ref.watch(agentsProvider).value ?? [];
    final effective = (value != null && agents.any((a) => a.id == value))
        ? value
        : null;
    return Container(
      decoration: BoxDecoration(
        color: Theme.of(
          context,
        ).colorScheme.surfaceContainerHighest.withValues(alpha: 0.3),
        borderRadius: BorderRadius.circular(6),
      ),
      padding: const EdgeInsets.all(10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Text(
                label,
                style: TextStyle(fontSize: 11, color: Colors.grey.shade500),
              ),
              const Spacer(),
              Text(
                'global',
                style: TextStyle(fontSize: 10, color: Colors.grey.shade600),
              ),
            ],
          ),
          const SizedBox(height: 6),
          DropdownButtonFormField<String?>(
            key: ValueKey('$label-$effective'),
            initialValue: effective,
            decoration: const InputDecoration(
              isDense: true,
              border: OutlineInputBorder(),
              contentPadding: EdgeInsets.symmetric(horizontal: 8, vertical: 8),
            ),
            style: const TextStyle(fontSize: 12),
            items: [
              const DropdownMenuItem<String?>(
                value: null,
                child: Text('default', style: TextStyle(fontSize: 12)),
              ),
              ...agents.map(
                (a) => DropdownMenuItem<String?>(
                  value: a.id,
                  child: Text(
                    a.name.isNotEmpty ? a.name : a.id,
                    style: const TextStyle(fontSize: 12),
                  ),
                ),
              ),
            ],
            onChanged: onChanged,
          ),
          Padding(
            padding: const EdgeInsets.only(top: 4),
            child: Text(
              helper,
              style: TextStyle(fontSize: 10, color: Colors.grey.shade600),
            ),
          ),
        ],
      ),
    );
  }

  Widget _settingsCard(String title, List<Widget> children) {
    return SizedBox(
      width: double.infinity,
      child: Card(
        margin: const EdgeInsets.only(bottom: 12),
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                title,
                style: const TextStyle(
                  fontWeight: FontWeight.w600,
                  fontSize: 15,
                ),
              ),
              const SizedBox(height: 12),
              ...children,
            ],
          ),
        ),
      ),
    );
  }

  // ── Save button ──────────────────────────────────────────────────────────

  Widget _saveButton(BuildContext context, AppConfig base, bool daemonRunning) {
    final isLoading = ref.watch(configNotifierProvider).isLoading;
    // NOTE: _buildConfig is called inside onPressed (not here at build time) so it
    // always reads the current state at the moment the user taps Save, avoiding
    // stale closure captures when setState and Save happen in the same frame.

    if (daemonRunning) {
      return SizedBox(
        width: double.infinity,
        child: ElevatedButton(
          onPressed: () async {
            final updated = _buildConfig(base);
            try {
              final token = _tokenController.text.trim();
              if (token.isNotEmpty && !_tokenFromGh) {
                await ref
                    .read(platformServicesProvider)
                    .storeGitHubToken(token);
                // Invalidate the cached token so the ApiClient re-reads it on the next request.
                ref.read(apiClientProvider).clearTokenCache();
              }
              await ref.read(configNotifierProvider.notifier).save(updated);
              if (context.mounted) showToast(context, 'Settings saved');
            } catch (e) {
              if (context.mounted) {
                showToast(context, 'Error: $e', isError: true);
              }
            }
          },
          child: const Text('Save'),
        ),
      );
    }

    return SizedBox(
      width: double.infinity,
      child: FilledButton.icon(
        icon: isLoading
            ? const SizedBox(
                width: 16,
                height: 16,
                child: CircularProgressIndicator(
                  strokeWidth: 2,
                  color: Colors.white,
                ),
              )
            : const Icon(Icons.rocket_launch),
        label: Text(isLoading ? 'Starting…' : 'Save and start Heimdallm'),
        onPressed: isLoading
            ? null
            : () async {
                final updated = _buildConfig(base);
                final token = _tokenController.text.trim();
                if (!_tokenFromGh && token.isEmpty) {
                  showToast(context, 'GitHub token is required', isError: true);
                  return;
                }
                await ref
                    .read(configNotifierProvider.notifier)
                    .saveAndStartDaemon(
                      token: _tokenFromGh
                          ? (_tokenController.text.trim())
                          : token,
                      config: updated,
                      daemonBinaryPath:
                          ref
                              .read(platformServicesProvider)
                              .defaultDaemonBinaryPath() ??
                          '',
                    );
                if (context.mounted) {
                  final state = ref.read(configNotifierProvider);
                  if (state.hasError) {
                    showToast(context, '${state.error}', isError: true);
                  } else {
                    ref.invalidate(daemonHealthProvider);
                    context.canPop() ? context.pop() : context.go('/');
                  }
                }
              },
      ),
    );
  }

  AppConfig _buildConfig(AppConfig base) => base.copyWith(
    pollInterval: _pollInterval,
    retentionDays: _retentionDays,
    repoConfigs: Map.from(_repoConfigs),
    issueTracking: _issueTracking,
    globalPRReviewers: _globalPRReviewers,
    globalPRLabels: _globalPRLabels,
    globalPRAssignee: _globalPRAssignee,
    globalPRDraft: _globalPRDraft,
    globalIssuePrompt: _issuePromptId ?? '',
    globalImplementPrompt: _developPromptId ?? '',
    autonomous: _autonomous,
    circuitBreaker: _circuitBreaker,
    // aiPrimary, aiFallback, reviewMode, agentConfigs managed in Agents tab
  );

  // ── Helpers ──────────────────────────────────────────────────────────────

  Widget _setupBanner() => Padding(
    padding: const EdgeInsets.only(bottom: 20),
    child: Container(
      width: double.infinity,
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: Colors.orange.shade700.withValues(alpha: 0.15),
        border: Border.all(color: Colors.orange.shade700),
        borderRadius: BorderRadius.circular(8),
      ),
      child: Row(
        children: [
          Icon(Icons.info_outline, color: Colors.orange.shade700),
          const SizedBox(width: 8),
          const Expanded(
            child: Text(
              'Heimdallm is not running. Configure and tap "Save and start".',
            ),
          ),
        ],
      ),
    ),
  );

  Widget _infoChip(IconData icon, String text, Color color) => Container(
    width: double.infinity,
    padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
    decoration: BoxDecoration(
      color: color.withValues(alpha: 0.12),
      border: Border.all(color: color.withValues(alpha: 0.4)),
      borderRadius: BorderRadius.circular(6),
    ),
    child: Row(
      children: [
        Icon(icon, size: 16, color: color),
        const SizedBox(width: 6),
        Expanded(
          child: Text(text, style: TextStyle(fontSize: 13, color: color)),
        ),
      ],
    ),
  );

  Widget _sectionHeader(String title) => Padding(
    padding: const EdgeInsets.only(bottom: 10),
    child: Text(
      title,
      style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 15),
    ),
  );

  Widget _sectionHeaderInline(String title) => Text(
    title,
    style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 15),
  );
}
