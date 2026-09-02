import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/agents/agents_screen.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/server/server_providers.dart';
import 'package:heimdallm/features/server/server_screen.dart';
import 'package:heimdallm/core/models/agent.dart';
import 'package:mocktail/mocktail.dart';

import '../../../core/platform/fake_platform_services.dart';

class _MockApiClient extends Mock implements ApiClient {}

Future<_MockApiClient> _mount(WidgetTester tester) async {
  final api = _MockApiClient();
  when(() => api.fetchConfig()).thenAnswer(
    (_) async => {
      'repositories': <String>[],
      'server_port': 7842,
      'bind_addr': '127.0.0.1',
      'poll_interval': '60s',
      'retention_days': 30,
      'ai_primary': 'claude',
      'ai_fallback': '',
      'review_mode': 'single',
      'issue_tracking': {'enabled': false},
    },
  );
  when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.daemon);
  when(() => api.fetchHealth()).thenAnswer((_) async => {'status': 'ok'});
  when(() => api.patchConfig(any())).thenAnswer((_) async => {});
  when(() => api.shutdownDaemon()).thenAnswer((_) async {});
  when(() => api.daemonPort).thenReturn(7842);

  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        apiClientProvider.overrideWithValue(api),
        configNotifierProvider.overrideWith(ConfigNotifier.new),
        agentsProvider.overrideWith((_) async => <ReviewPrompt>[]),
        serverHealthDetailProvider.overrideWith(
          (_) => Stream.value(const HealthDetail()),
        ),
        platformServicesProvider.overrideWithValue(FakePlatformServices()),
      ],
      child: const MaterialApp(home: ServerScreen(initialTab: 'status')),
    ),
  );
  await tester.pumpAndSettle();
  return api;
}

void main() {
  setUpAll(() => registerFallbackValue(<String, dynamic>{}));

  testWidgets('editing the port shows the restart banner', (tester) async {
    await _mount(tester);

    await tester.enterText(find.widgetWithText(TextField, 'Port'), '9000');
    await tester.pump();

    expect(
      find.text('Listen URL changed. Restart the server for it to take effect.'),
      findsOneWidget,
    );
    expect(
      find.textContaining('Port change also requires restarting'),
      findsOneWidget,
    );
  });

  testWidgets('tapping Restart server invokes the restart action', (
    tester,
  ) async {
    final api = await _mount(tester);
    // Restart's own daemon-down poll — none of these installs have a spawn
    // path once the binary lookup returns null, so it settles quickly.
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.none);

    await tester.enterText(find.widgetWithText(TextField, 'Bind address'), '0.0.0.0');
    await tester.pump();

    await tester.tap(find.text('Restart server'));
    await tester.pump();
    await tester.pump(const Duration(seconds: 3));
    await tester.pumpAndSettle();

    verify(() => api.shutdownDaemon()).called(1);
  });
}
