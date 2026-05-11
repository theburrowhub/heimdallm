import 'package:flutter_riverpod/flutter_riverpod.dart';
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

final issuesProvider = FutureProvider<List<TrackedIssue>>((ref) async {
  ref.watch(issueListRefreshProvider);
  final filters = ref.watch(activityFiltersProvider);
  final api = ref.watch(apiClientProvider);
  return api.fetchIssues(states: filters.states.toList());
});

final issueDetailProvider = FutureProvider.family<Map<String, dynamic>, int>((
  ref,
  issueId,
) async {
  ref.watch(sseStreamProvider);
  final api = ref.watch(apiClientProvider);
  return api.fetchIssue(issueId);
});
