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
  final bool monitored;
  final String? aiPrimary;   // null = use global
  final String? aiFallback;  // null = use global
  final String? promptId;    // null = use globally active prompt
  final String? reviewMode;  // null = use global ("single" | "multi")
  final String? localDir;    // local repo directory for full-repo analysis

  const RepoConfig({
    this.monitored = true,
    this.aiPrimary,
    this.aiFallback,
    this.promptId,
    this.reviewMode,
    this.localDir,
  });

  bool get hasAiOverride =>
      aiPrimary != null || aiFallback != null || promptId != null ||
      reviewMode != null || (localDir != null && localDir!.isNotEmpty);

  RepoConfig copyWith({
    bool? monitored,
    Object? aiPrimary  = _sentinel,
    Object? aiFallback = _sentinel,
    Object? promptId   = _sentinel,
    Object? reviewMode = _sentinel,
    Object? localDir   = _sentinel,
  }) {
    return RepoConfig(
      monitored:  monitored  ?? this.monitored,
      aiPrimary:  aiPrimary  == _sentinel ? this.aiPrimary  : aiPrimary  as String?,
      aiFallback: aiFallback == _sentinel ? this.aiFallback : aiFallback as String?,
      promptId:   promptId   == _sentinel ? this.promptId   : promptId   as String?,
      reviewMode: reviewMode == _sentinel ? this.reviewMode : reviewMode as String?,
      localDir:   localDir   == _sentinel ? this.localDir   : localDir   as String?,
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

class AppConfig {
  final int serverPort;
  final String pollInterval;
  final String aiPrimary;
  final String aiFallback;
  final String reviewMode; // "single" | "multi"
  final int retentionDays;
  final Map<String, CLIAgentConfig> agentConfigs; // keyed by CLI name
  final Map<String, RepoConfig> repoConfigs;      // keyed by "org/repo"

  const AppConfig({
    this.serverPort = 7842,
    this.pollInterval = '5m',
    this.aiPrimary = 'claude',
    this.aiFallback = '',
    this.reviewMode = 'single',
    this.retentionDays = 90,
    this.agentConfigs = const {},
    this.repoConfigs = const {},
  });

  /// Computed list of monitored repos — this is what the daemon uses.
  List<String> get repositories => (repoConfigs.entries
      .where((e) => e.value.monitored)
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
  }) {
    return AppConfig(
      serverPort:   serverPort   ?? this.serverPort,
      pollInterval: pollInterval ?? this.pollInterval,
      aiPrimary:    aiPrimary    ?? this.aiPrimary,
      aiFallback:   aiFallback   ?? this.aiFallback,
      reviewMode:   reviewMode   ?? this.reviewMode,
      retentionDays: retentionDays ?? this.retentionDays,
      agentConfigs: agentConfigs ?? this.agentConfigs,
      repoConfigs:  repoConfigs  ?? this.repoConfigs,
    );
  }

  Map<String, dynamic> toJson() => {
    'server_port':   serverPort,
    'poll_interval': pollInterval,
    'repositories':  repositories,
    'ai_primary':    aiPrimary,
    'ai_fallback':   aiFallback,
    'review_mode':   reviewMode,
    'retention_days': retentionDays,
  };

  factory AppConfig.fromJson(Map<String, dynamic> json) {
    final repos = (json['repositories'] as List<dynamic>?)?.cast<String>() ?? [];
    final configs = <String, RepoConfig>{
      for (final r in repos) r: const RepoConfig(monitored: true),
    };
    // Restore non-monitored repos
    final nonMonitored = (json['non_monitored'] as List<dynamic>?)?.cast<String>() ?? [];
    for (final r in nonMonitored) {
      configs.putIfAbsent(r, () => const RepoConfig(monitored: false));
    }
    // Per-repo overrides (normalize empty strings to null)
    final overrides = json['repo_overrides'] as Map<String, dynamic>?;
    if (overrides != null) {
      for (final entry in overrides.entries) {
        final ov = entry.value as Map<String, dynamic>;
        final existing = configs[entry.key];
        configs[entry.key] = RepoConfig(
          monitored:  existing?.monitored ?? configs.containsKey(entry.key),
          aiPrimary:  _nonEmpty(ov['primary']),
          aiFallback: _nonEmpty(ov['fallback']),
          reviewMode: _nonEmpty(ov['review_mode']),
          localDir:   _nonEmpty(ov['local_dir']),
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
    return AppConfig(
      serverPort:   (json['server_port']   as int?)    ?? 7842,
      pollInterval: (json['poll_interval'] as String?) ?? '5m',
      aiPrimary:    (json['ai_primary']    as String?) ?? 'claude',
      aiFallback:   (json['ai_fallback']   as String?) ?? '',
      reviewMode:   (json['review_mode']   as String?) ?? 'single',
      retentionDays: (json['retention_days'] as int?)  ?? 90,
      agentConfigs: agentConfigs,
      repoConfigs:  configs,
    );
  }
}
