/// Review flows: ordered rules that decide which agent reviews a PR
/// (mirrors the daemon's config.FlowConfig).
library;

/// A quota threshold on one agent's window.
class QuotaCondition {
  final String agent;

  /// session, weekly, monthly, credit, any, or `model:<id>`.
  final String window;

  /// below | above
  final String op;
  final double percent;

  const QuotaCondition({
    required this.agent,
    this.window = 'session',
    this.op = 'below',
    this.percent = 50,
  });

  QuotaCondition copyWith({
    String? agent,
    String? window,
    String? op,
    double? percent,
  }) => QuotaCondition(
    agent: agent ?? this.agent,
    window: window ?? this.window,
    op: op ?? this.op,
    percent: percent ?? this.percent,
  );

  factory QuotaCondition.fromJson(Map<String, dynamic> j) => QuotaCondition(
    agent: (j['agent'] as String?) ?? '',
    window: (j['window'] as String?) ?? 'session',
    op: (j['op'] as String?) ?? 'below',
    percent: (j['percent'] as num?)?.toDouble() ?? 0,
  );

  Map<String, dynamic> toJson() => {
    'agent': agent,
    'window': window,
    'op': op,
    'percent': percent,
  };

  String describe() =>
      '$agent $window quota ${op == 'above' ? '>' : '<'} ${formatPercent(percent)}%';
}

/// A weekday + time-of-day window.
class ScheduleCondition {
  final List<String> days; // mon..sun; empty = every day
  final String from; // HH:MM
  final String to; // HH:MM
  final String tz; // IANA zone; '' = daemon local

  const ScheduleCondition({
    this.days = const ['mon', 'tue', 'wed', 'thu', 'fri'],
    this.from = '08:00',
    this.to = '15:00',
    this.tz = '',
  });

  ScheduleCondition copyWith({
    List<String>? days,
    String? from,
    String? to,
    String? tz,
  }) => ScheduleCondition(
    days: days ?? this.days,
    from: from ?? this.from,
    to: to ?? this.to,
    tz: tz ?? this.tz,
  );

  factory ScheduleCondition.fromJson(Map<String, dynamic> j) =>
      ScheduleCondition(
        days: ((j['days'] as List<dynamic>?) ?? const [])
            .whereType<String>()
            .toList(),
        from: (j['from'] as String?) ?? '',
        to: (j['to'] as String?) ?? '',
        tz: (j['tz'] as String?) ?? '',
      );

  Map<String, dynamic> toJson() => {
    if (days.isNotEmpty) 'days': days,
    'from': from,
    'to': to,
    if (tz.isNotEmpty) 'tz': tz,
  };

  String describe() {
    final d = days.isEmpty ? 'every day' : days.join(', ');
    return '$d $from–$to ${tz.isEmpty ? 'daemon time' : tz}';
  }
}

/// One rule: use [agent] when its conditions hold.
class FlowRule {
  final String agent;

  /// 'all' or 'any'.
  final String match;
  final List<QuotaCondition> quota;
  final List<ScheduleCondition> schedule;

  const FlowRule({
    required this.agent,
    this.match = 'all',
    this.quota = const [],
    this.schedule = const [],
  });

  bool get isCatchAll => quota.isEmpty && schedule.isEmpty;

  FlowRule copyWith({
    String? agent,
    String? match,
    List<QuotaCondition>? quota,
    List<ScheduleCondition>? schedule,
  }) => FlowRule(
    agent: agent ?? this.agent,
    match: match ?? this.match,
    quota: quota ?? this.quota,
    schedule: schedule ?? this.schedule,
  );

  factory FlowRule.fromJson(Map<String, dynamic> j) => FlowRule(
    agent: (j['agent'] as String?) ?? '',
    match: ((j['match'] as String?) ?? '').isEmpty
        ? 'all'
        : j['match'] as String,
    quota: _orderedValues(j['quota'])
        .map((m) => QuotaCondition.fromJson(m))
        .toList(),
    schedule: _orderedValues(j['schedule'])
        .map((m) => ScheduleCondition.fromJson(m))
        .toList(),
  );

  Map<String, dynamic> toJson() => {
    'agent': agent,
    if (match == 'any') 'match': 'any',
    if (quota.isNotEmpty)
      'quota': {
        for (var i = 0; i < quota.length; i++) 'q${i + 1}': quota[i].toJson(),
      },
    if (schedule.isNotEmpty)
      'schedule': {
        for (var i = 0; i < schedule.length; i++)
          's${i + 1}': schedule[i].toJson(),
      },
  };
}

/// Compares map keys like the daemon: numeric keys first, numerically.
/// A threshold as written: 50 → "50", 49.5 → "49.5".
String formatPercent(double p) =>
    p == p.roundToDouble() ? p.round().toString() : p.toString();

int compareFlowKeys(String a, String b) {
  final ai = int.tryParse(a);
  final bi = int.tryParse(b);
  if (ai != null && bi != null && ai != bi) return ai.compareTo(bi);
  if (ai != null && bi != null) return a.compareTo(b);
  if (ai != null) return -1;
  if (bi != null) return 1;
  return a.compareTo(b);
}

List<Map<String, dynamic>> _orderedValues(dynamic raw) {
  if (raw is! Map<String, dynamic>) return const [];
  final keys = raw.keys.toList()..sort(compareFlowKeys);
  return [
    for (final k in keys)
      if (raw[k] is Map<String, dynamic>) raw[k] as Map<String, dynamic>,
  ];
}

/// A flow: an ordered list of rules.
class ReviewFlow {
  final String name;
  final List<FlowRule> rules;

