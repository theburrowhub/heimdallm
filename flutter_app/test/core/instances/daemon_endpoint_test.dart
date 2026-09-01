import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/daemon_endpoint.dart';

import '../platform/fake_platform_services.dart';

void main() {
  group('DaemonEndpoint.local', () {
    test('takes the URL and token from the platform', () async {
      final platform = FakePlatformServices(
        apiBaseUrl: 'http://127.0.0.1:7842',
        token: 'local-token',
      );
      final endpoint = DaemonEndpoint.local(platform);

      expect(endpoint.baseUrl, 'http://127.0.0.1:7842');
      expect(endpoint.instanceId, '');
      expect(endpoint.isLocal, isTrue);
      expect(await endpoint.loadToken(), 'local-token');
      expect(endpoint.uri('/prs').toString(), 'http://127.0.0.1:7842/prs');
    });

    test('trims a trailing slash', () {
      // A trailing slash would produce "//prs", which some proxies normalise
      // and others do not.
      final platform = FakePlatformServices(apiBaseUrl: 'http://host:7842/');
      expect(DaemonEndpoint.local(platform).baseUrl, 'http://host:7842');
    });

    test('supports the web build relative prefix', () {
      final platform = FakePlatformServices(apiBaseUrl: '/api');
      final endpoint = DaemonEndpoint.local(platform);
      expect(endpoint.uri('/prs').toString(), '/api/prs');
    });

    test('forwards token cache clearing to the platform', () {
      final platform = FakePlatformServices(token: 't');
      DaemonEndpoint.local(platform).clearTokenCache();
      expect(platform.clearApiTokenCacheCalls, greaterThan(0));
    });
  });

  group('DaemonEndpoint.viaHub', () {
    test('routes through the hub proxy and reuses the hub token', () async {
      // The app never opens a second connection to a remote daemon: that would
      // need CORS on every instance and every token in the browser.
      final platform = FakePlatformServices(
        apiBaseUrl: 'http://127.0.0.1:7842',
        token: 'hub-token',
      );
      final hub = DaemonEndpoint.local(platform);
      final remote = DaemonEndpoint.viaHub(
        hub: hub,
        instanceId: 'srv-a',
        name: 'Server A',
      );

      expect(remote.baseUrl, 'http://127.0.0.1:7842/instances/srv-a/proxy');
      expect(remote.instanceId, 'srv-a');
      expect(remote.name, 'Server A');
      expect(remote.isLocal, isFalse);
      expect(await remote.loadToken(), 'hub-token');
      expect(
        remote.uri('/prs').toString(),
        'http://127.0.0.1:7842/instances/srv-a/proxy/prs',
      );
    });

    test('works from the relative web prefix', () {
      final platform = FakePlatformServices(apiBaseUrl: '/api');
      final remote = DaemonEndpoint.viaHub(
        hub: DaemonEndpoint.local(platform),
        instanceId: 'srv-a',
      );
      expect(remote.uri('/events').toString(), '/api/instances/srv-a/proxy/events');
    });
  });

  test('raw endpoint is usable without a platform', () async {
    final endpoint = DaemonEndpoint.raw(
      baseUrl: 'http://example:9000/',
      instanceId: 'x',
      token: 'tok',
    );
    expect(endpoint.baseUrl, 'http://example:9000');
    expect(await endpoint.loadToken(), 'tok');
    expect(endpoint.toString(), contains('x'));
  });
}
