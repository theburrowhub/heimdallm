import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/graph/graph_validation.dart';

ReviewFlow g(Map<String, FlowNode> nodes, {String start = 'a1'}) =>
    ReviewFlow(name: 'x', start: start, nodes: nodes);
FlowNode agent(String id, {String next = ''}) =>
    FlowNode(id: id, type: FlowNodeType.agent, agent: 'claude', next: next);
FlowNode cond(String id, {String yes = '', String no = ''}) => FlowNode(
  id: id,
  type: FlowNodeType.condition,
  yes: yes,
  no: no,
  schedule: const [ScheduleCondition(from: '22:00', to: '06:00')],
);

void main() {
  test('valid graph has no findings', () {
    final r = checkGraph(g({'a1': agent('a1', next: 'a2'), 'a2': agent('a2')}));
    expect(r.ok, isTrue);
    expect(r.warnings, isEmpty);
  });

  test(
    'cycle, dangling edge, empty condition and missing start are errors',
    () {
      expect(
        checkGraph(g({'a1': agent('a1', next: 'a1')})).errorNodes,
        contains('a1'),
      );
      expect(
        checkGraph(g({'a1': agent('a1', next: 'zz')})).errorNodes,
        contains('a1'),
      );
      final empty = FlowNode(id: 'c1', type: FlowNodeType.condition, yes: 'a1');
      expect(
        checkGraph(g({'c1': empty, 'a1': agent('a1')}, start: 'c1')).errorNodes,
        contains('c1'),
      );
      expect(checkGraph(g({'a1': agent('a1')}, start: '')).ok, isFalse);
      final noAgent = FlowNode(id: 'a1', type: FlowNodeType.agent);
      expect(checkGraph(g({'a1': noAgent})).errorNodes, contains('a1'));
    },
  );

  test('limits, ids and condition fields mirror the daemon', () {
    final many = {for (var i = 0; i < 201; i++) 'n$i': agent('n$i')};
    expect(checkGraph(g(many, start: 'n0')).ok, isFalse);
    expect(checkGraph(g({'A': agent('A')}, start: 'A')).errorNodes, {'A'});
    final tooMany = FlowNode(
      id: 'c1',
      type: FlowNodeType.condition,
      schedule: List.filled(21, const ScheduleCondition()),
    );
    expect(checkGraph(g({'c1': tooMany}, start: 'c1')).errorNodes, {'c1'});
    final badTimes = FlowNode(
      id: 'c1',
      type: FlowNodeType.condition,
      schedule: const [ScheduleCondition(from: '25:00', to: '06:00')],
    );
    expect(checkGraph(g({'c1': badTimes}, start: 'c1')).errorNodes, {'c1'});
    final same = FlowNode(
      id: 'c1',
      type: FlowNodeType.condition,
      schedule: const [ScheduleCondition(from: '06:00', to: '06:00')],
    );
    expect(checkGraph(g({'c1': same}, start: 'c1')).errorNodes, {'c1'});
    final badQuota = FlowNode(
      id: 'c1',
      type: FlowNodeType.condition,
      quota: const [QuotaCondition(agent: '', percent: 120)],
    );
    expect(checkGraph(g({'c1': badQuota}, start: 'c1')).errorNodes, {'c1'});
  });

  test(
    'empty flow without a start is not an error; start without nodes is',
    () {
      expect(checkGraph(const ReviewFlow()).ok, isTrue);
      expect(checkGraph(const ReviewFlow(start: 'a1')).ok, isFalse);
      expect(checkGraph(g({'a1': agent('a1')}, start: 'zz')).ok, isFalse);
    },
  );

  test('unreachable nodes are warnings', () {
    final r = checkGraph(g({'a1': agent('a1'), 'a9': agent('a9')}));
    expect(r.ok, isTrue);
    expect(r.unreachable, {'a9'});
  });

  test('wouldCreateCycle', () {
    final f = g({
      'c1': cond('c1', yes: 'a1'),
      'a1': agent('a1', next: 'a2'),
      'a2': agent('a2'),
    }, start: 'c1');
    expect(wouldCreateCycle(f, 'a2', 'c1'), isTrue);
    expect(wouldCreateCycle(f, 'a2', 'a2'), isTrue);
    expect(wouldCreateCycle(f, 'c1', 'a2'), isFalse);
  });

  test('reachableFrom follows every edge', () {
    final f = g({
      'c1': cond('c1', yes: 'a1', no: 'a2'),
      'a1': agent('a1'),
      'a2': agent('a2'),
      'a3': agent('a3'),
    }, start: 'c1');
    expect(reachableFrom(f), {'c1', 'a1', 'a2'});
  });
}
