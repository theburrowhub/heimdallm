/// Per-repo AI override. null fields mean "use global default".
class RepoConfig {
  final bool monitored;
  final String? aiPrimary;   // null = use global
  final String? aiFallback;  // null = use global
  final String? promptId;    // null = use globally active prompt
  final String? reviewMode;  // null = use global ("single" | "multi")

  const RepoConfig({
    this.monitored = true,
    this.aiPrimary,
    this.aiFallback,
    this.promptId,
    this.reviewMode,
  });

  bool get hasAiOverride =>
      aiPrimary != null || aiFallback != null || promptId != null || reviewMode != null;

  RepoConfig copyWith({
    bool? monitored,
    Object? aiPrimary = _sentinel,
    Object? aiFallback = _sentinel,
    Object? promptId = _sentinel,
    Object? reviewMode = _sentinel,
  }) {
    return RepoConfig(
      monitored: monitored ?? this.monitored,
      aiPrimary: aiPrimary == _sentinel ? this.aiPrimary : aiPrimary as String?,
      aiFallback: aiFallback == _sentinel ? this.aiFallback : aiFallback as String?,
      promptId: promptId == _sentinel ? this.promptId : promptId as String?,
      reviewMode: reviewMode == _sentinel ? this.reviewMode : reviewMode as String?,
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

  /// All known repos and their per-repo settings.
  /// Key: "org/repo". Monitored repos are sent to the daemon.
  final Map<String, RepoConfig> repoConfigs;

  const AppConfig({
    this.serverPort = 7842,
    this.pollInterval = '5m',
    this.aiPrimary = 'claude',
    this.aiFallback = '',
    this.reviewMode = 'single',
    this.retentionDays = 90,
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
    Map<String, RepoConfig>? repoConfigs,
  }) {
    return AppConfig(
      serverPort: serverPort ?? this.serverPort,
      pollInterval: pollInterval ?? this.pollInterval,
      aiPrimary: aiPrimary ?? this.aiPrimary,
      aiFallback: aiFallback ?? this.aiFallback,
      reviewMode: reviewMode ?? this.reviewMode,
      retentionDays: retentionDays ?? this.retentionDays,
      repoConfigs: repoConfigs ?? this.repoConfigs,
    );
  }

  Map<String, dynamic> toJson() => {
    'server_port': serverPort,
    'poll_interval': pollInterval,
    'repositories': repositories,
    'ai_primary': aiPrimary,
    'ai_fallback': aiFallback,
    'review_mode': reviewMode,
    'retention_days': retentionDays,
  };

  factory AppConfig.fromJson(Map<String, dynamic> json) {
    final repos = (json['repositories'] as List<dynamic>?)?.cast<String>() ?? [];
    // Start with all monitored repos (no override)
    final configs = <String, RepoConfig>{
      for (final r in repos) r: const RepoConfig(monitored: true),
    };
    // Apply per-repo overrides from repo_overrides field.
    // Normalize empty strings to null — Go zero-value strings arrive as ""
    // and would break DropdownButtonFormField (value not in items list).
    final overrides = json['repo_overrides'] as Map<String, dynamic>?;
    if (overrides != null) {
      for (final entry in overrides.entries) {
        final ov = entry.value as Map<String, dynamic>;
        configs[entry.key] = RepoConfig(
          monitored: configs.containsKey(entry.key),
          aiPrimary:   _nonEmpty(ov['primary']),
          aiFallback:  _nonEmpty(ov['fallback']),
          reviewMode:  _nonEmpty(ov['review_mode']),
        );
      }
    }
    return AppConfig(
      serverPort: (json['server_port'] as int?) ?? 7842,
      pollInterval: (json['poll_interval'] as String?) ?? '5m',
      aiPrimary: (json['ai_primary'] as String?) ?? 'claude',
      aiFallback: (json['ai_fallback'] as String?) ?? '',
      reviewMode: (json['review_mode'] as String?) ?? 'single',
      retentionDays: (json['retention_days'] as int?) ?? 90,
      repoConfigs: configs,
    );
  }
}
