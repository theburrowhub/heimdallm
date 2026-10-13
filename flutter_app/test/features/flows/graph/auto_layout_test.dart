import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';

void main() {
  final flow = ReviewFlow(
    start: 'c1',
    nodes: {
      'c1': const FlowNode(
        id: 'c1',
        type: FlowNodeType.condition,
        yes: 'a1',
        no: 'a2',
      ),
      'a1': const FlowNode(
        id: 'a1',
        type: FlowNodeType.agent,
        agent: 'codex',
        next: 'a2',
      ),
      'a2': const FlowNode(id: 'a2', type: FlowNodeType.agent, agent: 'claude'),
      'x9': const FlowNode(id: 'x9', type: FlowNodeType.agent, agent: 'gemini'),
    },
  );

  test('columns follow the longest path from start; yes above no', () {
    final out = autoLayout(flow);
    final p = {for (final n in out.nodes.values) n.id: n.position!};
    final col = kNodeSize.width + kColumnGap;
    expect(p['c1']!.dx, kStartPosition.dx + col);
    expect(p['a1']!.dx, p['c1']!.dx + col);
    expect(p['a2']!.dx, p['a1']!.dx + col); // a2 is after a1 via fallback
    expect(p['a1']!.dy, lessThan(p['a2']!.dy + 1));
  });

  test('every node gets a position, unreachable ones below', () {
    final out = autoLayout(flow);
    expect(out.nodes.values.every((n) => n.position != null), isTrue);
    final maxReachableY = [
      'c1',
      'a1',
      'a2',
    ].map((id) => out.nodes[id]!.position!.dy).reduce((a, b) => a > b ? a : b);
    expect(out.nodes['x9']!.position!.dy, greaterThan(maxReachableY));
  });

  test('onlyMissing keeps existing positions', () {
    final placed = flow.copyWith(
      nodes: {
        ...flow.nodes,
        'a1': flow.nodes['a1']!.copyWith(position: const Offset(999, 999)),
      },
    );
    expect(
      autoLayout(placed, onlyMissing: true).nodes['a1']!.position,
      const Offset(999, 999),
    );
    expect(
      autoLayout(placed).nodes['a1']!.position,
      isNot(const Offset(999, 999)),
    );
  });

  test('layout is stable', () {
    expect(autoLayout(flow).toJson(), autoLayout(flow).toJson());
  });

  test('a flow with no start lays every node out as unreachable', () {
    final out = autoLayout(flow.copyWith(start: ''));
    expect(out.nodes.values.every((n) => n.position != null), isTrue);
  });
}
