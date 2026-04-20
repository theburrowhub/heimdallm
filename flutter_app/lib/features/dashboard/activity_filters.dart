import 'package:flutter_riverpod/flutter_riverpod.dart';

/// Filter state for the unified Activity view.
class ActivityFilters {
  final Set<String> types; // 'pr', 'it', 'dev' -- empty = show all
  final Set<String> orgs; // empty = all orgs
  final Set<String> repos; // empty = all repos
  final String search; // free text search

  const ActivityFilters({
    this.types = const {},
    this.orgs = const {},
    this.repos = const {},
    this.search = '',
  });

  ActivityFilters copyWith({
    Set<String>? types,
    Set<String>? orgs,
    Set<String>? repos,
    String? search,
  }) =>
      ActivityFilters(
        types: types ?? this.types,
        orgs: orgs ?? this.orgs,
        repos: repos ?? this.repos,
        search: search ?? this.search,
      );

  bool get hasFilters =>
      types.isNotEmpty ||
      orgs.isNotEmpty ||
      repos.isNotEmpty ||
      search.isNotEmpty;
}

final activityFiltersProvider =
    StateProvider<ActivityFilters>((ref) => const ActivityFilters());
