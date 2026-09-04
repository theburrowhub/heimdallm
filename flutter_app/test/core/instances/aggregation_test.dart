import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/api/daemon_endpoint.dart';
import 'package:heimdallm/core/instances/aggregation.dart';
import 'package:heimdallm/core/instances/models.dart';

DaemonInstance _instance(String id, {String name = ''}) =>
    DaemonInstance(id: id, name: name, baseUrl: 'http://$id:7842');

ApiClient _client(String id) =>
    ApiClient(endpoint: DaemonEndpoint.raw(baseUrl: 'http://$id', instanceId: id));

void main() {
  group('aggregate', () {
    test('merges results and tags each item with its instance', () async {
      final result = await aggregate<String>(
        targets: [_instance('a', name: 'Alpha'), _instance('b', name: 'Bravo')],
        clientFor: _client,
        fetch: (client) async => ['${client.instanceId}-1', '${client.instanceId}-2'],
      );

      expect(result.values, ['a-1', 'a-2', 'b-1', 'b-2']);
      expect(result.items.first.instanceId, 'a');
      expect(result.items.first.instanceName, 'Alpha');
      expect(result.items.last.instanceName, 'Bravo');
      expect(result.hasFailures, isFalse);
    });

    test('orders by instance, not by whichever answered first', () async {
      // A merged list that reshuffles between refreshes because one machine is
      // faster today is unusable.
      final result = await aggregate<String>(
        targets: [_instance('slow'), _instance('fast')],
        clientFor: _client,
        fetch: (client) async {
          if (client.instanceId == 'slow') {
            await Future<void>.delayed(const Duration(milliseconds: 30));
          }
          return [client.instanceId];
        },
      );
      expect(result.values, ['slow', 'fast']);
    });

    test('one instance failing still returns the others', () async {
      // Partial data plus an explicit list of what could not be reached beats
      // an error page: a dashboard that silently drops a machine's PRs looks
      // identical to that machine having no work.
      final result = await aggregate<String>(
        targets: [_instance('ok'), _instance('bad', name: 'Bad box')],
        clientFor: _client,
        fetch: (client) async {
          if (client.instanceId == 'bad') throw ApiException('boom');
          return ['value'];
        },
      );

      expect(result.values, ['value']);
      expect(result.hasFailures, isTrue);
      expect(result.failures.single.instanceId, 'bad');
      expect(result.failures.single.label, 'Bad box');
      expect('${result.failures.single.error}', contains('boom'));
    });

    test('every instance failing yields no items and all failures', () async {
      final result = await aggregate<String>(
        targets: [_instance('a'), _instance('b')],
        clientFor: _client,
        fetch: (_) async => throw ApiException('down'),
      );
      expect(result.items, isEmpty);
      expect(result.failures, hasLength(2));
    });

    test('no targets short-circuits', () async {
      var called = false;
      final result = await aggregate<String>(
        targets: const [],
        clientFor: _client,
        fetch: (_) async {
          called = true;
          return const [];
        },
      );
      expect(result.items, isEmpty);
      expect(called, isFalse);
    });
  });

  test('aggregateOne wraps a single-object endpoint', () async {
    final result = await aggregateOne<int>(
      targets: [_instance('a'), _instance('b')],
      clientFor: _client,
      fetch: (client) async => client.instanceId.length,
    );
    expect(result.values, [1, 1]);
    expect(result.items.map((e) => e.instanceId), ['a', 'b']);
  });

  test('singleInstanceResult wraps values without provenance', () {
    final result = singleInstanceResult(['x', 'y']);
    expect(result.values, ['x', 'y']);
    expect(result.items.every((e) => e.instanceId.isEmpty), isTrue);
    // Empty label means the UI renders no badge, which is what a
    // single-daemon install should look like.
    expect(result.items.first.label, isEmpty);
  });

  test('InstanceScoped.map preserves provenance', () {
    const scoped = InstanceScoped<int>(
      instanceId: 'a',
      instanceName: 'Alpha',
      value: 2,
    );
    final mapped = scoped.map((v) => v * 3);
    expect(mapped.value, 6);
    expect(mapped.instanceId, 'a');
    expect(mapped.label, 'Alpha');
  });

  test('label falls back to the id when unnamed', () {
    const scoped = InstanceScoped<int>(
      instanceId: 'srv-a',
      instanceName: '',
      value: 1,
    );
    expect(scoped.label, 'srv-a');
  });

  group('groupByIdentity', () {
    InstanceScoped<String> scoped(String id, String value) =>
        InstanceScoped<String>(instanceId: id, instanceName: id, value: value);

    test('collapses the same logical record seen from two instances', () {
      // theburrowhub/heimdallm#769: the same PR discovered by two instances
      // must not render as two rows.
      final items = [scoped('hub-1', 'heimdallm#769'), scoped('srv-a', 'heimdallm#769')];
      final groups = groupByIdentity<String>(items, keyOf: (v) => v);

      expect(groups, hasLength(1));
      expect(groups.single.members, hasLength(2));
      expect(groups.single.isShared, isTrue);
    });

    test('leaves distinct keys in separate groups', () {
      final items = [scoped('hub-1', 'a#1'), scoped('srv-a', 'b#2')];
      final groups = groupByIdentity<String>(items, keyOf: (v) => v);

      expect(groups, hasLength(2));
      expect(groups.every((g) => !g.isShared), isTrue);
    });

    test('preserves registration order of both groups and members', () {
      final items = [
        scoped('hub-1', 'a'),
        scoped('hub-1', 'b'),
        scoped('srv-a', 'a'),
        scoped('srv-a', 'b'),
      ];
      final groups = groupByIdentity<String>(items, keyOf: (v) => v);

      expect(groups.map((g) => g.value), ['a', 'b']);
      expect(groups.first.members.map((m) => m.instanceId), ['hub-1', 'srv-a']);
    });

    test('pick selects the primary; without it the first registered wins', () {
      final items = [scoped('hub-1', 'x'), scoped('srv-a', 'x')];

      final defaulted = groupByIdentity<String>(items, keyOf: (v) => v);
      expect(defaulted.single.instanceId, 'hub-1');

      final picked = groupByIdentity<String>(
        items,
        keyOf: (v) => v,
        pick: (candidates) => candidates.last,
      );
      expect(picked.single.instanceId, 'srv-a');
    });

    test('duplicate rows within the same instance also collapse', () {
      final items = [scoped('hub-1', 'x'), scoped('hub-1', 'x')];
      final groups = groupByIdentity<String>(items, keyOf: (v) => v);
      expect(groups, hasLength(1));
      expect(groups.single.members, hasLength(2));
    });

    test('empty input yields no groups', () {
      expect(groupByIdentity<String>(const [], keyOf: (v) => v), isEmpty);
    });
  });

  group('InstanceGroup', () {
    test('value and instanceId/instanceName come from the primary', () {
      final group = InstanceGroup<int>(
        primary: const InstanceScoped(instanceId: 'srv-a', instanceName: 'Srv A', value: 42),
        members: const [
          InstanceScoped(instanceId: 'srv-a', instanceName: 'Srv A', value: 42),
          InstanceScoped(instanceId: 'hub-1', instanceName: 'Hub', value: 42),
        ],
      );
      expect(group.value, 42);
      expect(group.instanceId, 'srv-a');
      expect(group.instanceName, 'Srv A');
      expect(group.isShared, isTrue);
      expect(group.others.map((m) => m.instanceId), ['hub-1']);
    });

    test('a single member is not shared and has no others', () {
      final group = InstanceGroup<int>(
        primary: const InstanceScoped(instanceId: 'hub-1', instanceName: '', value: 1),
        members: const [InstanceScoped(instanceId: 'hub-1', instanceName: '', value: 1)],
      );
      expect(group.isShared, isFalse);
      expect(group.others, isEmpty);
    });

    test('orderedMembers puts the primary first even when it was not', () {
      final group = InstanceGroup<int>(
        primary: const InstanceScoped(instanceId: 'srv-a', instanceName: '', value: 1),
        members: const [
          InstanceScoped(instanceId: 'hub-1', instanceName: '', value: 1),
          InstanceScoped(instanceId: 'srv-a', instanceName: '', value: 1),
        ],
      );
      expect(group.orderedMembers.map((m) => m.instanceId), ['srv-a', 'hub-1']);
    });
  });
}
