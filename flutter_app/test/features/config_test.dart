import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:mocktail/mocktail.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/config/config_screen.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';

class MockApiClient extends Mock implements ApiClient {}

void main() {
  setUpAll(() {
    registerFallbackValue(<String, dynamic>{});
  });

  testWidgets('ConfigScreen shows current poll interval', (tester) async {
    const config = AppConfig(
      pollInterval: '5m',
      aiPrimary: 'claude',
      repoConfigs: {'org/repo': RepoConfig(monitored: true)},
    );

    final mockApi = MockApiClient();
    when(() => mockApi.fetchConfig()).thenAnswer((_) async => config.toJson());
    when(() => mockApi.updateConfig(any())).thenAnswer((_) async {});

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(mockApi),
          configNotifierProvider.overrideWith(ConfigNotifier.new),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(routes: [
            GoRoute(path: '/', builder: (_, __) => const ConfigScreen()),
          ]),
        ),
      ),
    );
    await tester.pumpAndSettle();

    // Poll interval is still shown in Settings
    expect(find.text('5m'), findsAtLeastNWidgets(1));
    // Primary agent ('claude') moved to Agents tab — no longer in ConfigScreen
  });
}
