import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:mocktail/mocktail.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/agent.dart';
import 'package:heimdallm/core/models/cli_agent.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/agents/agents_screen.dart';
import 'package:heimdallm/features/cli_agents/agent_catalog_screen.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

class _MockApiClient extends Mock implements ApiClient {}

const _catalogJson = {
  'scanned_at': '2026-10-08T10:00:00Z',
  'agents': [
    {
      'id': 'claude',
      'name': 'Claude Code',
      'kind': 'agent',
      'executable': true,
      'installed': true,
      'configured': true,
      'version': '2.1.292',
      'path': '~/.local/bin/claude',
      'config_agent': 'claude',
      'homepage': 'https://x',
      'install_hint': 'npm i -g claude',
    },
    {
      'id': 'cursor',
      'name': 'Cursor IDE',
      'kind': 'ide',
      'executable': false,
      'installed': true,
      'config_agent': 'cursor_cli',
    },
    {
      'id': 'cursor_cli',
      'name': 'Cursor CLI',
      'kind': 'agent',
      'executable': true,
      'installed': true,
      'version': '2026.10.01',
      'config_agent': 'cursor_cli',
      'models': ['auto', 'gpt-5.2'],
    },
    {
      'id': 'openrouter',
      'name': 'OpenRouter',
      'kind': 'provider',
      'executable': true,
      'installed': true,
      'configured': true,
      'config_agent': 'openrouter',
      'models': ['anthropic/claude-sonnet-4.5', 'openai/gpt-5'],
    },
    {
      'id': 'copilot',
      'name': 'GitHub Copilot CLI',
      'kind': 'agent',
      'executable': true,
      'installed': false,
      'config_agent': 'copilot',
      'install_hint': 'npm install -g @github/copilot',
      'models': [1, 'gpt-5.5'],
    },
  ],
};

class _TestConfigNotifier extends ConfigNotifier {
  @override
  Future<AppConfig> build() async => const AppConfig(
    agentConfigs: {'claude': CLIAgentConfig(model: 'claude-opus-5')},
  );
}

Future<(_MockApiClient, GoRouter)> _pump(
  WidgetTester tester, {
  String location = '/cli-agents',
}) async {
  tester.view.physicalSize = const Size(1400, 1200);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);

  final api = _MockApiClient();
  when(
    () => api.fetchCliAgents(),
  ).thenAnswer((_) async => CliAgentCatalog.fromJson(_catalogJson));
  when(
    () => api.rescanCliAgents(),
  ).thenAnswer((_) async => CliAgentCatalog.fromJson(_catalogJson));
  when(() => api.patchConfig(any())).thenAnswer((_) async => {});
  when(() => api.fetchAgentKey('openrouter')).thenAnswer(
    (_) async => const AgentKeyStatus(configured: true, source: 'stored'),
  );
  when(() => api.setAgentKey('openrouter', any())).thenAnswer(
    (_) async => const AgentKeyStatus(configured: true, source: 'stored'),
  );
  when(() => api.deleteAgentKey('openrouter')).thenAnswer(
    (_) async => const AgentKeyStatus(configured: true, source: 'env'),
  );
  when(() => api.fetchAgentUsage('openrouter')).thenAnswer(
    (_) async => {
      'usage': 2.5,
      'usage_daily': 0.4,
      'limit': 10,
      'limit_remaining': 7.5,
    },
  );
  final router = GoRouter(
    initialLocation: location,
    routes: [
      GoRoute(
        path: '/cli-agents',
        builder: (_, _) => const Scaffold(body: AgentCatalogScreen()),
        routes: [
          GoRoute(
            path: ':id',
            builder: (_, state) => Scaffold(
              body: AgentConfigScreen(agentId: state.pathParameters['id']!),
            ),
          ),
        ],
      ),
    ],
  );
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        apiClientProvider.overrideWithValue(api),
        configNotifierProvider.overrideWith(_TestConfigNotifier.new),
        agentsProvider.overrideWith((ref) async => <ReviewPrompt>[]),
      ],
      child: MaterialApp.router(
        theme: HeimdallmTheme.light(),
        builder: (context, child) => HeimdallmTheme.scope(child: child!),
        routerConfig: router,
      ),
    ),
  );
  await tester.pumpAndSettle();
  return (api, router);
}

