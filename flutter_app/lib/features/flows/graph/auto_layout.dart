import 'dart:math' as math;

import 'package:flutter/painting.dart';
import 'package:heimdallm/core/models/flow.dart';

const kNodeSize = Size(220, 76);
const kColumnGap = 120.0;
const kRowGap = 40.0;
const kStartPosition = Offset(40, 200);

/// The least distance a laid-out step keeps from the canvas's top edge.
const kLayoutMargin = 40.0;

/// Layered layout: a node's column is the longest path from Start to it;
/// within a column, nodes keep the order in which a yes-before-no-before-
/// next depth-first walk meets them, so "yes" sits above "no". Unreachable
/// nodes go in a row below everything else. Tall columns are moved down so
/// nothing lands above [kLayoutMargin]. With [onlyMissing], nodes that
/// already have a position keep it; if one of those lies at a negative
/// coordinate, the whole graph moves so it is back on the canvas (the canvas
/// cannot reach a step there).
ReviewFlow autoLayout(ReviewFlow f, {bool onlyMissing = false}) {
  final order = <String>[];
  final seen = <String>{};
  void dfs(String id) {
    final n = f.nodes[id];
    if (n == null || !seen.add(id)) return;
    order.add(id);
    for (final e in n.edges) {
      dfs(e);
    }
  }

  dfs(f.start);
  final depth = <String, int>{for (final id in order) id: 0};
  // Longest path on a DAG: relax in topological order (Kahn). Edges that
  // would close a loop never reach indegree zero, so cycles cannot hang this.
  final indeg = <String, int>{for (final id in order) id: 0};
  for (final id in order) {
    for (final e in f.nodes[id]!.edges) {
      if (indeg.containsKey(e)) indeg[e] = indeg[e]! + 1;
    }
  }
  final ready = [
    for (final id in order)
      if (indeg[id] == 0) id,
  ];
  while (ready.isNotEmpty) {
    final id = ready.removeAt(0);
    for (final e in f.nodes[id]!.edges) {
      if (!indeg.containsKey(e)) continue;
      depth[e] = math.max(depth[e]!, depth[id]! + 1);
      indeg[e] = indeg[e]! - 1;
      if (indeg[e] == 0) ready.add(e);
    }
  }
  final columns = <int, List<String>>{};
  for (final id in order) {
    columns.putIfAbsent(depth[id]!, () => []).add(id);
  }
  final colW = kNodeSize.width + kColumnGap;
  final rowH = kNodeSize.height + kRowGap;
  final pos = <String, Offset>{};
  var maxRows = 1;
  columns.forEach((col, ids) {
    maxRows = math.max(maxRows, ids.length);
    final top = kStartPosition.dy - (ids.length - 1) * rowH / 2;
    for (var i = 0; i < ids.length; i++) {
      pos[ids[i]] = Offset(
        kStartPosition.dx + (col + 1) * colW,
        top + i * rowH,
      );
    }
  });
  final rest = f.nodes.keys.where((id) => !pos.containsKey(id)).toList()
    ..sort();
  final restY = kStartPosition.dy + (maxRows / 2 + 1) * rowH;
  for (var i = 0; i < rest.length; i++) {
    pos[rest[i]] = Offset(kStartPosition.dx + (i + 1) * colW, restY);
  }
  final top = pos.values.fold(kLayoutMargin, (m, p) => math.min(m, p.dy));
  final down = Offset(0, kLayoutMargin - top);
  final nodes = {
    for (final e in f.nodes.entries)
      e.key: onlyMissing && e.value.position != null
          ? e.value
          : e.value.copyWith(position: pos[e.key]! + down),
  };
  var minX = 0.0, minY = 0.0;
  for (final n in nodes.values) {
    minX = math.min(minX, n.position!.dx);
    minY = math.min(minY, n.position!.dy);
  }
  final shift = Offset(-minX, -minY);
  return f.copyWith(
    nodes: shift == Offset.zero
        ? nodes
        : {
            for (final e in nodes.entries)
              e.key: e.value.copyWith(position: e.value.position! + shift),
          },
  );
}
