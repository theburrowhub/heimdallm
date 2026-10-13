import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/canvas/canvas_overlays.dart';
import 'package:heimdallm/features/flows/canvas/flow_canvas.dart';
import 'package:heimdallm/features/flows/canvas/edge_painter.dart';
import 'package:heimdallm/features/flows/canvas/node_widgets.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';
import 'package:heimdallm/features/flows/graph/flow_diff.dart';
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

  Rect viewRect(WidgetTester tester) =>
      tester.getRect(find.byKey(const ValueKey('flow-canvas')));

  bool inView(WidgetTester tester, String key) {
    final view = viewRect(tester).inflate(1);
    final r = tester.getRect(find.byKey(ValueKey(key)));
    return view.contains(r.topLeft) && view.contains(r.bottomRight);
  }

  double scaleOf(WidgetTester tester) =>
      tester.getRect(find.byKey(const ValueKey('node-a1'))).width /
      kNodeSize.width;

  final farAgents = twoAgents.copyWith(
    nodes: {
      ...twoAgents.nodes,
      'a2': twoAgents.nodes['a2']!.copyWith(position: const Offset(3000, 2200)),
    },
  );

  group('view', () {
    testWidgets('a flow that fits is shown as is', (tester) async {
      await pumpCanvas(tester, twoAgents);
      expect(
        tester.getTopLeft(find.byKey(const ValueKey('node-a1'))),
        const Offset(300, 200),
      );
    });

    testWidgets('a flow with steps out of view is fitted when loaded', (
      tester,
    ) async {
      await pumpCanvas(tester, farAgents);
      expect(inView(tester, 'node-a1'), isTrue);
      expect(inView(tester, 'node-a2'), isTrue);
      expect(inView(tester, 'port-start'), isTrue);
      expect(scaleOf(tester), lessThan(1));
    });

    testWidgets('zoom in, zoom out and fit', (tester) async {
      await pumpCanvas(tester, twoAgents);
      await tester.tap(find.byKey(const ValueKey('canvas-zoom-in')));
      await tester.pump();
      expect(scaleOf(tester), closeTo(1.25, .001));
      await tester.tap(find.byKey(const ValueKey('canvas-zoom-out')));
      await tester.tap(find.byKey(const ValueKey('canvas-zoom-out')));
      await tester.pump();
      expect(scaleOf(tester), closeTo(.8, .001));
      for (var i = 0; i < 12; i++) {
        await tester.tap(find.byKey(const ValueKey('canvas-zoom-out')));
      }
      await tester.pump();
      expect(scaleOf(tester), closeTo(.3, .001), reason: 'clamped');
      // Fit never zooms in past 100 %, and centres the graph.
      await tester.tap(find.byKey(const ValueKey('canvas-fit')));
      await tester.pump();
      expect(scaleOf(tester), 1);
      expect(inView(tester, 'node-a2'), isTrue);
      final a1 = tester.getRect(find.byKey(const ValueKey('node-a1')));
      final a2 = tester.getRect(find.byKey(const ValueKey('node-a2')));
      final start = tester.getRect(find.byType(StartNodeCard));
      final graph = start.expandToInclude(a1).expandToInclude(a2);
      // Centred in the part of the view the zoom controls leave free.
      final controls = tester.getRect(find.byType(CanvasZoomControls));
      expect(graph.right, lessThan(controls.left));
      expect((graph.center.dy - viewRect(tester).center.dy).abs(), lessThan(1));
      expect(graph.left - viewRect(tester).left, greaterThan(100));
    });

    testWidgets('a fitted draft stays clear of the banner and the controls', (
      tester,
    ) async {
      final c = await pumpCanvas(tester, twoAgents);
      tester.view.physicalSize = const Size(850, 380);
      await tester.pumpAndSettle();
      c
          .read(flowEditorProvider.notifier)
          .showDraft(
            FlowAssistResult(
              flow: twoAgents.copyWith(
                nodes: {
                  'a1': twoAgents.nodes['a1']!.copyWith(next: 'a3'),
                  'a2': twoAgents.nodes['a2']!,
                  'a3': const FlowNode(
                    id: 'a3',
                    type: FlowNodeType.agent,
                    agent: 'gemini',
                    next: 'a4',
                  ),
                  'a4': const FlowNode(
                    id: 'a4',
                    type: FlowNodeType.agent,
                    agent: 'codex',
                  ),
                },
              ),
            ),
          );
      await tester.pumpAndSettle();
      final banner = tester.getRect(find.byType(CanvasLockedBanner));
      final controls = tester.getRect(find.byType(CanvasZoomControls));
      for (final id in ['a1', 'a2', 'a3', 'a4']) {
        final r = tester.getRect(find.byKey(ValueKey('node-$id')));
        expect(inView(tester, 'node-$id'), isTrue, reason: id);
        expect(r.overlaps(banner), isFalse, reason: '$id under the banner');
        expect(r.overlaps(controls), isFalse, reason: '$id under the zoom');
      }
    });

    testWidgets('a pan out of view is undone by Fit', (tester) async {
      await pumpCanvas(tester, twoAgents);
      await tester.dragFrom(const Offset(1200, 800), const Offset(-900, -500));
      await tester.pumpAndSettle();
      expect(inView(tester, 'node-a1'), isFalse);
      await tester.tap(find.byKey(const ValueKey('canvas-fit')));
      await tester.pump();
      expect(inView(tester, 'node-a1'), isTrue);
      expect(inView(tester, 'node-a2'), isTrue);
    });

    testWidgets('a draft with steps out of view is fitted, every step shown', (
      tester,
    ) async {
      final c = await pumpCanvas(tester, twoAgents);
      // The drafted steps come without positions; one kept step was moved
      // far away by the operator.
      c
          .read(flowEditorProvider.notifier)
          .moveNode('a2', const Offset(2600, 1800));
      await tester.pumpAndSettle();
      c
          .read(flowEditorProvider.notifier)
          .showDraft(
            FlowAssistResult(
              flow: ReviewFlow(
                start: 'c1',
                nodes: {
                  'c1': const FlowNode(
                    id: 'c1',
                    type: FlowNodeType.condition,
                    yes: 'a1',
                    no: 'a3',
                  ),
                  'a1': twoAgents.nodes['a1']!.copyWith(clearPosition: true),
                  'a3': const FlowNode(
                    id: 'a3',
                    type: FlowNodeType.agent,
                    agent: 'gemini',
                  ),
                  'a2': twoAgents.nodes['a2']!.copyWith(clearPosition: true),
                },
              ),
            ),
          );
      await tester.pumpAndSettle();
      for (final id in ['c1', 'a1', 'a2', 'a3']) {
        expect(inView(tester, 'node-$id'), isTrue, reason: id);
      }
      final cards = {
        for (final card in tester.widgetList<FlowNodeCard>(
          find.byType(FlowNodeCard),
        ))
          card.node.id: card.diff,
      };
      expect(cards['c1'], DiffKind.added);
      expect(cards['a3'], DiffKind.added);
    });

    testWidgets('a removed step is drawn struck through', (tester) async {
      final c = await pumpCanvas(tester, twoAgents);
      c
          .read(flowEditorProvider.notifier)
          .showDraft(
            FlowAssistResult(
              flow: ReviewFlow(
                start: 'a1',
                nodes: {'a1': twoAgents.nodes['a1']!},
              ),
            ),
          );
      await tester.pumpAndSettle();
      final ghost = tester
          .widgetList<FlowNodeCard>(find.byType(FlowNodeCard))
          .singleWhere((c) => c.ghost);
      expect(ghost.node.id, 'a2');
      final title = tester.widget<Text>(
        find.descendant(of: find.byWidget(ghost), matching: find.text('codex')),
      );
      expect(title.style?.decoration, TextDecoration.lineThrough);
      expect(find.byKey(const ValueKey('diff-a2')), findsOneWidget);
    });
  });

  group('drag reliability', () {
    // An edge running under a card used to show its scissors on the card;
    // they took the pointer and the card could not be dragged there.
    testWidgets('a step lying on an edge drags from anywhere on it', (
      tester,
    ) async {
      final c = await pumpCanvas(
        tester,
        twoAgents.copyWith(
          nodes: {
            'a1': twoAgents.nodes['a1']!.copyWith(next: 'a2'),
            'a2': twoAgents.nodes['a2']!.copyWith(
              position: const Offset(1100, 200),
            ),
            'a3': const FlowNode(
              id: 'a3',
              type: FlowNodeType.agent,
              position: Offset(700, 200),
            ),
          },
        ),
      );
      // The a1 → a2 edge passes under a3; this is its midpoint.
      const onEdge = Offset(810, 238);
      final mouse = await tester.createGesture(kind: PointerDeviceKind.mouse);
      await mouse.addPointer(location: onEdge);
      await tester.pump();
      expect(find.byKey(const ValueKey('edge-cut')), findsNothing);
      await mouse.down(onEdge);
      await mouse.moveBy(const Offset(20, 0));
      await mouse.moveBy(const Offset(20, 40));
      await mouse.up();
      await tester.pumpAndSettle();
      expect(
        c.read(flowEditorProvider).flow.nodes['a3']!.position,
        const Offset(740, 240),
      );
    });

    // With inertia, a step grabbed while the canvas still slid after a fling
    // rode the canvas and was dropped away from the pointer.
    testWidgets('a step grabbed right after a fling follows the pointer', (
      tester,
    ) async {
      final c = await pumpCanvas(tester, twoAgents);
      await tester.flingFrom(
        const Offset(1200, 800),
        const Offset(-200, 0),
        3000,
      );
      await tester.pump(const Duration(milliseconds: 16));
      final before = tester.getRect(find.byKey(const ValueKey('node-a1')));
      final g = await tester.startGesture(before.center);
      for (var i = 0; i < 6; i++) {
        await g.moveBy(const Offset(10, 0));
        await tester.pump(const Duration(milliseconds: 16));
      }
      await g.up();
      await tester.pumpAndSettle();
      expect(
        c.read(flowEditorProvider).flow.nodes['a1']!.position,
        const Offset(360, 200),
      );
      expect(
        tester.getRect(find.byKey(const ValueKey('node-a1'))).center -
            before.center,
        const Offset(60, 0),
      );
    });

    for (final (label, zoom) in [
      ('in', 'canvas-zoom-in'),
      ('out', 'canvas-zoom-out'),
    ]) {
      testWidgets('a step drags by the pointer distance zoomed $label', (
        tester,
      ) async {
        // a1 centred on the view, so it stays in it whatever the zoom.
        const at = Offset(690, 462);
        final c = await pumpCanvas(
          tester,
          twoAgents.copyWith(
            nodes: {
              ...twoAgents.nodes,
              'a1': twoAgents.nodes['a1']!.copyWith(position: at),
            },
          ),
        );
        for (var i = 0; i < 3; i++) {
          await tester.tap(find.byKey(ValueKey(zoom)));
        }
        await tester.pump();
        final scale = scaleOf(tester);
        final r = tester.getRect(find.byKey(const ValueKey('node-a1')));
        final g = await tester.startGesture(r.center);
        await g.moveBy(const Offset(30, 0));
        await g.moveBy(const Offset(30, 30));
        await g.up();
        await tester.pumpAndSettle();
        final p = c.read(flowEditorProvider).flow.nodes['a1']!.position!;
        expect(scale, isNot(closeTo(1, .01)));
        expect(p.dx, closeTo(at.dx + 60 / scale, .01));
        expect(p.dy, closeTo(at.dy + 30 / scale, .01));
      });
    }
  });

  group('locked', () {
    testWidgets('a shown draft is announced with Accept and Discard', (
      tester,
    ) async {
      final c = await pumpCanvas(tester, twoAgents);
      final ctl = c.read(flowEditorProvider.notifier);
      expect(find.byType(CanvasLockedBanner), findsNothing);
      final draft = FlowAssistResult(
        flow: twoAgents.copyWith(
          nodes: {
            ...twoAgents.nodes,
            'a1': twoAgents.nodes['a1']!.copyWith(agent: 'gemini'),
          },
        ),
      );
      ctl.showDraft(draft);
      await tester.pumpAndSettle();
      expect(
        find.text('Reviewing the AI draft — accept or discard to edit'),
        findsOneWidget,
      );
      await tester.tap(find.byKey(const ValueKey('canvas-draft-discard')));
      await tester.pumpAndSettle();
      expect(c.read(flowEditorProvider).draft, isNull);
      expect(c.read(flowEditorProvider).flow.nodes['a1']!.agent, 'claude');
      expect(find.byType(CanvasLockedBanner), findsNothing);
      ctl.showDraft(draft);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const ValueKey('canvas-draft-accept')));
      await tester.pumpAndSettle();
      expect(c.read(flowEditorProvider).flow.nodes['a1']!.agent, 'gemini');
      expect(c.read(flowEditorProvider).undoDepth, 1);
    });

    testWidgets('drafting is announced without buttons', (tester) async {
      final c = await pumpCanvas(tester, twoAgents);
      c.read(flowEditorProvider.notifier).setDrafting(true);
      await tester.pump();
      expect(find.byKey(const ValueKey('assist-drafting')), findsOneWidget);
      expect(
        find.text('The agent is drafting — editing is paused'),
        findsOneWidget,
      );
      expect(find.byKey(const ValueKey('canvas-draft-accept')), findsNothing);
    });

    testWidgets('dragging or tapping a step while locked flashes the banner', (
      tester,
    ) async {
      final c = await pumpCanvas(tester, twoAgents);
      c.read(flowEditorProvider.notifier).setDrafting(true);
      await tester.pump();
      bool flashing() => tester
          .widget<CanvasLockedBanner>(find.byType(CanvasLockedBanner))
          .flashing;
      expect(flashing(), isFalse);
      await tester.drag(
        find.byKey(const ValueKey('node-a1')),
        const Offset(50, 30),
      );
      await tester.pump();
      expect(flashing(), isTrue);
      expect(
        c.read(flowEditorProvider).flow.nodes['a1']!.position,
        const Offset(300, 200),
      );
      await tester.pump(const Duration(seconds: 1));
      expect(flashing(), isFalse);
      await tester.tap(find.byKey(const ValueKey('node-a2')));
      await tester.pump();
      expect(flashing(), isTrue);
      await tester.pump(const Duration(seconds: 1));
      final from = tester.getCenter(find.byKey(const ValueKey('port-a1-next')));
      await tester.dragFrom(from, const Offset(200, 0));
      await tester.pump();
      expect(flashing(), isTrue);
      await tester.pump(const Duration(seconds: 1));
    });

    testWidgets('steps show a not-allowed cursor while locked', (tester) async {
      final c = await pumpCanvas(tester, twoAgents);
      MouseCursor cursorOf(String key) => tester
          .widget<MouseRegion>(
            find
                .ancestor(
                  of: find.byKey(ValueKey(key)),
                  matching: find.byType(MouseRegion),
                )
                .first,
          )
          .cursor;
      expect(cursorOf('node-a1'), SystemMouseCursors.grab);
      c.read(flowEditorProvider.notifier).setDrafting(true);
      await tester.pump();
      expect(cursorOf('node-a1'), SystemMouseCursors.forbidden);
      expect(cursorOf('port-a1-next'), SystemMouseCursors.forbidden);
    });
  });

  group('port +', () {
    testWidgets('an open exit adds a connected, selected step', (tester) async {
      final c = await pumpCanvas(tester, twoAgents);
      expect(find.byKey(const ValueKey('port-add-a1-next')), findsOneWidget);
      await tester.tap(find.byKey(const ValueKey('port-add-a1-next')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const ValueKey('port-add-condition')));
      await tester.pumpAndSettle();
      final st = c.read(flowEditorProvider);
      expect(st.flow.nodes['a1']!.next, 'c1');
      expect(st.flow.nodes['c1']!.isCondition, isTrue);
      expect(st.selection, const NodeSelection('c1'));
      final c1 = st.flow.nodes['c1']!.position! & kNodeSize;
      expect(c1.left, greaterThan(300 + kNodeSize.width));
      for (final id in ['a1', 'a2']) {
        expect(
          c1.overlaps(st.flow.nodes[id]!.position! & kNodeSize),
          isFalse,
          reason: id,
        );
      }
      expect(inView(tester, 'node-c1'), isTrue);
      expect(st.undoDepth, 1);
    });

    testWidgets('a connected exit shows its + while its step is hovered', (
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
      expect(find.byKey(const ValueKey('port-add-a1-next')), findsNothing);
      expect(find.byKey(const ValueKey('port-add-start')), findsNothing);
      final mouse = await tester.createGesture(kind: PointerDeviceKind.mouse);
      await mouse.addPointer(
        location: tester.getCenter(find.byKey(const ValueKey('node-a1'))),
      );
      await tester.pump();
      final plus = find.byKey(const ValueKey('port-add-a1-next'));
      expect(plus, findsOneWidget);
      // Still there with the pointer on it, right of the card.
      await mouse.moveTo(tester.getCenter(plus));
      await tester.pump();
      expect(plus, findsOneWidget);
      await mouse.moveTo(const Offset(1200, 800));
      await tester.pump();
      expect(plus, findsNothing);
      // Start's exit, hovered.
      await mouse.moveTo(tester.getCenter(find.byType(StartNodeCard)));
      await tester.pump();
      await tester.tap(find.byKey(const ValueKey('port-add-start')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const ValueKey('port-add-agent')));
      await tester.pumpAndSettle();
      final st = c.read(flowEditorProvider);
      expect(st.flow.start, 'a3');
      expect(st.selection, const NodeSelection('a3'));
    });

    testWidgets('no + on a locked or read-only canvas', (tester) async {
      final c = await pumpCanvas(tester, twoAgents);
      c.read(flowEditorProvider.notifier).setDrafting(true);
      await tester.pump();
      expect(find.byKey(const ValueKey('port-add-a1-next')), findsNothing);
      await pumpCanvas(
        tester,
        twoAgents,
        body: const FlowCanvas(readOnly: true),
      );
      expect(find.byKey(const ValueKey('port-add-a1-next')), findsNothing);
    });

    testWidgets('dragging from the dot still connects', (tester) async {
      final c = await pumpCanvas(tester, twoAgents);
      final from = tester.getCenter(find.byKey(const ValueKey('port-a1-next')));
      final g = await tester.startGesture(from);
      await g.moveTo(from + const Offset(40, 0));
      await g.moveTo(tester.getCenter(find.byKey(const ValueKey('node-a2'))));
      await g.up();
      await tester.pumpAndSettle();
      expect(c.read(flowEditorProvider).flow.nodes['a1']!.next, 'a2');
    });
  });

  testWidgets('the controller zooms and fits too', (tester) async {
    final ctl = FlowCanvasController();
    await pumpCanvas(tester, twoAgents, body: FlowCanvas(controller: ctl));
    ctl.zoomIn();
    await tester.pump();
    expect(scaleOf(tester), closeTo(1.25, .001));
    ctl.zoomOut();
    await tester.pump();
    expect(scaleOf(tester), closeTo(1, .001));
    ctl.zoomOut();
    ctl.fit();
    await tester.pump();
    expect(scaleOf(tester), 1);
  });
}
