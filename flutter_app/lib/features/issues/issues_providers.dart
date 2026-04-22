import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/models/tracked_issue.dart';
import '../dashboard/activity_filters.dart';
import '../dashboard/dashboard_providers.dart';

/// Counter incremented by SSE events to trigger issue list refresh.
final issueListRefreshProvider = StateProvider<int>((ref) => 0);

/// Tracks issues currently being reviewed, keyed by "repo:issueNumber".
final reviewingIssuesProvider = StateProvider<Set<String>>((ref) => const {});

/// Tracks issues currently being promoted to develop, keyed by "repo:issueNumber".
final promotingIssuesProvider = StateProvider<Set<String>>((ref) => const {});

final issuesProvider = FutureProvider<List<TrackedIssue>>((ref) async {
  ref.watch(issueListRefreshProvider);
  final filters = ref.watch(activityFiltersProvider);
  final api = ref.watch(apiClientProvider);
  return api.fetchIssues(states: filters.states.toList());
});

final issueDetailProvider =
    FutureProvider.family<Map<String, dynamic>, int>((ref, issueId) async {
  ref.watch(sseStreamProvider);
  final api = ref.watch(apiClientProvider);
  return api.fetchIssue(issueId);
});
