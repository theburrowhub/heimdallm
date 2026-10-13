import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/editor/inspector/agent_inspector.dart';
import 'package:heimdallm/features/flows/editor/inspector/condition_inspector.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

const _agents = [
  FlowAgent(id: 'claude', installed: true),
  FlowAgent(id: 'codex', installed: true),
  FlowAgent(id: 'gemini'),
];

/// Loads a flow with one condition `c1` (one schedule) or one agent `a1`,
/// selects it and pumps the matching inspector, rebuilt from the provider
/// so edits show up as they would in the editor.
Future<ProviderContainer> pumpInspector(
  WidgetTester tester, {
  required bool condition,
}) async {
  tester.view.physicalSize = const Size(800, 1400);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  final c = ProviderContainer();
  addTearDown(c.dispose);
  c.listen(flowEditorProvider, (_, _) {});
  final id = condition ? 'c1' : 'a1';
  c
      .read(flowEditorProvider.notifier)
      .load(
        ReviewFlow(
          name: 'F',
          start: id,
          nodes: {
            id: condition
                ? const FlowNode(
                    id: 'c1',
                    type: FlowNodeType.condition,
                    schedule: [ScheduleCondition()],
                    position: Offset(300, 200),
                  )
                : const FlowNode(
                    id: 'a1',
                    type: FlowNodeType.agent,
                    position: Offset(300, 200),
                  ),
          },
        ),
      );
  c.read(flowEditorProvider.notifier).select(NodeSelection(id));
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
                final node = ref.watch(flowEditorProvider).flow.nodes[id]!;
                return condition
                    ? ConditionInspector(node: node, agents: _agents)
                    : AgentInspector(node: node, agents: _agents);
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

void main() {
  testWidgets('condition inspector edits match, adds schedule and quota rows', (
    tester,
  ) async {
    final c = await pumpInspector(tester, condition: true);
    await tester.tap(find.byKey(const ValueKey('inspector-match')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Any condition').last);
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.nodes['c1']!.match, 'any');
    await tester.tap(find.byKey(const ValueKey('inspector-add-quota')));
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.nodes['c1']!.quota, hasLength(1));
    await tester.tap(find.byKey(const ValueKey('inspector-add-schedule')));
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.nodes['c1']!.schedule, hasLength(2));
  });

  testWidgets('schedule explains and sets an all-day window', (tester) async {
    final c = await pumpInspector(tester, condition: true);
    expect(find.text('Same start and end = all day'), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('schedule-0-allday')));
    await tester.pumpAndSettle();
    final s = c.read(flowEditorProvider).flow.nodes['c1']!.schedule[0];
    expect((s.from, s.to), ('00:00', '00:00'));
  });

  testWidgets('agent inspector marks agents that are not installed', (
    tester,
  ) async {
    await pumpInspector(tester, condition: false);
    await tester.tap(find.byKey(const ValueKey('inspector-agent')));
    await tester.pumpAndSettle();
    expect(find.text('gemini — not installed'), findsOneWidget);
  });

  testWidgets('schedule fields follow undo', (tester) async {
    final c = await pumpInspector(tester, condition: true);
    final from = find.byKey(const ValueKey('schedule-0-from'));
    await tester.enterText(from, '09:30');
    await tester.pump();
    expect(
      c.read(flowEditorProvider).flow.nodes['c1']!.schedule[0].from,
      '09:30',
    );
    c.read(flowEditorProvider.notifier).undo();
    await tester.pump();
    expect(
      find.descendant(of: from, matching: find.text('08:00')),
      findsOneWidget,
    );
  });
}
