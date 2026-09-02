import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart'
    show apiClientProvider;
import 'package:heimdallm/features/instances/instances_screen.dart';
import 'package:mocktail/mocktail.dart';

import '../../core/platform/fake_platform_services.dart';

class _MockApiClient extends Mock implements ApiClient {}

ClusterRegistry _registry({List<Map<String, dynamic>> instances = const []}) {
  return ClusterRegistry.fromJson({
    'role': 'hub',
    'self_id': 'hub-1',
    'self_name': 'Local hub',
    'instances': instances,
  });
}

Widget _app(Widget child) {
  return MaterialApp.router(
    routerConfig: GoRouter(
      routes: [
        GoRoute(
          path: '/',
          builder: (_, _) => child,
          routes: [
            GoRoute(
              path: 'routing',
              builder: (_, _) => const Scaffold(body: Text('Routing screen')),
            ),
          ],
        ),
      ],
    ),
  );
}

/// Whether the widget carrying an "Add instance" label/icon is enabled.
/// [InstancesScreen] uses a [FloatingActionButton]; [InstancesTabView] uses a
/// plain [TextButton] — different types, both exposing `onPressed`.
bool _addInstanceEnabled(WidgetTester tester) {
  final fab = find.byType(FloatingActionButton);
  if (fab.evaluate().isNotEmpty) {
    return tester.widget<FloatingActionButton>(fab).onPressed != null;
  }
  final button = find.ancestor(
    of: find.text('Add instance'),
    matching: find.byType(TextButton),
  );
  return tester.widget<TextButton>(button).onPressed != null;
}

