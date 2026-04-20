/// Per-agent CLI execution settings.
/// Stored under ai.agents.<name> in config.toml.
class CLIAgentConfig {
  final String model;         // --model value ('' = use CLI default)
  final int maxTurns;         // claude: --max-turns (0 = not set)
  final String approvalMode;  // codex: --approval-mode ('' = not set)
  final String extraFlags;    // free-form additional CLI flags (space-separated)
  final String? promptId;     // agent-level prompt override (null = use global default)

  // Claude-specific flags
  final String effort;              // '' | 'low' | 'medium' | 'high' | 'max'
  final String permissionMode;      // '' | 'default' | 'auto' | 'bypassPermissions' | 'acceptEdits' | 'dontAsk'
  final bool bare;                  // --bare
  final bool dangerouslySkipPerms;  // --dangerously-skip-permissions
  final bool noSessionPersistence;  // --no-session-persistence

  const CLIAgentConfig({
    this.model = '',
    this.maxTurns = 0,
    this.approvalMode = '',
    this.extraFlags = '',
    this.promptId,
    this.effort = '',
    this.permissionMode = '',
    this.bare = false,
    this.dangerouslySkipPerms = false,
    this.noSessionPersistence = false,
  });

  bool get hasConfig =>
      model.isNotEmpty || maxTurns > 0 || approvalMode.isNotEmpty ||
      extraFlags.isNotEmpty || promptId != null || effort.isNotEmpty ||
      permissionMode.isNotEmpty || bare || dangerouslySkipPerms ||
      noSessionPersistence;

  CLIAgentConfig copyWith({
    String? model,
    int? maxTurns,
    String? approvalMode,
    String? extraFlags,
    Object? promptId = _sentinel,
    String? effort,
    String? permissionMode,
    bool? bare,
    bool? dangerouslySkipPerms,
    bool? noSessionPersistence,
  }) => CLIAgentConfig(
    model:                model        ?? this.model,
    maxTurns:             maxTurns     ?? this.maxTurns,
    approvalMode:         approvalMode ?? this.approvalMode,
    extraFlags:           extraFlags   ?? this.extraFlags,
    promptId:             promptId == _sentinel ? this.promptId : promptId as String?,
    effort:               effort              ?? this.effort,
    permissionMode:       permissionMode      ?? this.permissionMode,
    bare:                 bare                ?? this.bare,
    dangerouslySkipPerms: dangerouslySkipPerms ?? this.dangerouslySkipPerms,
    noSessionPersistence: noSessionPersistence ?? this.noSessionPersistence,
  );

  factory CLIAgentConfig.fromJson(Map<String, dynamic> json) => CLIAgentConfig(
    model:                (json['model']                  as String?) ?? '',
    maxTurns:             (json['max_turns']              as int?)    ?? 0,
    approvalMode:         (json['approval_mode']          as String?) ?? '',
    extraFlags:           (json['extra_flags']            as String?) ?? '',
    promptId:             _nonEmpty(json['prompt']),
    effort:               (json['effort']                 as String?) ?? '',
    permissionMode:       (json['permission_mode']        as String?) ?? '',
    bare:                 (json['bare']                   as bool?)   ?? false,
    dangerouslySkipPerms: (json['dangerously_skip_perms'] as bool?)   ?? false,
    noSessionPersistence: (json['no_session_persistence'] as bool?)   ?? false,
  );

  static const modelOptions = <String, List<String>>{
    'claude': ['claude-opus-4-6', 'claude-sonnet-4-6', 'claude-haiku-4-5-20251001'],
    'gemini': ['gemini-2.5-pro', 'gemini-2.0-flash', 'gemini-1.5-pro'],
    'codex':  ['o4-mini', 'o3', 'gpt-4o'],
  };

  static const approvalModeOptions = ['full-auto', 'auto-edit', 'suggest'];
  static const effortOptions       = ['low', 'medium', 'high', 'max'];
  static const permissionModeOptions = [
    'default', 'auto', 'bypassPermissions', 'acceptEdits', 'dontAsk',
  ];
}

/// Per-repo AI override. null fields mean "use global default".
class RepoConfig {
  // Per-feature activation (null = inherit global behavior)
  final bool? prEnabled;      // PR auto-review
  final bool? itEnabled;      // Issue tracking (triage)
  final bool? devEnabled;     // Develop (auto-implement)

