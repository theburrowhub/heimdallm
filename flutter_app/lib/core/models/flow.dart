/// Review flows: decision graphs that decide which agents review a PR
/// (mirrors the daemon's config.FlowConfig).
library;

import 'package:flutter/painting.dart';

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
    final window = from == to ? 'all day' : '$from–$to';
    return '$d $window ${tz.isEmpty ? 'daemon time' : tz}';
  }
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

enum FlowNodeType { condition, agent }

/// One step of a flow graph. A condition branches through [yes]/[no]; an
/// agent adds [agent] to the candidates and continues through [next], its
/// fallback. An empty edge ends the walk.
class FlowNode {
  final String id;
  final FlowNodeType type;

  /// 'all' or 'any' (conditions only).
  final String match;
  final List<QuotaCondition> quota;
  final List<ScheduleCondition> schedule;
  final String yes;
  final String no;
  final String agent;
  final String next;

  /// Canvas position; null until laid out.
  final Offset? position;

  const FlowNode({
    required this.id,
    required this.type,
    this.match = 'all',
    this.quota = const [],
    this.schedule = const [],
    this.yes = '',
    this.no = '',
    this.agent = '',
    this.next = '',
    this.position,
  });

  bool get isCondition => type == FlowNodeType.condition;

  /// Outgoing edges that are set, in yes, no, next order.
  List<String> get edges => [
    for (final e in [yes, no, next])
      if (e.isNotEmpty) e,
  ];

  FlowNode copyWith({
    String? id,
    String? match,
    List<QuotaCondition>? quota,
    List<ScheduleCondition>? schedule,
    String? yes,
    String? no,
    String? agent,
    String? next,
    Offset? position,
    bool clearPosition = false,
  }) => FlowNode(
    id: id ?? this.id,
    type: type,
    match: match ?? this.match,
    quota: quota ?? this.quota,
    schedule: schedule ?? this.schedule,
    yes: yes ?? this.yes,
    no: no ?? this.no,
    agent: agent ?? this.agent,
    next: next ?? this.next,
    position: clearPosition ? null : (position ?? this.position),
  );

  /// This node with every edge that points at [target] cleared.
  FlowNode withoutEdgesTo(String target) => copyWith(
    yes: yes == target ? '' : yes,
    no: no == target ? '' : no,
    next: next == target ? '' : next,
  );

  factory FlowNode.fromJson(String id, Map<String, dynamic> j) {
    final x = (j['x'] as num?)?.toDouble();
    final y = (j['y'] as num?)?.toDouble();
    final match = (j['match'] as String?) ?? '';
    return FlowNode(
      id: id,
      type: j['type'] == 'condition'
          ? FlowNodeType.condition
          : FlowNodeType.agent,
      match: match.isEmpty ? 'all' : match,
      quota: _orderedValues(j['quota']).map(QuotaCondition.fromJson).toList(),
      schedule: _orderedValues(
        j['schedule'],
      ).map(ScheduleCondition.fromJson).toList(),
      yes: (j['yes'] as String?) ?? '',
      no: (j['no'] as String?) ?? '',
      agent: (j['agent'] as String?) ?? '',
      next: (j['next'] as String?) ?? '',
      position: x != null && y != null ? Offset(x, y) : null,
    );
  }

  Map<String, dynamic> toJson() => {
    'type': isCondition ? 'condition' : 'agent',
    if (isCondition) ...{
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
      if (yes.isNotEmpty) 'yes': yes,
      if (no.isNotEmpty) 'no': no,
    } else ...{
      'agent': agent,
      if (next.isNotEmpty) 'next': next,
    },
    if (position != null) ...{'x': position!.dx, 'y': position!.dy},
  };
}

/// A flow: a decision graph walked from [start].
class ReviewFlow {
  final String name;
  final String start;
  final Map<String, FlowNode> nodes;

  const ReviewFlow({this.name = '', this.start = '', this.nodes = const {}});

  ReviewFlow copyWith({
    String? name,
    String? start,
    Map<String, FlowNode>? nodes,
  }) => ReviewFlow(
    name: name ?? this.name,
    start: start ?? this.start,
    nodes: nodes ?? this.nodes,
  );

  /// The first free id of the form `<prefix><n>`, n ≥ 1.
  String nextNodeId(String prefix) {
    var n = 1;
    while (nodes.containsKey('$prefix$n')) {
      n++;
    }
    return '$prefix$n';
  }

