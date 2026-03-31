class AppConfig {
  final int serverPort;
  final String pollInterval;
  final List<String> repositories;
  final String aiPrimary;
  final String aiFallback;
  final int retentionDays;

  const AppConfig({
    this.serverPort = 7842,
    this.pollInterval = '5m',
    this.repositories = const [],
    this.aiPrimary = 'claude',
    this.aiFallback = '',
    this.retentionDays = 90,
  });

  AppConfig copyWith({
    int? serverPort,
    String? pollInterval,
    List<String>? repositories,
    String? aiPrimary,
    String? aiFallback,
    int? retentionDays,
  }) {
    return AppConfig(
      serverPort: serverPort ?? this.serverPort,
      pollInterval: pollInterval ?? this.pollInterval,
      repositories: repositories ?? this.repositories,
      aiPrimary: aiPrimary ?? this.aiPrimary,
      aiFallback: aiFallback ?? this.aiFallback,
      retentionDays: retentionDays ?? this.retentionDays,
    );
  }

  Map<String, dynamic> toJson() => {
    'server_port': serverPort,
    'poll_interval': pollInterval,
    'repositories': repositories,
    'ai_primary': aiPrimary,
    'ai_fallback': aiFallback,
    'retention_days': retentionDays,
  };

  factory AppConfig.fromJson(Map<String, dynamic> json) => AppConfig(
    serverPort: (json['server_port'] as int?) ?? 7842,
    pollInterval: (json['poll_interval'] as String?) ?? '5m',
    repositories: (json['repositories'] as List<dynamic>?)?.cast<String>() ?? [],
    aiPrimary: (json['ai_primary'] as String?) ?? 'claude',
    aiFallback: (json['ai_fallback'] as String?) ?? '',
    retentionDays: (json['retention_days'] as int?) ?? 90,
  );
}
