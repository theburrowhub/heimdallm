import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/api/cluster_api.dart';
import 'package:heimdallm/core/api/daemon_endpoint.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// Records every request so the tests can assert on method, path and body. The
/// cluster calls are extension methods and cannot be stubbed, so the fake sits
/// at the HTTP layer.
class _Recorder {
  final List<http.BaseRequest> requests = [];
  final List<String> bodies = [];
}

ApiClient _api(
  _Recorder recorder,
  Map<String, dynamic> Function(http.Request request) respond, {
  int status = 200,
}) {
  final client = MockClient((request) async {
    recorder.requests.add(request);
    recorder.bodies.add(request.body);
    return http.Response(jsonEncode(respond(request)), status);
  });
  return ApiClient(
    httpClient: client,
    endpoint: DaemonEndpoint.raw(baseUrl: 'http://hub:7842', token: 'tok'),
  );
}

Map<String, dynamic> _peerJson({
  String id = 'srv-a',
  String status = 'new',
  String baseUrl = 'http://srv-a.local:7842',
}) {
  return {
    'instance_id': id,
    'name': 'Server A',
    'role': 'worker',
    'version': '0.8.17',
    'base_url': baseUrl,
    'hostname': 'srv-a.local',
    'addresses': ['10.0.0.11'],
    'status': status,
    'seen_at': '2026-09-04T10:00:00Z',
  };
}

void main() {
  group('DiscoveredPeer.fromJson', () {
    test('reads every field', () {
      final peer = DiscoveredPeer.fromJson(_peerJson());

      expect(peer.instanceId, 'srv-a');
      expect(peer.name, 'Server A');
      expect(peer.displayName, 'Server A');
      expect(peer.role, 'worker');
      expect(peer.version, '0.8.17');
      expect(peer.baseUrl, 'http://srv-a.local:7842');
      expect(peer.hostname, 'srv-a.local');
      expect(peer.addresses, ['10.0.0.11']);
      expect(peer.status, PeerStatus.newPeer);
      expect(peer.seenAt, DateTime.utc(2026, 9, 4, 10));
    });

    test('tolerates an empty object', () {
      // A daemon on an older or newer version must not crash the list.
      final peer = DiscoveredPeer.fromJson(const {});

      expect(peer.instanceId, '');
      expect(peer.addresses, isEmpty);
      expect(peer.status, PeerStatus.newPeer);
      expect(peer.seenAt, isNull);
    });

    test('falls back to the id when the peer has no name', () {
      final peer = DiscoveredPeer.fromJson({'instance_id': 'srv-a'});
      expect(peer.displayName, 'srv-a');
    });

    test('a registered peer is not something to act on', () {
      expect(
        DiscoveredPeer.fromJson(_peerJson(status: 'registered')).isActionable,
        isFalse,
      );
      expect(
        DiscoveredPeer.fromJson(_peerJson(status: 'new')).isActionable,
        isTrue,
      );
      expect(
        DiscoveredPeer.fromJson(
          _peerJson(status: 'address_changed'),
        ).isActionable,
        isTrue,
      );
    });
  });

  group('DiscoveredPeers', () {
    test('separates what needs registering from what needs repairing', () {
      final found = DiscoveredPeers.fromJson({
        'enabled': true,
        'peers': [
          _peerJson(id: 'srv-a', status: 'new'),
          _peerJson(id: 'srv-b', status: 'registered'),
          _peerJson(id: 'srv-c', status: 'address_changed')
            ..['registered_id'] = 'srv-c'
            ..['registered_base_url'] = 'http://10.0.0.11:7842',
        ],
      });

      expect(found.enabled, isTrue);
      expect(found.unregistered.map((p) => p.instanceId), ['srv-a']);
      expect(found.moved.map((p) => p.instanceId), ['srv-c']);
      expect(found.movedFor('srv-c')?.baseUrl, 'http://srv-a.local:7842');
      expect(found.movedFor('srv-b'), isNull);
      expect(found.movedFor('nobody'), isNull);
    });

    test('discovery off is distinct from finding nothing', () {
      final off = DiscoveredPeers.fromJson({'enabled': false, 'peers': []});
      final onButEmpty = DiscoveredPeers.fromJson({
        'enabled': true,
        'peers': [],
      });

      expect(off.enabled, isFalse);
      expect(onButEmpty.enabled, isTrue);
      expect(off.peers, isEmpty);
      expect(onButEmpty.peers, isEmpty);
    });
  });

  group('ClusterApi', () {
    test('fetchDiscoveredPeers reads the hub listing', () async {
      final recorder = _Recorder();
      final api = _api(
        recorder,
        (_) => {
          'enabled': true,
          'last_scan': '2026-09-04T10:00:00Z',
          'peers': [_peerJson()],
        },
      );

      final found = await api.fetchDiscoveredPeers();

      expect(found.enabled, isTrue);
      expect(found.peers, hasLength(1));
      expect(found.lastScan, DateTime.utc(2026, 9, 4, 10));
      expect(recorder.requests.single.url.path, '/cluster/discovered');
      expect(recorder.requests.single.method, 'GET');
    });

    test('a 404 means "not a hub", not an error', () async {
      // Same contract as fetchInstances: a plain single-daemon install answers
      // 404 on the control plane and must surface nothing at all.
      final recorder = _Recorder();
      final api = _api(recorder, (_) => {}, status: 404);

      final found = await api.fetchDiscoveredPeers();
      expect(found.enabled, isFalse);
      expect(found.peers, isEmpty);
    });

    test('scanForPeers posts and returns the fresh list', () async {
      final recorder = _Recorder();
      final api = _api(
        recorder,
        (_) => {
          'enabled': true,
          'peers': [_peerJson()],
        },
      );

      final found = await api.scanForPeers();

      expect(found.peers, hasLength(1));
      expect(recorder.requests.single.method, 'POST');
      expect(recorder.requests.single.url.path, '/cluster/discovered/scan');
    });

    test('registerInstance pins the discovered identity when asked', () async {
      final recorder = _Recorder();
      final api = _api(recorder, (_) => {'id': 'srv-a'});

      await api.registerInstance(
        baseUrl: 'http://srv-a.local:7842',
        token: 'secret',
        expectInstanceId: 'srv-a',
      );

      final body = jsonDecode(recorder.bodies.single) as Map<String, dynamic>;
      expect(body['expect_instance_id'], 'srv-a');
      expect(body['base_url'], 'http://srv-a.local:7842');
    });

    test('registerInstance omits the pin when there is nothing to pin', () async {
      final recorder = _Recorder();
      final api = _api(recorder, (_) => {'id': 'srv-a'});

      await api.registerInstance(
        baseUrl: 'http://srv-a.local:7842',
        token: 'secret',
      );

      final body = jsonDecode(recorder.bodies.single) as Map<String, dynamic>;
      expect(body.containsKey('expect_instance_id'), isFalse);
    });
  });
}
