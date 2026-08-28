import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/merge_tracking.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/merge_tracking/merge_tracking_providers.dart';
import 'package:heimdallm/features/merge_tracking/merge_tracking_screen.dart';
import 'package:mocktail/mocktail.dart';

class _MockApiClient extends Mock implements ApiClient {}

const _entry = MergeTrackingEntry(
  prId: 1,
  repo: 'acme/widgets',
  number: 7,
  phase: 'idle',
);

Widget _host(ApiClient api, {List<MergeTrackingEntry> entries = const []}) =>
    ProviderScope(
      overrides: [
        mergeTrackingProvider.overrideWith((ref) async => entries),
        mergeTrackingSseListenerProvider.overrideWithValue(null),
        apiClientProvider.overrideWithValue(api),
      ],
      child: const MaterialApp(home: Scaffold(body: MergeTrackingScreen())),
    );

void main() {
  // The Activity tab's Add PR routes through the review pipeline, which refuses
  // a PR the authenticated account authored — and Heimdallm authenticates as
  // the operator, so that is every PR they open. Merge tracking exists for
  // exactly those PRs, so the tab needs a door of its own.
  testWidgets('the Merge tab offers its own way to add a PR', (tester) async {
    await tester.pumpWidget(_host(_MockApiClient()));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('track-pr-button')), findsOneWidget);
    // Present with an empty listing too: that is exactly when it is needed.
    expect(find.text('No pull requests tracked yet'), findsOneWidget);
  });

  testWidgets('adding a PR calls the merge-tracking endpoint, not the review one', (
    tester,
  ) async {
    final api = _MockApiClient();
    when(() => api.addMergeTracking(any())).thenAnswer((_) async => _entry);

    await tester.pumpWidget(_host(api));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('track-pr-button')));
    await tester.pumpAndSettle();

    await tester.enterText(
      find.byKey(const Key('add-merge-pr-url-field')),
      'https://github.com/acme/widgets/pull/7',
    );
    await tester.tap(find.text('Track'));
    await tester.pumpAndSettle();

    verify(
      () => api.addMergeTracking('https://github.com/acme/widgets/pull/7'),
    ).called(1);
    // The review path must not be touched: it is what rejects these PRs.
    verifyNever(() => api.addPRByUrl(any()));
  });

  testWidgets('a link that is not a PR is refused before any request', (
    tester,
  ) async {
    final api = _MockApiClient();
    await tester.pumpWidget(_host(api));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('track-pr-button')));
    await tester.pumpAndSettle();

    await tester.enterText(
      find.byKey(const Key('add-merge-pr-url-field')),
      'https://github.com/acme/widgets/issues/7',
    );
    await tester.tap(find.text('Track'));
    await tester.pumpAndSettle();

    expect(find.textContaining('Enter a GitHub PR link'), findsOneWidget);
    verifyNever(() => api.addMergeTracking(any()));
  });

  // The daemon refuses a repo with merge tracking off, and says so in prose.
  // That sentence is the whole point of the error line.
  testWidgets('the daemon\'s refusal is shown verbatim', (tester) async {
    final api = _MockApiClient();
    when(() => api.addMergeTracking(any())).thenThrow(
      ApiException('merge tracking is disabled for acme/widgets'),
    );

    await tester.pumpWidget(_host(api));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('track-pr-button')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const Key('add-merge-pr-url-field')),
      'https://github.com/acme/widgets/pull/7',
    );
    await tester.tap(find.text('Track'));
    await tester.pumpAndSettle();

    expect(
      find.text('merge tracking is disabled for acme/widgets'),
      findsOneWidget,
    );
  });
}
