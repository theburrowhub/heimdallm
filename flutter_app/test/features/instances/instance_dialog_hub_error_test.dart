import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/api/daemon_endpoint.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart'
    show apiClientProvider;
import 'package:heimdallm/features/instances/instance_dialog.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:mocktail/mocktail.dart';

import '../../core/platform/fake_platform_services.dart';

class _MockApiClient extends Mock implements ApiClient {}

ApiClient _fakeHub({required int status, required Object body}) {
  return ApiClient(
    httpClient: MockClient(
      (request) async => http.Response(jsonEncode(body), status),
    ),
    endpoint: DaemonEndpoint.raw(baseUrl: 'http://hub:7842', token: 't'),
  );
}

Future<void> _open(
  WidgetTester tester,
  ApiClient api, {
  List<dynamic> extraOverrides = const [],
}) async {
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        hubApiClientProvider.overrideWithValue(api),
        ...extraOverrides,
      ],
      child: MaterialApp(
        home: Scaffold(
          body: Consumer(
            builder: (context, ref, _) => TextButton(
              onPressed: () => showInstanceDialog(context, ref),
              child: const Text('open'),
            ),
          ),
        ),
      ),
    ),
  );
  await tester.tap(find.text('open'));
  await tester.pumpAndSettle();
}

Future<void> _submit(WidgetTester tester) async {
  await tester.enterText(
    find.widgetWithText(TextFormField, 'Base URL'),
    'http://10.0.0.12:7842',
  );
  await tester.enterText(find.widgetWithText(TextFormField, 'Token'), 'sec');
  await tester.tap(find.text('Add'));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets(
    'humanises "this daemon is not a cluster hub" instead of showing it raw',
    (tester) async {
      await _open(
        tester,
        _fakeHub(
          status: 404,
          body: {'error': 'this daemon is not a cluster hub'},
        ),
      );
      await _submit(tester);

      // The regression this test exists to catch: the raw sentinel must
      // never reach the screen.
      expect(
        find.text('this daemon is not a cluster hub'),
        findsNothing,
      );
      expect(find.text('This daemon is not a cluster hub yet'), findsOneWidget);
      expect(find.text('Enable hub mode'), findsOneWidget);
    },
  );

  testWidgets('a different 404 still renders verbatim', (tester) async {
    await _open(
      tester,
      _fakeHub(status: 404, body: {'error': 'instance srv-a not found'}),
    );
    await _submit(tester);

    expect(find.text('instance srv-a not found'), findsOneWidget);
    expect(find.text('This daemon is not a cluster hub yet'), findsNothing);
    expect(find.text('Enable hub mode'), findsNothing);
  });

  testWidgets(
    'tapping Enable hub mode drives the enable-hub flow and closes the dialog',
    (tester) async {
      final localApi = _MockApiClient();
      when(() => localApi.fetchConfig()).thenAnswer(
        (_) async => {
          'poll_interval': '5m',
          'ai_primary': 'claude',
          'cluster': {'role': 'standalone'},
        },
      );
      when(() => localApi.patchConfig(any())).thenAnswer((_) async => {});
      registerFallbackValue(<String, dynamic>{});

      await _open(
        tester,
        _fakeHub(
          status: 404,
          body: {'error': 'this daemon is not a cluster hub'},
        ),
        extraOverrides: [
          apiClientProvider.overrideWithValue(localApi),
          configNotifierProvider.overrideWith(ConfigNotifier.new),
          platformServicesProvider.overrideWithValue(FakePlatformServices()),
        ],
      );
      await _submit(tester);

      await tester.ensureVisible(find.text('Enable hub mode'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('Enable hub mode'));
      await tester.pumpAndSettle();

      expect(find.text('Make this daemon a cluster hub?'), findsOneWidget);
      await tester.tap(find.text('Enable and restart'));
      await tester.pumpAndSettle();

      final captured = verify(
        () => localApi.patchConfig(captureAny()),
      ).captured;
      expect(captured, isNotEmpty);
      expect(captured.last, containsPair('cluster', {'role': 'hub'}));
      // The restart tears the form down; the dialog closing is the honest
      // signal that "add this instance again" is now the right next step.
      expect(find.text('Add instance'), findsNothing);
    },
  );
}
