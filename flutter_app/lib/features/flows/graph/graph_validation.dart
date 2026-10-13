import 'package:heimdallm/core/models/flow.dart';

/// Client-side mirror of the daemon's flow checks, so the canvas can mark
/// problems as the operator edits. The daemon remains the authority (it also
/// checks that each agent is installed and allowed).
class GraphCheck {
  final List<FlowIssue> errors;
  final List<FlowIssue> warnings;
  const GraphCheck(this.errors, this.warnings);

  bool get ok => errors.isEmpty;
  Set<String> get errorNodes => {
    for (final e in errors)
      if (e.node.isNotEmpty) e.node,
  };
  Set<String> get unreachable => {
    for (final w in warnings)
      if (w.node.isNotEmpty) w.node,
  };
}

final _idPattern = RegExp(r'^[a-z0-9][a-z0-9_-]{0,63}$');
final _hhmmPattern = RegExp(r'^([01]\d|2[0-3]):[0-5]\d$');
const _weekdays = {'mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'};
const _maxNodes = 200;
const _maxConditions = 20;
const _quotaWindows = {'session', 'weekly', 'monthly', 'credit', 'any'};
const _modelWindowPrefix = 'model:';
const _maxWindowLength = 128;

bool _validWindow(String w) =>
    _quotaWindows.contains(w) ||
    (w.startsWith(_modelWindowPrefix) &&
        w.length > _modelWindowPrefix.length &&
        w.length <= _maxWindowLength);

Set<String> reachableFrom(ReviewFlow f) {
  final seen = <String>{};
  final stack = [f.start];
  while (stack.isNotEmpty) {
    final id = stack.removeLast();
    final n = f.nodes[id];
    if (n == null || !seen.add(id)) continue;
    stack.addAll(n.edges);
  }
  return seen;
}

/// Whether adding the edge from → to would close a loop.
bool wouldCreateCycle(ReviewFlow f, String from, String to) {
  if (from == to) return true;
  final seen = <String>{};
  final stack = [to];
  while (stack.isNotEmpty) {
    final id = stack.removeLast();
    if (id == from) return true;
    if (!seen.add(id)) continue;
    stack.addAll(f.nodes[id]?.edges ?? const []);
  }
  return false;
}

String? _cycleNode(ReviewFlow f, List<String> ids) {
  final colour = <String, int>{};
  String? visit(String id) {
    colour[id] = 1;
    for (final e in f.nodes[id]!.edges) {
      if (!f.nodes.containsKey(e)) continue;
      if (colour[e] == 1) return e;
      if (colour[e] == null) {
        final c = visit(e);
        if (c != null) return c;
      }
    }
    colour[id] = 2;
    return null;
  }

  for (final id in ids) {
    if (colour[id] == null) {
      final c = visit(id);
      if (c != null) return c;
    }
  }
  return null;
}

GraphCheck checkGraph(ReviewFlow f) {
  final errors = <FlowIssue>[];
  void err(String node, String msg, {String field = ''}) =>
      errors.add(FlowIssue(node: node, field: field, message: msg));

  if (f.name.length > 120) {
    err('', 'Name is longer than 120 characters.', field: 'name');
  }
  if (f.nodes.length > _maxNodes) {
    err('', 'At most $_maxNodes steps per flow.', field: 'nodes');
    return GraphCheck(errors, const []);
  }
  if (f.nodes.isEmpty) {
    if (f.start.isNotEmpty) err('', 'The flow has no steps.', field: 'start');
    return GraphCheck(errors, const []);
  }
  if (f.start.isEmpty) {
    err('', 'Connect Start to the first step.', field: 'start');
  } else if (!f.nodes.containsKey(f.start)) {
    err('', 'Start points at a step that no longer exists.', field: 'start');
  }
  final ids = f.nodes.keys.toList()..sort();
  for (final id in ids) {
    final n = f.nodes[id]!;
    if (!_idPattern.hasMatch(id)) err(id, 'Invalid step id.');
    for (final e in n.edges) {
      if (!f.nodes.containsKey(e)) {
        err(id, 'Points at a step that no longer exists.');
      }
    }
    if (n.isCondition) {
      if (!const {'', 'all', 'any'}.contains(n.match)) {
        err(id, 'Match must be all or any.');
      }
      final total = n.quota.length + n.schedule.length;
      if (total == 0) err(id, 'Add at least one schedule or quota condition.');
      if (total > _maxConditions) {
        err(id, 'At most $_maxConditions conditions per step.');
      }
      for (final s in n.schedule) {
        if (!_hhmmPattern.hasMatch(s.from) || !_hhmmPattern.hasMatch(s.to)) {
          err(id, 'Times must be HH:MM.');
        } else if (s.from == s.to) {
          err(id, 'A schedule needs different start and end times.');
        }
        if (s.days.any((d) => !_weekdays.contains(d))) {
          err(id, 'Days must be mon to sun.');
        }
      }
      for (final q in n.quota) {
        if (q.agent.isEmpty) err(id, 'Pick the agent whose quota is checked.');
        if (!_validWindow(q.window)) {
          err(
            id,
            'Quota window must be session, weekly, monthly, credit, any or model:<id>.',
          );
        }
        if (q.op != 'below' && q.op != 'above') {
          err(id, 'Quota op must be below or above.');
        }
        if (q.percent < 0 || q.percent > 100) err(id, 'Percent must be 0-100.');
      }
    } else if (n.agent.isEmpty) {
      err(id, 'Pick an agent.');
    }
  }
  final cyc = _cycleNode(f, ids);
  if (cyc != null) err(cyc, 'This step is part of a loop.');
  final reach = reachableFrom(f);
  final warnings = [
    for (final id in ids)
      if (!reach.contains(id))
        FlowIssue(
          node: id,
          message: 'Not reachable from Start; it is ignored.',
        ),
  ];
  return GraphCheck(errors, warnings);
}
