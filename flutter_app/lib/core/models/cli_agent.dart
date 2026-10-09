/// One agent from the daemon's installed-agent catalog (GET /cli-agents).
class CliAgentInfo {
  final String id;
  final String name;

  /// 'agent' (runs reviews) or 'ide' (detected only; reviews go through
  /// [configAgent]).
  final String kind;
  final bool executable;
  final bool installed;
  final bool configured;
  final String version;
  final String path;

  /// The agent whose settings page this entry opens.
  final String configAgent;
  final String homepage;
  final String installHint;

  /// Models the CLI reported for this account (empty when it cannot list
  /// them).
  final List<String> models;

  const CliAgentInfo({
    required this.id,
    required this.name,
    this.kind = 'agent',
    this.executable = true,
    this.installed = false,
    this.configured = false,
    this.version = '',
    this.path = '',
    String? configAgent,
    this.homepage = '',
    this.installHint = '',
    this.models = const [],
  }) : configAgent = configAgent ?? id;

  bool get isIde => kind == 'ide';

  /// An API the daemon calls in-process; it needs a key instead of an
  /// installed binary.
  bool get isProvider => kind == 'provider';

  factory CliAgentInfo.fromJson(Map<String, dynamic> json) {
    final id = (json['id'] as String?) ?? '';
    return CliAgentInfo(
      id: id,
      name: (json['name'] as String?) ?? id,
      kind: (json['kind'] as String?) ?? 'agent',
      executable: (json['executable'] as bool?) ?? false,
      installed: (json['installed'] as bool?) ?? false,
      configured: (json['configured'] as bool?) ?? false,
      version: (json['version'] as String?) ?? '',
      path: (json['path'] as String?) ?? '',
      configAgent: (json['config_agent'] as String?) ?? id,
      homepage: (json['homepage'] as String?) ?? '',
      installHint: (json['install_hint'] as String?) ?? '',
      models: ((json['models'] as List<dynamic>?) ?? const [])
          .whereType<String>()
          .toList(),
    );
  }
}

/// The whole catalog plus when the daemon last scanned the machine.
class CliAgentCatalog {
  final List<CliAgentInfo> agents;
  final DateTime? scannedAt;

  const CliAgentCatalog({required this.agents, this.scannedAt});

  CliAgentInfo? byId(String id) {
    for (final a in agents) {
      if (a.id == id) return a;
    }
    return null;
  }

  factory CliAgentCatalog.fromJson(Map<String, dynamic> json) =>
      CliAgentCatalog(
        agents: ((json['agents'] as List<dynamic>?) ?? const [])
            .whereType<Map<String, dynamic>>()
            .map(CliAgentInfo.fromJson)
            .toList(),
        scannedAt: DateTime.tryParse((json['scanned_at'] as String?) ?? ''),
      );
}

/// Whether an in-process agent has an API key (GET /cli-agents/{id}/key).
class AgentKeyStatus {
  final bool configured;

  /// 'stored' (set from the app), 'env' (daemon environment) or ''.
  final String source;

  const AgentKeyStatus({required this.configured, this.source = ''});

  factory AgentKeyStatus.fromJson(Map<String, dynamic> json) => AgentKeyStatus(
    configured: (json['configured'] as bool?) ?? false,
    source: (json['source'] as String?) ?? '',
  );
}
