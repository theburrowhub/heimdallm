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
import 'package:heimdallm/shared/design_system/theme.dart';

class _MockApiClient extends Mock implements ApiClient {}

class _TestConfigNotifier extends ConfigNotifier {
  void showLoading() => state = const AsyncLoading();
  void showData(AppConfig config) => state = AsyncData(config);
}

class _ErrorConfigNotifier extends ConfigNotifier {
  @override
  Future<AppConfig> build() async => throw Exception('boom');
}

Map<String, dynamic> _agentConfigJson(CLIAgentConfig config) => {
  'model': config.model,
  'max_turns': config.maxTurns,
  'approval_mode': config.approvalMode,
  'extra_flags': config.extraFlags,
  'prompt': config.promptId,
  'effort': config.effort,
  'permission_mode': config.permissionMode,
  'bare': config.bare,
  'dangerously_skip_perms': config.dangerouslySkipPerms,
  'no_session_persistence': config.noSessionPersistence,
};

Widget _hosted(Widget child) {
  return MaterialApp(
    theme: HeimdallmTheme.light(),
    builder: (context, navigatorChild) =>
        HeimdallmTheme.scope(child: navigatorChild ?? const SizedBox.shrink()),
    home: Scaffold(body: child),
  );
}

void main() {
  setUpAll(() {
    registerFallbackValue(<String, dynamic>{});
  });

  Future<_MockApiClient> pumpScreen(
    WidgetTester tester, {
    Map<String, CLIAgentConfig> agentConfigs = const {},
    List<ReviewPrompt> prompts = const [],
    Size size = const Size(1400, 600),
  }) async {
    // Keep only the first (Claude) card in the ListView build/cache extent.
    // The pre-existing fixed-width Codex approval dropdown overflows under
    // Flutter's synthetic test font metrics and is unrelated to this switch.
    tester.view.physicalSize = size;
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);

    final config = AppConfig(agentConfigs: agentConfigs);
    final configJson = {
      ...config.toJson(),
      'agent_configs': {
        for (final entry in agentConfigs.entries)
          entry.key: _agentConfigJson(entry.value),
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
          agentsProvider.overrideWith((ref) => Future.value(prompts)),
        ],
        child: _hosted(const CLIAgentsScreen()),
      ),
    );
    await tester.pumpAndSettle();
    return api;
  }

  testWidgets('dangerous switch cannot enable an inactive bypass', (
    tester,
  ) async {
    await pumpScreen(
      tester,
      agentConfigs: const {
        'claude': CLIAgentConfig(dangerouslySkipPerms: false),
      },
    );

    final finder = find.byKey(
      const ValueKey('dangerously-skip-permissions-claude'),
    );
    final toggle = tester.widget<Switch>(finder);
    expect(toggle.value, isFalse);
    expect(toggle.onChanged, isNull);
  });

  testWidgets('dangerous switch only allows true to false', (tester) async {
    await pumpScreen(
      tester,
      agentConfigs: const {
        'claude': CLIAgentConfig(dangerouslySkipPerms: true),
      },
    );

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
    await pumpScreen(tester, agentConfigs: const {'claude': CLIAgentConfig()});

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
      agentConfigs: const {'claude': CLIAgentConfig(model: 'claude-future-1')},
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
      agentConfigs: const {'claude': CLIAgentConfig(model: 'claude-future-1')},
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

  testWidgets('shows an error when config loading fails', (tester) async {
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(_MockApiClient()),
          configNotifierProvider.overrideWith(_ErrorConfigNotifier.new),
          agentsProvider.overrideWith(
            (ref) => Future.value(const <ReviewPrompt>[]),
          ),
        ],
        child: _hosted(const CLIAgentsScreen()),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Could not load config'), findsOneWidget);
  });

  testWidgets('edits Claude flags and prompt, then auto-saves', (tester) async {
    final api = await pumpScreen(
      tester,
      agentConfigs: const {
        'claude': CLIAgentConfig(extraFlags: '--allowedTools Bash,Read'),
      },
      prompts: const [
        ReviewPrompt(
          id: 'prompt-1',
          name: 'Security prompt',
          instructions: 'Review security issues',
        ),
      ],
    );

    final flagChip = find.widgetWithText(InputChip, '--allowedTools Bash,Read');
    await tester.tap(flagChip);
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byType(TextFormField).last,
      '--allowedTools Bash,Read --verbose',
    );
    await tester.tap(find.byIcon(Icons.check).last);
    await tester.pumpAndSettle();

    await tester.tap(find.byIcon(Icons.close).last);
    await tester.pumpAndSettle();

    final promptDropdown = find.byWidgetPredicate(
      (widget) =>
          widget is DropdownButtonFormField<String?> &&
          widget.decoration.labelText == 'Default prompt',
    );
    await tester.tap(promptDropdown.first);
    await tester.pumpAndSettle();
    await tester.tap(find.text('Security prompt').last);
    await tester.pumpAndSettle();

    final addField = find.byType(TextFormField).last;
    await tester.enterText(addField, '--sandbox workspace-write');
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();

    verify(
      () => api.patchConfig(
        any(
          that: predicate<Map<String, dynamic>>(
            (json) =>
                ((json['ai'] as Map<String, dynamic>)['agents']
                    as Map<String, dynamic>)['claude'] !=
                null,
          ),
        ),
      ),
    ).called(greaterThanOrEqualTo(1));
    expect(find.text('Saved'), findsWidgets);

    await tester.pump(const Duration(seconds: 4));
    await tester.pumpAndSettle();
    expect(find.text('Ready'), findsWidgets);
  });

  testWidgets('Claude-specific controls persist through save', (tester) async {
    final api = await pumpScreen(
      tester,
      agentConfigs: const {'claude': CLIAgentConfig()},
    );

    await tester.enterText(find.byType(TextFormField).first, '5');
    await tester.pump();

    final effortDropdown = find.byWidgetPredicate(
      (widget) =>
          widget is DropdownButtonFormField<String> &&
          widget.decoration.labelText == '--effort',
    );
    await tester.tap(effortDropdown);
    await tester.pumpAndSettle();
    await tester.tap(find.text('high').last);
    await tester.pumpAndSettle();

    final permissionDropdown = find.byWidgetPredicate(
      (widget) =>
          widget is DropdownButtonFormField<String> &&
          widget.decoration.labelText == '--permission-mode',
    );
    await tester.tap(permissionDropdown);
    await tester.pumpAndSettle();
    await tester.tap(find.text('dontAsk').last);
    await tester.pumpAndSettle();

    await tester.tap(find.byType(Switch).first);
    await tester.pump();
    await tester.tap(find.byType(Switch).at(1));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();

    verify(
      () => api.patchConfig(
        any(
          that: predicate<Map<String, dynamic>>(
            (json) =>
                (((json['ai'] as Map<String, dynamic>)['agents']
                        as Map<String, dynamic>)['claude']
                    as Map<String, dynamic>)['max_turns'] ==
                5,
          ),
        ),
      ),
    ).called(greaterThanOrEqualTo(1));
  });

  testWidgets('Codex approval mode can be configured', (tester) async {
    tester.view.physicalSize = const Size(1400, 1000);
    tester.view.devicePixelRatio = 1;
    tester.platformDispatcher.textScaleFactorTestValue = 0.5;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    addTearDown(tester.platformDispatcher.clearTextScaleFactorTestValue);

    final api = await pumpScreen(
      tester,
      agentConfigs: const {'codex': CLIAgentConfig()},
      size: const Size(1800, 1000),
    );

    await tester.drag(find.byType(ListView).first, const Offset(0, -500));
    await tester.pumpAndSettle();

    final approvalDropdown = find.byWidgetPredicate(
      (widget) =>
          widget is DropdownButtonFormField<String> &&
          widget.decoration.labelText == '--ask-for-approval',
    );
    await tester.tap(approvalDropdown);
    await tester.pumpAndSettle();
    await tester.tap(find.text('full-auto').last);
    await tester.pumpAndSettle();
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();

    verify(
      () => api.patchConfig(
        any(
          that: predicate<Map<String, dynamic>>(
            (json) =>
                (((json['ai'] as Map<String, dynamic>)['agents']
                        as Map<String, dynamic>)['codex']
                    as Map<String, dynamic>)['approval_mode'] ==
                'full-auto',
          ),
        ),
      ),
    ).called(greaterThanOrEqualTo(1));
  });
}
