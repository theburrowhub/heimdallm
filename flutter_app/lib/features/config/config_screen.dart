import 'package:flutter/material.dart';
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

// Poll-interval bounds, mirrored from the daemon's config.ValidatePollInterval
// (daemon/internal/config/config.go: minPollInterval=1m, maxPollInterval=24h).
// The daemon is authoritative — these power a client-side UX guard so the
// operator gets immediate feedback instead of a round-trip 400.
const _minPollInterval = Duration(minutes: 1);
const _maxPollInterval = Duration(hours: 24);
const _pollIntervalSuggestions = ['1m', '5m', '30m', '1h'];

// Intentionally covers only the units that make sense for a [1m, 24h] poll
// interval. Sub-second units are accepted (so the parser stays a faithful
// subset of Go) but always fall below the 1m floor. Go's day-like 'd' is not a
// real unit and is correctly rejected; the Greek-mu micro variant ('μs',
// U+03BC) is not special-cased — irrelevant at this scale, and the daemon
// remains authoritative either way.
const _durationUnitMicros = <String, double>{
  'ns': 0.001,
  'us': 1,
  'µs': 1,
  'ms': 1000,
  's': 1000 * 1000.0,
  'm': 60 * 1000 * 1000.0,
  'h': 60 * 60 * 1000 * 1000.0,
};

// Order matters: multi-char units (ns, ms, µs/us) must precede the bare 's'
// so the regex consumes "ms" rather than matching "m" then leaving "s".
final _goDurationToken = RegExp(r'([0-9]*\.?[0-9]+)(ns|µs|us|ms|s|m|h)');

/// Parses the common subset of Go's `time.ParseDuration` that operators use for
/// `poll_interval` (e.g. `5m`, `90m`, `1h30m`, `1.5h`, `300s`) into a
/// [Duration], or returns null if the string is not a clean sequence of
/// number+unit tokens. This is a UX guard, not a full reimplementation — the
/// daemon still validates authoritatively, so anything missed here surfaces as
/// a backend error on save.
Duration? _parseGoDuration(String raw) {
  final s = raw.trim();
  if (s.isEmpty) return null;
  var consumed = 0;
  var micros = 0.0;
  for (final m in _goDurationToken.allMatches(s)) {
    if (m.start != consumed) return null; // junk between tokens
    consumed = m.end;
    final value = double.tryParse(m.group(1)!);
    final mult = _durationUnitMicros[m.group(2)];
    if (value == null || mult == null) return null;
    // Like Go's time.ParseDuration, repeated units accumulate
    // (e.g. "1h30m" → 90m, and "1h2h" → 3h).
    micros += value * mult;
  }
  if (consumed != s.length) return null; // leading/trailing junk
  return Duration(microseconds: micros.round());
}

/// Form validator for `poll_interval`: parseable as a duration within
/// [1m, 24h], mirroring the daemon. Returns an error string for the field, or
/// null when acceptable.
String? validatePollInterval(String? raw) {
  final s = (raw ?? '').trim();
  if (s.isEmpty) return 'Required (e.g. 5m, 90m, 1h30m)';
  final d = _parseGoDuration(s);
  if (d == null) return 'Invalid duration (e.g. 5m, 90m, 1h30m)';
  if (d < _minPollInterval || d > _maxPollInterval) {
    return 'Must be between 1m and 24h';
  }
  return null;
}

class ConfigScreen extends ConsumerStatefulWidget {
  const ConfigScreen({super.key});

  @override
  ConsumerState<ConfigScreen> createState() => _ConfigScreenState();
}

class _ConfigScreenState extends ConsumerState<ConfigScreen> {
  final _tokenController = TextEditingController();
  final _pollController = TextEditingController();
  final _pollFieldKey = GlobalKey<FormFieldState<String>>();
  bool _obscureToken = true;
  bool _tokenFromGh = false; // true = auto-detected from gh CLI

  String _pollInterval = '5m';
  int _retentionDays = 90;
  IssueTrackingConfig _issueTracking = const IssueTrackingConfig();
  String? _issuePromptId;
  String? _developPromptId;

  // All known repos. Key = "org/repo", Value = per-repo settings.
  Map<String, RepoConfig> _repoConfigs = {};

  bool _initialized = false;
  bool _discovering = false;
  String? _discoverError;

  @override
  void initState() {
    super.initState();
    _detectToken().then((_) => _autoDiscoverRepos());
  }

