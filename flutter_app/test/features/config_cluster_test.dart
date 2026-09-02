import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart' show ClusterRegistry;
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart'
    show ConfigNotifier, configNotifierProvider;
import 'package:heimdallm/features/config/config_screen.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:mocktail/mocktail.dart';

import '../core/platform/fake_platform_services.dart';

class _MockApiClient extends Mock implements ApiClient {}

/// Mounts the config screen with the daemon reporting [savedRole], and the
/// running process reporting [liveRole] via /health ([localClusterRoleProvider]
/// overridden directly rather than faking HTTP — the section only cares about
/// the resolved string, not how it got there).
Future<_MockApiClient> _mount(
  WidgetTester tester, {
  required String savedRole,
  required String? liveRole,
  List<dynamic> extraOverrides = const [],
}) async {
  const config = AppConfig(pollInterval: '5m', aiPrimary: 'claude');
  final json = {
    ...config.toJson(),
    'cluster': {'role': savedRole},
  };

  final mockApi = _MockApiClient();
  when(() => mockApi.fetchConfig()).thenAnswer((_) async => json);
  when(() => mockApi.updateConfig(any())).thenAnswer((_) async {});
  when(() => mockApi.patchConfig(any())).thenAnswer((_) async => json);
  when(
    () => mockApi.daemonReachable(),
  ).thenAnswer((_) async => PortOwner.daemon);

  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        apiClientProvider.overrideWithValue(mockApi),
        configNotifierProvider.overrideWith(ConfigNotifier.new),
        platformServicesProvider.overrideWithValue(FakePlatformServices()),
        localClusterRoleProvider.overrideWith((ref) async => liveRole),
        ...extraOverrides,
      ],
      child: MaterialApp.router(
        routerConfig: GoRouter(
          routes: [GoRoute(path: '/', builder: (_, _) => const ConfigScreen())],
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return mockApi;
}

Future<void> _reveal(WidgetTester tester, Finder finder) async {
  final scrollable = find.byType(Scrollable).first;
  await tester.scrollUntilVisible(finder, 200, scrollable: scrollable);
  await tester.pumpAndSettle();
  await tester.drag(scrollable, const Offset(0, 120));
  await tester.pumpAndSettle();
}

void main() {
  setUpAll(() => registerFallbackValue(<String, dynamic>{}));

  testWidgets('the dropdown reflects the loaded role', (tester) async {
    await _mount(tester, savedRole: 'hub', liveRole: 'hub');
    await _reveal(tester, find.text('Cluster'));

    expect(
      find.descendant(
        of: find.byType(DropdownButtonFormField<String>),
        matching: find.text('hub'),
      ),
      findsOneWidget,
    );
  });

  testWidgets('saving patches cluster.role', (tester) async {
    final api = await _mount(
      tester,
      savedRole: 'standalone',
      liveRole: 'standalone',
    );
    await _reveal(tester, find.text('Role'));

    await tester.tap(find.byType(DropdownButtonFormField<String>).last);
    await tester.pumpAndSettle();
    await tester.tap(find.text('hub').hitTestable().last);
    await tester.pumpAndSettle();

    final save = find.widgetWithText(ElevatedButton, 'Save');
    await tester.ensureVisible(save);
    await tester.pumpAndSettle();
    await tester.tap(save);
    await tester.pumpAndSettle();

    final captured = verify(() => api.patchConfig(captureAny())).captured;
    expect(captured, isNotEmpty);
    final patch = captured.last as Map<String, dynamic>;
    expect(patch['cluster'], {'role': 'hub'});
  });

  testWidgets(
    'shows the restart banner when the saved role has not taken effect',
    (tester) async {
      await _mount(tester, savedRole: 'hub', liveRole: 'standalone');
      await _reveal(tester, find.text('Cluster'));

      expect(find.textContaining('saved as "hub"'), findsOneWidget);
      expect(find.text('Restart server'), findsOneWidget);
    },
  );

  testWidgets('shows no banner once saved and live roles agree', (
    tester,
  ) async {
    await _mount(tester, savedRole: 'hub', liveRole: 'hub');
    await _reveal(tester, find.text('Cluster'));

    expect(find.text('Restart server'), findsNothing);
  });

  // An unreachable/unresolved live role must not be mistaken for a mismatch —
  // that would show "restart required" to someone whose daemon is simply
  // still starting up.
  testWidgets('shows no banner when the live role is unknown', (
    tester,
  ) async {
    await _mount(tester, savedRole: 'hub', liveRole: null);
    await _reveal(tester, find.text('Cluster'));

    expect(find.text('Restart server'), findsNothing);
  });

  testWidgets(
    'hides the section entirely when a remote instance is selected',
    (tester) async {
      await _mount(
        tester,
        savedRole: 'hub',
        liveRole: 'hub',
        extraOverrides: [
          activeInstanceProvider.overrideWith(_FixedActiveInstance.new),
        ],
      );
      // A role dropdown scoped to a remote daemon this app cannot restart
      // from here would be a trap — the whole card must be gone, not just
      // its restart affordance.
      expect(find.text('Cluster'), findsNothing);
    },
  );

  testWidgets(
    'tapping Restart server in the Cluster section restarts the daemon',
    (tester) async {
      final api = await _mount(
        tester,
        savedRole: 'hub',
        liveRole: 'standalone',
      );
      when(() => api.shutdownDaemon()).thenAnswer((_) async {});
      when(
        () => api.daemonReachable(),
      ).thenAnswer((_) async => PortOwner.none);
      when(() => api.daemonPort).thenReturn(7842);
      await _reveal(tester, find.text('Restart server'));

      await tester.tap(find.text('Restart server'));
      await tester.pump();
      await tester.pump(const Duration(seconds: 3));
      await tester.pumpAndSettle();

      verify(() => api.shutdownDaemon()).called(1);
    },
  );

  ClusterRegistry twoInstanceRegistry() => ClusterRegistry.fromJson({
    'self_id': 'hub-1',
    'instances': [
      {'id': 'hub-1', 'self': true},
      {'id': 'srv-a'},
    ],
  });

  testWidgets(
    'demoting a hub with registered instances asks for confirmation, '
    'and cancelling leaves the role unchanged',
    (tester) async {
      final api = await _mount(
        tester,
        savedRole: 'hub',
        liveRole: 'hub',
        extraOverrides: [
          daemonInstancesProvider.overrideWith(
            (ref) async => twoInstanceRegistry(),
          ),
        ],
      );
      await _reveal(tester, find.text('Role'));

      await tester.tap(find.byType(DropdownButtonFormField<String>).last);
      await tester.pumpAndSettle();
      await tester.tap(find.text('standalone').hitTestable().last);
      await tester.pumpAndSettle();

      expect(find.text('Stop being a hub?'), findsOneWidget);
      expect(find.textContaining('manages 2 instances'), findsOneWidget);

      await tester.tap(find.text('Cancel'));
      await tester.pumpAndSettle();

      // Assert on the outcome that actually matters (what gets saved), not on
      // the dropdown's own rendering — DropdownButtonFormField is known to
      // not always reflect a `value` that reverts asynchronously after the
      // widget's own selection handling has already run.
      final save = find.widgetWithText(ElevatedButton, 'Save');
      await tester.ensureVisible(save);
      await tester.pumpAndSettle();
      await tester.tap(save);
      await tester.pumpAndSettle();

      final calls = verify(
        () => api.patchConfig(captureAny()),
      ).captured;
      for (final c in calls) {
        final patch = c as Map<String, dynamic>;
        expect(
          patch['cluster'],
          isNot({'role': 'standalone'}),
          reason: 'cancelling must not persist the demotion',
        );
      }
    },
  );

  testWidgets(
    'confirming the demotion persists the new role',
    (tester) async {
      final api = await _mount(
        tester,
        savedRole: 'hub',
        liveRole: 'hub',
        extraOverrides: [
          daemonInstancesProvider.overrideWith(
            (ref) async => twoInstanceRegistry(),
          ),
        ],
      );
      await _reveal(tester, find.text('Role'));

      await tester.tap(find.byType(DropdownButtonFormField<String>).last);
      await tester.pumpAndSettle();
      await tester.tap(find.text('standalone').hitTestable().last);
      await tester.pumpAndSettle();

      await tester.tap(find.text('Change role'));
      await tester.pumpAndSettle();

      final save = find.widgetWithText(ElevatedButton, 'Save');
      await tester.ensureVisible(save);
      await tester.pumpAndSettle();
      await tester.tap(save);
      await tester.pumpAndSettle();

      final captured = verify(() => api.patchConfig(captureAny())).captured;
      expect(captured, isNotEmpty);
      expect(captured.last, containsPair('cluster', {'role': 'standalone'}));
    },
  );
}

/// Always resolves to a fixed remote instance id, bypassing shared-preferences
/// restoration.
class _FixedActiveInstance extends ActiveInstanceNotifier {
  @override
  String? build() => 'srv-a';
}
