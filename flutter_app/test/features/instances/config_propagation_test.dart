import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/core/api/daemon_endpoint.dart';
import 'package:heimdallm/features/instances/config_propagation_dialog.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// The cluster calls are extension methods, which cannot be stubbed, so the
/// fake sits at the HTTP layer instead — which exercises the encoding and
/// status handling too.
ApiClient _fakeHub(Future<http.Response> Function(http.Request) respond) {
  return ApiClient(
    httpClient: MockClient(respond),
    endpoint: DaemonEndpoint.raw(baseUrl: 'http://hub:7842', token: 't'),
  );
}

/// Opens the dialog inside a minimal host.
Future<void> _open(
  WidgetTester tester, {
  required List<InstanceDrift> drift,
  ApiClient? api,
}) async {
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        configDriftProvider.overrideWith((ref) async => drift),
        if (api != null) hubApiClientProvider.overrideWithValue(api),
      ],
      child: MaterialApp(
        home: Scaffold(
          body: Consumer(
            builder: (context, ref, _) => TextButton(
              onPressed: () => showConfigPropagationDialog(context, ref),
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

InstanceDrift _drift(Map<String, dynamic> json) => InstanceDrift.fromJson(json);

void main() {
  testWidgets('states which settings are shared and which stay local', (
    tester,
  ) async {
    await _open(tester, drift: const []);

    expect(
      find.textContaining('Shared settings'),
      findsOneWidget,
    );
    // The operator has to know a port or a token was deliberately not sent,
    // rather than wondering why it did not take effect.
    expect(find.textContaining('never sent'), findsOneWidget);
  });

  testWidgets('shows which instances differ and in what', (tester) async {
    await _open(
      tester,
      drift: [
        _drift({
          'instance_id': 'srv-a',
          'name': 'Server A',
          'ok': true,
          'drifts': [
            {
              'key': 'ai.review_mode',
              'hub_value': 'multi',
              'remote_value': 'single',
            },
          ],
        }),
        _drift({'instance_id': 'srv-b', 'name': 'Server B', 'ok': true}),
      ],
    );

    expect(find.text('Server A'), findsOneWidget);
    expect(find.text('1 setting differs'), findsOneWidget);
    expect(find.text('In sync'), findsOneWidget);

    await tester.tap(find.text('Server A'));
    await tester.pumpAndSettle();
    expect(find.text('ai.review_mode'), findsOneWidget);
    expect(find.text('single → multi'), findsOneWidget);
  });

  testWidgets('reports an instance that could not be inspected', (
    tester,
  ) async {
    await _open(
      tester,
      drift: [
        _drift({
          'instance_id': 'srv-a',
          'name': 'Server A',
          'error': 'connection refused',
        }),
      ],
    );

    expect(find.text('connection refused'), findsOneWidget);
  });

  testWidgets('a partial push reports per-instance outcomes', (tester) async {
    // One machine rebooting must not hide that the others were updated, and
    // the operator needs to know exactly which ones to retry.
    // 207 is the partial-success status the hub answers with.
    final api = _fakeHub(
      (_) async => http.Response(
        jsonEncode({
          'failures': 1,
          'skipped_local': ['server.port', 'github.token'],
          'results': [
            {
              'instance_id': 'hub-1',
              'name': 'Local hub',
              'ok': true,
              'skipped': true,
            },
            {
              'instance_id': 'srv-a',
              'name': 'Server A',
              'ok': true,
              'applied_keys': ['ai.review_mode', 'polling.tier2_interval'],
            },
            {
              'instance_id': 'srv-b',
              'name': 'Server B',
              'ok': false,
              'error': 'daemon is starting',
            },
          ],
        }),
        207,
      ),
    );

    await _open(tester, drift: const [], api: api);
    await tester.tap(find.text('Apply to all'));
    await tester.pumpAndSettle();

    expect(find.text('Applied 2 settings'), findsOneWidget);
    expect(find.text('daemon is starting'), findsOneWidget);
    expect(
      find.text('Kept local: server.port, github.token'),
      findsOneWidget,
    );
    expect(find.text('Close'), findsOneWidget);
  });

  testWidgets('surfaces a failed push instead of closing silently', (
    tester,
  ) async {
    final api = _fakeHub(
      (_) async => http.Response(
        jsonEncode({'error': 'hub is unreachable'}),
        502,
      ),
    );

    await _open(tester, drift: const [], api: api);
    await tester.tap(find.text('Apply to all'));
    await tester.pumpAndSettle();

    expect(find.text('hub is unreachable'), findsOneWidget);
  });
}
