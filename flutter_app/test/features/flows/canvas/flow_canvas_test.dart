import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/canvas/flow_canvas.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

Future<ProviderContainer> pumpCanvas(
  WidgetTester tester,
  ReviewFlow flow,
) async {
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
        home: const Scaffold(body: FlowCanvas()),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return c;
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

  testWidgets('a cancelled node drag still ends as one undo step', (
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
}
