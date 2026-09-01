import 'dart:async';

import '../api/api_client.dart';
import 'models.dart';

/// A value paired with the instance it came from.
///
/// A wrapper rather than an `instanceId` field on [PR], [TrackedIssue] and
/// friends: those models are serialised, compared and covered by their own
/// tests, and threading a transport-level concern through all of them would be
/// a much larger change for the same result.
class InstanceScoped<T> {
  final String instanceId;
  final String instanceName;
  final T value;

  const InstanceScoped({
    required this.instanceId,
    required this.instanceName,
    required this.value,
  });

  /// Label for a badge. Empty on a single-daemon install, where the UI shows
  /// no badge at all.
  String get label => instanceName.isNotEmpty ? instanceName : instanceId;

  InstanceScoped<R> map<R>(R Function(T) f) => InstanceScoped<R>(
    instanceId: instanceId,
    instanceName: instanceName,
    value: f(value),
  );
}

/// One instance that could not be read during an aggregation.
class InstanceFailure {
  final String instanceId;
  final String instanceName;
  final Object error;

  const InstanceFailure({
    required this.instanceId,
    required this.instanceName,
    required this.error,
  });

  String get label => instanceName.isNotEmpty ? instanceName : instanceId;
}

/// The result of fanning a read out across instances.
class AggregatedResult<T> {
  final List<InstanceScoped<T>> items;

  /// Instances that failed. Surfaced rather than swallowed: a dashboard that
  /// silently drops one machine's PRs looks identical to one where that machine
  /// simply has no work, which is exactly the confusion to avoid.
  final List<InstanceFailure> failures;

  const AggregatedResult({this.items = const [], this.failures = const []});

  bool get hasFailures => failures.isNotEmpty;

  /// The plain values, discarding provenance. For call sites that only need
  /// the data.
  List<T> get values => items.map((e) => e.value).toList(growable: false);
}

/// Fans [fetch] out across [targets] and merges the results.
///
/// Every instance is queried concurrently, and a failure on one never fails the
/// whole call: partial data plus an explicit list of what could not be reached
/// is strictly more useful than an error page. Results are ordered by instance
/// so the merged list is stable between refreshes rather than reshuffling with
/// whichever machine answered first.
Future<AggregatedResult<T>> aggregate<T>({
  required List<DaemonInstance> targets,
  required ApiClient Function(String instanceId) clientFor,
  required Future<List<T>> Function(ApiClient client) fetch,
}) async {
  if (targets.isEmpty) return const AggregatedResult();

  // Resolve every client BEFORE the first await. clientFor reads Riverpod
  // providers, and reading a provider across an async gap is an anti-pattern
  // that leaves the caller's own provider stuck in its loading state.
  final resolved = [
    for (final instance in targets) (instance: instance, client: clientFor(instance.id)),
  ];

  final settled = await Future.wait(
    resolved.map((target) async {
      try {
        final values = await fetch(target.client);
        return _Outcome<T>(instance: target.instance, values: values);
      } catch (error) {
        return _Outcome<T>(instance: target.instance, error: error);
      }
    }),
  );

  final items = <InstanceScoped<T>>[];
  final failures = <InstanceFailure>[];
  for (final outcome in settled) {
    if (outcome.error != null) {
      failures.add(
        InstanceFailure(
          instanceId: outcome.instance.id,
          instanceName: outcome.instance.displayName,
          error: outcome.error!,
        ),
      );
      continue;
    }
    for (final value in outcome.values) {
      items.add(
        InstanceScoped<T>(
          instanceId: outcome.instance.id,
          instanceName: outcome.instance.name,
          value: value,
        ),
      );
    }
  }
  return AggregatedResult(items: items, failures: failures);
}

/// Wraps values that all came from the same instance. Used when the fan-out
/// has a single target, and by tests building an expected result.
AggregatedResult<T> singleInstanceResult<T>(
  List<T> values, {
  String instanceId = '',
  String instanceName = '',
}) {
  return AggregatedResult<T>(
    items: [
      for (final value in values)
        InstanceScoped<T>(
          instanceId: instanceId,
          instanceName: instanceName,
          value: value,
        ),
    ],
  );
}

/// [aggregate] for endpoints that return a single object rather than a list.
Future<AggregatedResult<T>> aggregateOne<T>({
  required List<DaemonInstance> targets,
  required ApiClient Function(String instanceId) clientFor,
  required Future<T> Function(ApiClient client) fetch,
}) {
  return aggregate<T>(
    targets: targets,
    clientFor: clientFor,
    fetch: (client) async => [await fetch(client)],
  );
}

class _Outcome<T> {
  final DaemonInstance instance;
  final List<T> values;
  final Object? error;

  _Outcome({required this.instance, this.values = const [], this.error});
}
