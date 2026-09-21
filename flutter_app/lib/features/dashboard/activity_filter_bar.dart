import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import 'activity_filters.dart';

/// The filter/action bar for the unified Activity view — the app's
/// reference toolbar style, built on [AppToolbar]/[AppFilterChip]/
/// [AppSearchField]/[AppMultiSelectChip]/[AppViewToggle] instead of the
/// ad-hoc `Wrap` of literal colors/radii this file used to be.
class ActivityFilterBar extends ConsumerWidget {
  final Set<String> allRepos;
  final SortMode sort;
  final ValueChanged<SortMode> onSortChanged;
  final VoidCallback onAddPR;

  const ActivityFilterBar({
    super.key,
    required this.allRepos,
    required this.sort,
    required this.onSortChanged,
    required this.onAddPR,
  });

  Set<String> get _allOrgs =>
      allRepos.map((r) => r.contains('/') ? r.split('/').first : r).toSet();

  Set<String> _filteredRepos(ActivityFilters filters) {
    if (filters.orgs.isEmpty) return allRepos;
    return allRepos.where((r) {
      final org = r.contains('/') ? r.split('/').first : r;
      return filters.orgs.contains(org);
    }).toSet();
  }

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final filters = ref.watch(activityFiltersProvider);
    final notifier = ref.read(activityFiltersProvider.notifier);

    return AppToolbar(
      rowKey: const Key('activity-filter-toolbar-row'),
      leading: [
        AppFilterChip(
          label: 'Priority',
          icon: Icons.sort,
          selected: sort == SortMode.priority,
          onTap: () => onSortChanged(SortMode.priority),
        ),
        AppFilterChip(
          label: 'Newest',
          icon: Icons.schedule,
          selected: sort == SortMode.newest,
          onTap: () => onSortChanged(SortMode.newest),
        ),
        const _ToolbarDivider(),
        AppFilterChip(
          label: 'PR',
          selected: filters.types.contains('pr'),
          accent: AppColors.featurePrReview,
          onTap: () => notifier.update(
            filters.copyWith(types: _toggled(filters.types, 'pr')),
          ),
        ),
        AppFilterChip(
          label: 'IT',
          selected: filters.types.contains('it'),
          accent: AppColors.featureIssueTracking,
          onTap: () => notifier.update(
            filters.copyWith(types: _toggled(filters.types, 'it')),
          ),
        ),
        AppFilterChip(
          label: 'DEV',
          selected: filters.types.contains('dev'),
          accent: AppColors.featureDevelop,
          onTap: () => notifier.update(
            filters.copyWith(types: _toggled(filters.types, 'dev')),
          ),
        ),
        AppFilterChip(
          label: 'Open',
          selected: filters.states.contains('open'),
          accent: AppColors.success,
          onTap: () => notifier.update(
            filters.copyWith(states: _toggled(filters.states, 'open')),
          ),
        ),
        AppFilterChip(
          label: 'Closed',
          selected: filters.states.contains('closed'),
          onTap: () => notifier.update(
            filters.copyWith(states: _toggled(filters.states, 'closed')),
          ),
        ),
      ],
      filters: [
        if (_allOrgs.isNotEmpty)
          AppMultiSelectChip(
            label: 'Org',
            icon: Icons.business,
            allValues: _allOrgs.toList()..sort(),
            selected: filters.orgs,
            onChanged: (orgs) {
              var repos = filters.repos;
              if (orgs.isNotEmpty) {
                repos = repos.where((r) {
                  final org = r.contains('/') ? r.split('/').first : r;
                  return orgs.contains(org);
                }).toSet();
              }
              notifier.update(filters.copyWith(orgs: orgs, repos: repos));
            },
          ),
        if (allRepos.isNotEmpty)
          AppMultiSelectChip(
            label: 'Repo',
            icon: Icons.folder_outlined,
            allValues: _filteredRepos(filters).toList()..sort(),
            selected: filters.repos,
            displayFn: (v) => v.contains('/') ? v.split('/').last : v,
            onChanged: (repos) =>
                notifier.update(filters.copyWith(repos: repos)),
          ),
        AppSearchField(
          value: filters.search,
          hintText: 'Search...',
          width: 160,
          onChanged: (v) => notifier.update(filters.copyWith(search: v)),
        ),
        if (filters.hasFilters)
          AppFilterChip(
            label: 'Reset',
            icon: Icons.clear_all,
            selected: true,
            accent: AppColors.danger,
            onTap: () => notifier.update(const ActivityFilters()),
          ),
      ],
      trailing: [
        AppViewToggle(
          mode: filters.viewMode == 'grid'
              ? AppViewMode.grid
              : AppViewMode.list,
          onChanged: (mode) => notifier.update(
            filters.copyWith(
              viewMode: mode == AppViewMode.grid ? 'grid' : 'list',
            ),
          ),
        ),
        const SizedBox(width: 12),
        FilledButton.icon(
          key: const Key('dashboard-add-pr-button'),
          icon: const Icon(Icons.add_link, size: 18),
          label: const Text('Add PR'),
          onPressed: onAddPR,
        ),
      ],
    );
  }

  Set<String> _toggled(Set<String> current, String value) {
    final next = Set<String>.from(current);
    next.contains(value) ? next.remove(value) : next.add(value);
    return next;
  }
}

class _ToolbarDivider extends StatelessWidget {
  const _ToolbarDivider();

  @override
  Widget build(BuildContext context) {
    return SizedBox(
      height: 20,
      child: VerticalDivider(
        width: 16,
        thickness: 1,
        color: AppColors.border.resolve(context),
      ),
    );
  }
}
