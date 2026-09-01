import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/api/daemon_endpoint.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/features/instances/instance_dialog.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

class _Recorder {
  final List<http.Request> requests = [];
}

ApiClient _fakeHub(_Recorder recorder, {int status = 201, Object? body}) {
  return ApiClient(
    httpClient: MockClient((request) async {
      recorder.requests.add(request);
      return http.Response(jsonEncode(body ?? {'id': 'srv-b'}), status);
    }),
    endpoint: DaemonEndpoint.raw(baseUrl: 'http://hub:7842', token: 't'),
  );
}

Future<void> _open(
  WidgetTester tester,
  ApiClient api, {
  DaemonInstance? existing,
}) async {
  await tester.pumpWidget(
    ProviderScope(
      overrides: [hubApiClientProvider.overrideWithValue(api)],
      child: MaterialApp(
        home: Scaffold(
          body: Consumer(
            builder: (context, ref, _) => TextButton(
              onPressed: () =>
                  showInstanceDialog(context, ref, existing: existing),
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

void main() {
  testWidgets('rejects a base URL that is not an absolute http(s) URL', (
    tester,
  ) async {
    final recorder = _Recorder();
    await _open(tester, _fakeHub(recorder));

    await tester.enterText(
      find.widgetWithText(TextFormField, 'Base URL'),
      'not-a-url',
    );
    await tester.tap(find.text('Add'));
    await tester.pumpAndSettle();

    expect(find.text('Must be an absolute URL'), findsOneWidget);
    // A malformed URL must never reach the hub.
    expect(recorder.requests, isEmpty);
  });

  testWidgets('rejects a URL embedding credentials', (tester) async {
    final recorder = _Recorder();
    await _open(tester, _fakeHub(recorder));

    await tester.enterText(
      find.widgetWithText(TextFormField, 'Base URL'),
      'http://user:pass@host:7842',
    );
    await tester.tap(find.text('Add'));
    await tester.pumpAndSettle();

    expect(find.text('Must not embed credentials'), findsOneWidget);
    expect(recorder.requests, isEmpty);
  });

  testWidgets('registers with exactly one token source', (tester) async {
    final recorder = _Recorder();
    await _open(tester, _fakeHub(recorder));

    await tester.enterText(
      find.widgetWithText(TextFormField, 'Base URL'),
      'http://10.0.0.12:7842',
    );
    await tester.enterText(find.widgetWithText(TextFormField, 'Token'), 'sec');
    await tester.tap(find.text('Add'));
    await tester.pumpAndSettle();

    expect(recorder.requests, hasLength(1));
    final body =
        jsonDecode(recorder.requests.single.body) as Map<String, dynamic>;
    expect(body['base_url'], 'http://10.0.0.12:7842');
    expect(body['token'], 'sec');
    // The daemon rejects more than one declared source, so the form must send
    // only the selected one.
    expect(body.containsKey('token_env'), isFalse);
    expect(body.containsKey('token_file'), isFalse);
  });

  testWidgets('switching to an env var sends token_env instead', (
    tester,
  ) async {
    final recorder = _Recorder();
    await _open(tester, _fakeHub(recorder));

    await tester.enterText(
      find.widgetWithText(TextFormField, 'Base URL'),
      'http://10.0.0.12:7842',
    );
    await tester.tap(find.text('Env var'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.widgetWithText(TextFormField, 'Environment variable name'),
      'HEIMDALLM_SRV_B',
    );
    await tester.tap(find.text('Add'));
    await tester.pumpAndSettle();

    final body =
        jsonDecode(recorder.requests.single.body) as Map<String, dynamic>;
    expect(body['token_env'], 'HEIMDALLM_SRV_B');
    expect(body.containsKey('token'), isFalse);
  });

  testWidgets('shows the reason the hub refused the registration', (
    tester,
  ) async {
    // Registering something that never answers would leave an entry that looks
    // fine and silently never works, so the failure has to be visible.
    final recorder = _Recorder();
    final api = _fakeHub(
      recorder,
      status: 502,
      body: {'error': 'could not reach the instance at http://10.0.0.12:7842'},
    );
    await _open(tester, api);

    await tester.enterText(
      find.widgetWithText(TextFormField, 'Base URL'),
      'http://10.0.0.12:7842',
    );
    await tester.enterText(find.widgetWithText(TextFormField, 'Token'), 'sec');
    await tester.tap(find.text('Add'));
    await tester.pumpAndSettle();

    expect(find.textContaining('could not reach the instance'), findsOneWidget);
  });

  testWidgets('editing keeps the stored token when left blank', (tester) async {
    // Making the operator re-paste a secret just to rename a machine would be
    // a good way to get it wrong.
    final recorder = _Recorder();
    await _open(
      tester,
      _fakeHub(recorder, status: 200, body: const {}),
      existing: const DaemonInstance(
        id: 'srv-a',
        name: 'Server A',
        baseUrl: 'http://10.0.0.11:7842',
      ),
    );

    expect(find.text('Edit Server A'), findsOneWidget);
    expect(find.text('Leave blank to keep the current token'), findsOneWidget);

    await tester.enterText(
      find.widgetWithText(TextFormField, 'Name (optional)'),
      'Renamed',
    );
    await tester.tap(find.text('Save'));
    await tester.pumpAndSettle();

    final body =
        jsonDecode(recorder.requests.single.body) as Map<String, dynamic>;
    expect(body['name'], 'Renamed');
    expect(body.containsKey('token'), isFalse);
  });
}
