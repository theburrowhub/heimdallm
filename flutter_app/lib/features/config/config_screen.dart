import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/instances/instances_providers.dart';
import '../../core/instances/models.dart' show ClusterRole;
import '../../core/state/appearance_preferences.dart';
import '../instances/config_propagation_dialog.dart';
import '../../core/models/config_model.dart';
import '../../core/platform/platform_services_provider.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/autocomplete_chip_field.dart';
import '../../shared/widgets/restart_required_banner.dart';
import '../../shared/widgets/toast.dart';
import '../agents/agents_screen.dart' show agentsProvider;
import '../dashboard/dashboard_providers.dart';
import '../server/server_actions.dart' as server_actions;
import '../updates/check_for_updates_button.dart';
import 'config_providers.dart';

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

enum _ConfigSectionId {
  appearance,
  token,
  poll,
  retention,
  ai,
  polling,
  issueTracking,
  pipeline,
  develop,
  autonomous,
  mergeTracking,
  circuitBreaker,
  cluster,
}

class _ConfigSectionMeta {
  final _ConfigSectionId id;
  final String title;
  final String summary;
  final IconData icon;

  const _ConfigSectionMeta({
    required this.id,
    required this.title,
    required this.summary,
    required this.icon,
  });
}

const _configSections = <_ConfigSectionMeta>[
  _ConfigSectionMeta(
    id: _ConfigSectionId.appearance,
    title: 'Appearance',
    summary: 'Local UI preference for this device. Does not change daemon configuration.',
    icon: Icons.palette_outlined,
  ),
  _ConfigSectionMeta(
    id: _ConfigSectionId.token,
    title: 'GitHub Token',
    summary: 'Credentials Heimdallm uses to talk to GitHub.',
    icon: Icons.key_outlined,
  ),
  _ConfigSectionMeta(
    id: _ConfigSectionId.poll,
    title: 'Polling',
    summary: 'How often Heimdallm checks GitHub for new review work.',
    icon: Icons.schedule_outlined,
  ),
  _ConfigSectionMeta(
    id: _ConfigSectionId.retention,
    title: 'Retention',
    summary: 'How long local review history is kept.',
    icon: Icons.archive_outlined,
  ),
  _ConfigSectionMeta(
    id: _ConfigSectionId.ai,
    title: 'AI defaults',
    summary: 'Default agent and review behavior used across the app.',
    icon: Icons.smart_toy_outlined,
  ),
  _ConfigSectionMeta(
    id: _ConfigSectionId.polling,
    title: 'Polling / Rate Limit',
    summary: 'Adaptive polling and API backoff safeguards.',
    icon: Icons.tune_outlined,
  ),
  _ConfigSectionMeta(
    id: _ConfigSectionId.issueTracking,
    title: 'Issue Tracking',
    summary: 'Issue triage scope, filters, and prompts.',
    icon: Icons.bug_report_outlined,
  ),
  _ConfigSectionMeta(
    id: _ConfigSectionId.pipeline,
    title: 'Pipeline',
    summary: 'Shared defaults for triage ownership, clone paths, and promotions.',
    icon: Icons.account_tree_outlined,
  ),
  _ConfigSectionMeta(
    id: _ConfigSectionId.develop,
    title: 'Develop',
    summary: 'Auto-implementation labels, reviewers, and PR defaults.',
    icon: Icons.code_outlined,
  ),
  _ConfigSectionMeta(
    id: _ConfigSectionId.autonomous,
    title: 'Autonomous Mode',
    summary: 'How Heimdallm acts on tasks when autonomous execution is enabled.',
    icon: Icons.auto_mode_outlined,
  ),
  _ConfigSectionMeta(
    id: _ConfigSectionId.mergeTracking,
    title: 'Merge Tracking',
    summary: 'Tracking and automation for your pull requests.',
    icon: Icons.merge_type_outlined,
  ),
  _ConfigSectionMeta(
    id: _ConfigSectionId.circuitBreaker,
    title: 'Circuit Breaker',
    summary: 'Safety limits that cap automated activity.',
    icon: Icons.health_and_safety_outlined,
  ),
  _ConfigSectionMeta(
    id: _ConfigSectionId.cluster,
    title: 'Cluster',
    summary: 'Role and restart flow for multi-instance deployments.',
    icon: Icons.dns_outlined,
  ),
];

class ConfigScreen extends ConsumerStatefulWidget {
  const ConfigScreen({super.key});

  @override
  ConsumerState<ConfigScreen> createState() => _ConfigScreenState();
}

class _ConfigScreenState extends ConsumerState<ConfigScreen> {
  final _tokenController = TextEditingController();
  final _pollController = TextEditingController();
  final _cloneDirController = TextEditingController();
  final _formScrollController = ScrollController();
  final _pollFieldKey = GlobalKey<FormFieldState<String>>();
  final Map<_ConfigSectionId, GlobalKey> _sectionKeys = {
    for (final section in _configSections) section.id: GlobalKey(),
  };
  bool _obscureToken = true;
  bool _tokenFromGh = false; // true = auto-detected from gh CLI
  _ConfigSectionId _selectedSection = _ConfigSectionId.appearance;
  String? _saveError;

  String _pollInterval = '5m';
  int _retentionDays = 90;
  IssueTrackingConfig _issueTracking = const IssueTrackingConfig();
  String? _issuePromptId;
  String? _developPromptId;
  PollingConfig _polling = const PollingConfig();

  // All known repos. Key = "org/repo", Value = per-repo settings.
  Map<String, RepoConfig> _repoConfigs = {};

