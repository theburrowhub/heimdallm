import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart'
    show ConfigNotifier, configNotifierProvider;
import 'package:heimdallm/features/config/config_screen.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/widgets/review_limits_editor.dart';
import 'package:heimdallm/shared/widgets/token_saving_editor.dart';
import 'package:mocktail/mocktail.dart';

import '../core/platform/fake_platform_services.dart';

class _MockApiClient extends Mock implements ApiClient {}

void main() {
  setUpAll(() => registerFallbackValue(<String, dynamic>{}));

  testWidgets('review limits and token saving reach the daemon on save', (
    tester,
  ) async {
    const config = AppConfig(pollInterval: '5m', aiPrimary: 'claude');
    final json = config.toJson();
    final api = _MockApiClient();
    when(() => api.fetchConfig()).thenAnswer((_) async => json);
    when(() => api.patchConfig(any())).thenAnswer((_) async => json);
    when(() => api.fetchReviewLimits()).thenAnswer((_) async => const []);
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.daemon);

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(api),
          configNotifierProvider.overrideWith(ConfigNotifier.new),
          platformServicesProvider.overrideWithValue(FakePlatformServices()),
        ],
        child: MaterialApp.router(
          theme: HeimdallmTheme.light(),
          builder: (context, child) => HeimdallmTheme.scope(child: child!),
          routerConfig: GoRouter(
            routes: [
              GoRoute(path: '/', builder: (_, _) => const ConfigScreen()),
            ],
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    tester
        .widget<ReviewLimitsFields>(
          find.byWidgetPredicate(
            (w) => w is ReviewLimitsFields && w.keyPrefix == 'global',
          ),
        )
        .onChanged(const ReviewLimits(perMinute: 2));
    tester
        .widget<TokenSavingSettingsEditor>(
          find.byType(TokenSavingSettingsEditor),
        )
        .onChanged(const TokenSavingSettings(compactPrompt: false));
    await tester.pumpAndSettle();

    final save = find.widgetWithText(ElevatedButton, 'Save');
    await tester.ensureVisible(save);
    await tester.pumpAndSettle();
    await tester.tap(save);
    await tester.pumpAndSettle();

    final patch =
        verify(() => api.patchConfig(captureAny())).captured.last
            as Map<String, dynamic>;
    expect(patch['review_limits'], {
      'per_minute': 2,
      'per_hour': 0,
      'per_day': 0,
    });
    expect(patch['ai']['token_saving'], {'compact_prompt': false});
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('the global review flow is picked here and saved', (
    tester,
  ) async {
    const config = AppConfig(pollInterval: '5m', aiPrimary: 'claude');
    final json = config.toJson();
    expect(AppConfig.fromJson({...json, 'ai_flow': 'night'}).aiFlow, 'night');
    final api = _MockApiClient();
    when(() => api.fetchConfig()).thenAnswer((_) async => json);
    when(() => api.patchConfig(any())).thenAnswer((_) async => json);
    when(() => api.fetchReviewLimits()).thenAnswer((_) async => const []);
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.daemon);
    when(() => api.fetchFlows()).thenAnswer(
      (_) async => FlowListing.fromJson(const {
        'flows': {
          'default': {'rules': {}},
          'night': {'rules': {}},
        },
      }),
    );
    final router = GoRouter(
      routes: [
        GoRoute(path: '/', builder: (_, _) => const ConfigScreen()),
        GoRoute(path: '/flows', builder: (_, _) => const Text('flows page')),
      ],
    );
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(api),
          configNotifierProvider.overrideWith(ConfigNotifier.new),
          platformServicesProvider.overrideWithValue(FakePlatformServices()),
        ],
        child: MaterialApp.router(
          theme: HeimdallmTheme.light(),
          builder: (context, child) => HeimdallmTheme.scope(child: child!),
          routerConfig: router,
        ),
      ),
    );
    await tester.pumpAndSettle();

    final flowField = find.byKey(const ValueKey('global-review-flow'));
    await tester.ensureVisible(flowField);
    await tester.pumpAndSettle();
    await tester.tap(flowField);
    await tester.pumpAndSettle();
    await tester.tap(find.text('night').last);
    await tester.pumpAndSettle();

    final save = find.widgetWithText(ElevatedButton, 'Save');
    await tester.ensureVisible(save);
    await tester.pumpAndSettle();
    await tester.tap(save);
    await tester.pumpAndSettle();
    final patch =
        verify(() => api.patchConfig(captureAny())).captured.last
            as Map<String, dynamic>;
    expect(patch['ai']['flow'], 'night');
    expect((patch['ai'] as Map).containsKey('primary'), isFalse);

    await tester.ensureVisible(find.byKey(const ValueKey('manage-flows')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('manage-flows')));
    await tester.pumpAndSettle();
    expect(find.text('flows page'), findsOneWidget);
  });
}