void main() {
  setUpAll(() => registerFallbackValue(<String, dynamic>{}));

  test('catalog model parses the daemon payload', () {
    final c = CliAgentCatalog.fromJson(_catalogJson);
    expect(c.scannedAt, DateTime.utc(2026, 10, 8, 10));
    expect(c.byId('cursor')!.isIde, isTrue);
    expect(c.byId('cursor')!.configAgent, 'cursor_cli');
    expect(c.byId('copilot')!.models, ['gpt-5.5']);
    expect(c.byId('nope'), isNull);
    expect(const CliAgentInfo(id: 'x', name: 'X').configAgent, 'x');
    expect(CliAgentCatalog.fromJson(const {}).agents, isEmpty);
    expect(agentEmoji('openrouter'), '🔀');
    expect(agentEmoji('unknown'), '🤖');
  });

  testWidgets('catalog shows every agent with its detection state', (
    tester,
  ) async {
    final (api, _) = await _pump(tester);
    expect(find.text('Claude Code'), findsOneWidget);
    expect(find.text('Installed 2.1.292'), findsOneWidget);
    expect(find.text('Signed in / configured'), findsOneWidget);
    expect(find.text('Not installed'), findsOneWidget);
    expect(find.text('Reviews via Cursor CLI'), findsOneWidget);

    await tester.tap(find.byKey(const ValueKey('agents-rescan')));
    await tester.pumpAndSettle();
    verify(() => api.rescanCliAgents()).called(1);
    expect(find.text('Agents rescanned'), findsOneWidget);
  });

  testWidgets('rescan errors are reported', (tester) async {
    final (api, _) = await _pump(tester);
    when(() => api.rescanCliAgents()).thenThrow(ApiException('boom'));
    await tester.tap(find.byKey(const ValueKey('agents-rescan')));
    await tester.pumpAndSettle();
    expect(find.textContaining('boom'), findsOneWidget);
  });

  testWidgets('tapping the IDE opens the CLI it reviews through', (
    tester,
  ) async {
    final (_, router) = await _pump(tester);
    await tester.tap(find.byKey(const ValueKey('agent-card-cursor')));
    await tester.pumpAndSettle();
    expect(router.state.uri.path, '/cli-agents/cursor_cli');
    expect(find.text('Cursor CLI'), findsWidgets);
    expect(find.textContaining('reviews from the diff only'), findsOneWidget);

    // Discovered models replace the built-in list.
    await tester.tap(find.byKey(const ValueKey('model-cursor_cli')));
    await tester.pumpAndSettle();
    expect(find.text('gpt-5.2').last, findsOneWidget);
    await tester.tap(find.text('gpt-5.2').last);
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();
    final patch =
        verify(() => _apiOf(tester).patchConfig(captureAny())).captured.last
            as Map<String, dynamic>;
    final agents = patch['ai']['agents'] as Map<String, dynamic>;
    expect(agents.keys, [
      'cursor_cli',
    ], reason: 'only the edited agent is sent');
    expect(agents['cursor_cli']['model'], 'gpt-5.2');

    await tester.tap(find.byKey(const ValueKey('agent-config-back')));
    await tester.pumpAndSettle();
    expect(router.state.uri.path, '/cli-agents');
  });

  testWidgets('a missing agent shows how to install it', (tester) async {
    String? copied;
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
      SystemChannels.platform,
      (call) async {
        if (call.method == 'Clipboard.setData') {
          copied = (call.arguments as Map)['text'] as String;
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
    await _pump(tester, location: '/cli-agents/copilot');
    expect(find.text('Not installed'), findsOneWidget);
    expect(find.text('npm install -g @github/copilot'), findsOneWidget);
    expect(find.text('--reasoning-effort'), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('agent-copy-install')));
    await tester.pumpAndSettle();
    expect(copied, 'npm install -g @github/copilot');
  });

  testWidgets('OpenRouter page manages a write-only key and shows spend', (
    tester,
  ) async {
    final (api, _) = await _pump(tester, location: '/cli-agents/openrouter');
    expect(find.text('Key saved in Heimdallm'), findsOneWidget);
    expect(
      find.text(r'Spent $2.50 · today $0.40 · limit $10.00 · $7.50 left'),
      findsOneWidget,
    );

    await tester.enterText(
      find.byKey(const ValueKey('provider-key-field')),
      'sk-or-new',
    );
    await tester.tap(find.byKey(const ValueKey('provider-key-save')));
    await tester.pumpAndSettle();
    verify(() => api.setAgentKey('openrouter', 'sk-or-new')).called(1);
    expect(find.text('Key saved'), findsOneWidget);

    await tester.tap(find.byKey(const ValueKey('provider-key-remove')));
    await tester.pumpAndSettle();
    verify(() => api.deleteAgentKey('openrouter')).called(1);
    expect(
      find.text('Key from the daemon environment (OPENROUTER_API_KEY)'),
      findsOneWidget,
    );

    // Empty input does nothing.
    await tester.tap(find.byKey(const ValueKey('provider-key-save')));
    await tester.pumpAndSettle();
    verifyNever(() => api.setAgentKey('openrouter', ''));

    await tester.enterText(
      find.byKey(const ValueKey('openrouter-tool-rounds')),
      '20',
    );
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();
    final patch =
        verify(() => api.patchConfig(captureAny())).captured.last
            as Map<String, dynamic>;
    expect(patch['ai']['agents']['openrouter']['max_turns'], 20);
  });

  testWidgets('provider key errors and usage errors are shown', (tester) async {
    final (api, _) = await _pump(tester);
    when(() => api.fetchAgentKey('openrouter')).thenThrow(ApiException('down'));
    when(
      () => api.fetchAgentUsage('openrouter'),
    ).thenThrow(ApiException('401'));
    expect(find.text('API key set'), findsOneWidget);
    expect(find.text('Runs in Heimdallm'), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('agent-card-openrouter')));
    await tester.pumpAndSettle();
    expect(find.textContaining('down'), findsWidgets);

    when(() => api.fetchAgentKey('openrouter')).thenAnswer(
      (_) async => const AgentKeyStatus(configured: true, source: 'env'),
    );
    when(
      () => api.setAgentKey('openrouter', any()),
    ).thenThrow(ApiException('bad key'));
    await tester.tap(find.byKey(const ValueKey('agent-config-back')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('agent-card-openrouter')));
    await tester.pumpAndSettle();
    expect(find.textContaining('Usage unavailable'), findsOneWidget);
    await tester.enterText(
      find.byKey(const ValueKey('provider-key-field')),
      'x y',
    );
    await tester.tap(find.byKey(const ValueKey('provider-key-save')));
    await tester.pumpAndSettle();
    expect(find.textContaining('bad key'), findsOneWidget);
  });

  test('AgentKeyStatus parses', () {
    final s = AgentKeyStatus.fromJson(const {
      'configured': true,
      'source': 'env',
    });
    expect(s.configured, isTrue);
    expect(s.source, 'env');
    expect(AgentKeyStatus.fromJson(const {}).configured, isFalse);
  });
}

_MockApiClient _apiOf(WidgetTester tester) {
  final container = ProviderScope.containerOf(
    tester.element(find.byType(AgentConfigScreen)),
  );
  return container.read(apiClientProvider) as _MockApiClient;
}
