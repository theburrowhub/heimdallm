import 'dart:async';

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
import 'package:heimdallm/core/models/flow.dart';
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

const _defaultConfig = AppConfig(
  agentConfigs: {'claude': CLIAgentConfig(model: 'claude-opus-5')},
);

class _TestConfigNotifier extends ConfigNotifier {
  final AppConfig config;

  _TestConfigNotifier([this.config = _defaultConfig]);

  @override
  Future<AppConfig> build() async => config;
}

/// The catalog payload with OpenRouter offering [models] (and its key set
/// unless [configured] is false).
Map<String, dynamic> _catalogWithOpenRouterModels(
  List<String> models, {
  bool configured = true,
}) => {
  ..._catalogJson,
  'agents': [
    for (final a
        in (_catalogJson['agents']! as List).cast<Map<String, dynamic>>())
      if (a['id'] == 'openrouter')
        <String, dynamic>{
          ...a,
          'models': models,
          'installed': configured,
          'configured': configured,
        }
      else
        a,
  ],
};

Future<(_MockApiClient, GoRouter)> _pump(
  WidgetTester tester, {
  String location = '/cli-agents',
  bool quotasFail = false,
  AppConfig config = _defaultConfig,
  Map<String, dynamic> catalog = _catalogJson,
  bool catalogPending = false,
}) async {
  tester.view.physicalSize = const Size(1400, 1200);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);

  final api = _MockApiClient();
  final pending = Completer<CliAgentCatalog>();
  when(() => api.fetchCliAgents()).thenAnswer(
    (_) => catalogPending
        ? pending.future
        : Future.value(CliAgentCatalog.fromJson(catalog)),
  );
  when(
    () => api.rescanCliAgents(),
  ).thenAnswer((_) async => CliAgentCatalog.fromJson(catalog));
  when(() => api.patchConfig(any())).thenAnswer((_) async => {});
  if (quotasFail) {
    when(() => api.fetchQuotas()).thenThrow(ApiException('404'));
  } else {
    when(() => api.fetchQuotas()).thenAnswer(
      (_) async => [
        AgentQuota.fromJson({
          'agent': 'claude',
          'available': true,
          'windows': [
            {'kind': 'session', 'used_percent': 34},
            {'kind': 'weekly', 'used_percent': 95},
          ],
        }),
        AgentQuota.fromJson({'agent': 'codex', 'error': 'not signed in'}),
      ],
    );
  }
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
        configNotifierProvider.overrideWith(() => _TestConfigNotifier(config)),
        agentsProvider.overrideWith((ref) async => <ReviewPrompt>[]),
      ],
      child: MaterialApp.router(
        theme: HeimdallmTheme.light(),
        builder: (context, child) => HeimdallmTheme.scope(child: child!),
        routerConfig: router,
      ),
    ),
  );
  if (catalogPending) {
    // A spinner never settles: pump the config load and stop there.
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 50));
  } else {
    await tester.pumpAndSettle();
  }
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
    verify(() => api.fetchCliAgents()).called(greaterThan(0));
    debugDumpApp();
    expect(find.text('Agents rescanned'), findsOneWidget);
  });

  testWidgets('catalog cards show the remaining quota', (tester) async {
    await _pump(tester);
    expect(find.byKey(const ValueKey('quota-claude-5h')), findsOneWidget);
    final weekly = tester.widget<LinearProgressIndicator>(
      find.byKey(const ValueKey('quota-claude-7d')),
    );
    expect(weekly.value, closeTo(0.95, 0.001));
    expect(weekly.color, isNotNull);
    expect(find.text('34%'), findsOneWidget);
    expect(find.byKey(const ValueKey('quota-codex-5h')), findsNothing);
  });

  testWidgets(
    'catalog cards refresh quotas and show money, models and failures',
    (tester) async {
      final (api, _) = await _pump(tester);
      expect(find.text('34%'), findsOneWidget);

      // A minute later the cards show the new reading without leaving the page.
      when(() => api.fetchQuotas()).thenAnswer(
        (_) async => [
          AgentQuota.fromJson({
            'agent': 'claude',
            'available': true,
            'detail': '1.00 EUR extra usage spent · no limit',
            'windows': [
              {'kind': 'session', 'used_percent': 61},
              {'kind': 'weekly', 'used_percent': 46},
              {'kind': 'model', 'label': 'Fable', 'used_percent': 3},
              {'kind': 'model', 'label': 'Opus', 'used_percent': 12},
              // A model whose label equals the credit row's short label.
              {'kind': 'model', 'label': 'credit', 'used_percent': 7},
              {
                'kind': 'credit',
                'used_percent': 4.22,
                'detail': '31.65 / 750.00 EUR',
              },
            ],
          }),
          AgentQuota.fromJson({
            'agent': 'openrouter',
            'available': true,
            'detail': '3.00 USD spent · no limit',
          }),
          AgentQuota.fromJson({'agent': 'copilot', 'error': 'unavailable'}),
        ],
      );
      await tester.pump(kQuotaRefreshInterval);
      await tester.pumpAndSettle();

      verify(() => api.fetchQuotas()).called(2);
      expect(find.text('61%'), findsOneWidget);
      expect(find.text('34%'), findsNothing);
      expect(
        find.byKey(const ValueKey('quota-claude-model:Fable')),
        findsOneWidget,
      );
      // Six windows: none is cut, the credit row included, and a model
      // labelled like another kind keeps a key of its own.
      expect(
        find.byKey(const ValueKey('quota-claude-model:Opus')),
        findsOneWidget,
      );
      expect(
        find.byKey(const ValueKey('quota-claude-model:credit')),
        findsOneWidget,
      );
      expect(find.byKey(const ValueKey('quota-claude-credit')), findsOneWidget);
      expect(find.text('31.65 / 750.00 EUR'), findsOneWidget);
      expect(find.text('4%'), findsOneWidget);
      expect(
        find.byKey(const ValueKey('quota-openrouter-detail')),
        findsOneWidget,
      );
      expect(find.text('3.00 USD spent · no limit'), findsOneWidget);
      // A provider detail shows next to windows too (uncapped Claude credits).
      expect(
        find.text('1.00 EUR extra usage spent · no limit'),
        findsOneWidget,
      );
      expect(
        find.text('Usage unavailable: provider not reachable'),
        findsOneWidget,
      );

      // A failed refresh keeps the last good reading instead of blanking it.
      when(() => api.fetchQuotas()).thenThrow(ApiException('boom'));
      await tester.pump(kQuotaRefreshInterval);
      await tester.pumpAndSettle();
      expect(find.text('61%'), findsOneWidget);
      expect(find.text('31.65 / 750.00 EUR'), findsOneWidget);
    },
  );

  testWidgets('catalog renders without quotas on older daemons', (
    tester,
  ) async {
    await _pump(tester, quotasFail: true);
    expect(find.text('Claude Code'), findsOneWidget);
    expect(find.byType(LinearProgressIndicator), findsNothing);
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
    verifyInOrder([
      () => api.setAgentKey('openrouter', 'sk-or-new'),
      () => api.rescanCliAgents(),
    ]);
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

  testWidgets('OpenRouter has no CLI default: the model is a required, '
      'searchable choice', (tester) async {
    final (api, _) = await _pump(tester, location: '/cli-agents/openrouter');
    expect(find.text('CLI default'), findsNothing);
    expect(find.text('Required — OpenRouter has no default'), findsOneWidget);
    expect(find.text('Choose a model'), findsOneWidget);

    await tester.enterText(
      find.byKey(const ValueKey('model-openrouter')),
      'GPT',
    );
    await tester.pumpAndSettle();
    expect(find.text('openai/gpt-5'), findsOneWidget);
    expect(find.text('anthropic/claude-sonnet-4.5'), findsNothing);

    await tester.tap(find.text('openai/gpt-5'));
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();
    final patch =
        verify(() => api.patchConfig(captureAny())).captured.last
            as Map<String, dynamic>;
    expect(patch['ai']['agents']['openrouter']['model'], 'openai/gpt-5');
    expect(find.text('Choose a model'), findsNothing);
  });

  testWidgets('OpenRouter never offers a CLI default while the catalog loads', (
    tester,
  ) async {
    await _pump(
      tester,
      location: '/cli-agents/openrouter',
      catalogPending: true,
    );
    expect(find.text('CLI default'), findsNothing);
    expect(
      find.text('Loading the model list from OpenRouter…'),
      findsOneWidget,
    );
    expect(find.text('Required — OpenRouter has no default'), findsOneWidget);
  });

  testWidgets('Enter picks the highlighted match, or keeps an exact id', (
    tester,
  ) async {
    final (api, _) = await _pump(tester, location: '/cli-agents/openrouter');
    final field = find.byKey(const ValueKey('model-openrouter'));

    await tester.enterText(field, 'claude');
    await tester.pumpAndSettle();
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();
    var patch =
        verify(() => api.patchConfig(captureAny())).captured.last
            as Map<String, dynamic>;
    expect(
      patch['ai']['agents']['openrouter']['model'],
      'anthropic/claude-sonnet-4.5',
    );

    await tester.enterText(field, 'openai/gpt-5');
    await tester.pumpAndSettle();
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();
    patch =
        verify(() => api.patchConfig(captureAny())).captured.last
            as Map<String, dynamic>;
    expect(patch['ai']['agents']['openrouter']['model'], 'openai/gpt-5');
  });

  testWidgets('leaving the field drops an unsubmitted search', (tester) async {
    final (api, _) = await _pump(
      tester,
      location: '/cli-agents/openrouter',
      config: const AppConfig(
        agentConfigs: {'openrouter': CLIAgentConfig(model: 'openai/gpt-5')},
      ),
    );
    final field = find.byKey(const ValueKey('model-openrouter'));
    await tester.enterText(field, 'half-typed');
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('openrouter-tool-rounds')));
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();
    expect(tester.widget<TextField>(field).controller!.text, 'openai/gpt-5');
    verifyNever(() => api.patchConfig(any()));
  });

  testWidgets('a saved OpenRouter model that is no longer offered is flagged', (
    tester,
  ) async {
    await _pump(
      tester,
      location: '/cli-agents/openrouter',
      config: const AppConfig(
        agentConfigs: {'openrouter': CLIAgentConfig(model: 'x/retired')},
      ),
    );
    expect(
      find.byKey(const ValueKey('provider-model-missing')),
      findsOneWidget,
    );
    expect(
      find.textContaining('not offered by OpenRouter any more'),
      findsOneWidget,
    );
    expect(find.text('Choose a model'), findsNothing);
  });

  testWidgets('an unavailable OpenRouter model list offers Retry and still '
      'accepts a typed model id', (tester) async {
    final (api, _) = await _pump(
      tester,
      location: '/cli-agents/openrouter',
      catalog: _catalogWithOpenRouterModels(const []),
    );
    expect(
      find.text("Couldn't load the model list from OpenRouter"),
      findsOneWidget,
    );
    expect(find.byKey(const ValueKey('provider-model-missing')), findsNothing);

    await tester.enterText(
      find.byKey(const ValueKey('model-openrouter')),
      '  vendor/custom-model ',
    );
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();
    final patch =
        verify(() => api.patchConfig(captureAny())).captured.last
            as Map<String, dynamic>;
    expect(patch['ai']['agents']['openrouter']['model'], 'vendor/custom-model');

    when(() => api.fetchCliAgents()).thenAnswer(
      (_) async => CliAgentCatalog.fromJson(
        _catalogWithOpenRouterModels(const ['vendor/custom-model']),
      ),
    );
    await tester.tap(find.byKey(const ValueKey('provider-models-retry')));
    await tester.pumpAndSettle();
    verify(() => api.rescanCliAgents()).called(1);
    expect(
      find.text("Couldn't load the model list from OpenRouter"),
      findsNothing,
    );
  });

  testWidgets('without an API key the picker asks for one instead of Retry', (
    tester,
  ) async {
    await _pump(
      tester,
      location: '/cli-agents/openrouter',
      catalog: _catalogWithOpenRouterModels(const [], configured: false),
    );
    expect(
      find.text('Add an OpenRouter API key above to load the models'),
      findsOneWidget,
    );
    expect(find.byKey(const ValueKey('provider-models-retry')), findsNothing);
    expect(
      find.text("Couldn't load the model list from OpenRouter"),
      findsNothing,
    );
  });

  testWidgets('a key save still succeeds when the rescan after it fails', (
    tester,
  ) async {
    final (api, _) = await _pump(tester, location: '/cli-agents/openrouter');
    when(() => api.rescanCliAgents()).thenThrow(ApiException('busy'));
    await tester.enterText(
      find.byKey(const ValueKey('provider-key-field')),
      'sk-or-new',
    );
    await tester.tap(find.byKey(const ValueKey('provider-key-save')));
    await tester.pumpAndSettle();
    expect(find.text('Key saved'), findsOneWidget);
    expect(find.textContaining('busy'), findsNothing);
  });

  testWidgets('Enter in an empty model field keeps the saved model', (
    tester,
  ) async {
    final (api, _) = await _pump(
      tester,
      location: '/cli-agents/openrouter',
      config: const AppConfig(
        agentConfigs: {'openrouter': CLIAgentConfig(model: 'openai/gpt-5')},
      ),
    );
    final field = find.byKey(const ValueKey('model-openrouter'));
    await tester.enterText(field, '');
    await tester.pumpAndSettle();
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();
    expect(tester.widget<TextField>(field).controller!.text, 'openai/gpt-5');
    verifyNever(() => api.patchConfig(any()));
  });

  testWidgets('Enter in an empty model field never picks the first model', (
    tester,
  ) async {
    final (api, _) = await _pump(tester, location: '/cli-agents/openrouter');
    final field = find.byKey(const ValueKey('model-openrouter'));
    await tester.tap(field);
    await tester.pumpAndSettle();
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();
    expect(tester.widget<TextField>(field).controller!.text, isEmpty);
    verifyNever(() => api.patchConfig(any()));
    expect(find.text('Choose a model'), findsOneWidget);
  });

  testWidgets('a failed model list retry is reported', (tester) async {
    final (api, _) = await _pump(
      tester,
      location: '/cli-agents/openrouter',
      catalog: _catalogWithOpenRouterModels(const []),
    );
    when(() => api.rescanCliAgents()).thenThrow(ApiException('offline'));
    await tester.tap(find.byKey(const ValueKey('provider-models-retry')));
    await tester.pumpAndSettle();
    expect(find.textContaining('offline'), findsOneWidget);
  });

  testWidgets('the catalog asks for a model when OpenRouter has none', (
    tester,
  ) async {
    await _pump(tester);
    expect(
      find.byKey(const ValueKey('agent-needs-model-openrouter')),
      findsOneWidget,
    );
    expect(find.text('Choose a model'), findsOneWidget);
  });

  testWidgets('the catalog does not ask for a model once OpenRouter has one', (
    tester,
  ) async {
    await _pump(
      tester,
      config: const AppConfig(
        agentConfigs: {'openrouter': CLIAgentConfig(model: 'openai/gpt-5')},
      ),
    );
    expect(
      find.byKey(const ValueKey('agent-needs-model-openrouter')),
      findsNothing,
    );
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
