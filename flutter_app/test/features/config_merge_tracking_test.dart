import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart'
    show ConfigNotifier, configNotifierProvider;
import 'package:heimdallm/features/config/config_screen.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:mocktail/mocktail.dart';

import '../core/platform/fake_platform_services.dart';

class _MockApiClient extends Mock implements ApiClient {}

/// Mounts the config screen over a config whose merge-tracking section is in
/// the given state, and scrolls the section into view.
Future<_MockApiClient> _mount(
  WidgetTester tester,
  MergeTrackingConfig mergeTracking,
) async {
  const config = AppConfig(
    pollInterval: '5m',
    aiPrimary: 'claude',
    repoConfigs: {'org/repo': RepoConfig(prEnabled: true)},
  );
  // AppConfig.toJson is a partial serialiser and does not carry the section,
  // so the fixture supplies it the way the daemon does.
  final json = {...config.toJson(), 'merge_tracking': mergeTracking.toJson()};

  final mockApi = _MockApiClient();
  when(() => mockApi.fetchConfig()).thenAnswer((_) async => json);
  when(() => mockApi.updateConfig(any())).thenAnswer((_) async {});
  when(() => mockApi.patchConfig(any())).thenAnswer((_) async => json);
  // A reachable daemon puts the plain Save button on screen rather than the
  // "Save and start" bootstrap variant.
  when(() => mockApi.daemonReachable()).thenAnswer((_) async => PortOwner.daemon);

  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        apiClientProvider.overrideWithValue(mockApi),
        configNotifierProvider.overrideWith(ConfigNotifier.new),
        platformServicesProvider.overrideWithValue(FakePlatformServices()),
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
  // scrollUntilVisible stops as soon as the target is on screen, which can
  // leave it under the header. Nudge it clear so taps land on it.
  await tester.drag(scrollable, const Offset(0, 120));
  await tester.pumpAndSettle();
}

void main() {
  setUpAll(() => registerFallbackValue(<String, dynamic>{}));

  // With tracking off, the card shows one switch and nothing else: the
  // automations are consequences of tracking, not independent settings.
  testWidgets('the card collapses to its master switch when tracking is off', (
    tester,
  ) async {
    await _mount(tester, const MergeTrackingConfig());
    await _reveal(tester, find.text('Merge Tracking'));

    expect(find.text('Track my pull requests'), findsOneWidget);
    expect(find.text('Turn on auto-merge'), findsNothing);
    expect(find.text('Include PRs assigned to me'), findsNothing);
  });

  testWidgets('turning tracking on reveals the four automations', (
    tester,
  ) async {
    await _mount(tester, const MergeTrackingConfig());
    await _reveal(tester, find.text('Track my pull requests'));

    await tester.tap(find.byType(SwitchListTile).last);
    await tester.pumpAndSettle();

    await _reveal(tester, find.text('Automations'));
    expect(find.text('Include PRs assigned to me'), findsOneWidget);
    expect(find.text('Turn on auto-merge'), findsOneWidget);
  });

  // Every automation writes back to the notifier, and the conflict switch also
  // reveals the two fields that only make sense once an agent can run.
  testWidgets('the conflict automation reveals its timeout and effort', (
    tester,
  ) async {
    await _mount(
      tester,
      const MergeTrackingConfig(enabled: true, resolveConflicts: true),
    );
    await _reveal(tester, find.text('Conflict-resolution timeout'));

    expect(find.text('Conflict-resolution timeout'), findsOneWidget);
    expect(find.text('Conflict-resolution effort'), findsOneWidget);

    await tester.enterText(
      find.widgetWithText(TextFormField, 'Conflict-resolution timeout'),
      '45m',
    );
    await tester.pumpAndSettle();
    expect(find.text('45m'), findsOneWidget);
  });

  testWidgets('the timeout and effort stay hidden without the automation', (
    tester,
  ) async {
    await _mount(tester, const MergeTrackingConfig(enabled: true));
    await _reveal(tester, find.text('Merge method'));

    expect(find.text('Conflict-resolution timeout'), findsNothing);
    expect(find.text('Conflict-resolution effort'), findsNothing);
  });

  testWidgets('the check interval is editable', (tester) async {
    await _mount(tester, const MergeTrackingConfig(enabled: true, merge: true));
    await _reveal(tester, find.text('Check interval'));

    await tester.enterText(
      find.widgetWithText(TextFormField, 'Check interval'),
      '2m',
    );
    await tester.pumpAndSettle();
    expect(find.text('2m'), findsOneWidget);
  });

  // The method has to be one the repository allows, so it is a closed list
  // rather than a text field.
  testWidgets('the merge method offers exactly the three GitHub methods', (
    tester,
  ) async {
    await _mount(tester, const MergeTrackingConfig(enabled: true, merge: true));
    await _reveal(tester, find.text('Must be enabled on the repository'));

    await tester.tap(
      find.ancestor(
        of: find.text('Must be enabled on the repository'),
        matching: find.byType(DropdownButtonFormField<String>),
      ),
    );
    await tester.pumpAndSettle();
    for (final method in ['squash', 'merge', 'rebase']) {
      expect(find.text(method), findsWidgets, reason: method);
    }
    await tester.tap(find.text('rebase').hitTestable().last);
    await tester.pumpAndSettle();
  });

  // Saving is what turns the screen's state into a PATCH; the section is
  // worthless if its toggles never reach the daemon.
  testWidgets('the section reaches the daemon on save', (tester) async {
    final api = await _mount(tester, const MergeTrackingConfig());
    await _reveal(tester, find.text('Track my pull requests'));

    await tester.tap(find.byType(SwitchListTile).last);
    await tester.pumpAndSettle();

    final save = find.widgetWithText(ElevatedButton, 'Save');
    await tester.ensureVisible(save);
    await tester.pumpAndSettle();
    await tester.tap(save);
    await tester.pumpAndSettle();

    // Only what changed is sent, so the patch is the proof that the section is
    // wired to the save path at all.
    final captured = verify(() => api.patchConfig(captureAny())).captured;
    expect(captured, isNotEmpty);
    final patch = captured.last as Map<String, dynamic>;
    expect(
      patch['merge_tracking'],
      isA<Map<String, dynamic>>().having(
        (m) => m['enabled'],
        'enabled',
        isTrue,
      ),
    );
  });
}
