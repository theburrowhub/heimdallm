import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/api/daemon_endpoint.dart';
import 'package:heimdallm/core/api/sse_client.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/repositories/repos_screen.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

import '../../core/platform/fake_platform_services.dart';

class _FakeConfig extends ConfigNotifier {
  _FakeConfig(this.initial);
  final AppConfig initial;

  @override
  Future<AppConfig> build() async => initial;
}

class _HubRecorder {
  final List<String> requests = [];
  final List<String> bodies = [];

  /// [rules] is what GET /cluster/routing answers; [status] applies to the PUT.
  ApiClient client({int status = 200, Map<String, dynamic>? rules}) => ApiClient(
    httpClient: MockClient((request) async {
      requests.add('${request.method} ${request.url.path}');
      bodies.add(request.body);
      if (request.method == 'GET') {
        return http.Response(jsonEncode(rules ?? const {}), 200);
      }
      return http.Response(
        status >= 400 ? '{"error":"unknown instance"}' : '{}',
        status,
      );
    }),
    endpoint: DaemonEndpoint.raw(baseUrl: 'http://hub:7842', token: 't'),
  );
}

const _config = AppConfig(
  repoConfigs: {
    'acme/one': RepoConfig(prEnabled: true),
    'acme/two': RepoConfig(prEnabled: true),
  },
);

ClusterRegistry _registry() => ClusterRegistry.fromJson({
  'self_id': 'hub-1',
  'instances': [
    {'id': 'hub-1', 'name': 'Local hub', 'self': true, 'enabled': true},
    {'id': 'srv-a', 'name': 'Server A', 'enabled': true},
  ],
});

Future<void> _pump(
  WidgetTester tester,
  _HubRecorder recorder, {
  ClusterRegistry? registry,
  RoutingRules rules = const RoutingRules(),
  int status = 200,
}) async {
  tester.view.physicalSize = const Size(1400, 1600);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);

  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        platformServicesProvider.overrideWithValue(FakePlatformServices()),
        configNotifierProvider.overrideWith(() => _FakeConfig(_config)),
        sseStreamProvider.overrideWith((_) => const Stream<SseEvent>.empty()),
        hubApiClientProvider.overrideWithValue(
          recorder.client(
            status: status,
            rules: {'enabled': true, 'repos': rules.repos},
          ),
        ),
        daemonInstancesProvider.overrideWith(
          (ref) async => registry ?? ClusterRegistry.empty,
        ),
        routingRulesProvider.overrideWith((ref) async => rules),
      ],
      child: const MaterialApp(home: Scaffold(body: ReposScreen())),
    ),
  );
  await tester.pumpAndSettle();
}

/// Selects the first repository so the bulk bar appears.
Future<void> _selectFirstRepo(WidgetTester tester) async {
  await tester.tap(find.byKey(const Key('RepoListTile_checkbox')).first);
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('the routing control is absent on a single-daemon install', (
    tester,
  ) async {
    final recorder = _HubRecorder();
    await _pump(tester, recorder);
    await _selectFirstRepo(tester);

    expect(find.text('Route to instance'), findsNothing);
  });

  testWidgets('routes every selected repository in one request', (
    tester,
  ) async {
    final recorder = _HubRecorder();
    await _pump(
      tester,
      recorder,
      registry: _registry(),
      rules: const RoutingRules(repos: {'acme/untouched': 'hub-1'}),
    );
    await _selectFirstRepo(tester);

    expect(find.text('Route to instance'), findsOneWidget);
    await tester.tap(find.text('Choose…'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Server A').last);
    await tester.pumpAndSettle();

    // One PUT, not one per repo: the routing map is replaced wholesale, so a
    // per-repo loop would make each write race the previous one's state.
    expect(
      recorder.requests.where((r) => r == 'PUT /cluster/routing'),
      hasLength(1),
    );
    final body = jsonDecode(recorder.bodies.last) as Map<String, dynamic>;
    final repos = body['repos'] as Map<String, dynamic>;
    expect(repos['acme/untouched'], 'hub-1');
    expect(repos.values, contains('srv-a'));
    expect(find.textContaining('routed to srv-a'), findsOneWidget);
  });

  testWidgets('inherit removes the rules for the selection', (tester) async {
    final recorder = _HubRecorder();
    await _pump(
      tester,
      recorder,
      registry: _registry(),
      rules: const RoutingRules(repos: {'acme/one': 'srv-a', 'acme/two': 'srv-a'}),
    );
    await _selectFirstRepo(tester);

    await tester.tap(find.text('Choose…'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Inherit (default instance)'));
    await tester.pumpAndSettle();

    final body = jsonDecode(recorder.bodies.last) as Map<String, dynamic>;
    final repos = body['repos'] as Map<String, dynamic>;
    // Only the selected repo loses its rule.
    expect(repos.length, 1);
    expect(find.textContaining('inherit the default instance'), findsOneWidget);
  });

  testWidgets('a rejected write surfaces the reason', (tester) async {
    final recorder = _HubRecorder();
    await _pump(
      tester,
      recorder,
      registry: _registry(),
      status: 400,
    );
    await _selectFirstRepo(tester);

    await tester.tap(find.text('Choose…'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Server A').last);
    await tester.pumpAndSettle();

    expect(find.textContaining('unknown instance'), findsOneWidget);
  });
}
