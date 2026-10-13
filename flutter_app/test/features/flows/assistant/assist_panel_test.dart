import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/flows/assistant/assist_panel.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:http/http.dart' as http;
import 'package:mocktail/mocktail.dart';
import 'package:shared_preferences/shared_preferences.dart';

class _MockApi extends Mock implements ApiClient {}

const _agents = [
  FlowAgent(id: 'claude', installed: true),
  FlowAgent(id: 'codex', installed: true),
  FlowAgent(id: 'gemini'),
  FlowAgent(id: 'openrouter', installed: true, reviewOnly: true),
];

final _current = ReviewFlow(
  name: 'x',
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
final _draft = FlowAssistResult(
  plan: 'Codex first, Claude as fallback.',
  warnings: const ['some steps have no positions'],
  flow: ReviewFlow(
    name: 'x',
    start: 'a2',
    nodes: {
      'a2': const FlowNode(
        id: 'a2',
        type: FlowNodeType.agent,
        agent: 'codex',
        next: 'a1',
      ),
      'a1': const FlowNode(id: 'a1', type: FlowNodeType.agent, agent: 'claude'),
    },
  ),
);

Future<(ProviderContainer, _MockApi)> pumpPanel(
  WidgetTester tester, {
  Map<String, Object> prefs = const {},
  String defaultAgent = 'claude',
}) async {
  SharedPreferences.setMockInitialValues(prefs);
  registerFallbackValue(const ReviewFlow());
  final api = _MockApi();
  final c = ProviderContainer(
    overrides: [apiClientProvider.overrideWithValue(api)],
  );
  addTearDown(c.dispose);
  c.listen(flowEditorProvider, (_, _) {});
  c.read(flowEditorProvider.notifier).load(_current);
  await tester.pumpWidget(
    UncontrolledProviderScope(
      container: c,
      child: MaterialApp(
        builder: (context, child) => HeimdallmTheme.scope(child: child!),
        home: Scaffold(
          body: AssistPanel(
            agents: _agents,
            defaultAgent: defaultAgent,
            onClose: () {},
          ),
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return (c, api);
}

Finder _selectedAgent(String id) => find.descendant(
  of: find.byKey(const ValueKey('assist-agent')),
  matching: find.text(id),
);

void main() {
  testWidgets('offers only installed CLI agents, preselects the default', (
    tester,
  ) async {
    await pumpPanel(tester);
    await tester.tap(find.byKey(const ValueKey('assist-agent')));
    await tester.pumpAndSettle();
    expect(find.text('codex'), findsWidgets);
    expect(find.text('gemini'), findsNothing);
    expect(find.text('openrouter'), findsNothing);
  });

  testWidgets('remembers the last agent', (tester) async {
    final (_, api) = await pumpPanel(
      tester,
      prefs: {kAssistAgentPrefKey: 'codex'},
    );
    expect(_selectedAgent('codex'), findsOneWidget);
    when(
      () => api.assistFlow(
        agent: any(named: 'agent'),
        instruction: any(named: 'instruction'),
        current: any(named: 'current'),
        client: any(named: 'client'),
      ),
    ).thenAnswer((_) async => _draft);
    await tester.enterText(find.byKey(const ValueKey('assist-input')), 'x');
    await tester.tap(find.byKey(const ValueKey('assist-run')));
    await tester.pumpAndSettle();
    verify(
      () => api.assistFlow(
        agent: 'codex',
        instruction: 'x',
        current: any(named: 'current'),
        client: any(named: 'client'),
      ),
    ).called(1);
  });

  testWidgets('picking an agent stores it', (tester) async {
    await pumpPanel(tester);
    await tester.tap(find.byKey(const ValueKey('assist-agent')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('codex').last);
    await tester.pumpAndSettle();
    final prefs = await SharedPreferences.getInstance();
    expect(prefs.getString(kAssistAgentPrefKey), 'codex');
  });

  testWidgets('a remembered agent that is no longer runnable falls back', (
    tester,
  ) async {
    final (_, api) = await pumpPanel(
      tester,
      prefs: {kAssistAgentPrefKey: 'gemini'},
      defaultAgent: 'codex',
    );
    when(
      () => api.assistFlow(
        agent: any(named: 'agent'),
        instruction: any(named: 'instruction'),
        current: any(named: 'current'),
        client: any(named: 'client'),
      ),
    ).thenAnswer((_) async => _draft);
    await tester.enterText(find.byKey(const ValueKey('assist-input')), 'x');
    await tester.tap(find.byKey(const ValueKey('assist-run')));
    await tester.pumpAndSettle();
    verify(
      () => api.assistFlow(
        agent: 'codex',
        instruction: 'x',
        current: any(named: 'current'),
        client: any(named: 'client'),
      ),
    ).called(1);
  });

  testWidgets('run shows the draft with its plan; accept applies it', (
    tester,
  ) async {
    final (c, api) = await pumpPanel(tester);
    when(
      () => api.assistFlow(
        agent: any(named: 'agent'),
        instruction: any(named: 'instruction'),
        current: any(named: 'current'),
        client: any(named: 'client'),
      ),
    ).thenAnswer((_) async => _draft);
    await tester.enterText(
      find.byKey(const ValueKey('assist-input')),
      'codex first',
    );
    await tester.tap(find.byKey(const ValueKey('assist-run')));
    await tester.pumpAndSettle();
    final sent =
        verify(
              () => api.assistFlow(
                agent: 'claude',
                instruction: 'codex first',
                current: captureAny(named: 'current'),
                client: any(named: 'client'),
              ),
            ).captured.single
            as ReviewFlow;
    expect(sent.start, 'a1');
    expect(find.text('Codex first, Claude as fallback.'), findsOneWidget);
    expect(find.text('some steps have no positions'), findsOneWidget);
    expect(c.read(flowEditorProvider).draft, isNotNull);
    await tester.tap(find.byKey(const ValueKey('assist-accept')));
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.start, 'a2');
    expect(c.read(flowEditorProvider).draft, isNull);
  });

  testWidgets('refining sends the draft as the current flow; discard keeps '
      'the canvas', (tester) async {
    final (c, api) = await pumpPanel(tester);
    when(
      () => api.assistFlow(
        agent: any(named: 'agent'),
        instruction: any(named: 'instruction'),
        current: any(named: 'current'),
        client: any(named: 'client'),
      ),
    ).thenAnswer((_) async => _draft);
    await tester.enterText(
      find.byKey(const ValueKey('assist-input')),
      'codex first',
    );
    await tester.tap(find.byKey(const ValueKey('assist-run')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const ValueKey('assist-input')),
      'and gemini last',
    );
    await tester.tap(find.byKey(const ValueKey('assist-run')));
    await tester.pumpAndSettle();
    final second =
        verify(
              () => api.assistFlow(
                agent: any(named: 'agent'),
                instruction: 'and gemini last',
                current: captureAny(named: 'current'),
                client: any(named: 'client'),
              ),
            ).captured.single
            as ReviewFlow;
    expect(second.start, 'a2');
    await tester.tap(find.byKey(const ValueKey('assist-discard')));
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).flow.start, 'a1');
    expect(c.read(flowEditorProvider).draft, isNull);
  });

  testWidgets('cancel aborts the request and leaves the canvas', (
    tester,
  ) async {
    final (c, api) = await pumpPanel(tester);
    final pending = Completer<FlowAssistResult>();
    http.Client? used;
    when(
      () => api.assistFlow(
        agent: any(named: 'agent'),
        instruction: any(named: 'instruction'),
        current: any(named: 'current'),
        client: any(named: 'client'),
      ),
    ).thenAnswer((inv) {
      used = inv.namedArguments[#client] as http.Client?;
      return pending.future;
    });
    await tester.enterText(find.byKey(const ValueKey('assist-input')), 'x');
    await tester.tap(find.byKey(const ValueKey('assist-run')));
    await tester.pump();
    expect(find.byKey(const ValueKey('assist-cancel')), findsOneWidget);
    // The agent can take minutes: the wait shows how long it has been.
    await tester.pump(const Duration(seconds: 65));
    expect(find.textContaining('1:05'), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('assist-cancel')));
    await tester.pump();
    expect(used, isNotNull);
    pending.completeError(http.ClientException('Connection closed'));
    await tester.pumpAndSettle();
    expect(c.read(flowEditorProvider).draft, isNull);
    expect(find.textContaining('Connection closed'), findsNothing);
    // the input is kept so the request can be sent again
    expect(find.text('x'), findsOneWidget);
    expect(find.byKey(const ValueKey('assist-run')), findsOneWidget);
  });

  testWidgets('daemon errors are shown', (tester) async {
    final (_, api) = await pumpPanel(tester);
    when(
      () => api.assistFlow(
        agent: any(named: 'agent'),
        instruction: any(named: 'instruction'),
        current: any(named: 'current'),
        client: any(named: 'client'),
      ),
    ).thenThrow(ApiException('claude did not answer in time'));
    await tester.enterText(find.byKey(const ValueKey('assist-input')), 'x');
    await tester.tap(find.byKey(const ValueKey('assist-run')));
    await tester.pumpAndSettle();
    expect(find.text('claude did not answer in time'), findsOneWidget);
  });

  testWidgets('with no runnable agent, drafting is not offered', (
    tester,
  ) async {
    SharedPreferences.setMockInitialValues(const {});
    await tester.pumpWidget(
      ProviderScope(
        child: MaterialApp(
          builder: (context, child) => HeimdallmTheme.scope(child: child!),
          home: Scaffold(
            body: AssistPanel(
              agents: const [FlowAgent(id: 'gemini')],
              defaultAgent: 'gemini',
              onClose: () {},
            ),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('assist-agent')), findsNothing);
    final run = tester.widget<ButtonStyleButton>(
      find.byKey(const ValueKey('assist-run')),
    );
    expect(run.onPressed, isNull);
  });
}
