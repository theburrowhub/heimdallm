import 'dart:async';
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

void main() {
  setUpAll(() {
    registerFallbackValue(<String, dynamic>{});
  });

  Future<void> pumpScreen(
    WidgetTester tester, {
    required bool dangerouslySkipPerms,
    String claudeModel = '',
    Map<String, List<String>> modelCatalog = const {
      'claude': <String>[],
      'gemini': <String>[],
      'codex': <String>[],
    },
    Object? modelError,
    Completer<Map<String, List<String>>>? modelCompleter,
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
          model: claudeModel,
          dangerouslySkipPerms: dangerouslySkipPerms,
        ),
      },
    );
    final configJson = {
      ...config.toJson(),
      'agent_configs': {
        'claude': {
          'model': claudeModel,
          'dangerously_skip_perms': dangerouslySkipPerms,
        },
      },
    };
    final api = _MockApiClient();
    when(api.fetchConfig).thenAnswer((_) async => configJson);
    if (modelError != null) {
      when(() => api.fetchAgentModels()).thenAnswer((_) async => throw modelError);
    } else if (modelCompleter != null) {
      when(() => api.fetchAgentModels()).thenAnswer((_) => modelCompleter.future);
    } else {
      when(() => api.fetchAgentModels()).thenAnswer((_) async => modelCatalog);
    }
    when(() => api.patchConfig(any())).thenAnswer((_) async => configJson);

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(api),
          configNotifierProvider.overrideWith(ConfigNotifier.new),
          agentsProvider.overrideWith(
            (ref) => Future.value(const <ReviewPrompt>[]),
          ),
        ],
        child: const MaterialApp(home: Scaffold(body: CLIAgentsScreen())),
      ),
    );
    if (modelCompleter == null) {
      await tester.pumpAndSettle();
    } else {
      await tester.pump();
      await tester.pump();
    }
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

  testWidgets('model suggestions come from the daemon catalog', (tester) async {
    await pumpScreen(
      tester,
      dangerouslySkipPerms: false,
      modelCatalog: const {
        'claude': ['claude-live-model'],
        'gemini': <String>[],
        'codex': <String>[],
      },
    );

    await tester.enterText(
      find.byKey(const ValueKey('model-input-claude')),
      'live',
    );
    await tester.pump();

    expect(find.text('claude-live-model'), findsOneWidget);
    await tester.tap(find.text('claude-live-model'));
    await tester.pump();
    final field = tester.widget<TextFormField>(
      find.byKey(const ValueKey('model-input-claude')),
    );
    expect(field.controller!.text, 'claude-live-model');
  });

  testWidgets('shows loading while CLI model discovery is pending', (tester) async {
    final completer = Completer<Map<String, List<String>>>();
    await pumpScreen(
      tester,
      dangerouslySkipPerms: false,
      modelCompleter: completer,
    );

    expect(find.textContaining('Available models are read'), findsOneWidget);
    expect(find.byType(CircularProgressIndicator), findsWidgets);

    completer.complete(const {'claude': <String>[]});
    await tester.pumpAndSettle();
  });

  testWidgets('preserves manual input when model discovery fails', (tester) async {
    await pumpScreen(
      tester,
      dangerouslySkipPerms: false,
      claudeModel: 'private-model',
      modelError: Exception('unavailable'),
    );

    expect(
      find.text('Model discovery is unavailable. Saved values are preserved.'),
      findsOneWidget,
    );
    final field = tester.widget<TextFormField>(
      find.byKey(const ValueKey('model-input-claude')),
    );
    expect(field.controller!.text, 'private-model');
  });

  testWidgets('configured model stays editable when absent from catalog', (tester) async {
    await pumpScreen(
      tester,
      dangerouslySkipPerms: false,
      claudeModel: 'private-model',
      modelCatalog: const {
        'claude': ['sonnet'],
        'gemini': <String>[],
        'codex': <String>[],
      },
    );

    final field = tester.widget<TextFormField>(
      find.byKey(const ValueKey('model-input-claude')),
    );
    expect(field.controller!.text, 'private-model');
  });
}
