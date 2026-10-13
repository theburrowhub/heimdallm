import 'dart:ui' show Rect;

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';
import 'package:heimdallm/features/flows/graph/flow_diff.dart';

void main() {
  late ProviderContainer c;
  FlowEditorController ctl() => c.read(flowEditorProvider.notifier);
  FlowEditorState st() => c.read(flowEditorProvider);

  setUp(() {
    c = ProviderContainer();
    c.listen(
      flowEditorProvider,
      (_, _) {},
    ); // keep the autoDispose provider alive
    ctl().load(const ReviewFlow(name: 'x'));
  });
  tearDown(() => c.dispose());

  test('add, connect from start, dirty, undo/redo', () {
    final a = ctl().addNode(FlowNodeType.agent, const Offset(300, 200));
    expect(st().flow.nodes[a]!.position, const Offset(300, 200));
    expect(ctl().connect('', EdgePort.start, a), isTrue);
    expect(st().flow.start, a);
    expect(st().dirty, isTrue);
    ctl().undo();
    expect(st().flow.start, '');
    ctl().redo();
    expect(st().flow.start, a);
  });

  test('connect refuses cycles and replaces an occupied port', () {
    final a1 = ctl().addNode(FlowNodeType.agent, Offset.zero);
    final a2 = ctl().addNode(FlowNodeType.agent, Offset.zero);
    final a3 = ctl().addNode(FlowNodeType.agent, Offset.zero);
    expect(ctl().connect(a1, EdgePort.next, a2), isTrue);
    expect(ctl().connect(a2, EdgePort.next, a1), isFalse);
    expect(st().flow.nodes[a2]!.next, '');
    expect(ctl().connect(a1, EdgePort.next, a3), isTrue);
    expect(st().flow.nodes[a1]!.next, a3);
  });

  test('condition ports are yes/no; agent port is next', () {
    final cnd = ctl().addNode(FlowNodeType.condition, Offset.zero);
    final a = ctl().addNode(FlowNodeType.agent, Offset.zero);
    ctl().connect(cnd, EdgePort.yes, a);
    ctl().connect(cnd, EdgePort.no, a);
    expect(st().flow.nodes[cnd]!.yes, a);
    expect(st().flow.nodes[cnd]!.no, a);
    expect(
      ctl().connect(a, EdgePort.yes, cnd),
      isFalse,
    ); // agents have no yes port
  });

  test('deleting a node clears incoming edges and start', () {
    final a1 = ctl().addNode(FlowNodeType.agent, Offset.zero);
    final a2 = ctl().addNode(FlowNodeType.agent, Offset.zero);
    ctl().connect('', EdgePort.start, a2);
    ctl().connect(a1, EdgePort.next, a2);
    ctl().deleteNode(a2);
    expect(st().flow.nodes.containsKey(a2), isFalse);
    expect(st().flow.nodes[a1]!.next, '');
    expect(st().flow.start, '');
    expect(st().check.ok, isFalse); // nothing connected to Start
  });

  test('deleteSelection removes a selected edge', () {
    final a1 = ctl().addNode(FlowNodeType.agent, Offset.zero);
    final a2 = ctl().addNode(FlowNodeType.agent, Offset.zero);
    ctl().connect(a1, EdgePort.next, a2);
    ctl().select(EdgeSelection(a1, EdgePort.next));
    ctl().deleteSelection();
    expect(st().flow.nodes[a1]!.next, '');
    expect(st().flow.nodes.length, 2);
  });

  test('moves during a drag are one undo step', () {
    final a = ctl().addNode(FlowNodeType.agent, Offset.zero);
    ctl().moveNode(a, const Offset(1, 1), commit: false);
    ctl().moveNode(a, const Offset(2, 2), commit: false);
    ctl().moveNode(a, const Offset(3, 3));
    ctl().undo();
    expect(st().flow.nodes[a]!.position, Offset.zero);
  });

  test('undo stack is capped at 100', () {
    final a = ctl().addNode(FlowNodeType.agent, Offset.zero);
    for (var i = 0; i < 150; i++) {
      ctl().moveNode(a, Offset(i.toDouble(), 0));
    }
    var undos = 0;
    while (st().canUndo) {
      ctl().undo();
      undos++;
    }
    expect(undos, 100);
  });

  test(
    'draft: show computes diff and lays out, accept is one undo step, discard keeps flow',
    () {
      final a = ctl().addNode(FlowNodeType.agent, Offset.zero);
      ctl().connect('', EdgePort.start, a);
      final before = st().flow;
      final draftFlow = ReviewFlow(
        name: 'x',
        start: 'a9',
        nodes: {
          'a9': const FlowNode(
            id: 'a9',
            type: FlowNodeType.agent,
            agent: 'codex',
          ),
        },
      );
      ctl().showDraft(
        FlowAssistResult(flow: draftFlow, plan: 'p', warnings: const ['w']),
      );
      expect(st().draft!.diff.nodes, {
        'a9': DiffKind.added,
        a: DiffKind.removed,
      });
      expect(st().draft!.flow.nodes['a9']!.position, isNotNull);
      expect(
        st().flow.toJson(),
        before.toJson(),
      ); // canvas unchanged until accepted
      ctl().discardDraft();
      expect(st().draft, isNull);
      ctl().showDraft(FlowAssistResult(flow: draftFlow));
      ctl().acceptDraft();
      expect(st().flow.start, 'a9');
      ctl().undo();
      expect(st().flow.toJson(), before.toJson());
    },
  );

  test('markSaved clears dirty; load lays out nodes without positions', () {
    ctl().load(
      ReviewFlow(
        start: 'a1',
        nodes: {
          'a1': const FlowNode(
            id: 'a1',
            type: FlowNodeType.agent,
            agent: 'claude',
          ),
        },
      ),
    );
    expect(st().flow.nodes['a1']!.position, isNotNull);
    expect(st().dirty, isFalse);
    ctl().rename('y');
    expect(st().dirty, isTrue);
    ctl().markSaved();
    expect(st().dirty, isFalse);
  });

  test('cancelled drag then load does not leak into the next undo', () {
    final a = ctl().addNode(FlowNodeType.agent, Offset.zero);
    ctl().moveNode(a, const Offset(5, 5), commit: false);
    final other = ReviewFlow(
      name: 'other',
      start: 'a1',
      nodes: {
        'a1': const FlowNode(
          id: 'a1',
          type: FlowNodeType.agent,
          agent: 'claude',
          position: Offset(7, 7),
        ),
      },
    );
    ctl().load(other);
    ctl().rename('edited');
    ctl().undo();
    expect(st().flow.toJson(), other.toJson());
    expect(st().canUndo, isFalse);
  });

  test('undo during a drag returns to before the drag', () {
    final a = ctl().addNode(FlowNodeType.agent, const Offset(1, 1));
    ctl().moveNode(a, const Offset(9, 9), commit: false);
    ctl().undo();
    expect(st().flow.nodes[a]!.position, const Offset(1, 1));
    ctl().redo();
    expect(st().flow.nodes[a]!.position, const Offset(9, 9));
  });

  test('acceptDraft mid-drag undoes to the pre-drag flow', () {
    final a = ctl().addNode(FlowNodeType.agent, Offset.zero);
    final before = st().flow;
    ctl().moveNode(a, const Offset(4, 4), commit: false);
    ctl().showDraft(
      FlowAssistResult(
        flow: ReviewFlow(
          name: 'x',
          start: 'a9',
          nodes: {
            'a9': const FlowNode(
              id: 'a9',
              type: FlowNodeType.agent,
              agent: 'codex',
            ),
          },
        ),
      ),
    );
    ctl().acceptDraft();
    ctl().undo();
    expect(st().flow.toJson(), before.toJson());
  });

  test('undo that removes the selected node clears the selection', () {
    final a = ctl().addNode(FlowNodeType.agent, Offset.zero);
    expect(st().selection, NodeSelection(a));
    ctl().undo();
    expect(st().selection, isA<NoSelection>());
    ctl().redo();
    ctl().select(NodeSelection(a));
    ctl().redo();
    expect(st().selection, NodeSelection(a));
  });

  test('a node moved past the top-left corner stays on the canvas', () {
    final a = ctl().addNode(FlowNodeType.agent, const Offset(300, 200));
    ctl().moveNode(a, const Offset(-500, -40), commit: false);
    expect(st().flow.nodes[a]!.position, Offset.zero);
    ctl().moveNode(a, const Offset(80, -1));
    expect(st().flow.nodes[a]!.position, const Offset(80, 0));
  });

  test('selecting a step keeps the test run highlighted', () {
    final a = ctl().addNode(FlowNodeType.agent, const Offset(300, 200));
    const run = FlowDecision(
      flowId: 'x',
      path: [FlowPathStep(node: 'a1')],
    );
    ctl().setSimulation(run);
    ctl().select(NodeSelection(a));
    expect(st().simulation, same(run));
    ctl().select(const NoSelection());
    expect(st().simulation, same(run));
  });

  bool overlaps(Offset a, Offset b) => (a & kNodeSize).overlaps(b & kNodeSize);

  test('a step added on top of another moves below it', () {
    final a1 = ctl().addNode(FlowNodeType.agent, const Offset(300, 200));
    final a2 = ctl().addNode(FlowNodeType.agent, const Offset(310, 210));
    final p1 = st().flow.nodes[a1]!.position!;
    final p2 = st().flow.nodes[a2]!.position!;
    expect(overlaps(p1, p2), isFalse);
    expect(p2.dx, 310);
    expect(p2.dy, greaterThan(p1.dy + kNodeSize.height));
    expect(st().view?.reveal, a2);
  });

  test('addConnected places, connects and selects the new step as one undo '
      'step', () {
    final c = ctl().addNode(FlowNodeType.condition, const Offset(300, 200));
    final undo = st().undoDepth;
    final yes = ctl().addConnected(c, EdgePort.yes, FlowNodeType.agent)!;
    final no = ctl().addConnected(c, EdgePort.no, FlowNodeType.condition)!;
    final f = st().flow;
    expect(f.nodes[c]!.yes, yes);
    expect(f.nodes[c]!.no, no);
    expect(f.nodes[no]!.isCondition, isTrue);
    expect(f.nodes[yes]!.position!.dx, greaterThan(300 + kNodeSize.width));
    expect(overlaps(f.nodes[yes]!.position!, f.nodes[no]!.position!), isFalse);
    expect(st().selection, NodeSelection(no));
    expect(st().view?.reveal, no);
    expect(st().undoDepth, undo + 2);
    ctl().undo();
    expect(st().flow.nodes.containsKey(no), isFalse);
    expect(st().flow.nodes[c]!.no, '');
    // Agents have no yes exit.
    expect(ctl().addConnected(yes, EdgePort.yes, FlowNodeType.agent), isNull);
  });

  test('addConnected from Start sets the start step', () {
    final a = ctl().addConnected('', EdgePort.start, FlowNodeType.agent)!;
    expect(st().flow.start, a);
    expect(st().flow.nodes[a]!.position!.dx, greaterThan(kStartPosition.dx));
  });

  test('setEdge connects, ends and keeps the selection', () {
    final a1 = ctl().addNode(FlowNodeType.agent, const Offset(300, 200));
    final a2 = ctl().addNode(FlowNodeType.agent, const Offset(700, 200));
    ctl().select(NodeSelection(a1));
    expect(ctl().setEdge(a1, EdgePort.next, a2), isTrue);
    expect(st().flow.nodes[a1]!.next, a2);
    final depth = st().undoDepth;
    expect(ctl().setEdge(a1, EdgePort.next, a2), isTrue);
    expect(st().undoDepth, depth, reason: 'no change, no undo step');
    expect(ctl().setEdge(a2, EdgePort.next, a1), isFalse);
    expect(ctl().setEdge(a1, EdgePort.next, ''), isTrue);
    expect(st().flow.nodes[a1]!.next, '');
    expect(st().selection, NodeSelection(a1));
    expect(ctl().setEdge('', EdgePort.start, a2), isTrue);
    expect(st().flow.start, a2);
    expect(ctl().setEdge('', EdgePort.start, ''), isTrue);
    expect(st().flow.start, '');
  });

  test('connectableTargets leaves out the step itself and loops', () {
    final a1 = ctl().addNode(FlowNodeType.agent, const Offset(300, 200));
    final a2 = ctl().addNode(FlowNodeType.agent, const Offset(700, 200));
    final a3 = ctl().addNode(FlowNodeType.agent, const Offset(1100, 200));
    ctl().connect(a1, EdgePort.next, a2);
    ctl().connect(a2, EdgePort.next, a3);
    final f = st().flow;
    expect(connectableTargets(f, a3, EdgePort.next), isEmpty);
    expect(connectableTargets(f, a1, EdgePort.next), [a2, a3]);
    // a2's own edge does not count against it when it is replaced.
    expect(connectableTargets(f, a2, EdgePort.next), [a3]);
    expect(connectableTargets(f, '', EdgePort.start), [a1, a2, a3]);
  });

  test('a drafted step never lands on a kept or removed step', () {
    ctl().load(
      ReviewFlow(
        start: 'a1',
        nodes: {
          'a1': const FlowNode(
            id: 'a1',
            type: FlowNodeType.agent,
            agent: 'claude',
            next: 'a2',
            position: Offset(380, 200),
          ),
          // Sits where the layout puts a drafted step's column.
          'a2': const FlowNode(
            id: 'a2',
            type: FlowNodeType.agent,
            agent: 'codex',
            position: Offset(720, 200),
          ),
          'a3': const FlowNode(
            id: 'a3',
            type: FlowNodeType.agent,
            agent: 'gemini',
            position: Offset(1060, 200),
          ),
        },
      ),
    );
    final view = st().view;
    ctl().showDraft(
      FlowAssistResult(
        flow: ReviewFlow(
          start: 'a1',
          nodes: {
            'a1': const FlowNode(
              id: 'a1',
              type: FlowNodeType.agent,
              agent: 'claude',
              next: 'c1',
            ),
            'c1': const FlowNode(
              id: 'c1',
              type: FlowNodeType.condition,
              yes: 'a2',
              no: 'a4',
            ),
            'a2': const FlowNode(
              id: 'a2',
              type: FlowNodeType.agent,
              agent: 'codex',
            ),
            'a4': const FlowNode(
              id: 'a4',
              type: FlowNodeType.agent,
              agent: 'gemini',
            ),
          },
        ),
      ),
    );
    final d = st().draft!.flow.nodes;
    expect(d['a1']!.position, const Offset(380, 200));
    expect(d['a2']!.position, const Offset(720, 200));
    final placed = [
      ...d.values.map((n) => n.position!),
      const Offset(1060, 200), // the removed a3, drawn as a ghost
    ];
    for (var i = 0; i < placed.length; i++) {
      for (var j = i + 1; j < placed.length; j++) {
        expect(overlaps(placed[i], placed[j]), isFalse, reason: '$i/$j');
      }
    }
    expect(st().view, isNot(same(view)), reason: 'a draft asks for a fit');
  });

  test('load and arrange ask the canvas to fit', () {
    final loaded = st().view;
    expect(loaded, isNotNull);
    expect(loaded!.reveal, isNull);
    ctl().addNode(FlowNodeType.agent, const Offset(300, 200));
    ctl().arrange();
    expect(st().view!.reveal, isNull);
    expect(st().view, isNot(same(loaded)));
  });

  test('addConnected on a connected exit inserts in between', () {
    final c1 = ctl().addNode(FlowNodeType.condition, const Offset(300, 200));
    final a1 = ctl().addNode(FlowNodeType.agent, const Offset(700, 100));
    final a2 = ctl().addNode(FlowNodeType.agent, const Offset(1100, 100));
    ctl().connect('', EdgePort.start, c1);
    ctl().connect(c1, EdgePort.yes, a1);
    ctl().connect(a1, EdgePort.next, a2);
    final depth = st().undoDepth;
    final c2 = ctl().addConnected(c1, EdgePort.yes, FlowNodeType.condition)!;
    expect(st().flow.nodes[c1]!.yes, c2);
    expect(st().flow.nodes[c2]!.yes, a1);
    final a3 = ctl().addConnected(a1, EdgePort.next, FlowNodeType.agent)!;
    expect(st().flow.nodes[a1]!.next, a3);
    expect(st().flow.nodes[a3]!.next, a2);
    final s = ctl().addConnected('', EdgePort.start, FlowNodeType.agent)!;
    expect(st().flow.start, s);
    expect(st().flow.nodes[s]!.next, c1);
    expect(st().check.unreachable, isEmpty);
    expect(st().undoDepth, depth + 3);
  });

  test('a new step keeps clear of the + right of another step', () {
    final a1 = ctl().addNode(FlowNodeType.agent, const Offset(300, 200));
    final plus = Rect.fromCenter(
      center:
          st().flow.nodes[a1]!.position! +
          Offset(kNodeSize.width + 23, kNodeSize.height / 2),
      width: 18,
      height: 18,
    );
    final a2 = ctl().addNode(FlowNodeType.agent, const Offset(530, 200));
    expect(
      (st().flow.nodes[a2]!.position! & kNodeSize).overlaps(plus),
      isFalse,
    );
    expect(kPlaceGap, greaterThanOrEqualTo(36));
  });
}
