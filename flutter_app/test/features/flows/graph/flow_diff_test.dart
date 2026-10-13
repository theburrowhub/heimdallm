import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/graph/flow_diff.dart';

void main() {
  const a1 = FlowNode(id: 'a1', type: FlowNodeType.agent, agent: 'claude');
  final before = ReviewFlow(
    start: 'a1',
    nodes: {
      'a1': a1,
      'a2': const FlowNode(id: 'a2', type: FlowNodeType.agent, agent: 'codex'),
    },
  );

  test('added, changed, removed; positions ignored', () {
    final after = ReviewFlow(
      start: 'a1',
      nodes: {
        'a1': a1.copyWith(next: 'a3', position: const Offset(5, 5)),
        'a3': const FlowNode(
          id: 'a3',
          type: FlowNodeType.agent,
          agent: 'gemini',
        ),
      },
    );
    final d = diffFlows(before, after);
    expect(d.nodes, {
      'a1': DiffKind.changed,
      'a3': DiffKind.added,
      'a2': DiffKind.removed,
    });
  });

  test('position-only change is no change', () {
    final moved = before.copyWith(
      nodes: {
        ...before.nodes,
        'a1': a1.copyWith(position: const Offset(1, 1)),
      },
    );
    expect(diffFlows(before, moved).isEmpty, isTrue);
  });
}
