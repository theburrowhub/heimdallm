import 'dart:ui';

import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/canvas/canvas_geometry.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';

void main() {
  final f = ReviewFlow(
    start: 'c1',
    nodes: {
      'c1': const FlowNode(
        id: 'c1',
        type: FlowNodeType.condition,
        yes: 'a1',
        no: 'a2',
        position: Offset(400, 100),
      ),
      'a1': const FlowNode(
        id: 'a1',
        type: FlowNodeType.agent,
        agent: 'codex',
        next: 'a2',
        position: Offset(800, 50),
      ),
      'a2': const FlowNode(
        id: 'a2',
        type: FlowNodeType.agent,
        agent: 'claude',
        position: Offset(800, 250),
      ),
    },
  );

  test('ports sit on the node edges; yes above no', () {
    final c1 = f.nodes['c1']!;
    expect(inPort(c1.position!), Offset(400, 100 + kNodeSize.height / 2));
    expect(outPort(c1, EdgePort.yes).dx, 400 + kNodeSize.width);
    expect(outPort(c1, EdgePort.yes).dy, lessThan(outPort(c1, EdgePort.no).dy));
  });

  test('edges include start, yes, no and next', () {
    final e = canvasEdges(f);
    expect(
      e.map((x) => '${x.from}:${x.port.name}->${x.to}'),
      unorderedEquals([':start->c1', 'c1:yes->a1', 'c1:no->a2', 'a1:next->a2']),
    );
  });

  test('hit testing', () {
    expect(hitNode(f, const Offset(410, 110)), 'c1');
    expect(hitNode(f, const Offset(0, 0)), isNull);
    final edges = canvasEdges(f);
    final yes = edges.firstWhere((e) => e.port == EdgePort.yes);
    final mid = edgePath(yes.a, yes.b)
        .computeMetrics()
        .first
        .getTangentForOffset(
          edgePath(yes.a, yes.b).computeMetrics().first.length / 2,
        )!
        .position;
    expect(hitEdge(edges, mid)?.port, EdgePort.yes);
    expect(hitEdge(edges, const Offset(5, 5)), isNull);
  });

  test('the canvas covers every placed step, never below the minimum', () {
    expect(canvasSizeFor(f.nodes.values), kMinCanvasSize);
    const far = FlowNode(
      id: 'a9',
      type: FlowNodeType.agent,
      position: Offset(8000, 5000),
    );
    expect(
      canvasSizeFor([...f.nodes.values, far]),
      Size(
        8000 + kNodeSize.width + kCanvasSlack,
        5000 + kNodeSize.height + kCanvasSlack,
      ),
    );
  });
}