  // General
  final String? localDir;     // local repo directory for full-repo analysis

  // PR Review config
  final String? aiPrimary;    // null = use global
  final String? aiFallback;   // null = use global
  final String? promptId;     // null = use globally active prompt
  final String? reviewMode;   // null = use global ("single" | "multi")

  // Issue tracking overrides (null = inherit global)
  final List<String>? reviewOnlyLabels;
  final List<String>? skipLabels;
  final String? issueFilterMode;
  final String? issueDefaultAction;
  final List<String>? issueOrganizations;
  final List<String>? issueAssignees;
  final String? issuePromptId;

  // Develop overrides (null = inherit global)
  final List<String>? developLabels;
  final String? developPromptId;

  // PR metadata applied after auto_implement creates a PR
  final List<String>? prReviewers;
  final String? prAssignee;
  final List<String>? prLabels;
  final bool? prDraft;

  const RepoConfig({
    this.prEnabled,
    this.itEnabled,
    this.devEnabled,
    this.localDir,
    this.aiPrimary,
    this.aiFallback,
    this.promptId,
    this.reviewMode,
    this.reviewOnlyLabels,
    this.skipLabels,
    this.issueFilterMode,
    this.issueDefaultAction,
    this.issueOrganizations,
    this.issueAssignees,
    this.issuePromptId,
    this.developLabels,
    this.developPromptId,
    this.prReviewers,
    this.prAssignee,
    this.prLabels,
    this.prDraft,
  });

  /// True if any feature is actively enabled (per-repo or inherited).
  /// Used by the repo list to classify monitored vs not-monitored,
  /// and by the TOML writer to decide which repos go in `repositories`.
  bool get isMonitored =>
      (prEnabled ?? false) ||
      (itEnabled ?? false) ||
      (devEnabled ?? false) ||
      (reviewOnlyLabels != null && reviewOnlyLabels!.isNotEmpty) ||
      (developLabels != null && developLabels!.isNotEmpty);

  /// Legacy getter — repos with any override need to be written to TOML.
  bool get hasAiOverride =>
      prEnabled != null || itEnabled != null || devEnabled != null ||
      aiPrimary != null || aiFallback != null || promptId != null ||
      reviewMode != null || (localDir != null && localDir!.isNotEmpty) ||
      developLabels != null || reviewOnlyLabels != null || skipLabels != null ||
      issueFilterMode != null || issueDefaultAction != null ||
      issuePromptId != null || developPromptId != null ||
      prReviewers != null || prAssignee != null || prLabels != null;

  /// LED status for each feature: 'off', 'global', 'repo'
  String prLedStatus(bool globalMonitored) {
    if (prEnabled == true) return 'repo';
    if (prEnabled == false) return 'off';
    return globalMonitored ? 'global' : 'off';
  }
  String itLedStatus(bool globalITEnabled) {
    // Explicit toggle wins
    if (itEnabled == true) return 'repo';
    if (itEnabled == false) return 'off';
    // Labels configured = implicitly active (matches daemon behavior)
    if (reviewOnlyLabels != null && reviewOnlyLabels!.isNotEmpty) return 'repo';
    return globalITEnabled ? 'global' : 'off';
  }
  String devLedStatus(bool globalITEnabled, bool hasLocalDir) {
    if (devEnabled == true) return 'repo';
    if (devEnabled == false) return 'off';
    // Labels configured = implicitly active
    if (developLabels != null && developLabels!.isNotEmpty && hasLocalDir) return 'repo';
    return (globalITEnabled && hasLocalDir) ? 'global' : 'off';
  }

