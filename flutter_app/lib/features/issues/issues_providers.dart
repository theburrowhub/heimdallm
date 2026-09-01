import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/instances/aggregation.dart';
import '../../core/instances/instances_providers.dart';
import '../../core/models/tracked_issue.dart';
import '../../core/state/local_state_notifier.dart';
import '../dashboard/activity_filters.dart';
import '../dashboard/dashboard_providers.dart';

/// Counter incremented by SSE events to trigger issue list refresh.
final issueListRefreshProvider =
    NotifierProvider<LocalStateNotifier<int>, int>(
      () => LocalStateNotifier<int>(0),
    );

/// Tracks issues currently being reviewed, keyed by "repo:issueNumber".
final reviewingIssuesProvider =
    NotifierProvider<LocalStateNotifier<Set<String>>, Set<String>>(
      () => LocalStateNotifier<Set<String>>(const {}),
    );

/// Tracks issues currently being promoted to their next stage, keyed by "repo:issueNumber".
final promotingIssuesProvider =
    NotifierProvider<LocalStateNotifier<Set<String>>, Set<String>>(
      () => LocalStateNotifier<Set<String>>(const {}),
    );

/// Issues from every instance the UI is scoped to, tagged with their origin.
final issuesByInstanceProvider =
    FutureProvider<AggregatedResult<TrackedIssue>>((ref) async {
      ref.watch(issueListRefreshProvider);
      final filters = ref.watch(activityFiltersProvider);
      return aggregate<TrackedIssue>(
        targets: ref.watch(targetInstancesProvider),
        clientFor: (id) => ref.read(apiClientForProvider(id)),
        fetch: (client) => client.fetchIssues(states: filters.states.toList()),
      );
    });

final issuesProvider = FutureProvider<List<TrackedIssue>>((ref) async {
  final aggregated = await ref.watch(issuesByInstanceProvider.future);
  return aggregated.values;
});

/// Identifies one issue record; see [PRRef] for why the instance is part of
/// the key.
typedef IssueRef = ({String instanceId, int issueId});

final issueDetailProvider =
    FutureProvider.family<Map<String, dynamic>, IssueRef>((ref, key) async {
  ref.watch(sseStreamProvider);
  final api = ref.watch(apiClientForProvider(key.instanceId));
  return api.fetchIssue(key.issueId);
});
