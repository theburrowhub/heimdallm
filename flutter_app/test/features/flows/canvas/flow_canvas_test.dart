import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/canvas/flow_canvas.dart';
import 'package:heimdallm/features/flows/canvas/edge_painter.dart';
import 'package:heimdallm/features/flows/canvas/node_widgets.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

Future<ProviderContainer> pumpCanvas(
  WidgetTester tester,
  ReviewFlow flow, {
  Widget body = const FlowCanvas(),
}) async {
  tester.view.physicalSize = const Size(1600, 1000);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  final c = ProviderContainer();
  addTearDown(c.dispose);
  c.listen(flowEditorProvider, (_, _) {});
  c.read(flowEditorProvider.notifier).load(flow);
  await tester.pumpWidget(
    UncontrolledProviderScope(
      container: c,
      child: MaterialApp(
        builder: (context, child) => HeimdallmTheme.scope(child: child!),
        home: Scaffold(body: body),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return c;
}

EdgePainter edgePainter(WidgetTester tester) => tester
    .widgetList<CustomPaint>(find.byType(CustomPaint))
    .map((p) => p.painter)
    .whereType<EdgePainter>()
    .single;

/// Pans the canvas from an empty spot and reports whether the view moved.
Future<bool> canvasPans(WidgetTester tester) async {
  final before = tester.getTopLeft(find.byKey(const ValueKey('node-a1')));
  await tester.dragFrom(const Offset(1200, 800), const Offset(-150, -80));
  await tester.pumpAndSettle();
  return tester.getTopLeft(find.byKey(const ValueKey('node-a1'))) != before;
}

final twoAgents = ReviewFlow(
  start: 'a1',
  nodes: {
    'a1': const FlowNode(
      id: 'a1',
      type: FlowNodeType.agent,
      agent: 'claude',
      position: Offset(300, 200),
    ),
    'a2': const FlowNode(
      id: 'a2',
      type: FlowNodeType.agent,
      agent: 'codex',
      position: Offset(700, 200),
    ),
  },
);

void main() {
  testWidgets('renders Start, nodes and agent names', (tester) async {
    await pumpCanvas(tester, twoAgents);
    expect(find.byKey(const ValueKey('port-start')), findsOneWidget);
    expect(find.byKey(const ValueKey('node-a1')), findsOneWidget);
    expect(find.text('claude'), findsOneWidget);
  });

  testWidgets('drag from a port to a node connects them', (tester) async {
    final c = await pumpCanvas(tester, twoAgents);
    final from = tester.getCenter(find.byKey(const ValueKey('port-a1-next')));
    final to = tester.getCenter(find.byKey(const ValueKey('node-a2')));
    final g = await tester.startGesture(from);
    await g.moveTo(Offset.lerp(from, to, .5)!);
    await g.moveTo(to);
    await g.up();
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.nodes['a1']!.next, 'a2');
  });

  testWidgets('a drop that would loop is refused with a message', (
    tester,
  ) async {
    final c = await pumpCanvas(
      tester,
      twoAgents.copyWith(
        nodes: {
          ...twoAgents.nodes,
          'a1': twoAgents.nodes['a1']!.copyWith(next: 'a2'),
        },
      ),
    );
    final from = tester.getCenter(find.byKey(const ValueKey('port-a2-next')));
    final to = tester.getCenter(find.byKey(const ValueKey('node-a1')));
    final g = await tester.startGesture(from);
    await g.moveTo(to);
    await g.up();
    await tester.pump();
    expect(find.textContaining('loop'), findsOneWidget);
    expect(c.read(flowEditorProvider).flow.nodes['a2']!.next, '');
  });

  testWidgets('dragging a node moves it as one undo step', (tester) async {
    final c = await pumpCanvas(tester, twoAgents);
    await tester.drag(
      find.byKey(const ValueKey('node-a1')),
      const Offset(50, 30),
    );
    await tester.pumpAndSettle();
    expect(
      c.read(flowEditorProvider).flow.nodes['a1']!.position,
      const Offset(350, 230),
    );
    c.read(flowEditorProvider.notifier).undo();
    expect(
      c.read(flowEditorProvider).flow.nodes['a1']!.position,
      const Offset(300, 200),
    );
  });

  // After the drag is accepted, a pointer cancel is delivered through the
  // drag's end path (onPanEnd), so this covers end-by-cancel, not onPanCancel.
  testWidgets('a pointer cancel mid-drag commits the move as one undo step', (
    tester,
  ) async {
    final c = await pumpCanvas(tester, twoAgents);
    final g = await tester.startGesture(
      tester.getCenter(find.byKey(const ValueKey('node-a1'))),
    );
    await g.moveBy(const Offset(40, 0));
    await g.moveBy(const Offset(20, 0));
    await g.cancel();
    await tester.pumpAndSettle();
    final ctl = c.read(flowEditorProvider.notifier);
    expect(
      c.read(flowEditorProvider).flow.nodes['a1']!.position,
      const Offset(360, 200),
    );
    expect(c.read(flowEditorProvider).undoDepth, 1);
    ctl.undo();
    expect(
      c.read(flowEditorProvider).flow.nodes['a1']!.position,
      const Offset(300, 200),
    );
    expect(c.read(flowEditorProvider).canUndo, isFalse);
  });

  testWidgets('a draft arriving mid node-drag ends the drag as one step', (
    tester,
  ) async {
    final c = await pumpCanvas(tester, twoAgents);
    final ctl = c.read(flowEditorProvider.notifier);
    final g = await tester.startGesture(
      tester.getCenter(find.byKey(const ValueKey('node-a1'))),
    );
    await g.moveBy(const Offset(40, 0));
    await g.moveBy(const Offset(20, 0));
    ctl.showDraft(FlowAssistResult(flow: twoAgents));
    await tester.pump();
    // The half move is its own undo step, before the draft.
    expect(c.read(flowEditorProvider).undoDepth, 1);
    // Further movement while the draft is shown does nothing.
    await g.moveBy(const Offset(30, 30));
    await g.up();
    await tester.pumpAndSettle();
    expect(
      c.read(flowEditorProvider).flow.nodes['a1']!.position,
      const Offset(360, 200),
    );
    expect(c.read(flowEditorProvider).undoDepth, 1);
    ctl.discardDraft();
    await tester.pumpAndSettle();
    // The next edit is a separate step: no pending drag merges into it.
    ctl.moveNode('a2', const Offset(700, 400));
    expect(c.read(flowEditorProvider).undoDepth, 2);
    ctl.undo();
    expect(
      c.read(flowEditorProvider).flow.nodes['a1']!.position,
      const Offset(360, 200),
    );
    ctl.undo();
    expect(
      c.read(flowEditorProvider).flow.nodes['a1']!.position,
      const Offset(300, 200),
    );
    await tester.pumpAndSettle();
    expect(await canvasPans(tester), isTrue);
  });

  testWidgets('a draft arriving mid port-drag drops the edge being drawn', (
    tester,
  ) async {
    final c = await pumpCanvas(tester, twoAgents);
    final ctl = c.read(flowEditorProvider.notifier);
    final from = tester.getCenter(find.byKey(const ValueKey('port-a1-next')));
    final to = tester.getCenter(find.byKey(const ValueKey('node-a2')));
    final g = await tester.startGesture(from);
    await g.moveTo(Offset.lerp(from, to, .5)!);
    await tester.pump();
    expect(edgePainter(tester).dragFrom, isNotNull);
    ctl.showDraft(FlowAssistResult(flow: twoAgents));
    await tester.pump();
    expect(edgePainter(tester).dragFrom, isNull);
    await g.moveTo(to);
    await g.up();
    await tester.pumpAndSettle();
    expect(edgePainter(tester).dragFrom, isNull);
    ctl.discardDraft();
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.nodes['a1']!.next, '');
    expect(c.read(flowEditorProvider).undoDepth, 0);
    expect(edgePainter(tester).dragFrom, isNull);
    expect(await canvasPans(tester), isTrue);
  });

  testWidgets('a palette drop places the step centred under the pointer', (
    tester,
  ) async {
    final drops = <(FlowNodeType, Offset)>[];
    await pumpCanvas(
      tester,
      twoAgents,
      body: Stack(
        children: [
          Positioned.fill(
            child: FlowCanvas(onDropFromPalette: (t, at) => drops.add((t, at))),
          ),
          const Positioned(
            right: 20,
            bottom: 20,
            child: Draggable<FlowNodeType>(
              data: FlowNodeType.condition,
              feedback: SizedBox(width: 120, height: 40),
              child: ColoredBox(
                key: ValueKey('palette-item'),
                color: Color(0xFF888888),
                child: SizedBox(width: 120, height: 40),
              ),
            ),
          ),
        ],
      ),
    );
    // Grab the item off-centre; the feedback's corner is not the pointer.
    final grab =
        tester.getTopLeft(find.byKey(const ValueKey('palette-item'))) +
        const Offset(100, 30);
    const drop = Offset(500, 600);
    final g = await tester.startGesture(grab);
    await g.moveTo(const Offset(800, 700));
    await g.moveTo(drop);
    await g.up();
    await tester.pumpAndSettle();
    // The canvas sits at the origin with an identity transform.
    expect(drops, [
      (
        FlowNodeType.condition,
        drop - Offset(kNodeSize.width / 2, kNodeSize.height / 2),
      ),
    ]);
  });

  testWidgets('the scissors follow an edge whose end moved', (tester) async {
    final c = await pumpCanvas(
      tester,
      twoAgents.copyWith(
        nodes: {
          ...twoAgents.nodes,
          'a1': twoAgents.nodes['a1']!.copyWith(next: 'a2'),
        },
      ),
    );
    final mouse = await tester.createGesture(kind: PointerDeviceKind.mouse);
    await mouse.addPointer(location: const Offset(610, 238));
    await tester.pump();
    expect(
      tester.getCenter(find.byKey(const ValueKey('edge-cut'))),
      const Offset(610, 238),
    );
    c.read(flowEditorProvider.notifier).moveNode('a2', const Offset(700, 300));
    await tester.pump();
    // a1 next leaves at (520, 238) and now enters a2 at (700, 338).
    expect(
      tester.getCenter(find.byKey(const ValueKey('edge-cut'))),
      const Offset(610, 288),
    );
  });

  testWidgets('tap selects; Delete removes the selected node', (tester) async {
    final c = await pumpCanvas(tester, twoAgents);
    await tester.tap(find.byKey(const ValueKey('node-a2')));
    await tester.pump();
    expect(c.read(flowEditorProvider).selection, const NodeSelection('a2'));
    await tester.sendKeyEvent(LogicalKeyboardKey.delete);
    await tester.pump();
    expect(c.read(flowEditorProvider).flow.nodes.containsKey('a2'), isFalse);
  });

  testWidgets('hovering an edge shows scissors that cut it', (tester) async {
    final c = await pumpCanvas(
      tester,
      twoAgents.copyWith(
        nodes: {
          ...twoAgents.nodes,
          'a1': twoAgents.nodes['a1']!.copyWith(next: 'a2'),
        },
      ),
    );
    final mid = Offset.lerp(
      tester.getCenter(find.byKey(const ValueKey('port-a1-next'))),
      tester.getTopLeft(find.byKey(const ValueKey('node-a2'))) +
          const Offset(0, 38),
      .5,
    )!;
    final mouse = await tester.createGesture(kind: PointerDeviceKind.mouse);
    await mouse.addPointer(location: mid);
    await tester.pump();
    await tester.tap(find.byKey(const ValueKey('edge-cut')));
    await tester.pump();
    expect(c.read(flowEditorProvider).flow.nodes['a1']!.next, '');
  });

  testWidgets('a read-only canvas selects but neither deletes nor connects', (
    tester,
  ) async {
    final c = await pumpCanvas(
      tester,
      twoAgents,
      body: const FlowCanvas(readOnly: true),
    );
    await tester.tap(find.byKey(const ValueKey('node-a2')));
    await tester.pump();
    expect(c.read(flowEditorProvider).selection, const NodeSelection('a2'));
    await tester.sendKeyEvent(LogicalKeyboardKey.delete);
    await tester.pump();
    expect(c.read(flowEditorProvider).flow.nodes.containsKey('a2'), isTrue);
    final from = tester.getCenter(find.byKey(const ValueKey('port-a1-next')));
    final to = tester.getCenter(find.byKey(const ValueKey('node-a2')));
    final g = await tester.startGesture(from);
    await g.moveTo(Offset.lerp(from, to, .5)!);
    await g.moveTo(to);
    await g.up();
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.nodes['a1']!.next, '');
  });

  testWidgets('errorNodes marks steps the daemon rejected', (tester) async {
    await pumpCanvas(
      tester,
      twoAgents,
      body: const FlowCanvas(errorNodes: {'a2'}),
    );
    final cards = {
      for (final card in tester.widgetList<FlowNodeCard>(
        find.byType(FlowNodeCard),
      ))
        card.node.id: card.hasError,
    };
    expect(cards, {'a1': false, 'a2': true});
  });

  testWidgets('a step dragged past the top-left corner can still be tapped', (
    tester,
  ) async {
    final c = await pumpCanvas(tester, twoAgents);
    await tester.drag(
      find.byKey(const ValueKey('node-a1')),
      const Offset(-700, -500),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('node-a1')));
    await tester.pump();
    expect(c.read(flowEditorProvider).selection, const NodeSelection('a1'));
  });

  testWidgets('the canvas grows to hold a step placed far to the right', (
    tester,
  ) async {
    final c = await pumpCanvas(
      tester,
      twoAgents.copyWith(
        nodes: {
          ...twoAgents.nodes,
          'a2': twoAgents.nodes['a2']!.copyWith(
            position: const Offset(6500, 4300),
          ),
        },
      ),
    );
    tester.view.physicalSize = const Size(7200, 4800);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('node-a2')));
    await tester.pump();
    expect(c.read(flowEditorProvider).selection, const NodeSelection('a2'));
  });
}
