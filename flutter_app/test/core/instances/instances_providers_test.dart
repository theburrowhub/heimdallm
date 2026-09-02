import 'dart:convert';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/api/daemon_endpoint.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../platform/fake_platform_services.dart';

ClusterRegistry _registry(List<Map<String, dynamic>> instances) =>
    ClusterRegistry.fromJson({'self_id': 'hub-1', 'instances': instances});

ProviderContainer _container({
  ClusterRegistry? registry,
  FakePlatformServices? platform,
}) {
  final container = ProviderContainer(
    overrides: [
      platformServicesProvider.overrideWithValue(
        platform ??
            FakePlatformServices(
              apiBaseUrl: 'http://127.0.0.1:7842',
              token: 'hub-token',
            ),
      ),
      if (registry != null)
        daemonInstancesProvider.overrideWith((ref) async => registry),
    ],
  );
  addTearDown(container.dispose);
  return container;
}

void main() {
  setUp(() => SharedPreferences.setMockInitialValues({}));

  group('localEndpointProvider', () {
    test('describes the daemon the app manages', () {
      final endpoint = _container().read(localEndpointProvider);
      expect(endpoint.baseUrl, 'http://127.0.0.1:7842');
      expect(endpoint.isLocal, isTrue);
    });
  });

  group('instanceEndpointProvider', () {
    test('an empty id is the local daemon', () {
      final container = _container();
      expect(
        container.read(instanceEndpointProvider('')).baseUrl,
        'http://127.0.0.1:7842',
      );
    });

    test('a remote instance is routed through the hub proxy', () async {
      final container = _container(
        registry: _registry([
          {'id': 'hub-1', 'self': true},
          {'id': 'srv-a', 'name': 'Server A'},
        ]),
      );
      await container.read(daemonInstancesProvider.future);

      final endpoint = container.read(instanceEndpointProvider('srv-a'));
      expect(endpoint.baseUrl, 'http://127.0.0.1:7842/instances/srv-a/proxy');
      expect(endpoint.name, 'Server A');
    });

    test('the hub serves itself directly rather than proxying', () async {
      // Proxying to ourselves would add a network hop for no benefit.
      final container = _container(
        registry: _registry([
          {'id': 'hub-1', 'self': true},
          {'id': 'srv-a'},
        ]),
      );
      await container.read(daemonInstancesProvider.future);

      expect(
        container.read(instanceEndpointProvider('hub-1')).baseUrl,
        'http://127.0.0.1:7842',
      );
    });
  });

  test('apiClientForProvider targets the requested instance', () async {
    final container = _container(
      registry: _registry([
        {'id': 'hub-1', 'self': true},
        {'id': 'srv-a'},
      ]),
    );
    await container.read(daemonInstancesProvider.future);

    expect(container.read(apiClientForProvider('')).instanceId, '');
    expect(container.read(apiClientForProvider('srv-a')).instanceId, 'srv-a');
  });

  group('targetInstancesProvider', () {
    test('a single-daemon install has one unnamed target', () async {
      // Every aggregating provider then has exactly one source and behaves
      // identically to the pre-instances app.
      final container = _container(registry: ClusterRegistry.empty);
      await container.read(daemonInstancesProvider.future);

      final targets = container.read(targetInstancesProvider);
      expect(targets, hasLength(1));
      expect(targets.single.id, isEmpty);
    });

    test('one instance is still not multi-instance', () async {
      final container = _container(
        registry: _registry([
          {'id': 'hub-1', 'self': true},
        ]),
      );
      await container.read(daemonInstancesProvider.future);

      expect(container.read(targetInstancesProvider).single.id, isEmpty);
    });

    test('with no selection every usable instance is a target', () async {
      final container = _container(
        registry: _registry([
          {'id': 'hub-1', 'self': true},
          {'id': 'srv-a'},
          {'id': 'off', 'enabled': false},
        ]),
      );
      await container.read(daemonInstancesProvider.future);

      expect(
        container.read(targetInstancesProvider).map((i) => i.id),
        ['hub-1', 'srv-a'],
      );
    });

    test('a selection narrows to that instance', () async {
      final container = _container(
        registry: _registry([
          {'id': 'hub-1', 'self': true},
          {'id': 'srv-a'},
        ]),
      );
      await container.read(daemonInstancesProvider.future);
      await container.read(activeInstanceProvider.notifier).select('srv-a');

      expect(container.read(targetInstancesProvider).single.id, 'srv-a');
    });

    test('a stale selection falls back to everything', () async {
      // An instance that has since been removed or disabled must not leave the
      // dashboard permanently empty.
      final container = _container(
        registry: _registry([
          {'id': 'hub-1', 'self': true},
          {'id': 'srv-a'},
        ]),
      );
      await container.read(daemonInstancesProvider.future);
      await container.read(activeInstanceProvider.notifier).select('removed');

      expect(container.read(targetInstancesProvider), hasLength(2));
    });
  });

  group('activeInstanceProvider', () {
    test('persists and clears the selection', () async {
      final container = _container();
      expect(container.read(activeInstanceProvider), isNull);

      await container.read(activeInstanceProvider.notifier).select('srv-a');
      expect(container.read(activeInstanceProvider), 'srv-a');
      final prefs = await SharedPreferences.getInstance();
      expect(prefs.getString(ActiveInstanceNotifier.prefsKey), 'srv-a');

      await container.read(activeInstanceProvider.notifier).select(null);
      expect(container.read(activeInstanceProvider), isNull);
      expect(prefs.getString(ActiveInstanceNotifier.prefsKey), isNull);
    });

    test('restores a saved selection', () async {
      SharedPreferences.setMockInitialValues({
        ActiveInstanceNotifier.prefsKey: 'srv-b',
      });
      final container = _container();
      container.read(activeInstanceProvider);
      // The restore is asynchronous so the first frame is not blocked.
      await Future<void>.delayed(Duration.zero);
      expect(container.read(activeInstanceProvider), 'srv-b');
    });
  });

  group('degrades instead of failing', () {
    test('an unreachable hub yields an empty registry', () async {
      // The control-plane fetch must never break the dashboard: a hub that is
      // starting, or briefly unreachable, degrades to single-daemon behaviour.
      final container = ProviderContainer(
        overrides: [
          platformServicesProvider.overrideWithValue(
            FakePlatformServices(apiBaseUrl: 'http://127.0.0.1:1'),
          ),
        ],
      );
      addTearDown(container.dispose);

      final registry = await container.read(daemonInstancesProvider.future);
      expect(registry.instances, isEmpty);
      expect(container.read(targetInstancesProvider).single.id, isEmpty);
    });

    test('an unreachable hub yields empty routing rules', () async {
      final container = ProviderContainer(
        overrides: [
          platformServicesProvider.overrideWithValue(
            FakePlatformServices(apiBaseUrl: 'http://127.0.0.1:1'),
          ),
        ],
      );
      addTearDown(container.dispose);

      final rules = await container.read(routingRulesProvider.future);
      expect(rules.enabled, isFalse);
    });
  });

  group('localClusterRoleProvider', () {
    ApiClient fakeHub(Map<String, dynamic> healthBody, {int status = 200}) {
      return ApiClient(
        httpClient: MockClient(
          (request) async => http.Response(jsonEncode(healthBody), status),
        ),
        endpoint: DaemonEndpoint.raw(baseUrl: 'http://hub:7842', token: 't'),
      );
    }

    test('reports the role the running daemon returns', () async {
      final container = ProviderContainer(
        overrides: [
          hubApiClientProvider.overrideWithValue(
            fakeHub({'status': 'ok', 'version': '0.9.0', 'role': 'hub'}),
          ),
        ],
      );
      addTearDown(container.dispose);

      expect(await container.read(localClusterRoleProvider.future), 'hub');
      expect(container.read(localIsHubProvider), isTrue);
    });

    test('a plain standalone daemon reports an empty role, not null', () async {
      final container = ProviderContainer(
        overrides: [
          hubApiClientProvider.overrideWithValue(
            fakeHub({'status': 'ok', 'version': '0.9.0'}),
          ),
        ],
      );
      addTearDown(container.dispose);

      expect(await container.read(localClusterRoleProvider.future), '');
      expect(container.read(localIsHubProvider), isFalse);
    });

    test('an unreachable daemon is unknown, not "not a hub"', () async {
      final container = ProviderContainer(
        overrides: [
          hubApiClientProvider.overrideWithValue(
            fakeHub({}, status: 500),
          ),
        ],
      );
      addTearDown(container.dispose);

      expect(await container.read(localClusterRoleProvider.future), isNull);
      expect(container.read(localIsHubProvider), isNull);
    });
  });

  group('route helpers', () {
    test('omit the instance on a single-daemon install', () {
      expect(prDetailRoute(42, ''), '/prs/42');
      expect(issueDetailRoute(9, ''), '/issues/9');
    });

    test('carry the instance so the record is unambiguous', () {
      // Store ids are per-instance: /prs/42 alone can mean two different pull
      // requests once more than one daemon is registered.
      expect(prDetailRoute(42, 'srv-a'), '/prs/42?instance=srv-a');
      expect(issueDetailRoute(9, 'srv a'), contains('instance=srv+a'));
    });
  });
}
