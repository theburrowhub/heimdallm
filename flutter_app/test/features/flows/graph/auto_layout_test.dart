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

  test('a tall column is shifted so every step lies on the canvas', () {
    // A binary tree three conditions deep: eight agents share column 3.
    final nodes = <String, FlowNode>{};
    for (var i = 1; i <= 7; i++) {
      nodes['c$i'] = FlowNode(
        id: 'c$i',
        type: FlowNodeType.condition,
        yes: i < 4 ? 'c${2 * i}' : 'a${2 * i - 7}',
        no: i < 4 ? 'c${2 * i + 1}' : 'a${2 * i - 6}',
      );
    }
    for (var i = 1; i <= 8; i++) {
      nodes['a$i'] = FlowNode(id: 'a$i', type: FlowNodeType.agent);
    }
    final out = autoLayout(ReviewFlow(start: 'c1', nodes: nodes));
    for (final n in out.nodes.values) {
      expect(n.position!.dy, greaterThanOrEqualTo(kLayoutMargin), reason: n.id);
      expect(n.position!.dx, greaterThanOrEqualTo(kLayoutMargin), reason: n.id);
    }
  });

  test('positions kept by onlyMissing are moved off negative coordinates', () {
    final placed = flow.copyWith(
      nodes: {
        ...flow.nodes,
        'a1': flow.nodes['a1']!.copyWith(position: const Offset(-300, -50)),
      },
    );
    final out = autoLayout(placed, onlyMissing: true);
    for (final n in out.nodes.values) {
      expect(n.position!.dx, greaterThanOrEqualTo(0), reason: n.id);
      expect(n.position!.dy, greaterThanOrEqualTo(0), reason: n.id);
    }
  });
}