  factory ReviewFlow.fromJson(Map<String, dynamic> j) {
    final raw = j['nodes'];
    return ReviewFlow(
      name: (j['name'] as String?) ?? '',
      start: (j['start'] as String?) ?? '',
      nodes: {
        if (raw is Map<String, dynamic>)
          for (final e in raw.entries)
            if (e.value is Map<String, dynamic>)
              e.key: FlowNode.fromJson(e.key, e.value as Map<String, dynamic>),
      },
    );
  }

  Map<String, dynamic> toJson() => {
    'name': name,
    'start': start,
    'nodes': {for (final e in nodes.entries) e.key: e.value.toJson()},
  };
}

/// An agent the editor can offer (GET /flows `agents`).
class FlowAgent {
  final String id;
  final bool installed;
  final bool reviewOnly;
  final bool writeCapable;

  const FlowAgent({
    required this.id,
    this.installed = false,
    this.reviewOnly = false,
    this.writeCapable = false,
  });

  factory FlowAgent.fromJson(Map<String, dynamic> j) => FlowAgent(
    id: (j['id'] as String?) ?? '',
    installed: (j['installed'] as bool?) ?? false,
    reviewOnly: (j['review_only'] as bool?) ?? false,
    writeCapable: (j['write_capable'] as bool?) ?? false,
  );
}

/// GET /flows.
class FlowListing {
  final Map<String, ReviewFlow> flows;
  final String selected;
  final String defaultId;
  final List<FlowAgent> agents;
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
      agents: ((j['agents'] as List<dynamic>?) ?? const [])
          .whereType<Map<String, dynamic>>()
          .map(FlowAgent.fromJson)
          .toList(),
      writeCapable: strings(j['write_capable']),
    );
  }
}

/// One node a simulated flow visited.
class FlowPathStep {
  final String node;
  final String type;
  final String agent;
  final bool? holds;
  final bool? available;
  final bool added;
  final List<String> reasons;

  const FlowPathStep({
    required this.node,
    this.type = '',
    this.agent = '',
    this.holds,
    this.available,
    this.added = false,
    this.reasons = const [],
  });

  factory FlowPathStep.fromJson(Map<String, dynamic> j) => FlowPathStep(
    node: (j['node'] as String?) ?? '',
    type: (j['type'] as String?) ?? '',
    agent: (j['agent'] as String?) ?? '',
    holds: j['holds'] as bool?,
    available: j['available'] as bool?,
    added: (j['added'] as bool?) ?? false,
    reasons: ((j['reasons'] as List<dynamic>?) ?? const [])
        .whereType<String>()
        .toList(),
  );
}

/// POST /flows/simulate.
class FlowDecision {
  final String flowId;
  final List<String> candidates;
  final List<FlowPathStep> path;

  const FlowDecision({
    required this.flowId,
    this.candidates = const [],
    this.path = const [],
  });

  factory FlowDecision.fromJson(Map<String, dynamic> j) => FlowDecision(
    flowId: (j['flow_id'] as String?) ?? '',
    candidates: ((j['candidates'] as List<dynamic>?) ?? const [])
        .whereType<String>()
        .toList(),
    path: ((j['path'] as List<dynamic>?) ?? const [])
        .whereType<Map<String, dynamic>>()
        .map(FlowPathStep.fromJson)
        .toList(),
  );
}

/// A validation finding from the daemon, pinned to a node when it has one.
class FlowIssue {
  final String node;
  final String field;
  final String message;

  const FlowIssue({this.node = '', this.field = '', required this.message});

  factory FlowIssue.fromJson(Map<String, dynamic> j) => FlowIssue(
    node: (j['node'] as String?) ?? '',
    field: (j['field'] as String?) ?? '',
    message: (j['message'] as String?) ?? '',
  );
}

/// POST /flows/assist: an unsaved draft.
class FlowAssistResult {
  final ReviewFlow flow;
  final String plan;
  final List<String> warnings;
  final String agent;
  final int durationMs;

  const FlowAssistResult({
    required this.flow,
    this.plan = '',
    this.warnings = const [],
    this.agent = '',
    this.durationMs = 0,
  });

  factory FlowAssistResult.fromJson(Map<String, dynamic> j) => FlowAssistResult(
    flow: ReviewFlow.fromJson((j['flow'] as Map<String, dynamic>?) ?? const {}),
    plan: (j['plan'] as String?) ?? '',
    warnings: ((j['warnings'] as List<dynamic>?) ?? const [])
        .whereType<String>()
        .toList(),
    agent: (j['agent'] as String?) ?? '',
    durationMs: (j['duration_ms'] as num?)?.toInt() ?? 0,
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
