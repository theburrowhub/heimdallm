import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/agent.dart';
import 'package:heimdallm/features/agents/agents_screen.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/server/server_providers.dart';
import 'package:heimdallm/features/server/server_screen.dart';
import 'package:mocktail/mocktail.dart';

class MockApiClient extends Mock implements ApiClient {}

Future<void> _pump(WidgetTester tester, MockApiClient api,
    {String initialTab = 'status'}) async {
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        apiClientProvider.overrideWithValue(api),
        configNotifierProvider.overrideWith(ConfigNotifier.new),
        agentsProvider.overrideWith((_) async => <ReviewPrompt>[]),
        // Override to avoid the 30-second polling timer leaking into tests.
        serverHealthDetailProvider.overrideWith(
          (_) => Stream.value(const HealthDetail()),
        ),
      ],
      child: MaterialApp(home: ServerScreen(initialTab: initialTab)),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  setUpAll(() => registerFallbackValue(<String, dynamic>{}));

  testWidgets('renders Status / Events / Logs tabs', (tester) async {
    final api = MockApiClient();
    when(() => api.fetchConfig()).thenAnswer((_) async => {
          'repositories': <String>[],
          'server_port': 7842,
          'bind_addr': '127.0.0.1',
          'poll_interval': '60s',
          'retention_days': 30,
          'ai_primary': 'claude',
          'ai_fallback': '',
          'review_mode': 'single',
          'issue_tracking': {'enabled': false},
        });
    when(() => api.checkHealth()).thenAnswer((_) async => true);
    when(() => api.fetchHealth()).thenAnswer((_) async => {'status': 'ok'});

    await _pump(tester, api);

    expect(find.text('Status'), findsOneWidget);
    expect(find.text('Events'), findsOneWidget);
    expect(find.text('Logs'), findsOneWidget);
  });

  testWidgets('Status tab shows Stop button when daemon running', (tester) async {
    final api = MockApiClient();
    when(() => api.fetchConfig()).thenAnswer((_) async => {
          'repositories': <String>[],
          'server_port': 7842,
          'bind_addr': '127.0.0.1',
          'poll_interval': '60s',
          'retention_days': 30,
          'ai_primary': 'claude',
          'ai_fallback': '',
          'review_mode': 'single',
          'issue_tracking': {'enabled': false},
        });
    when(() => api.checkHealth()).thenAnswer((_) async => true);
    when(() => api.fetchHealth()).thenAnswer((_) async => {'status': 'ok'});

    await _pump(tester, api);

    expect(find.text('Stop server'), findsOneWidget);
    expect(find.text('Start server'), findsNothing);
  });

  testWidgets('Status tab shows Start button when daemon stopped',
      (tester) async {
    final api = MockApiClient();
    when(() => api.fetchConfig()).thenAnswer((_) async => {
          'repositories': <String>[],
          'server_port': 7842,
          'bind_addr': '127.0.0.1',
          'poll_interval': '60s',
          'retention_days': 30,
          'ai_primary': 'claude',
          'ai_fallback': '',
          'review_mode': 'single',
          'issue_tracking': {'enabled': false},
        });
    when(() => api.checkHealth()).thenAnswer((_) async => false);
    when(() => api.fetchHealth()).thenAnswer((_) async => null);

    await _pump(tester, api);

    expect(find.text('Start server'), findsOneWidget);
    expect(find.text('Stop server'), findsNothing);
  });

  testWidgets('initialTab=events selects the Events tab', (tester) async {
    final api = MockApiClient();
    when(() => api.fetchConfig()).thenAnswer((_) async => {
          'repositories': <String>[],
          'server_port': 7842,
          'bind_addr': '127.0.0.1',
          'poll_interval': '60s',
          'retention_days': 30,
          'ai_primary': 'claude',
          'ai_fallback': '',
          'review_mode': 'single',
          'issue_tracking': {'enabled': false},
        });
    when(() => api.checkHealth()).thenAnswer((_) async => true);
    when(() => api.fetchHealth()).thenAnswer((_) async => {'status': 'ok'});

    await _pump(tester, api, initialTab: 'events');
    // The Events tab is selected: its placeholder string should be visible.
    expect(find.textContaining('Waiting for events'), findsOneWidget);
  });
}