  RepoConfig copyWith({
    Object? prEnabled          = _sentinel,
    Object? itEnabled          = _sentinel,
    Object? devEnabled         = _sentinel,
    Object? localDir           = _sentinel,
    Object? aiPrimary          = _sentinel,
    Object? aiFallback         = _sentinel,
    Object? promptId           = _sentinel,
    Object? reviewMode         = _sentinel,
    Object? reviewOnlyLabels   = _sentinel,
    Object? skipLabels         = _sentinel,
    Object? issueFilterMode    = _sentinel,
    Object? issueDefaultAction = _sentinel,
    Object? issueOrganizations = _sentinel,
    Object? issueAssignees     = _sentinel,
    Object? issuePromptId      = _sentinel,
    Object? developLabels      = _sentinel,
    Object? developPromptId    = _sentinel,
    Object? prReviewers        = _sentinel,
    Object? prAssignee         = _sentinel,
    Object? prLabels           = _sentinel,
    Object? prDraft            = _sentinel,
  }) {
    return RepoConfig(
      prEnabled:          prEnabled          == _sentinel ? this.prEnabled          : prEnabled          as bool?,
      itEnabled:          itEnabled          == _sentinel ? this.itEnabled          : itEnabled          as bool?,
      devEnabled:         devEnabled         == _sentinel ? this.devEnabled         : devEnabled         as bool?,
      localDir:           localDir           == _sentinel ? this.localDir           : localDir           as String?,
      aiPrimary:          aiPrimary          == _sentinel ? this.aiPrimary          : aiPrimary          as String?,
      aiFallback:         aiFallback         == _sentinel ? this.aiFallback         : aiFallback         as String?,
      promptId:           promptId           == _sentinel ? this.promptId           : promptId           as String?,
      reviewMode:         reviewMode         == _sentinel ? this.reviewMode         : reviewMode         as String?,
      reviewOnlyLabels:   reviewOnlyLabels   == _sentinel ? this.reviewOnlyLabels   : reviewOnlyLabels   as List<String>?,
      skipLabels:         skipLabels         == _sentinel ? this.skipLabels         : skipLabels         as List<String>?,
      issueFilterMode:    issueFilterMode    == _sentinel ? this.issueFilterMode    : issueFilterMode    as String?,
      issueDefaultAction: issueDefaultAction == _sentinel ? this.issueDefaultAction : issueDefaultAction as String?,
      issueOrganizations: issueOrganizations == _sentinel ? this.issueOrganizations : issueOrganizations as List<String>?,
      issueAssignees:     issueAssignees     == _sentinel ? this.issueAssignees     : issueAssignees     as List<String>?,
      issuePromptId:      issuePromptId      == _sentinel ? this.issuePromptId      : issuePromptId      as String?,
      developLabels:      developLabels      == _sentinel ? this.developLabels      : developLabels      as List<String>?,
      developPromptId:    developPromptId    == _sentinel ? this.developPromptId    : developPromptId    as String?,
      prReviewers:        prReviewers        == _sentinel ? this.prReviewers        : prReviewers        as List<String>?,
      prAssignee:         prAssignee         == _sentinel ? this.prAssignee         : prAssignee         as String?,
      prLabels:           prLabels           == _sentinel ? this.prLabels           : prLabels           as List<String>?,
      prDraft:            prDraft            == _sentinel ? this.prDraft            : prDraft            as bool?,
    );
  }
}

const _sentinel = Object();

/// Returns null for empty or null strings — prevents DropdownButtonFormField
/// assertion errors when Go zero-value strings ("") arrive from the daemon.
String? _nonEmpty(dynamic v) {
  final s = v as String?;
  return (s == null || s.isEmpty) ? null : s;
}

/// Returns null when the list is absent or empty, otherwise a non-empty String list.
List<String>? _nullableStringList(dynamic v) {
  final list = (v as List<dynamic>?)?.cast<String>().where((s) => s.isNotEmpty).toList();
  return (list != null && list.isNotEmpty) ? list : null;
}

/// Issue tracking pipeline configuration.
class IssueTrackingConfig {
  final bool enabled;
  final String filterMode;      // "exclusive" | "inclusive"
  final String defaultAction;   // "ignore" | "review_only"
  final List<String> developLabels;
  final List<String> reviewOnlyLabels;
  final List<String> skipLabels;
  final List<String> organizations;
  final List<String> assignees;

  const IssueTrackingConfig({
    this.enabled = false,
    this.filterMode = 'exclusive',
    this.defaultAction = 'ignore',
    this.developLabels = const [],
    this.reviewOnlyLabels = const [],
    this.skipLabels = const [],
    this.organizations = const [],
    this.assignees = const [],
  });

