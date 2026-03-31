import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:mocktail/mocktail.dart';
import 'package:auto_pr/core/api/api_client.dart';
import 'package:auto_pr/core/models/config_model.dart';
import 'package:auto_pr/features/config/config_providers.dart';
import 'package:auto_pr/features/config/config_screen.dart';
import 'package:auto_pr/features/dashboard/dashboard_providers.dart';

class MockApiClient extends Mock implements ApiClient {}

void main() {
  setUpAll(() {
    registerFallbackValue(<String, dynamic>{});
  });

  testWidgets('ConfigScreen shows current poll interval', (tester) async {
    final config = AppConfig(pollInterval: '5m', aiPrimary: 'claude', repositories: ['org/repo']);

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

    expect(find.text('5m'), findsAtLeastNWidgets(1));
    expect(find.text('claude'), findsAtLeastNWidgets(1));
  });
}
