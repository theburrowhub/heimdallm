import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:mocktail/mocktail.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/agent.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/agents/agents_screen.dart';
import 'package:heimdallm/features/cli_agents/cli_agents_screen.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';

class _MockApiClient extends Mock implements ApiClient {}

class _TestConfigNotifier extends ConfigNotifier {
  void showLoading() => state = const AsyncLoading();
  void showData(AppConfig config) => state = AsyncData(config);
}

void main() {
  setUpAll(() {
    registerFallbackValue(<String, dynamic>{});
  });

  Future<void> pumpScreen(
    WidgetTester tester, {
    required bool dangerouslySkipPerms,
    String model = '',
  }) async {
    // Keep only the first (Claude) card in the ListView build/cache extent.
    // The pre-existing fixed-width Codex approval dropdown overflows under
    // Flutter's synthetic test font metrics and is unrelated to this switch.
    tester.view.physicalSize = const Size(1400, 600);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);

    final config = AppConfig(
      agentConfigs: {
        'claude': CLIAgentConfig(
          model: model,
          dangerouslySkipPerms: dangerouslySkipPerms,
        ),
      },
    );
    final configJson = {
      ...config.toJson(),
      'agent_configs': {
        'claude': {
          'model': model,
          'dangerously_skip_perms': dangerouslySkipPerms,
        },
      },
    };
    final api = _MockApiClient();
    when(api.fetchConfig).thenAnswer((_) async => configJson);
    when(() => api.patchConfig(any())).thenAnswer((_) async => configJson);

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(api),
          configNotifierProvider.overrideWith(_TestConfigNotifier.new),
          agentsProvider.overrideWith(
            (ref) => Future.value(const <ReviewPrompt>[]),
          ),
        ],
        child: const MaterialApp(home: Scaffold(body: CLIAgentsScreen())),
      ),
    );
    await tester.pumpAndSettle();
  }

  testWidgets('dangerous switch cannot enable an inactive bypass', (
    tester,
  ) async {
    await pumpScreen(tester, dangerouslySkipPerms: false);

    final finder = find.byKey(
      const ValueKey('dangerously-skip-permissions-claude'),
    );
    final toggle = tester.widget<Switch>(finder);
    expect(toggle.value, isFalse);
    expect(toggle.onChanged, isNull);
  });

  testWidgets('dangerous switch only allows true to false', (tester) async {
    await pumpScreen(tester, dangerouslySkipPerms: true);

    final finder = find.byKey(
      const ValueKey('dangerously-skip-permissions-claude'),
    );
    expect(tester.widget<Switch>(finder).onChanged, isNotNull);

    await tester.tap(finder);
    await tester.pump();

    final disabled = tester.widget<Switch>(finder);
    expect(disabled.value, isFalse);
    expect(disabled.onChanged, isNull);
  });

  testWidgets('static model dropdown updates the selected model', (
    tester,
  ) async {
    await pumpScreen(tester, dangerouslySkipPerms: false);

    final dropdown = find
        .byWidgetPredicate(
          (widget) =>
              widget is DropdownButtonFormField<String> &&
              widget.decoration.labelText == 'Model',
        )
        .first;
    expect(dropdown, findsOneWidget);

    await tester.tap(dropdown);
    await tester.pumpAndSettle();
    await tester.tap(find.text('claude-sonnet-5').last);
    await tester.pump();

    expect(
      tester.widget<DropdownButtonFormField<String>>(dropdown).initialValue,
      'claude-sonnet-5',
    );
  });

  testWidgets('keeps an unlisted configured model reversible', (tester) async {
    await pumpScreen(
      tester,
      dangerouslySkipPerms: false,
      model: 'claude-future-1',
    );

    final dropdown = find.byKey(const ValueKey('model-claude'));
    await tester.tap(dropdown);
    await tester.pumpAndSettle();
    await tester.tap(find.text('claude-sonnet-5').last);
    await tester.pumpAndSettle();

    await tester.tap(dropdown);
    await tester.pumpAndSettle();
    expect(find.text('claude-future-1 (not listed)'), findsWidgets);
    await tester.tap(find.text('claude-future-1 (not listed)').last);
    await tester.pumpAndSettle();

    expect(
      tester.widget<DropdownButtonFormField<String>>(dropdown).initialValue,
      'claude-future-1',
    );
  });

  testWidgets('keeps an unlisted model after its card is recreated', (
    tester,
  ) async {
    await pumpScreen(
      tester,
      dangerouslySkipPerms: false,
      model: 'claude-future-1',
    );

    final dropdown = find.byKey(const ValueKey('model-claude'));

    expect(
      tester.widget<DropdownButtonFormField<String>>(dropdown).initialValue,
      'claude-future-1',
    );
    expect(find.text('claude-future-1 (not listed)'), findsOneWidget);

    await tester.tap(dropdown);
    await tester.pumpAndSettle();
    await tester.tap(find.text('claude-sonnet-5').last);
    await tester.pumpAndSettle();

    expect(
      tester.widget<DropdownButtonFormField<String>>(dropdown).initialValue,
      'claude-sonnet-5',
    );

    final section = find.byKey(const ValueKey('agent-section-claude'));
    final screenState = tester.state(find.byType(CLIAgentsScreen));
    final initialSectionState = tester.state(section);
    final container = ProviderScope.containerOf(
      tester.element(find.byType(CLIAgentsScreen)),
    );
    final config = container.read(configNotifierProvider).requireValue;
    final notifier =
        container.read(configNotifierProvider.notifier) as _TestConfigNotifier;

    notifier.showLoading();
    await tester.pump();
    expect(screenState.mounted, isTrue);
    expect(initialSectionState.mounted, isFalse);
    expect(
      find.byKey(const ValueKey('agent-section-claude'), skipOffstage: false),
      findsNothing,
    );

    notifier.showData(config);
    await tester.pumpAndSettle();
    expect(tester.state(find.byType(CLIAgentsScreen)), same(screenState));
    expect(tester.state(section), isNot(same(initialSectionState)));
    expect(dropdown, findsOneWidget);
    expect(
      tester.widget<DropdownButtonFormField<String>>(dropdown).initialValue,
      'claude-sonnet-5',
    );

    await tester.tap(dropdown);
    await tester.pumpAndSettle();
    expect(find.text('claude-future-1 (not listed)'), findsWidgets);
    await tester.tap(find.text('claude-future-1 (not listed)').last);
    await tester.pumpAndSettle();

    expect(
      tester.widget<DropdownButtonFormField<String>>(dropdown).initialValue,
      'claude-future-1',
    );
  });
}
