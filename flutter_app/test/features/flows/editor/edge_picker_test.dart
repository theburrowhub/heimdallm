import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/editor/inspector/agent_inspector.dart';
import 'package:heimdallm/features/flows/editor/inspector/condition_inspector.dart';
import 'package:heimdallm/features/flows/editor/inspector/flow_inspector.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

const _agents = [
  FlowAgent(id: 'claude', installed: true),
  FlowAgent(id: 'codex', installed: true),
];

/// c1 (yes → a1) → a1 (next → a2) → a2, with Start on c1.
final _flow = ReviewFlow(
  name: 'F',
  start: 'c1',
  nodes: {
    'c1': const FlowNode(
      id: 'c1',
      type: FlowNodeType.condition,
      yes: 'a1',
      schedule: [ScheduleCondition()],
      position: Offset(300, 200),
    ),
    'a1': const FlowNode(
      id: 'a1',
      type: FlowNodeType.agent,
      agent: 'claude',
      next: 'a2',
      position: Offset(700, 140),
    ),
    'a2': const FlowNode(
      id: 'a2',
      type: FlowNodeType.agent,
      agent: 'codex',
      position: Offset(1100, 140),
    ),
  },
);

/// Pumps the inspector the editor would show for the current selection.
Future<ProviderContainer> pumpInspectorFor(
  WidgetTester tester,
  Selection selection, {
  bool readOnly = false,
}) async {
  tester.view.physicalSize = const Size(800, 1400);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  final c = ProviderContainer();
  addTearDown(c.dispose);
  c.listen(flowEditorProvider, (_, _) {});
  c.read(flowEditorProvider.notifier).load(_flow);
  c.read(flowEditorProvider.notifier).select(selection);
  await tester.pumpWidget(
    UncontrolledProviderScope(
      container: c,
      child: MaterialApp(
        builder: (context, child) => HeimdallmTheme.scope(child: child!),
        home: Scaffold(
          body: SingleChildScrollView(
            padding: const EdgeInsets.all(16),
            child: Consumer(
              builder: (context, ref, _) {
                final st = ref.watch(flowEditorProvider);
                final sel = st.selection;
                final n = sel is NodeSelection ? st.flow.nodes[sel.id] : null;
                if (n == null) {
                  return FlowInspector(
                    flowId: 'f',
                    usedBy: const [],
                    readOnly: readOnly,
                  );
                }
                return n.isCondition
                    ? ConditionInspector(
                        key: ValueKey(n.id),
                        node: n,
                        agents: _agents,
                        readOnly: readOnly,
                      )
                    : AgentInspector(
                        key: ValueKey(n.id),
                        node: n,
                        agents: _agents,
                        readOnly: readOnly,
                      );
              },
            ),
          ),
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return c;
}

Future<void> pick(WidgetTester tester, String key, String item) async {
  await tester.tap(find.byKey(ValueKey(key)));
  await tester.pumpAndSettle();
  await tester.tap(find.text(item).last);
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('a condition connects yes and no from the inspector', (
    tester,
  ) async {
    final c = await pumpInspectorFor(tester, const NodeSelection('c1'));
    final ctl = c.read(flowEditorProvider.notifier);
    expect(
      find.descendant(
        of: find.byKey(const ValueKey('inspector-edge-yes')),
        matching: find.text('a1 · claude'),
      ),
      findsOneWidget,
    );
    await pick(tester, 'inspector-edge-no', 'a2 · codex');
    expect(c.read(flowEditorProvider).flow.nodes['c1']!.no, 'a2');
    expect(c.read(flowEditorProvider).selection, const NodeSelection('c1'));
    await pick(tester, 'inspector-edge-yes', '— end —');
    expect(c.read(flowEditorProvider).flow.nodes['c1']!.yes, '');
    // One undo step per change.
    ctl.undo();
    expect(c.read(flowEditorProvider).flow.nodes['c1']!.yes, 'a1');
    await tester.pumpAndSettle();
    expect(
      find.descendant(
        of: find.byKey(const ValueKey('inspector-edge-yes')),
        matching: find.text('a1 · claude'),
      ),
      findsOneWidget,
    );
  });

  testWidgets('steps that would loop are not offered', (tester) async {
    await pumpInspectorFor(tester, const NodeSelection('a2'));
    await tester.tap(find.byKey(const ValueKey('inspector-edge-next')));
    await tester.pumpAndSettle();
    // a2 is reached from c1 and a1: pointing back at either loops.
    expect(find.text('a1 · claude'), findsNothing);
    expect(find.text('c1 · condition'), findsNothing);
    expect(find.text('a2 · codex'), findsNothing);
    expect(find.text('+ New agent'), findsWidgets);
  });

  testWidgets('"+ New agent" adds, connects and selects a step', (
    tester,
  ) async {
    final c = await pumpInspectorFor(tester, const NodeSelection('a2'));
    expect(find.text('If it fails or runs out of quota'), findsOneWidget);
    await pick(tester, 'inspector-edge-next', '+ New agent');
    final st = c.read(flowEditorProvider);
    final id = st.flow.nodes['a2']!.next;
    expect(id, 'a3');
    expect(st.selection, const NodeSelection('a3'));
    final p = st.flow.nodes['a3']!.position!;
    expect(p.dx, greaterThan(1100 + kNodeSize.width));
    expect(st.undoDepth, 1);
    // The inspector now shows the new step.
    expect(find.text('a3'), findsWidgets);
  });

  testWidgets('"+ New condition" from a condition\'s no', (tester) async {
    final c = await pumpInspectorFor(tester, const NodeSelection('c1'));
    await pick(tester, 'inspector-edge-no', '+ New condition');
    final st = c.read(flowEditorProvider);
    expect(st.flow.nodes['c1']!.no, 'c2');
    expect(st.flow.nodes['c2']!.isCondition, isTrue);
    expect(st.selection, const NodeSelection('c2'));
  });

  testWidgets('the flow inspector picks the first step', (tester) async {
    final c = await pumpInspectorFor(tester, const NoSelection());
    await pick(tester, 'inspector-edge-start', 'a1 · claude');
    expect(c.read(flowEditorProvider).flow.start, 'a1');
    await pick(tester, 'inspector-edge-start', '— none —');
    expect(c.read(flowEditorProvider).flow.start, '');
    await pick(tester, 'inspector-edge-start', '+ New condition');
    final st = c.read(flowEditorProvider);
    expect(st.flow.start, 'c2');
    expect(st.selection, const NodeSelection('c2'));
  });

  testWidgets('read-only steps show their exits but cannot change them', (
    tester,
  ) async {
    final c = await pumpInspectorFor(
      tester,
      const NodeSelection('c1'),
      readOnly: true,
    );
    for (final k in ['inspector-edge-yes', 'inspector-edge-no']) {
      expect(
        tester
            .widget<DropdownButtonFormField<String>>(find.byKey(ValueKey(k)))
            .onChanged,
        isNull,
      );
    }
    c.read(flowEditorProvider.notifier).select(const NoSelection());
    await tester.pumpAndSettle();
    expect(
      tester
          .widget<DropdownButtonFormField<String>>(
            find.byKey(const ValueKey('inspector-edge-start')),
          )
          .onChanged,
      isNull,
    );
  });
}