  IssueTrackingConfig copyWith({
    bool? enabled,
    String? filterMode,
    String? defaultAction,
    List<String>? developLabels,
    List<String>? reviewOnlyLabels,
    List<String>? skipLabels,
    List<String>? organizations,
    List<String>? assignees,
  }) => IssueTrackingConfig(
    enabled:          enabled          ?? this.enabled,
    filterMode:       filterMode       ?? this.filterMode,
    defaultAction:    defaultAction    ?? this.defaultAction,
    developLabels:    developLabels    ?? this.developLabels,
    reviewOnlyLabels: reviewOnlyLabels ?? this.reviewOnlyLabels,
    skipLabels:       skipLabels       ?? this.skipLabels,
    organizations:    organizations    ?? this.organizations,
    assignees:        assignees        ?? this.assignees,
  );

  Map<String, dynamic> toJson() => {
    'enabled':            enabled,
    'filter_mode':        filterMode,
    'default_action':     defaultAction,
    'develop_labels':     developLabels,
    'review_only_labels': reviewOnlyLabels,
    'skip_labels':        skipLabels,
    'organizations':      organizations,
    'assignees':          assignees,
  };

  static const validFilterModes = ['exclusive', 'inclusive'];
  static const validDefaultActions = ['ignore', 'review_only'];

  factory IssueTrackingConfig.fromJson(Map<String, dynamic> json) {
    final rawFilterMode = (json['filter_mode'] as String?) ?? 'exclusive';
    final rawDefaultAction = (json['default_action'] as String?) ?? 'ignore';
    return IssueTrackingConfig(
        enabled:          (json['enabled']       as bool?)   ?? false,
        filterMode:       validFilterModes.contains(rawFilterMode) ? rawFilterMode : 'exclusive',
        defaultAction:    validDefaultActions.contains(rawDefaultAction) ? rawDefaultAction : 'ignore',
        developLabels:    _stringList(json['develop_labels']),
        reviewOnlyLabels: _stringList(json['review_only_labels']),
        skipLabels:       _stringList(json['skip_labels']),
        organizations:    _stringList(json['organizations']),
        assignees:        _stringList(json['assignees']),
      );
  }

  static List<String> _stringList(dynamic v) =>
      (v as List<dynamic>?)?.cast<String>() ?? [];
}

class AppConfig {
  final int serverPort;
  final String pollInterval;
  final String aiPrimary;
  final String aiFallback;
  final String reviewMode; // "single" | "multi"
  final int retentionDays;
  final Map<String, CLIAgentConfig> agentConfigs; // keyed by CLI name
  final Map<String, RepoConfig> repoConfigs;      // keyed by "org/repo"
  final IssueTrackingConfig issueTracking;

  const AppConfig({
    this.serverPort = 7842,
    this.pollInterval = '5m',
    this.aiPrimary = 'claude',
    this.aiFallback = '',
    this.reviewMode = 'single',
    this.retentionDays = 90,
    this.agentConfigs = const {},
    this.repoConfigs = const {},
    this.issueTracking = const IssueTrackingConfig(),
  });

  /// Computed list of monitored repos — this is what the daemon uses.
  /// A repo is monitored if any of its 3 features (PR, IT, Dev) is active.
  List<String> get repositories => (repoConfigs.entries
      .where((e) => e.value.isMonitored)
      .map((e) => e.key)
      .toList()
    ..sort());

  AppConfig copyWith({
    int? serverPort,
    String? pollInterval,
    String? aiPrimary,
    String? aiFallback,
    String? reviewMode,
    int? retentionDays,
    Map<String, CLIAgentConfig>? agentConfigs,
    Map<String, RepoConfig>? repoConfigs,
    IssueTrackingConfig? issueTracking,
  }) {
    return AppConfig(
      serverPort:    serverPort    ?? this.serverPort,
      pollInterval:  pollInterval  ?? this.pollInterval,
      aiPrimary:     aiPrimary     ?? this.aiPrimary,
      aiFallback:    aiFallback    ?? this.aiFallback,
      reviewMode:    reviewMode    ?? this.reviewMode,
      retentionDays: retentionDays ?? this.retentionDays,
      agentConfigs:  agentConfigs  ?? this.agentConfigs,
      repoConfigs:   repoConfigs   ?? this.repoConfigs,
      issueTracking: issueTracking ?? this.issueTracking,
    );
  }