  const ReviewFlow({this.name = '', this.rules = const []});

  ReviewFlow copyWith({String? name, List<FlowRule>? rules}) =>
      ReviewFlow(name: name ?? this.name, rules: rules ?? this.rules);

  factory ReviewFlow.fromJson(Map<String, dynamic> j) {
    final rules = j['rules'];
    final keys = rules is Map<String, dynamic>
        ? (rules.keys.toList()..sort(compareFlowKeys))
        : <String>[];
    return ReviewFlow(
      name: (j['name'] as String?) ?? '',
      rules: [
        for (final k in keys)
          if ((rules as Map<String, dynamic>)[k] is Map<String, dynamic>)
            FlowRule.fromJson(rules[k] as Map<String, dynamic>),
      ],
    );
  }

  /// Rules are renumbered 10, 20, 30… in their current order.
  Map<String, dynamic> toJson() => {
    'name': name,
    'rules': {
      for (var i = 0; i < rules.length; i++)
        '${(i + 1) * 10}': rules[i].toJson(),
    },
  };
}

/// GET /flows.
class FlowListing {
  final Map<String, ReviewFlow> flows;
  final String selected;
  final String defaultId;
  final List<String> agents;
  final List<String> writeCapable;

  const FlowListing({
    this.flows = const {},
    this.selected = 'default',
    this.defaultId = 'default',
    this.agents = const [],
    this.writeCapable = const [],
  });

  List<String> get ids {
    final ids = flows.keys.toList()
      ..sort((a, b) {
        if (a == defaultId) return -1;
        if (b == defaultId) return 1;
        return a.compareTo(b);
      });
    return ids;
  }

  factory FlowListing.fromJson(Map<String, dynamic> j) {
    final raw = (j['flows'] as Map<String, dynamic>?) ?? const {};
    List<String> strings(dynamic v) =>
        ((v as List<dynamic>?) ?? const []).whereType<String>().toList();
    return FlowListing(
      flows: {
        for (final e in raw.entries)
          if (e.value is Map<String, dynamic>)
            e.key: ReviewFlow.fromJson(e.value as Map<String, dynamic>),
      },
      selected: (j['selected'] as String?) ?? 'default',
      defaultId: (j['default_id'] as String?) ?? 'default',
      agents: strings(j['agents']),
      writeCapable: strings(j['write_capable']),
    );
  }
}

/// One rule's outcome in a simulation.
class FlowRuleResult {
  final String key;
  final String agent;
  final bool matched;
  final bool available;
  final List<String> reasons;

  const FlowRuleResult({
    required this.key,
    required this.agent,
    required this.matched,
    required this.available,
    this.reasons = const [],
  });

  factory FlowRuleResult.fromJson(Map<String, dynamic> j) => FlowRuleResult(
    key: (j['key'] as String?) ?? '',
    agent: (j['agent'] as String?) ?? '',
    matched: (j['matched'] as bool?) ?? false,
    available: (j['available'] as bool?) ?? false,
    reasons: ((j['reasons'] as List<dynamic>?) ?? const [])
        .whereType<String>()
        .toList(),
  );
}

/// POST /flows/simulate.
class FlowDecision {
  final String flowId;
  final List<String> candidates;
  final List<FlowRuleResult> rules;

  const FlowDecision({
    required this.flowId,
    this.candidates = const [],
    this.rules = const [],
  });

  factory FlowDecision.fromJson(Map<String, dynamic> j) => FlowDecision(
    flowId: (j['flow_id'] as String?) ?? '',
    candidates: ((j['candidates'] as List<dynamic>?) ?? const [])
        .whereType<String>()
        .toList(),
    rules: ((j['rules'] as List<dynamic>?) ?? const [])
        .whereType<Map<String, dynamic>>()
        .map(FlowRuleResult.fromJson)
        .toList(),
  );
}

/// One quota window (GET /quotas).
class QuotaWindow {
  final String kind;
  final String label;
  final double usedPercent;
  final DateTime? resetsAt;

  const QuotaWindow({
    required this.kind,
    this.label = '',
    required this.usedPercent,
    this.resetsAt,
  });

  String get shortLabel => switch (kind) {
    'session' => '5h',
    'weekly' => '7d',
    'monthly' => 'month',
    'credit' => 'credit',
    'model' => label,
    _ => kind,
  };

  factory QuotaWindow.fromJson(Map<String, dynamic> j) {
    final reset = DateTime.tryParse((j['resets_at'] as String?) ?? '');
    return QuotaWindow(
      kind: (j['kind'] as String?) ?? '',
      label: (j['label'] as String?) ?? '',
      usedPercent: (j['used_percent'] as num?)?.toDouble() ?? 0,
      resetsAt: reset == null || reset.year <= 1 ? null : reset,
    );
  }
}

/// One agent's quota.
class AgentQuota {
  final String agent;
  final bool available;
  final List<QuotaWindow> windows;
  final String error;

  const AgentQuota({
    required this.agent,
    this.available = false,
    this.windows = const [],
    this.error = '',
  });

  String get summary => windows.isEmpty
      ? (available ? 'no limits' : '')
      : windows
            .map((w) => '${w.shortLabel} ${w.usedPercent.round()}%')
            .join(' · ');

  factory AgentQuota.fromJson(Map<String, dynamic> j) => AgentQuota(
    agent: (j['agent'] as String?) ?? '',
    available: (j['available'] as bool?) ?? false,
    windows: ((j['windows'] as List<dynamic>?) ?? const [])
        .whereType<Map<String, dynamic>>()
        .map(QuotaWindow.fromJson)
        .toList(),
    error: (j['error'] as String?) ?? '',
  );
}