void main() {
  // Both entry points must behave identically — they are the same feature
  // shown two ways, and nothing should let them silently diverge.
  for (final entry in {
    'InstancesScreen': const InstancesScreen(),
    'InstancesTabView': const InstancesTabView(),
  }.entries) {
    group(entry.key, () {
      testWidgets('not a hub: shows the enable CTA, disables hub-only actions', (
        tester,
      ) async {
        await tester.pumpWidget(
          ProviderScope(
            overrides: [
              daemonInstancesProvider.overrideWith((ref) async => _registry()),
              localClusterRoleProvider.overrideWith((ref) async => ''),
              configNotifierProvider.overrideWith(
                () => _FakeConfigNotifier(
                  const AppConfig(clusterRole: ClusterRole.standalone),
                ),
              ),
            ],
            child: _app(entry.value),
          ),
        );
        await tester.pumpAndSettle();

        expect(find.text('Enable clustering'), findsOneWidget);
        expect(find.text('Restart server'), findsNothing);
        expect(_addInstanceEnabled(tester), isFalse);
        expect(find.byTooltip('Enable hub mode first'), findsWidgets);
      });

      testWidgets('confirmed hub: no CTA, actions enabled', (tester) async {
        await tester.pumpWidget(
          ProviderScope(
            overrides: [
              daemonInstancesProvider.overrideWith((ref) async => _registry()),
              localClusterRoleProvider.overrideWith((ref) async => 'hub'),
              configNotifierProvider.overrideWith(
                () => _FakeConfigNotifier(
                  const AppConfig(clusterRole: ClusterRole.hub),
                ),
              ),
            ],
            child: _app(entry.value),
          ),
        );
        await tester.pumpAndSettle();

        expect(find.text('Enable clustering'), findsNothing);
        expect(find.text('Restart server'), findsNothing);
        expect(_addInstanceEnabled(tester), isTrue);
        expect(find.byTooltip('Routing rules'), findsOneWidget);
      });

      testWidgets('unknown (daemon unreachable): no CTA at all', (
        tester,
      ) async {
        await tester.pumpWidget(
          ProviderScope(
            overrides: [
              daemonInstancesProvider.overrideWith((ref) async => _registry()),
              localClusterRoleProvider.overrideWith((ref) async => null),
              configNotifierProvider.overrideWith(
                () => _FakeConfigNotifier(
                  const AppConfig(clusterRole: ClusterRole.standalone),
                ),
              ),
            ],
            child: _app(entry.value),
          ),
        );
        await tester.pumpAndSettle();

        expect(find.text('Enable clustering'), findsNothing);
        expect(find.text('Restart server'), findsNothing);
      });

      testWidgets(
        'saved as hub but not restarted yet: shows the restart banner',
        (tester) async {
          await tester.pumpWidget(
            ProviderScope(
              overrides: [
                daemonInstancesProvider.overrideWith(
                  (ref) async => _registry(),
                ),
                localClusterRoleProvider.overrideWith((ref) async => ''),
                configNotifierProvider.overrideWith(
                  () => _FakeConfigNotifier(
                    const AppConfig(clusterRole: ClusterRole.hub),
                  ),
                ),
              ],
              child: _app(entry.value),
            ),
          );
          await tester.pumpAndSettle();

          expect(find.text('Enable clustering'), findsNothing);
          expect(find.text('Restart server'), findsOneWidget);
          expect(find.textContaining('not active yet'), findsOneWidget);
        },
      );
    });
  }

  // Line-coverage note: these two exercise the banner's own onPressed/onRestart
  // closures, which only the merely-rendered assertions above don't reach —
  // Dart only counts a closure's body as covered once it is actually invoked.
  group('_HubSetupBanner actions', () {
    testWidgets('tapping Enable clustering drives the enable-hub flow', (
      tester,
    ) async {
      registerFallbackValue(<String, dynamic>{});
      final api = _MockApiClient();
      when(() => api.fetchConfig()).thenAnswer(
        (_) async => {
          'poll_interval': '5m',
          'ai_primary': 'claude',
          'cluster': {'role': 'standalone'},
        },
      );
      when(() => api.patchConfig(any())).thenAnswer((_) async => {});

      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
            localClusterRoleProvider.overrideWith((ref) async => ''),
            apiClientProvider.overrideWithValue(api),
            configNotifierProvider.overrideWith(ConfigNotifier.new),
            platformServicesProvider.overrideWithValue(FakePlatformServices()),
          ],
          child: _app(const InstancesTabView()),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.text('Enable clustering'));
      await tester.pumpAndSettle();

      expect(find.text('Make this daemon a cluster hub?'), findsOneWidget);
      await tester.tap(find.text('Enable and restart'));
      await tester.pumpAndSettle();

      final captured = verify(() => api.patchConfig(captureAny())).captured;
      expect(captured, isNotEmpty);
      expect(captured.last, containsPair('cluster', {'role': 'hub'}));
    });

    testWidgets(
      'tapping Restart server in the pending-restart banner restarts the daemon',
      (tester) async {
        final api = _MockApiClient();
        when(() => api.shutdownDaemon()).thenAnswer((_) async {});
        when(
          () => api.daemonReachable(),
        ).thenAnswer((_) async => PortOwner.none);
        when(() => api.daemonPort).thenReturn(7842);

        await tester.pumpWidget(
          ProviderScope(
            overrides: [
              daemonInstancesProvider.overrideWith(
                (ref) async => _registry(),
              ),
              localClusterRoleProvider.overrideWith((ref) async => ''),
              apiClientProvider.overrideWithValue(api),
              platformServicesProvider.overrideWithValue(
                FakePlatformServices(),
              ),
              configNotifierProvider.overrideWith(
                () => _FakeConfigNotifier(
                  const AppConfig(clusterRole: ClusterRole.hub),
                ),
              ),
            ],
            child: _app(const InstancesTabView()),
          ),
        );
        await tester.pumpAndSettle();

        await tester.tap(find.text('Restart server'));
        await tester.pump();
        await tester.pump(const Duration(seconds: 3));
        await tester.pumpAndSettle();

        verify(() => api.shutdownDaemon()).called(1);
      },
    );
  });
}

/// A [ConfigNotifier] stand-in that starts already resolved to [initial] and
/// never talks to a daemon — the banner only needs to read
/// `configNotifierProvider.value.clusterRole`.
class _FakeConfigNotifier extends ConfigNotifier {
  _FakeConfigNotifier(this.initial);
  final AppConfig initial;

  @override
  Future<AppConfig> build() async => initial;
}
