import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
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
}
