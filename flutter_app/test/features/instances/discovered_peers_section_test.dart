import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/features/instances/discovered_peers_section.dart';
import 'package:heimdallm/features/instances/instances_screen.dart';

ClusterRegistry _registry({List<Map<String, dynamic>> instances = const []}) {
  return ClusterRegistry.fromJson({
    'role': 'hub',
    'self_id': 'hub-1',
    'self_name': 'Local hub',
    'instances': instances,
  });
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
  String baseUrl = 'http://srv-a.local:7842',
  String registeredId = '',
  String registeredBaseUrl = '',
}) {
  return {
    'instance_id': id,
    'name': 'Server A',
    'role': 'worker',
    'version': '0.8.17',
    'base_url': baseUrl,
    'hostname': 'srv-a.local',
    'status': status,
    'registered_id': registeredId,
    'registered_base_url': registeredBaseUrl,
  };
}

Widget _app(Widget child) {
  return MaterialApp.router(
    routerConfig: GoRouter(
      routes: [GoRoute(path: '/', builder: (_, _) => child)],
    ),
  );
}

Future<void> _pump(
  WidgetTester tester, {
  required DiscoveredPeers found,
  ClusterRegistry? registry,
  bool? isHub = true,
  Widget? child,
}) async {
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        discoveredPeersProvider.overrideWith((ref) async => found),
        daemonInstancesProvider.overrideWith(
          (ref) async => registry ?? _registry(),
        ),
        localClusterRoleProvider.overrideWith(
          (ref) async => isHub == null
              ? null
              : isHub
              ? 'hub'
              : 'standalone',
        ),
      ],
      child: _app(child ?? const InstancesScreen()),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  group('DiscoveredPeersSection', () {
    testWidgets('offers an unregistered daemon found on the network', (
      tester,
    ) async {
      await _pump(tester, found: _found(peers: [_peer()]));

      expect(
        find.textContaining('1 daemon found on this network'),
        findsOneWidget,
      );
      expect(find.text('Server A'), findsOneWidget);
      expect(
        find.textContaining('http://srv-a.local:7842'),
        findsOneWidget,
      );
      expect(find.widgetWithText(FilledButton, 'Register'), findsOneWidget);
    });

    testWidgets('says the token does not travel over the network', (
      tester,
    ) async {
      // The security story has to be on the surface where someone is about to
      // adopt a machine, not only in the docs.
      await _pump(tester, found: _found(peers: [_peer()]));

      expect(find.textContaining('API token'), findsOneWidget);
      expect(
        find.textContaining('only adopt machines you recognise'),
        findsOneWidget,
      );
    });

    testWidgets('the section shows even with nothing registered', (
      tester,
    ) async {
      // A fresh hub with a daemon waiting on the LAN is the case this feature
      // exists for; the empty state must not hide it.
      await _pump(tester, found: _found(peers: [_peer()]));

      expect(find.text('No instances registered'), findsOneWidget);
      expect(
        find.textContaining('found on this network'),
        findsOneWidget,
      );
    });

    testWidgets('a peer already registered is not offered again', (
      tester,
    ) async {
      await _pump(
        tester,
        found: _found(peers: [_peer(status: 'registered')]),
      );

      expect(find.textContaining('found on this network'), findsNothing);
      expect(find.widgetWithText(FilledButton, 'Register'), findsNothing);
    });

    testWidgets('offers to turn discovery on when it is off', (tester) async {
      // Without this the feature is invisible until someone finds the key in
      // config.toml.
      await _pump(tester, found: _found(enabled: false));

      expect(find.text('Find instances on this network'), findsOneWidget);
      expect(find.widgetWithText(FilledButton, 'Turn on'), findsOneWidget);
      expect(
        find.textContaining('Only works within one subnet'),
        findsOneWidget,
      );
    });

    testWidgets('stays out of the way on a non-hub daemon', (tester) async {
      await _pump(tester, found: _found(enabled: false), isHub: false);

      expect(find.text('Find instances on this network'), findsNothing);
      expect(find.textContaining('found on this network'), findsNothing);
    });

    testWidgets('does not invite an unreachable daemon to turn discovery on', (
      tester,
    ) async {
      // A non-hub answers 404, which maps to an empty disabled listing — the
      // same shape a hub with discovery off produces. Offering the CTA on an
      // unconfirmed daemon would be offering to change a setting that may not
      // exist there.
      await _pump(tester, found: _found(enabled: false), isHub: null);

      expect(find.text('Find instances on this network'), findsNothing);
    });

    testWidgets('still lists peers when the role has not resolved yet', (
      tester,
    ) async {
      // Peers can only have come from a hub, so a slow /health response is no
      // reason to hide a machine that is demonstrably out there.
      await _pump(tester, found: _found(peers: [_peer()]), isHub: null);

      expect(find.textContaining('found on this network'), findsOneWidget);
    });
  });

  group('AddressChangedBanner', () {
    testWidgets('warns when a registered instance has moved', (tester) async {
      await _pump(
        tester,
        registry: _registry(
          instances: [
            {
              'id': 'srv-a',
              'name': 'Server A',
              'base_url': 'http://10.0.0.11:7842',
            },
          ],
        ),
        found: _found(
          peers: [
            _peer(
              status: 'address_changed',
              registeredId: 'srv-a',
              registeredBaseUrl: 'http://10.0.0.11:7842',
            ),
          ],
        ),
      );

      expect(
        find.textContaining('Answering at http://srv-a.local:7842'),
        findsOneWidget,
      );
      expect(find.widgetWithText(TextButton, 'Update address'), findsOneWidget);
      // The consequence, not just the fact — this is the #765 failure forming.
      expect(
        find.textContaining('take over its repositories'),
        findsOneWidget,
      );
    });

    testWidgets('is absent when the address still matches', (tester) async {
      await _pump(
        tester,
        registry: _registry(
          instances: [
            {
              'id': 'srv-a',
              'name': 'Server A',
              'base_url': 'http://srv-a.local:7842',
            },
          ],
        ),
        found: _found(peers: [_peer(status: 'registered')]),
      );

      expect(find.textContaining('Answering at'), findsNothing);
    });

    testWidgets('only marks the instance that actually moved', (tester) async {
      await _pump(
        tester,
        registry: _registry(
          instances: [
            {'id': 'srv-a', 'base_url': 'http://10.0.0.11:7842'},
            {'id': 'srv-b', 'base_url': 'http://srv-b.local:7842'},
          ],
        ),
        found: _found(
          peers: [
            _peer(
              status: 'address_changed',
              registeredId: 'srv-a',
              registeredBaseUrl: 'http://10.0.0.11:7842',
            ),
          ],
        ),
      );

      expect(find.widgetWithText(TextButton, 'Update address'), findsOneWidget);
    });

    testWidgets('renders nothing standalone when there is no match', (
      tester,
    ) async {
      await _pump(
        tester,
        found: _found(peers: [_peer()]),
        child: const Scaffold(
          body: AddressChangedBanner(instanceId: 'nobody'),
        ),
      );

      expect(find.textContaining('Answering at'), findsNothing);
    });
  });
}
