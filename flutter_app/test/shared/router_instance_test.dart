import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/features/issues/issue_detail_screen.dart';
import 'package:heimdallm/features/issues/issues_providers.dart';
import 'package:heimdallm/features/pr_detail/pr_detail_providers.dart';
import 'package:heimdallm/features/pr_detail/pr_detail_screen.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/shared/router.dart';

// Overrides are typed loosely because flutter_riverpod does not export the
// Override type for a helper to name.
Widget _routedApp(String location, List<dynamic> overrides) {
  return ProviderScope(
    overrides: overrides.cast(),
    child: MaterialApp.router(
      routerConfig: createRouter(initialLocation: location),
    ),
  );
}

void main() {
  testWidgets('the instances route is reachable', (tester) async {
    await tester.pumpWidget(
      _routedApp('/instances', [
        daemonInstancesProvider.overrideWith(
          (ref) async => throw ApiException('offline'),
        ),
      ]),
    );
    await tester.pumpAndSettle();
    expect(find.text('Instances'), findsWidgets);
  });

  testWidgets('the routing route is nested under instances', (tester) async {
    await tester.pumpWidget(
      _routedApp('/instances/routing', [
        daemonInstancesProvider.overrideWith(
          (ref) async => throw ApiException('offline'),
        ),
      ]),
    );
    await tester.pumpAndSettle();
    expect(find.text('Routing'), findsWidgets);
  });

  testWidgets('a PR route carries the instance into the provider key', (
    tester,
  ) async {
    // Store ids are per-instance, so /prs/42 alone is ambiguous once more than
    // one daemon is registered.
    PRRef? requested;
    await tester.pumpWidget(
      _routedApp('/prs/42?instance=srv-a', [
        sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        // The screen's error state is rendered instead of a real PR: this
        // test is about which key the route builds, not the detail layout.
        prDetailProvider.overrideWith((ref, key) async {
          requested = key;
          throw ApiException('not needed for this test');
        }),
      ]),
    );
    await tester.pumpAndSettle();

    expect(find.byType(PRDetailScreen), findsOneWidget);
    expect(requested?.instanceId, 'srv-a');
    expect(requested?.prId, 42);
  });

  testWidgets('a PR route without an instance means the local daemon', (
    tester,
  ) async {
    PRRef? requested;
    await tester.pumpWidget(
      _routedApp('/prs/42', [
        sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        // The screen's error state is rendered instead of a real PR: this
        // test is about which key the route builds, not the detail layout.
        prDetailProvider.overrideWith((ref, key) async {
          requested = key;
          throw ApiException('not needed for this test');
        }),
      ]),
    );
    await tester.pumpAndSettle();
    expect(requested?.instanceId, isEmpty);
  });

  testWidgets('an issue route carries the instance too', (tester) async {
    IssueRef? requested;
    await tester.pumpWidget(
      _routedApp('/issues/9?instance=srv-b', [
        sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        issueDetailProvider.overrideWith((ref, key) async {
          requested = key;
          throw ApiException('not needed for this test');
        }),
      ]),
    );
    await tester.pumpAndSettle();

    expect(find.byType(IssueDetailScreen), findsOneWidget);
    expect(requested?.instanceId, 'srv-b');
    expect(requested?.issueId, 9);
  });
}
