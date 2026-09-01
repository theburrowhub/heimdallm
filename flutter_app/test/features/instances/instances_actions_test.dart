import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/api/daemon_endpoint.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/features/instances/instances_screen.dart';
import 'package:heimdallm/features/instances/routing_screen.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:mocktail/mocktail.dart';

class _MockApiClient extends Mock implements ApiClient {}

/// Records what the screens send to the hub. The cluster calls are extension
/// methods and cannot be stubbed, so the fake sits at the HTTP layer.
class _HubRecorder {
  final List<String> requests = [];
  final List<String> bodies = [];

  ApiClient client({
    Map<String, dynamic> Function(http.Request request)? respond,
    int status = 200,
  }) {
    return ApiClient(
      httpClient: MockClient((request) async {
        requests.add('${request.method} ${request.url.path}');
        bodies.add(request.body);
        return http.Response(
          jsonEncode(respond?.call(request) ?? const <String, dynamic>{}),
          status,
        );
      }),
      endpoint: DaemonEndpoint.raw(baseUrl: 'http://hub:7842', token: 't'),
    );
  }
}

Widget _app(Widget child) => MaterialApp.router(
  routerConfig: GoRouter(
    routes: [GoRoute(path: '/', builder: (_, _) => child)],
  ),
);

ClusterRegistry _registry() => ClusterRegistry.fromJson({
  'self_id': 'hub-1',
  'instances': [
    {'id': 'hub-1', 'name': 'Local hub', 'self': true, 'enabled': true},
    {'id': 'srv-a', 'name': 'Server A', 'enabled': true},
  ],
});

