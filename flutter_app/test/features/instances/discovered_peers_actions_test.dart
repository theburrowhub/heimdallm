import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/api/daemon_endpoint.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/features/instances/discovered_peers_section.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// Records what reached the daemon. The cluster calls are extension methods and
/// cannot be stubbed, so the fake sits at the HTTP layer.
class _Recorder {
  final List<String> calls = [];
  final List<String> bodies = [];
}

ApiClient _api(
  _Recorder rec, {
  int status = 200,
  Map<String, dynamic> body = const {},
}) {
  final client = MockClient((request) async {
    rec.calls.add('${request.method} ${request.url.path}');
    rec.bodies.add(request.body);
    return http.Response(jsonEncode(body), status);
  });
  return ApiClient(
    httpClient: client,
    endpoint: DaemonEndpoint.raw(baseUrl: 'http://hub:7842', token: 'tok'),
  );
}

DiscoveredPeers _found({
  bool enabled = true,
  List<Map<String, dynamic>> peers = const [],
}) {
  return DiscoveredPeers.fromJson({'enabled': enabled, 'peers': peers});
}

Map<String, dynamic> _peer({
  String id = 'srv-a',
  String status = 'new',
  String registeredId = '',
  String registeredBaseUrl = '',
}) {
  return {
    'instance_id': id,
    'name': 'Server A',
    'role': 'worker',
    'version': '0.8.17',
    'base_url': 'http://srv-a.local:7842',
    'hostname': 'srv-a.local',
    'status': status,
    'registered_id': registeredId,
    'registered_base_url': registeredBaseUrl,
  };
}

Future<void> _pump(
  WidgetTester tester,
  Widget child, {
  required DiscoveredPeers found,
  required ApiClient api,
}) async {
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        discoveredPeersProvider.overrideWith((ref) async => found),
        daemonInstancesProvider.overrideWith(
          (ref) async => ClusterRegistry.empty,
        ),
        localClusterRoleProvider.overrideWith((ref) async => 'hub'),
        hubApiClientProvider.overrideWithValue(api),
      ],
      child: MaterialApp(home: Scaffold(body: child)),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  group('turning discovery on', () {
    testWidgets('patches cluster.discovery rather than a whole config', (
      tester,
    ) async {
      final rec = _Recorder();
      await _pump(
        tester,
        const DiscoveredPeersSection(),
        found: _found(enabled: false),
        api: _api(rec),
      );

      await tester.tap(find.widgetWithText(FilledButton, 'Turn on'));
      await tester.pumpAndSettle();

      expect(rec.calls, contains('PATCH /config'));
      final body = jsonDecode(rec.bodies.first) as Map<String, dynamic>;
      // Scoped: turning discovery on must not rewrite anything else.
      expect(body, {
        'cluster': {'discovery': 'mdns'},
      });
    });

    testWidgets('shows why it failed instead of pretending it worked', (
      tester,
    ) async {
      final rec = _Recorder();
      await _pump(
        tester,
        const DiscoveredPeersSection(),
        found: _found(enabled: false),
        api: _api(rec, status: 500, body: {'error': 'config is read-only'}),
      );

      await tester.tap(find.widgetWithText(FilledButton, 'Turn on'));
      await tester.pumpAndSettle();

      expect(find.textContaining('read-only'), findsOneWidget);
      // The button comes back, so the operator can retry.
      expect(find.widgetWithText(FilledButton, 'Turn on'), findsOneWidget);
    });
  });

  group('scanning', () {
    testWidgets('asks the hub to browse now', (tester) async {
      final rec = _Recorder();
      await _pump(
        tester,
        const DiscoveredPeersSection(),
        found: _found(peers: [_peer()]),
        api: _api(rec, body: {'enabled': true, 'peers': []}),
      );

      await tester.tap(find.byTooltip('Scan the network now'));
      await tester.pumpAndSettle();

      expect(rec.calls, contains('POST /cluster/discovered/scan'));
    });

    testWidgets('surfaces a failed scan', (tester) async {
      // Someone pressing Scan is waiting on an answer, so silence is the wrong
      // response to an error here even though the section is quiet otherwise.
      final rec = _Recorder();
      await _pump(
        tester,
        const DiscoveredPeersSection(),
        found: _found(peers: [_peer()]),
        api: _api(rec, status: 503, body: {'error': 'browse failed'}),
      );

      await tester.tap(find.byTooltip('Scan the network now'));
      await tester.pumpAndSettle();

      expect(find.textContaining('Scan failed'), findsOneWidget);
    });
  });

  group('repairing a moved instance', () {
    testWidgets('patches only the address', (tester) async {
      final rec = _Recorder();
      await _pump(
        tester,
        const AddressChangedBanner(instanceId: 'srv-a'),
        found: _found(
          peers: [
            _peer(
              status: 'address_changed',
              registeredId: 'srv-a',
              registeredBaseUrl: 'http://10.0.0.11:7842',
            ),
          ],
        ),
        api: _api(rec),
      );

      await tester.tap(find.widgetWithText(TextButton, 'Update address'));
      await tester.pumpAndSettle();

      expect(rec.calls, contains('PATCH /instances/srv-a'));
      final body = jsonDecode(rec.bodies.first) as Map<String, dynamic>;
      expect(body, {'base_url': 'http://srv-a.local:7842'});
    });

    testWidgets('reports a refused update', (tester) async {
      final rec = _Recorder();
      await _pump(
        tester,
        const AddressChangedBanner(instanceId: 'srv-a'),
        found: _found(
          peers: [
            _peer(
              status: 'address_changed',
              registeredId: 'srv-a',
              registeredBaseUrl: 'http://10.0.0.11:7842',
            ),
          ],
        ),
        api: _api(rec, status: 400, body: {'error': 'base_url is invalid'}),
      );

      await tester.tap(find.widgetWithText(TextButton, 'Update address'));
      await tester.pumpAndSettle();

      expect(find.textContaining('invalid'), findsOneWidget);
    });
  });
}
