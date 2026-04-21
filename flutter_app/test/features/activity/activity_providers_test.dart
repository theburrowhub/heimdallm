import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/models/activity.dart';
import 'package:heimdallm/features/activity/activity_providers.dart';

void main() {
  group('ActivityQueryNotifier', () {
    test('default query is today', () {
      final c = ProviderContainer();
      addTearDown(c.dispose);

      final q = c.read(activityQueryProvider);
      final today = DateTime.now();
      expect(q.date?.year,  today.year);
      expect(q.date?.month, today.month);
      expect(q.date?.day,   today.day);
      expect(q.from, isNull);
      expect(q.to,   isNull);
    });

    test('setDate clears range and keeps filters', () {
      final c = ProviderContainer();
      addTearDown(c.dispose);

      final n = c.read(activityQueryProvider.notifier);
      n.toggleOrg('acme');
      n.setDate(DateTime(2026, 4, 18));

      final q = c.read(activityQueryProvider);
      expect(q.date,  DateTime(2026, 4, 18));
      expect(q.from,  isNull);
      expect(q.to,    isNull);
      expect(q.orgs,  {'acme'});
    });

    test('setRange clears date and keeps filters', () {
      final c = ProviderContainer();
      addTearDown(c.dispose);

      final n = c.read(activityQueryProvider.notifier);
      n.toggleRepo('acme/api');
      n.setRange(DateTime(2026, 4, 18), DateTime(2026, 4, 20));

      final q = c.read(activityQueryProvider);
      expect(q.date,  isNull);
      expect(q.from,  DateTime(2026, 4, 18));
      expect(q.to,    DateTime(2026, 4, 20));
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
      expect(c.read(activityQueryProvider).actions,
          {ActivityAction.review, ActivityAction.triage});

      n.toggleAction(ActivityAction.review);
      expect(c.read(activityQueryProvider).actions, {ActivityAction.triage});
    });

    test('setRange swaps from/to when inverted', () {
      final c = ProviderContainer();
      addTearDown(c.dispose);
      final n = c.read(activityQueryProvider.notifier);

      n.setRange(DateTime(2026, 4, 20), DateTime(2026, 4, 18));

      final q = c.read(activityQueryProvider);
      expect(q.from, DateTime(2026, 4, 18));
      expect(q.to,   DateTime(2026, 4, 20));
    });

    test('clearFilters resets only filter sets', () {
      final c = ProviderContainer();
      addTearDown(c.dispose);
      final n = c.read(activityQueryProvider.notifier);

      n.setDate(DateTime(2026, 4, 18));
      n.toggleOrg('a');
      n.toggleRepo('a/b');
      n.toggleAction(ActivityAction.review);
      n.clearFilters();

      final q = c.read(activityQueryProvider);
      expect(q.date,    DateTime(2026, 4, 18));   // date preserved
      expect(q.orgs,    isEmpty);
      expect(q.repos,   isEmpty);
      expect(q.actions, isEmpty);
    });
  });
}