void main() {
  group('instance actions', () {
    testWidgets('probe now reports the result', (tester) async {
      final recorder = _HubRecorder();
      final api = recorder.client(
        respond: (_) => {'reachable': true, 'version': '0.9.0'},
      );

      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            hubApiClientProvider.overrideWithValue(api),
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
          ],
          child: _app(const InstancesScreen()),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.more_vert).last);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Probe now'));
      await tester.pumpAndSettle();

      expect(recorder.requests, contains('POST /instances/srv-a/probe'));
      expect(find.textContaining('is reachable'), findsOneWidget);
    });

    testWidgets('a failed probe shows the reason', (tester) async {
      final recorder = _HubRecorder();
      final api = recorder.client(
        respond: (_) => {'reachable': false, 'last_error': 'connection refused'},
      );

      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            hubApiClientProvider.overrideWithValue(api),
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
          ],
          child: _app(const InstancesScreen()),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.more_vert).last);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Probe now'));
      await tester.pumpAndSettle();

      expect(find.textContaining('connection refused'), findsOneWidget);
    });

    testWidgets('disable sends the toggle', (tester) async {
      final recorder = _HubRecorder();
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            hubApiClientProvider.overrideWithValue(recorder.client()),
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
          ],
          child: _app(const InstancesScreen()),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.more_vert).last);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Disable'));
      await tester.pumpAndSettle();

      expect(recorder.requests, contains('PATCH /instances/srv-a'));
      expect(recorder.bodies.last, contains('"enabled":false'));
    });

    testWidgets('removal asks first and explains the consequence', (
      tester,
    ) async {
      final recorder = _HubRecorder();
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            hubApiClientProvider.overrideWithValue(recorder.client()),
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
          ],
          child: _app(const InstancesScreen()),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.more_vert).last);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Remove…'));
      await tester.pumpAndSettle();

      expect(find.text('Remove Server A?'), findsOneWidget);
      expect(find.textContaining('keeps running'), findsOneWidget);
      expect(find.textContaining('is unrouted'), findsOneWidget);

      // Cancelling must not send anything.
      await tester.tap(find.text('Cancel'));
      await tester.pumpAndSettle();
      expect(recorder.requests, isEmpty);
    });

    testWidgets('confirmed removal deregisters the instance', (tester) async {
      final recorder = _HubRecorder();
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            hubApiClientProvider.overrideWithValue(recorder.client()),
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
          ],
          child: _app(const InstancesScreen()),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.more_vert).last);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Remove…'));
      await tester.pumpAndSettle();
      await tester.tap(find.widgetWithText(FilledButton, 'Remove'));
      await tester.pumpAndSettle();

      expect(recorder.requests, contains('DELETE /instances/srv-a'));
      expect(find.textContaining('Removed Server A'), findsOneWidget);
    });

    testWidgets('an action failure surfaces the daemon message', (tester) async {
      final recorder = _HubRecorder();
      final api = recorder.client(
        status: 409,
        respond: (_) => {'error': 'the hub cannot deregister itself'},
      );

      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            hubApiClientProvider.overrideWithValue(api),
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
          ],
          child: _app(const InstancesScreen()),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.more_vert).last);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Disable'));
      await tester.pumpAndSettle();

      expect(find.textContaining('cannot deregister itself'), findsOneWidget);
    });

    testWidgets('a failed registry load is reported', (tester) async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith(
              (ref) async => throw ApiException('hub is unreachable'),
            ),
          ],
          child: _app(const InstancesScreen()),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.textContaining('Could not load instances'), findsOneWidget);
    });
  });

  group('routing actions', () {
    const configJson = <String, dynamic>{
      'repositories': ['acme/tools'],
    };

    Future<void> pumpRouting(
      WidgetTester tester,
      _HubRecorder recorder, {
      RoutingRules rules = const RoutingRules(),
    }) async {
      tester.view.physicalSize = const Size(1200, 2000);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);

      final configApi = _MockApiClient();
      when(configApi.fetchConfig).thenAnswer((_) async => configJson);

      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            apiClientProvider.overrideWithValue(configApi),
            hubApiClientProvider.overrideWithValue(recorder.client()),
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
            routingRulesProvider.overrideWith((ref) async => rules),
            configNotifierProvider.overrideWith(ConfigNotifier.new),
          ],
          child: _app(const RoutingScreen()),
        ),
      );
      await tester.pumpAndSettle();
    }

    testWidgets('switching mode is persisted', (tester) async {
      final recorder = _HubRecorder();
      await pumpRouting(tester, recorder);

      await tester.tap(find.text('Dispatch'));
      await tester.pumpAndSettle();

      expect(recorder.requests, contains('PUT /cluster/routing'));
      expect(recorder.bodies.last, contains('"mode":"dispatch"'));
    });

    testWidgets('deselecting an op expands the implicit "all"', (tester) async {
      // An empty list means every operation, so the first deselection has to be
      // written out as the explicit full set minus that one.
      final recorder = _HubRecorder();
      await pumpRouting(
        tester,
        recorder,
        rules: const RoutingRules(mode: RoutingMode.dispatch),
      );

      await tester.tap(find.widgetWithText(FilterChip, RoutingOp.merge));
      await tester.pumpAndSettle();

      final body = jsonDecode(recorder.bodies.last) as Map<String, dynamic>;
      expect(body['round_robin_ops'], containsAll([RoutingOp.review, RoutingOp.issue]));
      expect(body['round_robin_ops'], isNot(contains(RoutingOp.merge)));
    });

    testWidgets('deselecting a pool member expands the implicit "all"', (
      tester,
    ) async {
      final recorder = _HubRecorder();
      await pumpRouting(tester, recorder);

      await tester.tap(find.widgetWithText(FilterChip, 'Server A'));
      await tester.pumpAndSettle();

      final body = jsonDecode(recorder.bodies.last) as Map<String, dynamic>;
      expect(body['round_robin_pool'], ['hub-1']);
    });

    testWidgets('assigning a repository sends the whole map', (tester) async {
      final recorder = _HubRecorder();
      await pumpRouting(
        tester,
        recorder,
        rules: const RoutingRules(repos: {'acme/other': 'hub-1'}),
      );

      // The repository dropdown is the last one on the screen.
      await tester.tap(find.byType(DropdownButtonFormField<String>).last);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Server A').last);
      await tester.pumpAndSettle();

      final body = jsonDecode(recorder.bodies.last) as Map<String, dynamic>;
      // PUT replaces the map wholesale, so an untouched rule must be resent.
      expect(body['repos'], containsPair('acme/other', 'hub-1'));
      expect(body['repos'], containsPair('acme/tools', 'srv-a'));
    });

    testWidgets('a rejected write surfaces the daemon message', (tester) async {
      final recorder = _HubRecorder();
      final configApi = _MockApiClient();
      when(configApi.fetchConfig).thenAnswer((_) async => configJson);

      tester.view.physicalSize = const Size(1200, 2000);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);

      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            apiClientProvider.overrideWithValue(configApi),
            hubApiClientProvider.overrideWithValue(
              recorder.client(
                status: 400,
                respond: (_) => {'error': 'unknown instance "ghost"'},
              ),
            ),
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
            routingRulesProvider.overrideWith(
              (ref) async => const RoutingRules(),
            ),
            configNotifierProvider.overrideWith(ConfigNotifier.new),
          ],
          child: _app(const RoutingScreen()),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.text('Dispatch'));
      await tester.pumpAndSettle();

      expect(find.textContaining('unknown instance'), findsOneWidget);
    });

    testWidgets('a failed routing load is reported', (tester) async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
            routingRulesProvider.overrideWith(
              (ref) async => throw ApiException('hub is unreachable'),
            ),
          ],
          child: _app(const RoutingScreen()),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.textContaining('Could not load routing'), findsOneWidget);
    });
  });
}
