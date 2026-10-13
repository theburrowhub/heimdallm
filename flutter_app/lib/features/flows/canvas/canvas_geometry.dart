import 'dart:math' as math;
import 'dart:ui';

import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';

/// Size of the Start pill drawn at [kStartPosition].
const kStartSize = Size(96, 40);

/// The canvas is never smaller than this.
const kMinCanvasSize = Size(6000, 4000);

/// Room left past the furthest step, to drop and drag into.
const kCanvasSlack = 1000.0;

/// A canvas big enough for every placed node with [kCanvasSlack] to spare:
/// pointers only reach what lies inside it.
Size canvasSizeFor(Iterable<FlowNode> nodes) {
  var w = kMinCanvasSize.width, h = kMinCanvasSize.height;
  for (final n in nodes) {
    final p = n.position;
    if (p == null) continue;
    w = math.max(w, p.dx + kNodeSize.width + kCanvasSlack);
    h = math.max(h, p.dy + kNodeSize.height + kCanvasSlack);
  }
  return Size(w, h);
}

/// One drawn connection: [from] ('' for Start) leaves through [port] at [a]
/// and enters [to] at [b]. Coordinates are canvas coordinates.
class CanvasEdge {
  final String from;
  final EdgePort port;
  final String to;
  final Offset a;
  final Offset b;
  const CanvasEdge(this.from, this.port, this.to, this.a, this.b);
}

/// Where edges enter a node placed at [nodePos]: the middle of its left side.
Offset inPort(Offset nodePos) => nodePos + Offset(0, kNodeSize.height / 2);

/// Where [port] leaves [n]: on its right side, yes above no.
Offset outPort(FlowNode n, EdgePort port) {
  final p = n.position ?? Offset.zero;
  final y = switch (port) {
    EdgePort.yes => kNodeSize.height * .3,
    EdgePort.no => kNodeSize.height * .7,
    _ => kNodeSize.height / 2,
  };
  return p + Offset(kNodeSize.width, y);
}

Offset startOutPort() =>
    kStartPosition + Offset(kStartSize.width, kStartSize.height / 2);

/// Every connection of [f] whose ends are laid out: Start first, then each
/// node's yes, no and next.
List<CanvasEdge> canvasEdges(ReviewFlow f) {
  final out = <CanvasEdge>[];
  final start = f.nodes[f.start];
  if (start?.position != null) {
    out.add(
      CanvasEdge(
        '',
        EdgePort.start,
        f.start,
        startOutPort(),
        inPort(start!.position!),
      ),
    );
  }
  for (final n in f.nodes.values) {
    for (final (port, to) in [
      (EdgePort.yes, n.yes),
      (EdgePort.no, n.no),
      (EdgePort.next, n.next),
    ]) {
      final target = f.nodes[to];
      if (to.isEmpty || target?.position == null || n.position == null) {
        continue;
      }
      out.add(
        CanvasEdge(n.id, port, to, outPort(n, port), inPort(target!.position!)),
      );
    }
  }
  return out;
}

double _bend(Offset a, Offset b) =>
    ((b.dx - a.dx).abs() / 2).clamp(40.0, 200.0);

/// A horizontal S-curve from [a] to [b]; it is point-symmetric, so its
/// midpoint is the chord midpoint.
Path edgePath(Offset a, Offset b) {
  final dx = _bend(a, b);
  return Path()
    ..moveTo(a.dx, a.dy)
    ..cubicTo(a.dx + dx, a.dy, b.dx - dx, b.dy, b.dx, b.dy);
}

/// The first edge passing within [tolerance] of [p], if any.
CanvasEdge? hitEdge(List<CanvasEdge> edges, Offset p, {double tolerance = 8}) {
  for (final e in edges) {
    // A cubic stays inside the box of its control points: cheap reject.
    final dx = _bend(e.a, e.b);
    final box = Rect.fromPoints(e.a, e.b)
        .expandToInclude(
          Rect.fromPoints(e.a + Offset(dx, 0), e.b - Offset(dx, 0)),
        )
        .inflate(tolerance);
    if (!box.contains(p)) continue;
    for (final m in edgePath(e.a, e.b).computeMetrics()) {
      for (var d = 0.0; d <= m.length; d += 4) {
        if ((m.getTangentForOffset(d)!.position - p).distance <= tolerance) {
          return e;
        }
      }
    }
  }
  return null;
}

/// The node under [p], topmost first (nodes are stacked in id order).
String? hitNode(ReviewFlow f, Offset p) {
  final ids = f.nodes.keys.toList()..sort();
  for (final id in ids.reversed) {
    final pos = f.nodes[id]!.position;
    if (pos != null && (pos & kNodeSize).contains(p)) return id;
  }
  return null;
}