  @override
  void dispose() {
    _tokenController.dispose();
    _pollController.dispose();
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
    _pollController.text = config.pollInterval;
    _retentionDays = config.retentionDays;
    _repoConfigs = Map.from(config.repoConfigs);
    _issueTracking = config.issueTracking;
    _issuePromptId = config.globalIssuePrompt.isEmpty
        ? null
        : config.globalIssuePrompt;
    _developPromptId = config.globalImplementPrompt.isEmpty
        ? null
        : config.globalImplementPrompt;
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
            _aiSection(config),
            const SizedBox(height: 20),
            _issueTrackingSection(config),
            const SizedBox(height: 20),
            _pipelineSection(config),
            const SizedBox(height: 20),
            _developSection(config),
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
        TextFormField(
          key: _pollFieldKey,
          controller: _pollController,
          decoration: const InputDecoration(
            labelText: 'Poll interval',
            helperText:
                'How often to check GitHub for new review requests '
                '(any duration from 1m to 24h, e.g. 5m, 90m, 1h30m)',
            border: OutlineInputBorder(),
          ),
          autovalidateMode: AutovalidateMode.onUserInteraction,
          validator: validatePollInterval,
          onChanged: (v) => setState(() => _pollInterval = v.trim()),
        ),
        const SizedBox(height: 8),
        Wrap(
          spacing: 8,
          children: _pollIntervalSuggestions
              .map((v) => ActionChip(label: Text(v), onPressed: () => _pickPollInterval(v)))
              .toList(),
        ),
      ],
    );
  }

  /// Applies a quick-pick suggestion. Sets the controller value (cursor at end,
  /// since a bare `.text =` would reset it to 0) and re-runs the field
  /// validator — a programmatic change does not count as user interaction under
  /// [AutovalidateMode.onUserInteraction], so without this an error from prior
  /// typing would linger even though the chosen value is valid.
  void _pickPollInterval(String v) {
    setState(() {
      _pollInterval = v;
      _pollController.value = TextEditingValue(
        text: v,
        selection: TextSelection.collapsed(offset: v.length),
      );
    });
    _pollFieldKey.currentState?.validate();
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
  bool _globalNeverApproveWithIssues = false;
  String _globalTriageOwner = '';
  String _globalCloneDir = '';
  bool _globalAutoPromoteTriage = false;
  bool _globalAutoPromoteRefinement = false;
  bool _globalGeneratePRDescription = false;
  bool _developInitialized = false;

  String _aiPrimary = 'claude';
  String _aiFallback = '';
  String _reviewMode = 'single';
  bool _aiInitialized = false;

  void _initAiFromConfig(AppConfig config) {
    if (_aiInitialized) return;
    _aiInitialized = true;
    _aiPrimary = config.aiPrimary.isEmpty ? 'claude' : config.aiPrimary;
    _aiFallback = config.aiFallback;
    _reviewMode = config.reviewMode.isEmpty ? 'single' : config.reviewMode;
  }

  Widget _aiSection(AppConfig config) {
    _initAiFromConfig(config);
    return _settingsCard('AI defaults', [
      DropdownButtonFormField<String>(
        initialValue: _aiPrimary,
        decoration: const InputDecoration(
          labelText: 'Primary agent',
          border: OutlineInputBorder(),
          isDense: true,
        ),
        items: const ['claude', 'gemini', 'codex']
            .map((v) => DropdownMenuItem(value: v, child: Text(v)))
            .toList(),
        onChanged: (v) => setState(() => _aiPrimary = v ?? 'claude'),
      ),
      const SizedBox(height: 12),
      DropdownButtonFormField<String>(
        initialValue: _aiFallback.isEmpty ? 'none' : _aiFallback,
        decoration: const InputDecoration(
          labelText: 'Fallback agent',
          border: OutlineInputBorder(),
          isDense: true,
        ),
        items: const ['none', 'claude', 'gemini', 'codex']
            .map((v) => DropdownMenuItem(value: v, child: Text(v)))
            .toList(),
        onChanged: (v) =>
            setState(() => _aiFallback = (v == null || v == 'none') ? '' : v),
      ),
      const SizedBox(height: 12),
      DropdownButtonFormField<String>(
        initialValue: _reviewMode,
        decoration: const InputDecoration(
          labelText: 'Feedback mode',
          helperText:
              'single = one consolidated review; multi = one comment per issue',
          border: OutlineInputBorder(),
          isDense: true,
        ),
        items: const ['single', 'multi']
            .map((v) => DropdownMenuItem(value: v, child: Text(v)))
            .toList(),
        onChanged: (v) => setState(() => _reviewMode = v ?? 'single'),
      ),
    ]);
  }

  void _initDevelopFromConfig(AppConfig config) {
    if (_developInitialized) return;
    _developInitialized = true;
    _globalPRReviewers = List.from(config.globalPRReviewers);
    _globalPRLabels = List.from(config.globalPRLabels);
    _globalPRAssignee = config.globalPRAssignee;
    _globalPRDraft = config.globalPRDraft;
    _globalNeverApproveWithIssues = config.globalNeverApproveWithIssues;
    _globalTriageOwner = config.globalTriageOwner;
    _globalCloneDir = config.globalCloneDir;
    _globalAutoPromoteTriage = config.globalAutoPromoteTriage ?? false;
    _globalAutoPromoteRefinement = config.globalAutoPromoteRefinement ?? false;
    _globalGeneratePRDescription = config.globalGeneratePRDescription;
  }

  Widget _globalSwitchTile(
    String title,
    String subtitle,
    bool value,
    ValueChanged<bool> onChanged,
  ) {
    return Container(
      decoration: BoxDecoration(
        color: Theme.of(
          context,
        ).colorScheme.surfaceContainerHighest.withValues(alpha: 0.3),
        borderRadius: BorderRadius.circular(6),
      ),
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 2),
      child: SwitchListTile(
        title: Text(title, style: const TextStyle(fontSize: 11)),
        subtitle: Text(
          subtitle,
          style: TextStyle(fontSize: 10, color: Colors.grey.shade600),
        ),
        dense: true,
        contentPadding: EdgeInsets.zero,
        value: value,
        onChanged: (v) => setState(() => onChanged(v)),
      ),
    );
  }

  Widget _pipelineSection(AppConfig config) {
    _initDevelopFromConfig(config);
    return _settingsCard('Pipeline', [
      TextFormField(
        initialValue: _globalTriageOwner,
        decoration: const InputDecoration(
          labelText: 'Triage owner',
          hintText: 'GitHub username that owns triaged issues',
          isDense: true,
        ),
        onChanged: (v) => _globalTriageOwner = v.trim(),
      ),
      const SizedBox(height: 12),
      TextFormField(
        initialValue: _globalCloneDir,
        decoration: const InputDecoration(
          labelText: 'Clone directory',
          hintText: 'Base directory for managed repo clones',
          isDense: true,
        ),
        onChanged: (v) => _globalCloneDir = v.trim(),
      ),
      const SizedBox(height: 12),
      _globalSwitchTile(
        'Auto-promote triage',
        'Promote triaged issues to refinement automatically',
        _globalAutoPromoteTriage,
        (v) => _globalAutoPromoteTriage = v,
      ),
      const SizedBox(height: 10),
      _globalSwitchTile(
        'Auto-promote refinement',
        'Promote refined issues to develop automatically',
        _globalAutoPromoteRefinement,
        (v) => _globalAutoPromoteRefinement = v,
      ),
      const SizedBox(height: 10),
      _globalSwitchTile(
        'Generate PR description',
        'Use an LLM to generate PR titles and descriptions for auto_implement PRs',
        _globalGeneratePRDescription,
        (v) => _globalGeneratePRDescription = v,
      ),
    ]);
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
              'No aprobar PRs con issues',
              style: TextStyle(fontSize: 11),
            ),
            subtitle: Text(
              'Si la review encuentra issues (de cualquier severidad), se publica '
              'como comentario en la PR en vez de una aprobación. Los casos que '
              'bloquean (severidad alta) siguen siendo "cambios solicitados".',
              style: TextStyle(fontSize: 10, color: Colors.grey.shade600),
            ),
            dense: true,
            contentPadding: EdgeInsets.zero,
            value: _globalNeverApproveWithIssues,
            onChanged: (v) =>
                setState(() => _globalNeverApproveWithIssues = v),
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

    // Gate Save on a valid poll_interval so the client-side check actually
    // prevents the round-trip 400 (the inline validator alone only displays the
    // error). onChanged/_pickPollInterval call setState, so this re-evaluates as
    // the user types or picks a chip. The daemon stays authoritative.
    final pollInvalid = validatePollInterval(_pollInterval) != null;

    if (daemonRunning) {
      return SizedBox(
        width: double.infinity,
        child: ElevatedButton(
          onPressed: pollInvalid
              ? null
              : () async {
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
                    await ref
                        .read(configNotifierProvider.notifier)
                        .save(updated);
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
        onPressed: (isLoading || pollInvalid)
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
    globalNeverApproveWithIssues: _globalNeverApproveWithIssues,
    globalTriageOwner: _globalTriageOwner,
    globalCloneDir: _globalCloneDir,
    globalAutoPromoteTriage: _globalAutoPromoteTriage,
    globalAutoPromoteRefinement: _globalAutoPromoteRefinement,
    globalGeneratePRDescription: _globalGeneratePRDescription,
    globalIssuePrompt: _issuePromptId ?? '',
    globalImplementPrompt: _developPromptId ?? '',
    aiPrimary: _aiPrimary,
    aiFallback: _aiFallback,
    reviewMode: _reviewMode,
    // agentConfigs (per-CLI) managed in Agents tab
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
