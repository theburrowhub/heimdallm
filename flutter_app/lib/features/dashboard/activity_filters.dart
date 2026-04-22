import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// Filter state for the unified Activity view.
class ActivityFilters {
  final Set<String> types; // 'pr', 'it', 'dev' -- empty = show all
  final Set<String> orgs; // empty = all orgs
  final Set<String> repos; // empty = all repos
  final Set<String> states; // 'open', 'closed' — empty = all
  final String search; // free text search
  final String viewMode; // 'list' or 'grid'

  const ActivityFilters({
    this.types = const {},
    this.orgs = const {},
    this.repos = const {},
    this.states = const {'open'}, // default: only open
    this.search = '',
    this.viewMode = 'list',
  });

  ActivityFilters copyWith({
    Set<String>? types,
    Set<String>? orgs,
    Set<String>? repos,
    Set<String>? states,
    String? search,
    String? viewMode,
  }) =>
      ActivityFilters(
        types: types ?? this.types,
        orgs: orgs ?? this.orgs,
        repos: repos ?? this.repos,
        states: states ?? this.states,
        search: search ?? this.search,
        viewMode: viewMode ?? this.viewMode,
      );

  bool get hasFilters =>
      types.isNotEmpty ||
      orgs.isNotEmpty ||
      repos.isNotEmpty ||
      !(states.length == 1 && states.contains('open')) ||
      search.isNotEmpty;
}

class ActivityFiltersNotifier extends Notifier<ActivityFilters> {
  static const _typesKey = 'activity_type_filter';
  static const _orgsKey = 'activity_org_filter';
  static const _reposKey = 'activity_repo_filter';
  static const _statesKey = 'activity_state_filter';
  static const _viewModeKey = 'activity_view_mode';

  @override
  ActivityFilters build() {
    _loadAsync();
    return const ActivityFilters();
  }

  Future<void> _loadAsync() async {
    final prefs = await SharedPreferences.getInstance();
    state = ActivityFilters(
      types: _loadSet(prefs, _typesKey),
      orgs: _loadSet(prefs, _orgsKey),
      repos: _loadSet(prefs, _reposKey),
      states: _loadSet(prefs, _statesKey, defaultVal: {'open'}),
      viewMode: prefs.getString(_viewModeKey) ?? 'list',
    );
  }

  Set<String> _loadSet(SharedPreferences p, String key,
      {Set<String>? defaultVal}) {
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
    prefs.setString(_typesKey, f.types.join(','));
    prefs.setString(_orgsKey, f.orgs.join(','));
    prefs.setString(_reposKey, f.repos.join(','));
    prefs.setString(_statesKey, f.states.join(','));
    prefs.setString(_viewModeKey, f.viewMode);
  }
}

final activityFiltersProvider =
    NotifierProvider<ActivityFiltersNotifier, ActivityFilters>(
        ActivityFiltersNotifier.new);

/// Sort mode for the Activity view — shared between dashboard_screen
/// and activity_filter_bar to avoid duplication.
enum SortMode { priority, newest }