  Map<String, dynamic> toJson() => {
    'server_port':    serverPort,
    'poll_interval':  pollInterval,
    'repositories':   repositories,
    'ai_primary':     aiPrimary,
    'ai_fallback':    aiFallback,
    'review_mode':    reviewMode,
    'retention_days': retentionDays,
    'issue_tracking': issueTracking.toJson(),
  };

  factory AppConfig.fromJson(Map<String, dynamic> json) {
    final repos = (json['repositories'] as List<dynamic>?)?.cast<String>() ?? [];
    final configs = <String, RepoConfig>{
      // Repos in the monitored list have PR review enabled
      for (final r in repos) r: const RepoConfig(prEnabled: true),
    };
    // Restore non-monitored repos
    final nonMonitored = (json['non_monitored'] as List<dynamic>?)?.cast<String>() ?? [];
    for (final r in nonMonitored) {
      configs.putIfAbsent(r, () => const RepoConfig());
    }
    // Per-repo overrides (normalize empty strings to null)
    final overrides = json['repo_overrides'] as Map<String, dynamic>?;
    if (overrides != null) {
      for (final entry in overrides.entries) {
        final ov = entry.value as Map<String, dynamic>;
        final existing = configs[entry.key];
        final itRaw = ov['issue_tracking'] as Map<String, dynamic>?;
        final itEnabled = itRaw?['enabled'] as bool?;
        configs[entry.key] = RepoConfig(
          prEnabled:          existing?.prEnabled,
          itEnabled:          itEnabled,
          devEnabled:         itRaw?['develop_enabled'] as bool?,
          localDir:           _nonEmpty(ov['local_dir']),
          aiPrimary:          _nonEmpty(ov['primary']),
          aiFallback:         _nonEmpty(ov['fallback']),
          reviewMode:         _nonEmpty(ov['review_mode']),
          promptId:           _nonEmpty(ov['prompt']),
          reviewOnlyLabels:   itRaw != null ? _nullableStringList(itRaw['review_only_labels']) : null,
          skipLabels:         itRaw != null ? _nullableStringList(itRaw['skip_labels']) : null,
          issueFilterMode:    itRaw != null ? _nonEmpty(itRaw['filter_mode']) : null,
          issueDefaultAction: itRaw != null ? _nonEmpty(itRaw['default_action']) : null,
          issueOrganizations: itRaw != null ? _nullableStringList(itRaw['organizations']) : null,
          issueAssignees:     itRaw != null ? _nullableStringList(itRaw['assignees']) : null,
          issuePromptId:      itRaw != null ? _nonEmpty(itRaw['issue_prompt']) : null,
          developLabels:      itRaw != null ? _nullableStringList(itRaw['develop_labels']) : null,
          developPromptId:    itRaw != null ? _nonEmpty(itRaw['develop_prompt']) : null,
          prReviewers:        _nullableStringList(ov['pr_reviewers']),
          prAssignee:         _nonEmpty(ov['pr_assignee']),
          prLabels:           _nullableStringList(ov['pr_labels']),
          prDraft:            ov['pr_draft'] as bool?,
        );
      }
    }
    // Agent configs
    final agentsRaw = json['agent_configs'] as Map<String, dynamic>?;
    final agentConfigs = <String, CLIAgentConfig>{};
    if (agentsRaw != null) {
      for (final entry in agentsRaw.entries) {
        agentConfigs[entry.key] =
            CLIAgentConfig.fromJson(entry.value as Map<String, dynamic>);
      }
    }
    final itRaw = json['issue_tracking'] as Map<String, dynamic>?;
    final issueTracking = itRaw != null
        ? IssueTrackingConfig.fromJson(itRaw)
        : const IssueTrackingConfig();

    return AppConfig(
      serverPort:    (json['server_port']   as int?)    ?? 7842,
      pollInterval:  (json['poll_interval'] as String?) ?? '5m',
      aiPrimary:     (json['ai_primary']    as String?) ?? 'claude',
      aiFallback:    (json['ai_fallback']   as String?) ?? '',
      reviewMode:    (json['review_mode']   as String?) ?? 'single',
      retentionDays: (json['retention_days'] as int?)   ?? 90,
      agentConfigs:  agentConfigs,
      repoConfigs:   configs,
      issueTracking: issueTracking,
    );
  }
}
