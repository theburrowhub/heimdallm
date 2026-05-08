import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/api/sse_client.dart';
import 'package:heimdallm/core/models/activity.dart';
import 'package:heimdallm/features/activity/activity_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:mocktail/mocktail.dart';

class MockApiClient extends Mock implements ApiClient {}

void main() {
  setUpAll(() {
    registerFallbackValue(const ActivityQuery());
  });

  group('ActivityQueryNotifier', () {
    test('default query is today', () {
      final c = ProviderContainer();
      addTearDown(c.dispose);

      final q = c.read(activityQueryProvider);
      final today = DateTime.now();
      expect(q.date?.year, today.year);
      expect(q.date?.month, today.month);
      expect(q.date?.day, today.day);
      expect(q.from, isNull);
      expect(q.to, isNull);
    });

    test('setDate clears range and keeps filters', () {
      final c = ProviderContainer();
      addTearDown(c.dispose);

      final n = c.read(activityQueryProvider.notifier);
      n.toggleOrg('acme');
      n.setDate(DateTime(2026, 4, 18));

      final q = c.read(activityQueryProvider);
      expect(q.date, DateTime(2026, 4, 18));
      expect(q.from, isNull);
      expect(q.to, isNull);
      expect(q.orgs, {'acme'});
    });

    test('setRange clears date and keeps filters', () {
      final c = ProviderContainer();
      addTearDown(c.dispose);

      final n = c.read(activityQueryProvider.notifier);
      n.toggleRepo('acme/api');
      n.setRange(DateTime(2026, 4, 18), DateTime(2026, 4, 20));

      final q = c.read(activityQueryProvider);
      expect(q.date, isNull);
      expect(q.from, DateTime(2026, 4, 18));
      expect(q.to, DateTime(2026, 4, 20));
      expect(q.repos, {'acme/api'});
    });

    test('toggle functions add then remove', () {
      final c = ProviderContainer();
      addTearDown(c.dispose);
      final n = c.read(activityQueryProvider.notifier);

      n.toggleOrg('a');
      n.toggleOrg('b');
      expect(c.read(activityQueryProvider).orgs, {'a', 'b'});

      n.toggleOrg('a');
      expect(c.read(activityQueryProvider).orgs, {'b'});

      n.toggleAction(ActivityAction.review);
      n.toggleAction(ActivityAction.triage);
      expect(c.read(activityQueryProvider).actions, {
        ActivityAction.review,
        ActivityAction.triage,
      });

      n.toggleAction(ActivityAction.review);
      expect(c.read(activityQueryProvider).actions, {ActivityAction.triage});

      n.toggleItemType('pr');
      n.toggleOutcome('draft');
      expect(c.read(activityQueryProvider).itemTypes, {'pr'});
      expect(c.read(activityQueryProvider).outcomes, {'draft'});

      n.toggleItemType('pr');
      n.toggleOutcome('draft');
      expect(c.read(activityQueryProvider).itemTypes, isEmpty);
      expect(c.read(activityQueryProvider).outcomes, isEmpty);
    });

    test('setQuickFilter updates requested filter dimensions', () {
      final c = ProviderContainer();
      addTearDown(c.dispose);
      final n = c.read(activityQueryProvider.notifier);

      n.setQuickFilter(
        action: ActivityAction.reviewSkipped,
        itemType: 'pr',
        outcome: 'draft',
        enabled: true,
      );

      var q = c.read(activityQueryProvider);
      expect(q.actions, {ActivityAction.reviewSkipped});
      expect(q.itemTypes, {'pr'});
      expect(q.outcomes, {'draft'});

      n.setQuickFilter(
        action: ActivityAction.reviewSkipped,
        itemType: 'pr',
        outcome: 'draft',
        enabled: false,
      );

      q = c.read(activityQueryProvider);
      expect(q.actions, isEmpty);
      expect(q.itemTypes, isEmpty);
      expect(q.outcomes, isEmpty);
    });

    test('setRange swaps from/to when inverted', () {
      final c = ProviderContainer();
      addTearDown(c.dispose);
      final n = c.read(activityQueryProvider.notifier);

      n.setRange(DateTime(2026, 4, 20), DateTime(2026, 4, 18));

      final q = c.read(activityQueryProvider);
      expect(q.from, DateTime(2026, 4, 18));
      expect(q.to, DateTime(2026, 4, 20));
    });

    test('clearFilters resets only filter sets', () {
      final c = ProviderContainer();
      addTearDown(c.dispose);
      final n = c.read(activityQueryProvider.notifier);

      n.setDate(DateTime(2026, 4, 18));
      n.toggleOrg('a');
      n.toggleRepo('a/b');
      n.toggleItemType('pr');
      n.toggleAction(ActivityAction.review);
      n.toggleOutcome('draft');
      n.clearFilters();

      final q = c.read(activityQueryProvider);
      expect(q.date, DateTime(2026, 4, 18)); // date preserved
      expect(q.orgs, isEmpty);
      expect(q.repos, isEmpty);
      expect(q.itemTypes, isEmpty);
      expect(q.actions, isEmpty);
      expect(q.outcomes, isEmpty);
    });
  });

  group('activityOptionsProvider', () {
    test('uses a daemon-compatible limit for option discovery', () async {
      final api = MockApiClient();
      ActivityQuery? captured;
      when(() => api.fetchActivity(any())).thenAnswer((invocation) async {
        captured = invocation.positionalArguments.single as ActivityQuery;
        return const ActivityPage(entries: [], truncated: false, count: 0);
      });
      final c = ProviderContainer(
        overrides: [apiClientProvider.overrideWithValue(api)],
      );
      addTearDown(c.dispose);

      await c.read(activityOptionsProvider.future);

      expect(captured?.limit, 5000);
    });
  });

  group('activityLiveRefreshProvider', () {
    test('refreshes persisted activity when refinement completes', () async {
      final api = MockApiClient();
      var calls = 0;
      when(() => api.fetchActivity(any())).thenAnswer((_) async {
        calls++;
        return const ActivityPage(entries: [], truncated: false, count: 0);
      });
      final events = StreamController<SseEvent>.broadcast();
      final c = ProviderContainer(
        overrides: [
          apiClientProvider.overrideWithValue(api),
          sseStreamProvider.overrideWith((ref) => events.stream),
        ],
      );
      addTearDown(() async {
        await events.close();
        c.dispose();
      });

      c.read(activityLiveUpdatesProvider.notifier).state = true;
      c.read(activityLiveRefreshProvider);
      await c.read(activityEntriesProvider.future);
      expect(calls, 1);

      events.add(
        const SseEvent(
          type: 'issue_refinement_done',
          data: '{"repo":"acme/api","issue_number":12}',
        ),
      );
      await _waitFor(() => calls > 1);

      expect(calls, greaterThan(1));
    });
  });
}

Future<void> _waitFor(bool Function() condition) async {
  final deadline = DateTime.now().add(const Duration(seconds: 2));
  while (DateTime.now().isBefore(deadline)) {
    if (condition()) return;
    await Future<void>.delayed(const Duration(milliseconds: 10));
  }
  fail('condition was not met before timeout');
}
