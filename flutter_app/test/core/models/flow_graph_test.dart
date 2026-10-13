import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';

void main() {
  const json = {
    'name': 'Night',
    'start': 'c1',
    'nodes': {
      'c1': {
        'type': 'condition',
        'match': 'any',
        'yes': 'a1',
        'no': 'a2',
        'schedule': {
          's2': {'from': '22:00', 'to': '06:00'},
          's1': {
            'from': '08:00',
            'to': '09:00',
            'days': ['mon'],
          },
        },
      },
      'a1': {
        'type': 'agent',
        'agent': 'codex',
        'next': 'a2',
        'x': 10,
        'y': 20.5,
      },
      'a2': {'type': 'agent', 'agent': 'claude'},
    },
  };

  test('parses a graph with ordered conditions and positions', () {
    final f = ReviewFlow.fromJson(json);
    expect(f.start, 'c1');
    expect(f.nodes['c1']!.type, FlowNodeType.condition);
    expect(f.nodes['c1']!.schedule.first.from, '08:00');
    expect(f.nodes['a1']!.position, const Offset(10, 20.5));
    expect(f.nodes['a2']!.position, isNull);
    expect(f.nodes['c1']!.edges, ['a1', 'a2']);
  });

  test('round-trips to the daemon shape', () {
    final out = ReviewFlow.fromJson(json).toJson();
    final c1 = (out['nodes'] as Map)['c1'] as Map;
    expect(c1['match'], 'any');
    expect((c1['schedule'] as Map).keys, ['s1', 's2']);
    expect(c1.containsKey('agent'), isFalse);
    final a2 = (out['nodes'] as Map)['a2'] as Map;
    expect(a2.containsKey('x'), isFalse);
    expect(a2.containsKey('next'), isFalse);
  });

  test('nextNodeId never collides', () {
    final f = ReviewFlow.fromJson(json);
    expect(f.nextNodeId('a'), 'a3');
    expect(f.nextNodeId('c'), 'c2');
  });

  test('listing parses agent objects', () {
    final l = FlowListing.fromJson({
      'flows': {'night': json},
      'agents': [
        {
          'id': 'claude',
          'installed': true,
          'review_only': false,
          'write_capable': true,
        },
        {'id': 'openrouter', 'installed': false, 'review_only': true},
      ],
    });
    expect(l.agents.first.installed, isTrue);
    expect(l.agents.last.reviewOnly, isTrue);
    expect(l.flows['night']!.nodes.length, 3);
  });

  test('decision parses the path', () {
    final d = FlowDecision.fromJson({
      'flow_id': 'night',
      'candidates': ['codex'],
      'path': [
        {
          'node': 'c1',
          'type': 'condition',
          'holds': true,
          'reasons': ['ok'],
        },
        {
          'node': 'a1',
          'type': 'agent',
          'agent': 'codex',
          'available': true,
          'added': true,
          'reasons': [],
        },
      ],
    });
    expect(d.path.map((s) => s.node), ['c1', 'a1']);
    expect(d.path.first.holds, isTrue);
    expect(d.path.last.added, isTrue);
  });
}
