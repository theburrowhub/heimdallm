import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_screen.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:mocktail/mocktail.dart';

class _MockApi extends Mock implements ApiClient {}

final _night = ReviewFlow(
  name: 'Night',
  start: 'a1',
  nodes: {
    'a1': const FlowNode(
      id: 'a1',
      type: FlowNodeType.agent,
      agent: 'claude',
      position: Offset(300, 200),
    ),
  },
);

FlowListing _listing({Map<String, ReviewFlow>? flows}) => FlowListing(
  flows: flows ?? {'night': _night},
  selected: 'default',
  agents: const [
    FlowAgent(id: 'claude', installed: true),
    FlowAgent(id: 'codex', installed: true),
    FlowAgent(id: 'gemini'),
  ],
);

Future<(_MockApi, GoRouter)> pumpEditor(
  WidgetTester tester, {
  String id = 'night',
  FlowListing? listing,
}) async {
  tester.view.physicalSize = const Size(1800, 1100);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  registerFallbackValue(const ReviewFlow());
  final api = _MockApi();
  when(() => api.fetchFlows()).thenAnswer((_) async => listing ?? _listing());
  when(() => api.fetchQuotas()).thenAnswer((_) async => const []);
  final router = GoRouter(
    initialLocation: '/flows/$id',
    routes: [
      GoRoute(
        path: '/flows',
        builder: (_, _) => const Scaffold(body: Text('list')),
        routes: [
          GoRoute(
            path: ':id',
            builder: (_, s) =>
                FlowEditorScreen(flowId: s.pathParameters['id']!),
            onExit: (context, _) => flowEditorOnExit(context),
          ),
        ],
      ),
    ],
  );
  await tester.pumpWidget(
    ProviderScope(
      overrides: [apiClientProvider.overrideWithValue(api)],
      child: MaterialApp.router(
        routerConfig: router,
        builder: (context, child) => HeimdallmTheme.scope(child: child!),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return (api, router);
}

void main() {
  testWidgets('loads the flow onto the canvas with palette and inspector', (
    tester,
  ) async {
    await pumpEditor(tester);
    expect(find.byKey(const ValueKey('node-a1')), findsOneWidget);
    expect(find.byKey(const ValueKey('palette-condition')), findsOneWidget);
    expect(find.byKey(const ValueKey('flow-name')), findsOneWidget);
  });

  testWidgets(
    'double-tapping a palette row adds a step; Save sends the graph',
    (tester) async {
      final (api, _) = await pumpEditor(tester);
      when(
        () => api.putFlow(any(), any()),
      ).thenAnswer((_) async => <String, dynamic>{});
      await tester.tap(find.byKey(const ValueKey('palette-agent')));
      await tester.pump(const Duration(milliseconds: 50));
      await tester.tap(find.byKey(const ValueKey('palette-agent')));
      await tester.pumpAndSettle();
      expect(find.byKey(const ValueKey('node-a2')), findsOneWidget);
      // a2 has no agent yet → client validation blocks the save with a message
      await tester.tap(find.byKey(const ValueKey('flow-save')));
      await tester.pumpAndSettle();
      verifyNever(() => api.putFlow(any(), any()));
      expect(find.textContaining('Pick an agent'), findsWidgets);
      // pick an agent in the inspector, then save
      await tester.tap(find.byKey(const ValueKey('inspector-agent')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('codex').last);
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const ValueKey('flow-save')));
      await tester.pumpAndSettle();
      final sent =
          verify(() => api.putFlow('night', captureAny())).captured.single
              as ReviewFlow;
      expect(sent.nodes['a2']!.agent, 'codex');
    },
  );

  testWidgets('a 422 pins issues on the nodes', (tester) async {
    final (api, _) = await pumpEditor(tester);
    when(() => api.putFlow(any(), any())).thenThrow(
      FlowValidationException('nodes.a1: bad', const [
        FlowIssue(node: 'a1', message: 'bad agent'),
      ]),
    );
    await tester.tap(find.byKey(const ValueKey('flow-name')));
    await tester.enterText(find.byKey(const ValueKey('flow-name')), 'Night 2');
    await tester.tap(find.byKey(const ValueKey('flow-save')));
    await tester.pumpAndSettle();
    expect(find.textContaining('bad agent'), findsWidgets);
  });

  testWidgets('Test highlights the path and lists candidates', (tester) async {
    final (api, _) = await pumpEditor(tester);
    when(
      () => api.simulateFlow(
        graph: any(named: 'graph'),
        at: any(named: 'at'),
        repo: any(named: 'repo'),
      ),
    ).thenAnswer(
      (_) async => const FlowDecision(
        flowId: 'night',
        candidates: ['claude'],
        path: [
          FlowPathStep(
            node: 'a1',
            type: 'agent',
            agent: 'claude',
            available: true,
            added: true,
          ),
        ],
      ),
    );
    await tester.tap(find.byKey(const ValueKey('flow-test')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('flow-test-run')));
    await tester.pumpAndSettle();
    expect(find.textContaining('Reviews with claude'), findsOneWidget);
  });

  testWidgets('leaving with unsaved changes asks first', (tester) async {
    final (_, router) = await pumpEditor(tester);
    await tester.enterText(find.byKey(const ValueKey('flow-name')), 'Changed');
    await tester.pump();
    router.go('/flows');
    await tester.pumpAndSettle();
    expect(find.text('Discard unsaved changes?'), findsOneWidget);
    await tester.tap(find.text('Keep editing'));
    await tester.pumpAndSettle();
    expect(find.text('list'), findsNothing);
    router.go('/flows');
    await tester.pumpAndSettle();
    await tester.tap(find.text('Discard'));
    await tester.pumpAndSettle();
    expect(find.text('list'), findsOneWidget);
  });

  testWidgets('keyboard undo/redo', (tester) async {
    await pumpEditor(tester);
    await tester.enterText(find.byKey(const ValueKey('flow-name')), 'Other');
    await tester.pump();
    await tester.tap(find.byKey(const ValueKey('flow-undo')));
    await tester.pump();
    expect(find.widgetWithText(TextField, 'Night'), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('flow-redo')));
    await tester.pump();
    expect(find.widgetWithText(TextField, 'Other'), findsOneWidget);
  });

  testWidgets('the default flow is read-only with a duplicate action', (
    tester,
  ) async {
    await pumpEditor(
      tester,
      id: 'default',
      listing: _listing(flows: {'default': _night}),
    );
    expect(find.byKey(const ValueKey('flow-save')), findsNothing);
    expect(find.text('Duplicate as a new flow'), findsOneWidget);
  });

  testWidgets('selecting another step shows that step\'s own values', (
    tester,
  ) async {
    final flow = ReviewFlow(
      name: 'Two',
      start: 'c1',
      nodes: {
        'c1': const FlowNode(
          id: 'c1',
          type: FlowNodeType.condition,
          yes: 'c2',
          schedule: [ScheduleCondition(from: '07:00')],
          position: Offset(300, 200),
        ),
        'c2': const FlowNode(
          id: 'c2',
          type: FlowNodeType.condition,
          schedule: [ScheduleCondition(from: '19:00')],
          position: Offset(700, 200),
        ),
      },
    );
    await pumpEditor(
      tester,
      id: 'two',
      listing: _listing(flows: {'two': flow}),
    );
    final from = find.byKey(const ValueKey('schedule-0-from'));
    await tester.tap(find.byKey(const ValueKey('node-c1')));
    await tester.pumpAndSettle();
    expect(
      find.descendant(of: from, matching: find.text('07:00')),
      findsOneWidget,
    );
    await tester.tap(find.byKey(const ValueKey('node-c2')));
    await tester.pumpAndSettle();
    expect(
      find.descendant(of: from, matching: find.text('19:00')),
      findsOneWidget,
    );
  });

  ProviderContainer containerOf(WidgetTester tester) =>
      ProviderScope.containerOf(tester.element(find.byType(FlowEditorScreen)));

  testWidgets('an edit made while the save is in flight stays unsaved', (
    tester,
  ) async {
    final (api, _) = await pumpEditor(tester);
    final put = Completer<Map<String, dynamic>>();
    when(() => api.putFlow(any(), any())).thenAnswer((_) => put.future);
    await tester.enterText(find.byKey(const ValueKey('flow-name')), 'Sent');
    await tester.pump();
    await tester.tap(find.byKey(const ValueKey('flow-save')));
    await tester.pump();
    await tester.enterText(find.byKey(const ValueKey('flow-name')), 'Later');
    await tester.pump();
    put.complete(<String, dynamic>{});
    await tester.pumpAndSettle();
    final sent =
        verify(() => api.putFlow('night', captureAny())).captured.single
            as ReviewFlow;
    expect(sent.name, 'Sent');
    final state = containerOf(tester).read(flowEditorProvider);
    expect(state.saved.name, 'Sent');
    expect(state.dirty, isTrue);
    expect(find.byKey(const ValueKey('flow-unsaved')), findsOneWidget);
  });

  testWidgets('daemon findings survive moving a step and Arrange', (
    tester,
  ) async {
    final (api, _) = await pumpEditor(tester);
    when(() => api.putFlow(any(), any())).thenThrow(
      FlowValidationException('nodes.a1: bad', const [
        FlowIssue(node: 'a1', message: 'bad agent'),
      ]),
    );
    await tester.tap(find.byKey(const ValueKey('flow-save')));
    await tester.pumpAndSettle();
    expect(find.textContaining('bad agent'), findsWidgets);
    final ctl = containerOf(tester).read(flowEditorProvider.notifier);
    ctl.moveNode('a1', const Offset(900, 600));
    await tester.pumpAndSettle();
    expect(find.textContaining('bad agent'), findsWidgets);
    ctl.arrange();
    await tester.pumpAndSettle();
    expect(find.textContaining('bad agent'), findsWidgets);
    final a1 = containerOf(tester).read(flowEditorProvider).flow.nodes['a1']!;
    ctl.updateNode(a1.copyWith(agent: 'codex'));
    await tester.pumpAndSettle();
    expect(find.textContaining('bad agent'), findsNothing);
  });

  testWidgets('Use globally waits for the flow to exist and for the PATCH', (
    tester,
  ) async {
    final (api, _) = await pumpEditor(tester, id: 'fresh');
    Switch sw() =>
        tester.widget<Switch>(find.byKey(const ValueKey('flow-use-globally')));
    expect(sw().onChanged, isNull);

    final (api2, _) = await pumpEditor(tester);
    final patch = Completer<Map<String, dynamic>>();
    when(() => api2.patchConfig(any())).thenAnswer((_) => patch.future);
    await tester.tap(find.byKey(const ValueKey('flow-use-globally')));
    await tester.pump();
    expect(sw().onChanged, isNull);
    await tester.tap(
      find.byKey(const ValueKey('flow-use-globally')),
      warnIfMissed: false,
    );
    await tester.pump();
    patch.complete(<String, dynamic>{});
    await tester.pumpAndSettle();
    verify(() => api2.patchConfig(any())).called(1);
    verifyNever(() => api.patchConfig(any()));
    expect(sw().onChanged, isNotNull);
  });

  testWidgets('a failed listing refresh after a save keeps the save', (
    tester,
  ) async {
    final (api, _) = await pumpEditor(tester);
    when(
      () => api.putFlow(any(), any()),
    ).thenAnswer((_) async => <String, dynamic>{});
    when(() => api.fetchFlows()).thenThrow(ApiException('daemon gone'));
    await tester.enterText(find.byKey(const ValueKey('flow-name')), 'Night 2');
    await tester.pump();
    await tester.tap(find.byKey(const ValueKey('flow-save')));
    await tester.pump();
    expect(find.text('Flow saved'), findsOneWidget);
    expect(find.text('daemon gone'), findsNothing);
    expect(containerOf(tester).read(flowEditorProvider).dirty, isFalse);
    await tester.pumpAndSettle(const Duration(seconds: 4));
  });

  testWidgets('any load failure offers Retry', (tester) async {
    tester.view.physicalSize = const Size(1800, 1100);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    final api = _MockApi();
    var calls = 0;
    when(() => api.fetchFlows()).thenAnswer((_) async {
      if (calls++ == 0) throw const FormatException('bad json');
      return _listing();
    });
    await tester.pumpWidget(
      ProviderScope(
        overrides: [apiClientProvider.overrideWithValue(api)],
        child: MaterialApp(
          builder: (context, child) => HeimdallmTheme.scope(child: child!),
          home: const FlowEditorScreen(flowId: 'night'),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.textContaining('bad json'), findsOneWidget);
    await tester.tap(find.text('Retry'));
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('node-a1')), findsOneWidget);
  });

  testWidgets(
    'double-click adds the step in the middle of the visible canvas',
    (tester) async {
      await pumpEditor(tester);
      final view = tester.getRect(find.byKey(const ValueKey('flow-canvas')));
      await tester.dragFrom(
        view.center + const Offset(200, 200),
        const Offset(-400, -300),
      );
      await tester.pumpAndSettle();
      Future<void> doubleTap() async {
        await tester.tap(find.byKey(const ValueKey('palette-condition')));
        await tester.pump(const Duration(milliseconds: 50));
        await tester.tap(find.byKey(const ValueKey('palette-condition')));
        await tester.pumpAndSettle();
      }

      await doubleTap();
      final c1 = tester.getRect(find.byKey(const ValueKey('node-c1')));
      expect((c1.center - view.center).distance, lessThan(1));
      await doubleTap();
      final c2 = tester.getRect(find.byKey(const ValueKey('node-c2')));
      expect(c2.topLeft - c1.topLeft, const Offset(30, 30));
    },
  );
}
