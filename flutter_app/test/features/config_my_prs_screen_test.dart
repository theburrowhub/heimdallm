import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart'
    show ConfigNotifier, configNotifierProvider;
import 'package:heimdallm/features/config/config_screen.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:mocktail/mocktail.dart';

import '../core/platform/fake_platform_services.dart';

class _MockApiClient extends Mock implements ApiClient {}

Future<_MockApiClient> _mount(WidgetTester tester, MyPrsConfig myPrs) async {
  const config = AppConfig(
    pollInterval: '5m',
    aiPrimary: 'claude',
    repoConfigs: {'org/repo': RepoConfig(prEnabled: true)},
  );
  final json = {...config.toJson(), 'my_prs': myPrs.toJson()};

  final mockApi = _MockApiClient();
  when(() => mockApi.fetchConfig()).thenAnswer((_) async => json);
  when(() => mockApi.patchConfig(any())).thenAnswer((_) async => json);
  when(() => mockApi.daemonReachable()).thenAnswer((_) async => PortOwner.daemon);

  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        apiClientProvider.overrideWithValue(mockApi),
        configNotifierProvider.overrideWith(ConfigNotifier.new),
        platformServicesProvider.overrideWithValue(FakePlatformServices()),
      ],
      child: MaterialApp.router(
        theme: HeimdallmTheme.light(),
        darkTheme: HeimdallmTheme.dark(),
        builder: (context, child) => HeimdallmTheme.scope(child: child!),
        routerConfig: GoRouter(
          routes: [GoRoute(path: '/', builder: (_, _) => const ConfigScreen())],
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return mockApi;
}

Future<void> _reveal(WidgetTester tester, Finder finder) async {
  await tester.scrollUntilVisible(
    finder,
    200,
    scrollable: find.byType(Scrollable).first,
  );
  await tester.pumpAndSettle();
}

void main() {
  setUpAll(() => registerFallbackValue(<String, dynamic>{}));

  testWidgets('the section shows the daemon values and hides the rest when off', (
    tester,
  ) async {
    await _mount(tester, const MyPrsConfig());
    await _reveal(tester, find.byKey(const Key('my-prs-notify-transitions')));

    expect(find.text('Watch my pull requests'), findsOneWidget);
    expect(find.widgetWithText(TextFormField, '3d'), findsOneWidget);
    expect(find.widgetWithText(TextFormField, '10:00'), findsOneWidget);

    await _reveal(tester, find.byKey(const Key('my-prs-enabled')));
    await tester.tap(find.byKey(const Key('my-prs-enabled')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('my-prs-stale-after')), findsNothing);
    expect(find.byKey(const Key('my-prs-notify-transitions')), findsNothing);
  });

  testWidgets('an invalid threshold is flagged and blocks saving', (
    tester,
  ) async {
    await _mount(tester, const MyPrsConfig());
    await _reveal(tester, find.byKey(const Key('my-prs-stale-after')));

    await tester.enterText(find.byKey(const Key('my-prs-stale-after')), 'soon');
    await tester.pumpAndSettle();
    expect(find.text('Invalid duration (e.g. 90m, 12h, 3d)'), findsOneWidget);

    final save = find.widgetWithText(ElevatedButton, 'Save');
    expect(tester.widget<ElevatedButton>(save).onPressed, isNull);

    await tester.enterText(find.byKey(const Key('my-prs-stale-after')), '12h');
    await tester.pumpAndSettle();
    expect(tester.widget<ElevatedButton>(save).onPressed, isNotNull);
  });

  testWidgets('saving sends only the changed my_prs fields', (tester) async {
    final api = await _mount(tester, const MyPrsConfig());
    await _reveal(tester, find.byKey(const Key('my-prs-notify-transitions')));
    await tester.tap(find.byKey(const Key('my-prs-notify-transitions')));
    await tester.pumpAndSettle();

    await tester.tap(find.widgetWithText(ElevatedButton, 'Save'));
    await tester.pumpAndSettle();

    final sent =
        verify(() => api.patchConfig(captureAny())).captured.last
            as Map<String, dynamic>;
    expect(sent['my_prs'], {'notify_transitions': true});
  });
}
