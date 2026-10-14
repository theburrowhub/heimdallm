import 'dart:convert';

import 'package:heimdallm/core/models/flow.dart';

enum DiffKind { added, changed, removed }

class FlowDiff {
  final Map<String, DiffKind> nodes;
  const FlowDiff(this.nodes);
  bool get isEmpty => nodes.isEmpty;
}

String _shape(FlowNode n) =>
    jsonEncode(n.copyWith(clearPosition: true).toJson());

/// Node-level differences from [before] to [after], ignoring positions.
FlowDiff diffFlows(ReviewFlow before, ReviewFlow after) {
  final out = <String, DiffKind>{};
  for (final e in after.nodes.entries) {
    final old = before.nodes[e.key];
    if (old == null) {
      out[e.key] = DiffKind.added;
    } else if (_shape(old) != _shape(e.value)) {
      out[e.key] = DiffKind.changed;
    }
  }
  for (final id in before.nodes.keys) {
    if (!after.nodes.containsKey(id)) out[id] = DiffKind.removed;
  }
  return FlowDiff(out);
}
