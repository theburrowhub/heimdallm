import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// Filter state for the unified Activity view.
class ActivityFilters {
  final Set<String> orgs; // empty = all orgs
  final Set<String> repos; // empty = all repos
  final Set<String> states; // 'open', 'closed' — empty = all
  final String search; // free text search
  final String viewMode; // 'board', 'list' or 'grid'

  const ActivityFilters({
    this.orgs = const {},
    this.repos = const {},
    this.states = const {'open'}, // default: only open
    this.search = '',
    this.viewMode = 'board',
  });

  ActivityFilters copyWith({
    Set<String>? orgs,
    Set<String>? repos,
    Set<String>? states,
    String? search,
    String? viewMode,
  }) => ActivityFilters(
    orgs: orgs ?? this.orgs,
    repos: repos ?? this.repos,
    states: states ?? this.states,
    search: search ?? this.search,
    viewMode: viewMode ?? this.viewMode,
  );

  bool get hasFilters =>
      orgs.isNotEmpty ||
      repos.isNotEmpty ||
      !(states.length == 1 && states.contains('open')) ||
      search.isNotEmpty;
}

class ActivityFiltersNotifier extends Notifier<ActivityFilters> {
  static const _orgsKey = 'activity_org_filter';
  static const _reposKey = 'activity_repo_filter';
  static const _statesKey = 'activity_state_filter';

  /// The view since the board (#920). A new key, because the old one was
  /// written on every filter change, so a stored `list` says nothing about a
  /// choice; only a stored `grid` was picked on purpose and is carried over.
  static const _viewKey = 'activity_view';
  static const _legacyViewModeKey = 'activity_view_mode';

  @override
  ActivityFilters build() {
    _loadAsync();
    return const ActivityFilters();
  }

  Future<void> _loadAsync() async {
    final prefs = await SharedPreferences.getInstance();
    state = ActivityFilters(
      orgs: _loadSet(prefs, _orgsKey),
      repos: _loadSet(prefs, _reposKey),
      states: _loadSet(prefs, _statesKey, defaultVal: {'open'}),
      viewMode:
          prefs.getString(_viewKey) ??
          (prefs.getString(_legacyViewModeKey) == 'grid' ? 'grid' : 'board'),
    );
  }

  Set<String> _loadSet(
    SharedPreferences p,
    String key, {
    Set<String>? defaultVal,
  }) {
    final v = p.getString(key);
    if (v == null || v.isEmpty) return defaultVal ?? {};
    return v.split(',').toSet();
  }

  void update(ActivityFilters filters) {
    state = filters;
    _saveAsync(filters);
  }

  Future<void> _saveAsync(ActivityFilters f) async {
    final prefs = await SharedPreferences.getInstance();
    prefs.setString(_orgsKey, f.orgs.join(','));
    prefs.setString(_reposKey, f.repos.join(','));
    prefs.setString(_statesKey, f.states.join(','));
    prefs.setString(_viewKey, f.viewMode);
  }
}

final activityFiltersProvider =
    NotifierProvider<ActivityFiltersNotifier, ActivityFilters>(
      ActivityFiltersNotifier.new,
    );

/// Sort mode for the Activity view — shared between dashboard_screen
/// and activity_filter_bar to avoid duplication.
enum SortMode { priority, newest }
