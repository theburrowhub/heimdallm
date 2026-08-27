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
        'claude': CLIAgentConfig(dangerouslySkipPerms: dangerouslySkipPerms),
      },
    );
    final configJson = {
      ...config.toJson(),
      'agent_configs': {
        'claude': {'dangerously_skip_perms': dangerouslySkipPerms},
      },
    };
    final api = _MockApiClient();
    when(api.fetchConfig).thenAnswer((_) async => configJson);
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
}