  // Autonomous mode
  AutonomousConfig _autonomous = const AutonomousConfig();
  MergeTrackingConfig _mergeTracking = const MergeTrackingConfig();
  CircuitBreakerConfig _circuitBreaker = const CircuitBreakerConfig();
  String _clusterRole = ClusterRole.standalone;
  late TextEditingController _devMaxTurnsController;
  late TextEditingController _devTimeoutController;
  late TextEditingController _claimLeaseController;
  late TextEditingController _mtPollIntervalController;
  late TextEditingController _mtResolveTimeoutController;
  late TextEditingController _perPr24hController;
  late TextEditingController _perRepoHrController;
  late TextEditingController _perIssue24hController;
  late TextEditingController _perIssueRepoHrController;
  late TextEditingController _perImplRepoHrController;
  bool _autonomousControllersInitialized = false;

  bool _initialized = false;

  @override
  void initState() {
    super.initState();
    _devMaxTurnsController = TextEditingController();
    _devTimeoutController = TextEditingController();
    _claimLeaseController = TextEditingController();
    _mtPollIntervalController = TextEditingController();
    _mtResolveTimeoutController = TextEditingController();
    _perPr24hController = TextEditingController();
    _perRepoHrController = TextEditingController();
    _perIssue24hController = TextEditingController();
    _perIssueRepoHrController = TextEditingController();
    _perImplRepoHrController = TextEditingController();
    _detectToken();
  }

  @override
  void dispose() {
    _tokenController.dispose();
    _pollController.dispose();
    _cloneDirController.dispose();
    _formScrollController.dispose();
    _devMaxTurnsController.dispose();
    _devTimeoutController.dispose();
    _claimLeaseController.dispose();
    _mtPollIntervalController.dispose();
    _mtResolveTimeoutController.dispose();
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
    _pollController.text = config.pollInterval;
    _retentionDays = config.retentionDays;
    _repoConfigs = Map.from(config.repoConfigs);
    _issueTracking = config.issueTracking;
    _polling = config.polling;
    _issuePromptId = config.globalIssuePrompt.isEmpty
        ? null
        : config.globalIssuePrompt;
    _developPromptId = config.globalImplementPrompt.isEmpty
        ? null
        : config.globalImplementPrompt;
    _clusterRole = config.clusterRole;
    _initAutonomousFromConfig(config);
  }

