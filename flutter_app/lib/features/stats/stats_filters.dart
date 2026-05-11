import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/state/local_state_notifier.dart';

/// Filter state for the Stats view — org and repo multi-select.
class StatsFilters {
  final Set<String> orgs;
  final Set<String> repos;

  const StatsFilters({this.orgs = const {}, this.repos = const {}});

  StatsFilters copyWith({Set<String>? orgs, Set<String>? repos}) =>
      StatsFilters(
        orgs: orgs ?? this.orgs,
        repos: repos ?? this.repos,
      );

  bool get hasFilters => orgs.isNotEmpty || repos.isNotEmpty;
}

final statsFiltersProvider =
    NotifierProvider<LocalStateNotifier<StatsFilters>, StatsFilters>(
      () => LocalStateNotifier<StatsFilters>(const StatsFilters()),
    );
