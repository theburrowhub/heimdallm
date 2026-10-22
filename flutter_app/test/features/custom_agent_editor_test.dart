import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/agent_schema.dart';
import 'package:heimdallm/core/models/cli_agent.dart';
import 'package:heimdallm/features/cli_agents/custom_agent_editor_screen.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:mocktail/mocktail.dart';

import '../support/agent_schemas.dart';

class _MockApiClient extends Mock implements ApiClient {}

final _aider = AgentSchema.fromJson({
  'name': 'aider',
  'display_name': 'Aider',
  'binary': 'aider',
  'fields': [
    {
      'key': 'edit_format',
      'type': 'select',
      'label': 'Format',
      'flag': '--edit-format',
      'options': ['whole', 'diff'],
    },
    {'key': 'fast', 'type': 'bool', 'label': 'Fast', 'flag': '--fast'},
  ],
});

Future<(_MockApiClient, GoRouter, List<String>)> _pump(
  WidgetTester tester, {
  String location = '/cli-agents/custom/new',
  bool apiEnabled = false,
  bool schemasFail = false,
}) async {
  tester.view.physicalSize = const Size(1400, 1400);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  final api = _MockApiClient();
  if (schemasFail) {
    when(() => api.fetchAgentSchemas()).thenThrow(ApiException('404'));
  } else {
    when(() => api.fetchAgentSchemas()).thenAnswer(
      (_) async => agentSchemaCatalog(
        custom: [_aider],
        customAgentsApiEnabled: apiEnabled,
      ),
    );
  }
  when(
    () => api.fetchCliAgents(),
  ).thenAnswer((_) async => const CliAgentCatalog(agents: []));
  when(
    () => api.rescanCliAgents(),
  ).thenAnswer((_) async => const CliAgentCatalog(agents: []));
  final clipboard = <String>[];
  tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
    SystemChannels.platform,
    (call) async {
      if (call.method == 'Clipboard.setData') {
        clipboard.add((call.arguments as Map)['text'] as String);
      }
      return null;
    },
  );
  addTearDown(
    () => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
      SystemChannels.platform,
      null,
    ),
  );
  final router = GoRouter(
    initialLocation: location,
    routes: [
      GoRoute(
        path: '/cli-agents',
        builder: (_, _) => const Scaffold(body: Text('catalog')),
        routes: [
          GoRoute(
            path: 'custom/new',
            builder: (_, _) => const Scaffold(body: CustomAgentEditorScreen()),
          ),
          GoRoute(
            path: 'custom/:name',
            builder: (_, state) => Scaffold(
              body: CustomAgentEditorScreen(
                agentName: state.pathParameters['name'],
              ),
            ),
          ),
          GoRoute(
            path: ':id',
            builder: (_, state) =>
                Scaffold(body: Text('agent ${state.pathParameters['id']}')),
          ),
        ],
      ),
    ],
  );
  await tester.pumpWidget(
    ProviderScope(
      overrides: [apiClientProvider.overrideWithValue(api)],
      child: MaterialApp.router(
        theme: HeimdallmTheme.light(),
        builder: (context, child) => HeimdallmTheme.scope(child: child!),
        routerConfig: router,
      ),
    ),
  );
  await tester.pumpAndSettle();
  return (api, router, clipboard);
}

Finder _k(String key) => find.byKey(ValueKey(key));

/// Taps a widget after scrolling it into view (the editor is a long list).
Future<void> _tap(WidgetTester tester, String key) async {
  await tester.ensureVisible(_k(key));
  await tester.pumpAndSettle();
  await tester.tap(_k(key));
  await tester.pumpAndSettle();
}

Future<void> _enter(WidgetTester tester, String key, String text) async {
  await tester.ensureVisible(_k(key));
  await tester.pumpAndSettle();
  await tester.enterText(_k(key), text);
  await tester.pump();
}