  void _initAutonomousFromConfig(AppConfig config) {
    if (_autonomousControllersInitialized) return;
    _autonomousControllersInitialized = true;
    _autonomous = config.autonomous;
    _mergeTracking = config.mergeTracking;
    _circuitBreaker = config.circuitBreaker;
    _devMaxTurnsController.text = config.autonomous.devMaxTurns.toString();
    _devTimeoutController.text = config.autonomous.devTimeout;
    _claimLeaseController.text = config.autonomous.claimLease;
    _mtPollIntervalController.text = config.mergeTracking.pollInterval;
    _mtResolveTimeoutController.text = config.mergeTracking.resolveTimeout;
    _perPr24hController.text = config.circuitBreaker.perPr24h.toString();
    _perRepoHrController.text = config.circuitBreaker.perRepoHr.toString();
    _perIssue24hController.text = config.circuitBreaker.perIssue24h.toString();
    _perIssueRepoHrController.text = config.circuitBreaker.perIssueRepoHr
        .toString();
    _perImplRepoHrController.text = config.circuitBreaker.perImplRepoHr
        .toString();
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
          return _buildScreenContent(context, config, daemonRunning);
        },
      ),
    );
  }

  Widget _buildScreenContent(
    BuildContext context,
    AppConfig config,
    bool daemonRunning,
  ) {
    final width = MediaQuery.sizeOf(context).width;
    final isWide = width >= AppBreakpoints.medium;
    final visibleSections = _visibleSections();
    final selectedSection = visibleSections.any((s) => s.id == _selectedSection)
        ? _selectedSection
        : visibleSections.first.id;

    final form = SingleChildScrollView(
      key: const Key('config-scroll-view'),
      controller: _formScrollController,
      padding: EdgeInsets.fromLTRB(24, isWide ? 24 : 16, 24, 24),
      child: Align(
        alignment: Alignment.topLeft,
        child: ConstrainedBox(
          constraints: BoxConstraints(
            maxWidth: isWide ? 980 : double.infinity,
          ),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              if (!daemonRunning) _setupBanner(),
              const AppUpdateSettingsCard(),
              _appearanceSection(),
              _tokenSection(),
              _pollSection(),
              _retentionSection(),
              _aiSection(config),
              _pollingSection(),
              _issueTrackingSection(config),
              _pipelineSection(config),
              _developSection(config),
              _autonomousSection(),
              _mergeTrackingSection(),
              _circuitBreakerSection(),
              if (_showClusterSection()) _clusterSection(config),
            ],
          ),
        ),
      ),
    );

    final content = isWide
        ? Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              SizedBox(
                width: 280,
                child: Padding(
                  padding: const EdgeInsets.fromLTRB(24, 24, 0, 24),
                  child: _buildWideSectionIndex(
                    context,
                    sections: visibleSections,
                    selectedSection: selectedSection,
                  ),
                ),
              ),
              Expanded(child: form),
            ],
          )
        : Column(
            children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(24, 16, 24, 0),
                child: _buildCompactSectionIndex(
                  sections: visibleSections,
                  selectedSection: selectedSection,
                ),
              ),
              Expanded(child: form),
            ],
          );

    return Column(
      children: [
        Expanded(child: content),
        _buildSaveBar(context, config, daemonRunning),
      ],
    );
  }

  List<_ConfigSectionMeta> _visibleSections() => [
    for (final section in _configSections)
      if (section.id != _ConfigSectionId.cluster || _showClusterSection())
        section,
  ];

  bool _showClusterSection() {
    final activeInstance = ref.watch(activeInstanceProvider);
    return activeInstance == null || activeInstance.isEmpty;
  }

  _ConfigSectionMeta _sectionMeta(_ConfigSectionId id) =>
      _configSections.firstWhere((section) => section.id == id);

  _ConfigSectionId _sectionIdForTitle(String title) => switch (title) {
    'GitHub Token' => _ConfigSectionId.token,
    'Polling' => _ConfigSectionId.poll,
    'Retention' => _ConfigSectionId.retention,
    'AI defaults' => _ConfigSectionId.ai,
    'Polling / Rate Limit' => _ConfigSectionId.polling,
    'Issue Tracking' => _ConfigSectionId.issueTracking,
    'Pipeline' => _ConfigSectionId.pipeline,
    'Develop' => _ConfigSectionId.develop,
    'Autonomous Mode' => _ConfigSectionId.autonomous,
    'Merge Tracking' => _ConfigSectionId.mergeTracking,
    'Circuit Breaker' => _ConfigSectionId.circuitBreaker,
    'Cluster' => _ConfigSectionId.cluster,
    _ => throw ArgumentError.value(title, 'title', 'Unknown config section'),
  };

  String _navLabel(int index, _ConfigSectionMeta section) =>
      '${index + 1}. ${section.title}';

  Future<void> _scrollToSection(_ConfigSectionId id) async {
    setState(() => _selectedSection = id);
    final targetContext = _sectionKeys[id]?.currentContext;
    if (targetContext == null) return;
    await Scrollable.ensureVisible(
      targetContext,
      duration: const Duration(milliseconds: 250),
      curve: Curves.easeInOut,
      alignment: 0.04,
    );
  }

  Widget _buildWideSectionIndex(
    BuildContext context, {
    required List<_ConfigSectionMeta> sections,
    required _ConfigSectionId selectedSection,
  }) {
    return SingleChildScrollView(
      child: AppSurface(
        elevation: AppSurfaceElevation.raised,
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const AppText.sectionTitle('Settings sections'),
            const SizedBox(height: 6),
            const AppText.muted(
              'Jump within the single settings draft. Appearance is local to this device; everything else saves to the daemon.',
            ),
            const SizedBox(height: 16),
            for (var i = 0; i < sections.length; i++) ...[
              _SectionIndexButton(
                icon: sections[i].icon,
                label: _navLabel(i, sections[i]),
                summary: sections[i].summary,
                selected: sections[i].id == selectedSection,
                onTap: () => _scrollToSection(sections[i].id),
              ),
              const SizedBox(height: 8),
            ],
          ],
        ),
      ),
    );
  }

  Widget _buildCompactSectionIndex({
    required List<_ConfigSectionMeta> sections,
    required _ConfigSectionId selectedSection,
  }) {
    return AppSurface(
      elevation: AppSurfaceElevation.raised,
      padding: const EdgeInsets.all(16),
      child: DropdownButtonFormField<_ConfigSectionId>(
        key: ValueKey(selectedSection),
        initialValue: selectedSection,
        decoration: const InputDecoration(
          labelText: 'Jump to section',
          helperText: 'One page, one draft',
          border: OutlineInputBorder(),
          isDense: true,
        ),
        items: [
          for (var i = 0; i < sections.length; i++)
            DropdownMenuItem(
              value: sections[i].id,
              child: Text(_navLabel(i, sections[i])),
            ),
        ],
        onChanged: (value) {
          if (value != null) _scrollToSection(value);
        },
      ),
    );
  }

  Widget _appearanceSection() {
    final mode = ref.watch(appearanceProvider);
    final theme = Theme.of(context);

    return _buildSectionCard(
      _ConfigSectionId.appearance,
      trailing: AppBadge(
        label: 'Local only',
        foreground: theme.colorScheme.onSecondaryContainer,
        background: theme.colorScheme.secondaryContainer,
      ),
      children: [
        const AppText.muted(
          'This changes only the app theme on this device. It saves immediately and never touches config.toml.',
        ),
        const SizedBox(height: 12),
        Wrap(
          spacing: 8,
          runSpacing: 8,
          children: [
            for (final option in const [
              (mode: ThemeMode.system, label: 'System', icon: Icons.brightness_auto),
              (mode: ThemeMode.light, label: 'Light', icon: Icons.light_mode_outlined),
              (mode: ThemeMode.dark, label: 'Dark', icon: Icons.dark_mode_outlined),
            ])
              ChoiceChip(
                label: Text(option.label),
                avatar: Icon(option.icon, size: 18),
                selected: mode == option.mode,
                onSelected: (_) =>
                    ref.read(appearanceProvider.notifier).set(option.mode),
              ),
          ],
        ),
      ],
    );
  }

  // ── Token ───────────────────────────────────────────────────────────────

  Widget _tokenSection() {
    return _buildSectionCard(
      _ConfigSectionId.token,
      children: [
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

  // ── Poll interval ─────────────────────────────────────────────────────────

  Widget _pollSection() {
    return _buildSectionCard(
      _ConfigSectionId.poll,
      children: [
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
              .map(
                (v) => ActionChip(
                  label: Text(v),
                  onPressed: () => _pickPollInterval(v),
                ),
              )
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
    return _buildSectionCard(
      _ConfigSectionId.retention,
      children: [
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

  // ── Polling / Rate-limit ─────────────────────────────────────────────────

  Widget _pollingSection() {
    return _settingsCard('Polling / Rate Limit', [
      SwitchListTile(
        title: const Text('Adaptive polling', style: TextStyle(fontSize: 13)),
        subtitle: const Text(
          'Dynamically adjust poll interval based on activity',
          style: TextStyle(fontSize: 11),
        ),
        dense: true,
        contentPadding: EdgeInsets.zero,
        value: _polling.adaptive,
        onChanged: (v) => setState(() {
          _polling = _polling.copyWith(adaptive: v);
        }),
      ),
      const SizedBox(height: 10),
      TextFormField(
        initialValue: _polling.pollInterval,
        decoration: const InputDecoration(
          // Deliberately distinct from the 'Poll interval' field in Settings:
          // two fields sharing a label make find.text() ambiguous and break
          // the existing widget test.
          labelText: 'Tier-2 poll interval',
          helperText: 'Override global poll interval (e.g. 2m, 30s)',
          border: OutlineInputBorder(),
        ),
        onChanged: (v) => setState(() {
          _polling = _polling.copyWith(pollInterval: v);
        }),
      ),
      if (_polling.adaptive) ...[
        const SizedBox(height: 10),
        Row(
          children: [
            Expanded(
              child: TextFormField(
                initialValue: _polling.minInterval,
                decoration: const InputDecoration(
                  labelText: 'Min interval',
                  helperText: 'Shortest allowed poll cycle',
                  border: OutlineInputBorder(),
                ),
                onChanged: (v) => setState(() {
                  _polling = _polling.copyWith(minInterval: v);
                }),
              ),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: TextFormField(
                initialValue: _polling.maxInterval,
                decoration: const InputDecoration(
                  labelText: 'Max interval',
                  helperText: 'Longest allowed poll cycle',
                  border: OutlineInputBorder(),
                ),
                onChanged: (v) => setState(() {
                  _polling = _polling.copyWith(maxInterval: v);
                }),
              ),
            ),
          ],
        ),
        const SizedBox(height: 10),
        Row(
          children: [
            Expanded(
              child: TextFormField(
                initialValue: _polling.discoveryInterval,
                decoration: const InputDecoration(
                  labelText: 'Discovery interval',
                  helperText: 'Repo discovery scan cadence',
                  border: OutlineInputBorder(),
                ),
                onChanged: (v) => setState(() {
                  _polling = _polling.copyWith(discoveryInterval: v);
                }),
              ),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: TextFormField(
                initialValue: _polling.tier3Interval,
                decoration: const InputDecoration(
                  labelText: 'Tier-3 interval',
                  helperText: 'Slow-lane repos poll cadence',
                  border: OutlineInputBorder(),
                ),
                onChanged: (v) => setState(() {
                  _polling = _polling.copyWith(tier3Interval: v);
                }),
              ),
            ),
          ],
        ),
      ],
      const SizedBox(height: 10),
      TextFormField(
        initialValue: _polling.rateLimitSafetyThreshold.toString(),
        decoration: const InputDecoration(
          labelText: 'Rate-limit safety threshold',
          helperText: 'Remaining API requests before backing off',
          border: OutlineInputBorder(),
        ),
        keyboardType: TextInputType.number,
        onChanged: (v) => setState(() {
          _polling = _polling.copyWith(
            rateLimitSafetyThreshold: int.tryParse(v) ?? 100,
          );
        }),
      ),
      const SizedBox(height: 10),
      Material(
        // Material (not a colored Container) so the SwitchListTiles' ink/bg
        // paint on it — a BoxDecoration color here throws a ListTile assertion
        // in widget tests (same reason as _dangerousToggle below, c1eb4e7).
        color: Theme.of(
          context,
        ).colorScheme.surfaceContainerHighest.withValues(alpha: 0.3),
        borderRadius: BorderRadius.circular(6),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 2),
          child: Column(
            children: [
              SwitchListTile(
                title: const Text(
                  'Use ETag caching',
                  style: TextStyle(fontSize: 11),
                ),
                subtitle: Text(
                  'Skip unchanged responses using HTTP ETags',
                  style: TextStyle(fontSize: 10, color: Colors.grey.shade600),
                ),
                dense: true,
                contentPadding: EdgeInsets.zero,
                value: _polling.useEtag,
                onChanged: (v) => setState(() {
                  _polling = _polling.copyWith(useEtag: v);
                }),
              ),
              SwitchListTile(
                title: const Text(
                  'Use GraphQL API',
                  style: TextStyle(fontSize: 11),
                ),
                subtitle: Text(
                  'Fetch PR/issue data via GraphQL instead of REST',
                  style: TextStyle(fontSize: 10, color: Colors.grey.shade600),
                ),
                dense: true,
                contentPadding: EdgeInsets.zero,
                value: _polling.useGraphql,
                onChanged: (v) => setState(() {
                  _polling = _polling.copyWith(useGraphql: v);
                }),
              ),
            ],
          ),
        ),
      ),
    ]);
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
  String _globalNeverApproveMinSeverity = defaultNeverApproveMinSeverity;
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
    // never_approve lives here but is initialized by _initDevelopFromConfig,
    // which otherwise only runs in the later Pipeline/Develop sections — call
    // it now (idempotent) so the switch shows the real value on the first frame.
    _initDevelopFromConfig(config);
    return _settingsCard('AI defaults', [
      DropdownButtonFormField<String>(
        initialValue: _aiPrimary,
        decoration: const InputDecoration(
          labelText: 'Primary agent',
          border: OutlineInputBorder(),
          isDense: true,
        ),
        items: const [
          'claude',
          'gemini',
          'codex',
        ].map((v) => DropdownMenuItem(value: v, child: Text(v))).toList(),
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
        items: const [
          'none',
          'claude',
          'gemini',
          'codex',
        ].map((v) => DropdownMenuItem(value: v, child: Text(v))).toList(),
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
        items: const [
          'single',
          'multi',
        ].map((v) => DropdownMenuItem(value: v, child: Text(v))).toList(),
        onChanged: (v) => setState(() => _reviewMode = v ?? 'single'),
      ),
      const SizedBox(height: 12),
      _globalSwitchTile(
        'Never approve PRs with issues',
        'If the review raises a finding at or above the threshold below, '
            "it's posted as a comment on the PR instead of an approval "
            '(high severity still requests changes)',
        _globalNeverApproveWithIssues,
        (v) => _globalNeverApproveWithIssues = v,
      ),
      const SizedBox(height: 12),
      DropdownButtonFormField<String>(
        initialValue: _globalNeverApproveMinSeverity,
        decoration: const InputDecoration(
          labelText: 'Never approve — minimum severity',
          helperText:
              'Findings below this severity keep the approval; they are still '
              'listed in the review body. Default: medium (all-low reviews '
              'approve).',
          border: OutlineInputBorder(),
          isDense: true,
        ),
        items: neverApproveMinSeverityOptions
            .map((v) => DropdownMenuItem(value: v, child: Text(v)))
            .toList(),
        // Disabled while the toggle is off: the threshold has no effect then,
        // and a live-looking control that changes nothing reads as a bug.
        onChanged: _globalNeverApproveWithIssues
            ? (v) => setState(
                () => _globalNeverApproveMinSeverity =
                    v ?? defaultNeverApproveMinSeverity,
              )
            : null,
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
    _globalNeverApproveMinSeverity = config.globalNeverApproveMinSeverity;
    _globalTriageOwner = config.globalTriageOwner;
    _globalCloneDir = config.globalCloneDir;
    _cloneDirController.text = config.globalCloneDir;
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
    // Material (not a colored DecoratedBox) so the SwitchListTile's ink/bg
    // paints on it — a colored Container here throws a ListTile assertion in
    // widget tests (see theburrowhub/heimdallm c1eb4e7).
    return Material(
      color: Theme.of(
        context,
      ).colorScheme.surfaceContainerHighest.withValues(alpha: 0.3),
      borderRadius: BorderRadius.circular(6),
      child: SwitchListTile(
        title: Text(title, style: const TextStyle(fontSize: 11)),
        subtitle: Text(
          subtitle,
          style: TextStyle(fontSize: 10, color: Colors.grey.shade600),
        ),
        dense: true,
        contentPadding: const EdgeInsets.symmetric(horizontal: 10, vertical: 2),
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
        controller: _cloneDirController,
        decoration: InputDecoration(
          labelText: 'Clone directory',
          hintText: 'Base directory for managed repo clones',
          isDense: true,
          suffixIcon: IconButton(
            tooltip: 'Browse…',
            icon: const Icon(Icons.folder_open, size: 18),
            onPressed: () async {
              final dir = await FilePicker.getDirectoryPath(
                dialogTitle: 'Select clone directory',
                lockParentWindow: true,
              );
              if (dir == null || dir.isEmpty) return;
              _cloneDirController.text = dir;
              _globalCloneDir = dir;
            },
          ),
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
        _globalSwitchTile(
          'Create as draft',
          'PRs are created as drafts by default',
          _globalPRDraft,
          (v) => _globalPRDraft = v,
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
        title: const Text(
          'Enable autonomous mode',
          style: TextStyle(fontSize: 13),
        ),
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
          title: const Text(
            'Auto-merge approved PRs',
            style: TextStyle(fontSize: 13),
          ),
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
          items: [
            'squash',
            'merge',
            'rebase',
          ].map((v) => DropdownMenuItem(value: v, child: Text(v))).toList(),
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
          items: [
            'low',
            'medium',
            'high',
            'max',
          ].map((v) => DropdownMenuItem(value: v, child: Text(v))).toList(),
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
            _autonomous = _autonomous.copyWith(
              devMaxTurns: int.tryParse(v) ?? 0,
            );
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
          title: const Text(
            "Take others' tasks",
            style: TextStyle(fontSize: 13),
          ),
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
              color: Theme.of(
                context,
              ).colorScheme.surfaceContainerHighest.withValues(alpha: 0.3),
              borderRadius: BorderRadius.circular(6),
            ),
            padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 2),
            child: SwitchListTile(
              title: const Text(
                'Reassign on take',
                style: TextStyle(fontSize: 11),
              ),
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

  // ── Merge tracking ─────────────────────────────────────────────────────────

  /// The four automation levels for the operator's own PRs.
  ///
  /// Each toggle is nested under the master switch and carries a subtitle that
  /// says what Heimdallm will actually do — "Resolve conflicts" in particular
  /// means an agent force-pushes to your branch, and that has to be stated on
  /// the switch rather than buried in the docs.
  Widget _mergeTrackingSection() {
    return _settingsCard('Merge Tracking', [
      SwitchListTile(
        title: const Text(
          'Track my pull requests',
          style: TextStyle(fontSize: 13),
        ),
        subtitle: const Text(
          'Watch the open PRs you authored or are assigned to, and report '
          'exactly what is blocking each merge',
          style: TextStyle(fontSize: 11),
        ),
        dense: true,
        contentPadding: EdgeInsets.zero,
        value: _mergeTracking.enabled,
        onChanged: (v) => setState(() {
          _mergeTracking = _mergeTracking.copyWith(enabled: v);
        }),
      ),
      if (_mergeTracking.enabled) ...[
        const SizedBox(height: 4),
        SwitchListTile(
          title: const Text(
            'Include PRs assigned to me',
            style: TextStyle(fontSize: 13),
          ),
          subtitle: const Text(
            'Also track PRs someone else opened but assigned to you',
            style: TextStyle(fontSize: 11),
          ),
          dense: true,
          contentPadding: EdgeInsets.zero,
          value: _mergeTracking.includeAssigned,
          onChanged: (v) => setState(() {
            _mergeTracking = _mergeTracking.copyWith(includeAssigned: v);
          }),
        ),
        const Divider(height: 20),
        const Padding(
          padding: EdgeInsets.only(bottom: 4),
          child: Text(
            'Automations',
            style: TextStyle(fontSize: 12, fontWeight: FontWeight.w700),
          ),
        ),
        SwitchListTile(
          title: const Text(
            'Turn on auto-merge',
            style: TextStyle(fontSize: 13),
          ),
          subtitle: const Text(
            "Enable GitHub's own auto-merge so it merges the PR as soon as "
            'every requirement is met',
            style: TextStyle(fontSize: 11),
          ),
          dense: true,
          contentPadding: EdgeInsets.zero,
          value: _mergeTracking.enableAutoMerge,
          onChanged: (v) => setState(() {
            _mergeTracking = _mergeTracking.copyWith(enableAutoMerge: v);
          }),
        ),
        SwitchListTile(
          title: const Text(
            'Update stale branches',
            style: TextStyle(fontSize: 13),
          ),
          subtitle: const Text(
            'Bring a PR up to date with its base branch when it falls behind',
            style: TextStyle(fontSize: 11),
          ),
          dense: true,
          contentPadding: EdgeInsets.zero,
          value: _mergeTracking.updateBranch,
          onChanged: (v) => setState(() {
            _mergeTracking = _mergeTracking.copyWith(updateBranch: v);
          }),
        ),
        SwitchListTile(
          title: const Text(
            'Resolve conflicts',
            style: TextStyle(fontSize: 13),
          ),
          subtitle: const Text(
            'Let the configured agent resolve merge conflicts and FORCE-PUSH '
            'to your branch. The PR gets a comment naming the commit it was '
            'at beforehand, so a bad resolution can be undone.',
            style: TextStyle(fontSize: 11),
          ),
          dense: true,
          contentPadding: EdgeInsets.zero,
          value: _mergeTracking.resolveConflicts,
          onChanged: (v) => setState(() {
            _mergeTracking = _mergeTracking.copyWith(resolveConflicts: v);
          }),
        ),
        SwitchListTile(
          title: const Text('Merge when ready', style: TextStyle(fontSize: 13)),
          subtitle: const Text(
            'Merge the PR yourself once every requirement is met. Heimdallm '
            're-checks immediately before merging and refuses if anything '
            'changed.',
            style: TextStyle(fontSize: 11),
          ),
          dense: true,
          contentPadding: EdgeInsets.zero,
          value: _mergeTracking.merge,
          onChanged: (v) => setState(() {
            _mergeTracking = _mergeTracking.copyWith(merge: v);
          }),
        ),
        const Divider(height: 20),
        SwitchListTile(
          title: const Text(
            'Require an approving review',
            style: TextStyle(fontSize: 13),
          ),
          subtitle: const Text(
            'Never merge without an approval, even where the repository does '
            'not require one',
            style: TextStyle(fontSize: 11),
          ),
          dense: true,
          contentPadding: EdgeInsets.zero,
          value: _mergeTracking.requireApproval,
          onChanged: (v) => setState(() {
            _mergeTracking = _mergeTracking.copyWith(requireApproval: v);
          }),
        ),
        const SizedBox(height: 8),
        DropdownButtonFormField<String>(
          // ignore: deprecated_member_use
          value: _mergeTracking.mergeMethod,
          decoration: const InputDecoration(
            labelText: 'Merge method',
            helperText: 'Must be enabled on the repository',
            border: OutlineInputBorder(),
            isDense: true,
          ),
          items: [
            'squash',
            'merge',
            'rebase',
          ].map((v) => DropdownMenuItem(value: v, child: Text(v))).toList(),
          onChanged: (v) => setState(() {
            _mergeTracking = _mergeTracking.copyWith(mergeMethod: v);
          }),
        ),
        const SizedBox(height: 8),
        TextFormField(
          controller: _mtPollIntervalController,
          decoration: const InputDecoration(
            labelText: 'Check interval',
            helperText: 'e.g. 5m. Empty inherits the shared poll interval.',
            border: OutlineInputBorder(),
            isDense: true,
          ),
          onChanged: (v) => setState(() {
            _mergeTracking = _mergeTracking.copyWith(pollInterval: v.trim());
          }),
        ),
        if (_mergeTracking.resolveConflicts) ...[
          const SizedBox(height: 8),
          TextFormField(
            controller: _mtResolveTimeoutController,
            decoration: const InputDecoration(
              labelText: 'Conflict-resolution timeout',
              helperText: 'Wall clock for one agent run, e.g. 30m',
              border: OutlineInputBorder(),
              isDense: true,
            ),
            onChanged: (v) => setState(() {
              _mergeTracking = _mergeTracking.copyWith(
                resolveTimeout: v.trim(),
              );
            }),
          ),
          const SizedBox(height: 8),
          DropdownButtonFormField<String>(
            // ignore: deprecated_member_use
            value: _mergeTracking.resolveEffort,
            decoration: const InputDecoration(
              labelText: 'Conflict-resolution effort',
              border: OutlineInputBorder(),
              isDense: true,
            ),
            items: [
              'low',
              'medium',
              'high',
              'max',
            ].map((v) => DropdownMenuItem(value: v, child: Text(v))).toList(),
            onChanged: (v) => setState(() {
              _mergeTracking = _mergeTracking.copyWith(resolveEffort: v);
            }),
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

  // ── Cluster ─────────────────────────────────────────────────────────────

  /// Hidden while a remote instance is selected: this section edits the
  /// LOCAL daemon's own role, and this app cannot restart a remote instance
  /// from here, so scoping the dropdown to one would be a trap — it would
  /// look like it worked and then nothing would ever restart.
  Widget _clusterSection(AppConfig config) {
    if (!_showClusterSection()) {
      return const SizedBox.shrink();
    }

    final liveRole = ref.watch(localClusterRoleProvider).value;
    final effectiveLiveRole = (liveRole == null || liveRole.isEmpty)
        ? ClusterRole.standalone
        : liveRole.toLowerCase();
    // Compares what was SAVED (config.clusterRole, optimistic once Save is
    // tapped) against what the running process actually reports, not the
    // unsaved dropdown value — so this survives navigating away and back,
    // and also catches a role edited by hand in config.toml.
    final restartPending =
        liveRole != null && config.clusterRole != effectiveLiveRole;

    return _settingsCard('Cluster', [
      const Text(
        'A hub manages other Heimdallm daemons — it registers them, routes '
        'organizations and repositories to them, and can push this '
        'configuration to all of them. A worker is managed by a hub. '
        'Standalone is a single daemon on its own.',
        style: TextStyle(fontSize: 11),
      ),
      const SizedBox(height: 8),
      DropdownButtonFormField<String>(
        // ignore: deprecated_member_use
        value: _clusterRole,
        decoration: const InputDecoration(
          labelText: 'Role',
          helperText: 'Changing this requires restarting the daemon',
          border: OutlineInputBorder(),
          isDense: true,
        ),
        items: ClusterRole.all
            .map((v) => DropdownMenuItem(value: v, child: Text(v)))
            .toList(),
        onChanged: (v) => _onClusterRoleChanged(v),
      ),
      if (restartPending) ...[
        const SizedBox(height: 12),
        RestartRequiredBanner(
          message:
              'Cluster role saved as "${config.clusterRole}", but the '
              'daemon is still running as "$effectiveLiveRole". Restart it '
              'for the change to take effect.',
          onRestart: () => server_actions.restartDaemon(context, ref),
          starting: ref.watch(daemonStartingProvider),
        ),
      ],
    ]);
  }

  Future<void> _onClusterRoleChanged(String? next) async {
    if (next == null || next == _clusterRole) return;

    // Demoting a hub that already has instances unmounts the control plane
    // (ClusterDeps goes nil) while the registry stays in config.toml: every
    // registered instance becomes unreachable from the GUI — Remove/Edit/
    // Routing all 404 — until the role is set back to hub. Confirm first
    // rather than let that happen silently.
    final registeredCount =
        ref.read(daemonInstancesProvider).value?.instances.length ?? 0;
    if (_clusterRole == ClusterRole.hub &&
        next != ClusterRole.hub &&
        registeredCount > 1) {
      final confirmed = await showDialog<bool>(
        context: context,
        builder: (context) => AlertDialog(
          title: const Text('Stop being a hub?'),
          content: Text(
            'This hub currently manages $registeredCount instances. They '
            'stay in config.toml, but become unmanageable from this app — '
            'unreachable, unroutable, not removable — until the role is set '
            'back to hub.',
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.of(context).pop(false),
              child: const Text('Cancel'),
            ),
            FilledButton(
              onPressed: () => Navigator.of(context).pop(true),
              child: const Text('Change role'),
            ),
          ],
        ),
      );
      if (confirmed != true) return;
    }

    // The confirm dialog is an await gap: the widget can be disposed while
    // it's open (e.g. the user navigates away from Settings), and setState
    // after dispose throws.
    if (!mounted) return;
    setState(() => _clusterRole = next);
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
    return _buildSectionCard(
      _sectionIdForTitle(title),
      children: children,
    );
  }

  Widget _buildSectionCard(
    _ConfigSectionId id, {
    required List<Widget> children,
    Widget? trailing,
  }) {
    final meta = _sectionMeta(id);
    final theme = Theme.of(context);

    return Padding(
      padding: const EdgeInsets.only(bottom: 20),
      child: AppSurface(
        key: _sectionKeys[id],
        elevation: AppSurfaceElevation.surface,
        padding: const EdgeInsets.all(16),
        // The AppSurface below paints a colored/bordered DecoratedBox. Any
        // SwitchListTile/CheckboxListTile/RadioListTile in `children` needs
        // its own nearest Material ancestor to paint ink/background, or it
        // throws a "ListTile background color or ink splashes may be
        // invisible" assertion — fatal in widget tests (see
        // theburrowhub/heimdallm c1eb4e7). Wrapping the whole card body once
        // here covers every section instead of patching each tile.
        child: Material(
          type: MaterialType.transparency,
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Icon(meta.icon, size: 20, color: theme.colorScheme.primary),
                  const SizedBox(width: 12),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        AppText.sectionTitle(meta.title),
                        const SizedBox(height: 4),
                        AppText.muted(meta.summary),
                      ],
                    ),
                  ),
                  if (trailing != null) ...[
                    const SizedBox(width: 12),
                    trailing,
                  ],
                ],
              ),
              const SizedBox(height: 16),
              ...children,
            ],
          ),
        ),
      ),
    );
  }

  // ── Save button ──────────────────────────────────────────────────────────

  Widget _buildSaveBar(
    BuildContext context,
    AppConfig base,
    bool daemonRunning,
  ) {
    final isLoading = ref.watch(configNotifierProvider).isLoading;
    final pollInvalid = validatePollInterval(_pollInterval) != null;
    final multiInstance =
        ref.watch(daemonInstancesProvider).value?.isMultiInstance ?? false;

    return Material(
      elevation: 8,
      color: Theme.of(context).colorScheme.surface,
      child: SafeArea(
        top: false,
        child: Padding(
          padding: const EdgeInsets.fromLTRB(24, 16, 24, 20),
          child: AppSurface(
            elevation: AppSurfaceElevation.raised,
            padding: const EdgeInsets.all(16),
            child: LayoutBuilder(
              builder: (context, constraints) {
                final stacked = constraints.maxWidth < 860;
                final actions = _buildSaveActions(
                  context,
                  base,
                  daemonRunning,
                  isLoading,
                  pollInvalid,
                  multiInstance,
                );

                return Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    if (_saveError != null) ...[
                      _statusMessage(
                        icon: Icons.error_outline,
                        text: _saveError!,
                        background:
                            Theme.of(context).colorScheme.errorContainer,
                        foreground:
                            Theme.of(context).colorScheme.onErrorContainer,
                      ),
                      const SizedBox(height: 12),
                    ],
                    stacked
                        ? Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: actions,
                          )
                        : Row(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              Expanded(child: actions.first),
                              if (actions.length > 1) ...[
                                const SizedBox(width: 16),
                                Expanded(child: actions.last),
                              ],
                            ],
                          ),
                  ],
                );
              },
            ),
          ),
        ),
      ),
    );
  }

  List<Widget> _buildSaveActions(
    BuildContext context,
    AppConfig base,
    bool daemonRunning,
    bool isLoading,
    bool pollInvalid,
    bool multiInstance,
  ) {
    if (daemonRunning) {
      return [
        Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const AppText.sectionTitle('Daemon settings'),
            const SizedBox(height: 4),
            const AppText.muted(
              'All sections above except Appearance save into the same daemon-backed draft.',
            ),
            const SizedBox(height: 12),
            SizedBox(
              width: double.infinity,
              child: ElevatedButton(
                onPressed: pollInvalid
                    ? null
                    : () => _saveDaemonSettings(context, base),
                child: const Text('Save'),
              ),
            ),
          ],
        ),
        if (multiInstance)
          Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const AppText.sectionTitle('Cluster propagation'),
              const SizedBox(height: 4),
              AppText.muted(
                'Pushes shared settings to every registered instance. Ports, tokens and local paths stay per-machine.',
                overflow: TextOverflow.visible,
              ),
              const SizedBox(height: 12),
              SizedBox(
                width: double.infinity,
                child: OutlinedButton.icon(
                  icon: const Icon(Icons.sync_alt, size: 16),
                  label: const Text('Apply to all instances…'),
                  onPressed: () => showConfigPropagationDialog(context, ref),
                ),
              ),
            ],
          ),
      ];
    }

    return [
      Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const AppText.sectionTitle('First-run setup'),
          const SizedBox(height: 4),
          const AppText.muted(
            'Save the daemon configuration and start Heimdallm with the current draft.',
          ),
          const SizedBox(height: 12),
          SizedBox(
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
              label: Text(
                isLoading ? 'Starting…' : 'Save and start Heimdallm',
              ),
              onPressed: (isLoading || pollInvalid)
                  ? null
                  : () => _saveAndStartDaemon(context, base),
            ),
          ),
        ],
      ),
    ];
  }

  AppConfig _buildConfig(AppConfig base) => base.copyWith(
    pollInterval: _pollInterval,
    retentionDays: _retentionDays,
    repoConfigs: Map.from(_repoConfigs),
    issueTracking: _issueTracking,
    polling: _polling,
    globalPRReviewers: _globalPRReviewers,
    globalPRLabels: _globalPRLabels,
    globalPRAssignee: _globalPRAssignee,
    globalPRDraft: _globalPRDraft,
    globalNeverApproveWithIssues: _globalNeverApproveWithIssues,
    globalNeverApproveMinSeverity: _globalNeverApproveMinSeverity,
    globalTriageOwner: _globalTriageOwner,
    globalCloneDir: _globalCloneDir,
    globalAutoPromoteTriage: _globalAutoPromoteTriage,
    globalAutoPromoteRefinement: _globalAutoPromoteRefinement,
    globalGeneratePRDescription: _globalGeneratePRDescription,
    globalIssuePrompt: _issuePromptId ?? '',
    globalImplementPrompt: _developPromptId ?? '',
    autonomous: _autonomous,
    mergeTracking: _mergeTracking,
    circuitBreaker: _circuitBreaker,
    aiPrimary: _aiPrimary,
    aiFallback: _aiFallback,
    reviewMode: _reviewMode,
    clusterRole: _clusterRole,
    // agentConfigs (per-CLI) managed in Agents tab
  );

  Future<void> _saveDaemonSettings(BuildContext context, AppConfig base) async {
    final updated = _buildConfig(base);
    setState(() => _saveError = null);
    try {
      final token = _tokenController.text.trim();
      if (token.isNotEmpty && !_tokenFromGh) {
        await ref.read(platformServicesProvider).storeGitHubToken(token);
        // Invalidate the cached token so the ApiClient re-reads it on the next request.
        ref.read(apiClientProvider).clearTokenCache();
      }
      await ref.read(configNotifierProvider.notifier).save(updated);
      if (context.mounted) showToast(context, 'Settings saved');
    } catch (e) {
      if (!mounted) return;
      setState(() => _saveError = 'Could not save settings: $e');
      if (context.mounted) {
        showToast(context, 'Error: $e', isError: true);
      }
    }
  }

  Future<void> _saveAndStartDaemon(
    BuildContext context,
    AppConfig base,
  ) async {
    final updated = _buildConfig(base);
    final token = _tokenController.text.trim();
    setState(() => _saveError = null);
    if (!_tokenFromGh && token.isEmpty) {
      showToast(context, 'GitHub token is required', isError: true);
      return;
    }

    await ref.read(configNotifierProvider.notifier).saveAndStartDaemon(
      token: _tokenFromGh ? _tokenController.text.trim() : token,
      config: updated,
      daemonBinaryPath:
          ref.read(platformServicesProvider).defaultDaemonBinaryPath() ?? '',
    );

    if (!context.mounted) return;
    final state = ref.read(configNotifierProvider);
    if (state.hasError) {
      if (mounted) {
        setState(() => _saveError = '${state.error}');
      }
      showToast(context, '${state.error}', isError: true);
      return;
    }

    ref.invalidate(daemonHealthProvider);
    context.canPop() ? context.pop() : context.go('/');
  }

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

  Widget _statusMessage({
    required IconData icon,
    required String text,
    required Color background,
    required Color foreground,
  }) {
    return DecoratedBox(
      decoration: BoxDecoration(
        color: background,
        borderRadius: BorderRadius.circular(8),
      ),
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Icon(icon, size: 18, color: foreground),
            const SizedBox(width: 8),
            Expanded(
              child: Text(
                text,
                style: TextStyle(color: foreground),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _SectionIndexButton extends StatelessWidget {
  const _SectionIndexButton({
    required this.icon,
    required this.label,
    required this.summary,
    required this.selected,
    required this.onTap,
  });

  final IconData icon;
  final String label;
  final String summary;
  final bool selected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final background = selected
        ? theme.colorScheme.secondaryContainer
        : theme.colorScheme.surface;
    final foreground = selected
        ? theme.colorScheme.onSecondaryContainer
        : theme.colorScheme.onSurface;

    return Material(
      color: background,
      borderRadius: BorderRadius.circular(8),
      child: InkWell(
        borderRadius: BorderRadius.circular(8),
        onTap: onTap,
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
          child: Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Icon(icon, size: 18, color: foreground),
              const SizedBox(width: 10),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      label,
                      style: theme.textTheme.titleSmall?.copyWith(
                        color: foreground,
                        fontWeight: FontWeight.w600,
                      ),
                    ),
                    const SizedBox(height: 2),
                    Text(
                      summary,
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: foreground.withValues(alpha: 0.8),
                      ),
                    ),
                  ],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