void main() {
  setUpAll(() => registerFallbackValue(const AgentSchema(name: 'x')));

  testWidgets('builds a new agent and copies it for config.toml', (
    tester,
  ) async {
    final (_, _, clipboard) = await _pump(tester);
    expect(_k('custom-agent-api-disabled'), findsOneWidget);
    expect(_k('custom-agent-save'), findsNothing);

    await tester.enterText(_k('custom-agent-name'), 'tool');
    await tester.enterText(_k('custom-agent-display-name'), 'Tool');
    await tester.enterText(_k('custom-agent-binary'), 'tool-cli');
    await tester.enterText(_k('custom-agent-icon-color'), '#112233');
    await tester.enterText(_k('custom-agent-description'), 'A tool');
    await tester.pump();
    await _tap(tester, 'custom-agent-add-field');
    await tester.pumpAndSettle();
    await _enter(tester, 'custom-agent-field-0-key', 'depth');
    await _enter(tester, 'custom-agent-field-0-label', 'Depth');
    await _enter(tester, 'custom-agent-field-0-flag', '--depth');
    await _enter(tester, 'custom-agent-field-0-default', 'x');
    await _enter(tester, 'custom-agent-field-0-description', 'How deep');
    await tester.pump();

    await tester.tap(_k('custom-agent-copy-toml'));
    await tester.pump();
    // Let the "Copied" toast clear the footer.
    await tester.pump(const Duration(seconds: 5));
    await tester.pumpAndSettle();
    await tester.tap(_k('custom-agent-copy-json'));
    await tester.pump();
    await tester.pump(const Duration(seconds: 5));
    await tester.pumpAndSettle();
    expect(clipboard, hasLength(2));
    expect(
      clipboard.first,
      startsWith('[ai.agents.tool]\nbinary = "tool-cli"'),
    );
    expect(clipboard.last, contains('"key": "depth"'));
  });

  testWidgets('edits every attribute of every field type', (tester) async {
    await _pump(tester);
    await tester.enterText(_k('custom-agent-name'), 'tool');
    await tester.enterText(_k('custom-agent-binary'), 'tool');
    for (var i = 0; i < 3; i++) {
      await _tap(tester, 'custom-agent-add-field');
      await tester.pumpAndSettle();
    }
    Future<void> setType(int i, String label) async {
      await _tap(tester, 'custom-agent-field-$i-type');
      await tester.pumpAndSettle();
      await tester.tap(find.text(label).last);
      await tester.pumpAndSettle();
    }

    await setType(0, 'Dropdown');
    await _enter(tester, 'custom-agent-field-0-options', 'a, b,');
    await _enter(tester, 'custom-agent-field-0-default', 'a');
    await _enter(tester, 'custom-agent-field-0-empty-label', 'CLI default');
    await _tap(tester, 'custom-agent-field-0-allow-custom');
    await tester.pump();

    await setType(1, 'Toggle');
    await _enter(tester, 'custom-agent-field-1-false-flag', '--no-x');
    await _tap(tester, 'custom-agent-field-1-default-on');
    await tester.pump();
    await _tap(tester, 'custom-agent-field-1-default-on');
    await tester.pump();
    await _tap(tester, 'custom-agent-field-1-warning');
    await tester.pump();

    await setType(2, 'Number');
    await _enter(tester, 'custom-agent-field-2-min', '1');
    await _enter(tester, 'custom-agent-field-2-max', '9');
    await _enter(tester, 'custom-agent-field-2-default', '3');
    await tester.pump();

    // The JSON tab reflects the visual edits.
    await tester.tap(_k('custom-agent-tab-json'));
    await tester.pumpAndSettle();
    final json = tester
        .widget<TextField>(_k('custom-agent-json'))
        .controller!
        .text;
    expect(json, contains('"options": [\n        "a",\n        "b"\n      ]'));
    expect(json, contains('"allow_custom": true'));
    expect(json, contains('"false_flag": "--no-x"'));
    expect(json, contains('"warning": true'));
    expect(json, contains('"min": 1'));
    expect(json, contains('"default": 3'));
  });

  testWidgets('reorders and removes fields', (tester) async {
    await _pump(tester, location: '/cli-agents/custom/aider', apiEnabled: true);
    expect(find.text('Edit Aider'), findsOneWidget);
    // Name is fixed while editing.
    expect(
      tester
          .widget<TextField>(
            find.descendant(
              of: _k('custom-agent-name'),
              matching: find.byType(TextField),
            ),
          )
          .enabled,
      isFalse,
    );
    await _tap(tester, 'custom-agent-field-1-up');
    await tester.pumpAndSettle();
    await tester.tap(_k('custom-agent-tab-json'));
    await tester.pumpAndSettle();
    var json = tester
        .widget<TextField>(_k('custom-agent-json'))
        .controller!
        .text;
    expect(json.indexOf('"fast"'), lessThan(json.indexOf('"edit_format"')));

    await tester.tap(_k('custom-agent-tab-fields'));
    await tester.pumpAndSettle();
    await _tap(tester, 'custom-agent-field-0-down');
    await tester.pumpAndSettle();
    await _tap(tester, 'custom-agent-field-1-remove');
    await tester.pumpAndSettle();
    await tester.tap(_k('custom-agent-tab-json'));
    await tester.pumpAndSettle();
    json = tester.widget<TextField>(_k('custom-agent-json')).controller!.text;
    expect(json, contains('"edit_format"'));
    expect(json, isNot(contains('"fast"')));
  });

  testWidgets('applies raw JSON and reports invalid JSON', (tester) async {
    await _pump(tester, location: '/cli-agents/custom/aider');
    await tester.tap(_k('custom-agent-tab-json'));
    await tester.pumpAndSettle();
    await tester.enterText(_k('custom-agent-json'), '{');
    await tester.tap(_k('custom-agent-apply-json'));
    await tester.pump();
    expect(find.textContaining('Invalid JSON'), findsOneWidget);
    await tester.enterText(_k('custom-agent-json'), '[]');
    await tester.tap(_k('custom-agent-apply-json'));
    await tester.pump();
    expect(find.textContaining('must be a JSON object'), findsOneWidget);

    await tester.enterText(
      _k('custom-agent-json'),
      '{"name":"renamed","binary":"aider","fields":[{"key":"x","type":"text","label":"X","flag":"--x"}]}',
    );
    await tester.tap(_k('custom-agent-apply-json'));
    await tester.pumpAndSettle();
    await tester.tap(_k('custom-agent-tab-preview'));
    await tester.pumpAndSettle();
    expect(_k('agent-field-preview-x'), findsOneWidget);
    await tester.enterText(_k('agent-field-preview-x'), 'value');
    await tester.enterText(_k('agent-field-preview-x'), '');
    await tester.pump();
    // Editing keeps the agent's name.
    await tester.tap(_k('custom-agent-tab-json'));
    await tester.pumpAndSettle();
    expect(
      tester.widget<TextField>(_k('custom-agent-json')).controller!.text,
      contains('"name": "aider"'),
    );
  });

  testWidgets('saves and deletes through the API when opted in', (
    tester,
  ) async {
    final (api, router, _) = await _pump(
      tester,
      location: '/cli-agents/custom/aider',
      apiEnabled: true,
    );
    when(() => api.putAgentSchema(any())).thenAnswer((_) async {});
    when(() => api.deleteAgentSchema('aider')).thenAnswer((_) async {});

    await tester.tap(_k('custom-agent-save'));
    await tester.pumpAndSettle();
    final saved =
        verify(() => api.putAgentSchema(captureAny())).captured.single
            as AgentSchema;
    expect(saved.name, 'aider');
    expect(find.text('agent aider'), findsOneWidget);

    await tester.pump(const Duration(seconds: 5));
    router.go('/cli-agents/custom/aider');
    await tester.pumpAndSettle();
    await tester.tap(_k('custom-agent-delete'));
    await tester.pumpAndSettle();
    await tester.tap(_k('custom-agent-confirm-delete'));
    await tester.pumpAndSettle();
    verify(() => api.deleteAgentSchema('aider')).called(1);
    expect(find.text('catalog'), findsOneWidget);
  });

  testWidgets('shows the daemon error when saving fails', (tester) async {
    final (api, _, _) = await _pump(
      tester,
      location: '/cli-agents/custom/aider',
      apiEnabled: true,
    );
    when(() => api.putAgentSchema(any())).thenThrow(ApiException('403 nope'));
    when(
      () => api.deleteAgentSchema(any()),
    ).thenThrow(ApiException('409 in use'));
    await tester.tap(_k('custom-agent-save'));
    await tester.pumpAndSettle();
    expect(find.textContaining('403 nope'), findsOneWidget);
    await tester.pump(const Duration(seconds: 5));
    await tester.pumpAndSettle();

    await tester.tap(_k('custom-agent-delete'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Cancel'));
    await tester.pumpAndSettle();
    verifyNever(() => api.deleteAgentSchema(any()));
    await tester.tap(_k('custom-agent-delete'));
    await tester.pumpAndSettle();
    await tester.tap(_k('custom-agent-confirm-delete'));
    await tester.pumpAndSettle();
    expect(find.textContaining('409 in use'), findsOneWidget);
    await tester.pump(const Duration(seconds: 5));
    await tester.pumpAndSettle();
  });

  testWidgets('back returns to the catalog', (tester) async {
    await _pump(tester);
    await tester.tap(_k('custom-agent-back'));
    await tester.pumpAndSettle();
    expect(find.text('catalog'), findsOneWidget);
  });

  testWidgets('says so when the schemas cannot load', (tester) async {
    await _pump(
      tester,
      location: '/cli-agents/custom/aider',
      schemasFail: true,
    );
    expect(find.text('Could not load the agent schemas'), findsOneWidget);
  });
}
