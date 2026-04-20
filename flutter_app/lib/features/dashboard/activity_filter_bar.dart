import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'activity_filters.dart';

/// Sort mode — must match dashboard_screen.dart's _SortMode.
/// Duplicated here to avoid exposing private enum; the dashboard passes
/// the current value and a callback.
enum SortMode { priority, newest }

/// Compact filter bar for the unified Activity view.
/// Includes sort buttons, type chips, org/repo popups, search, and reset.
class ActivityFilterBar extends ConsumerStatefulWidget {
  final Set<String> allRepos;
  final SortMode sort;
  final ValueChanged<SortMode> onSortChanged;

  const ActivityFilterBar({
    super.key,
    required this.allRepos,
    required this.sort,
    required this.onSortChanged,
  });

  @override
  ConsumerState<ActivityFilterBar> createState() => _ActivityFilterBarState();
}

class _ActivityFilterBarState extends ConsumerState<ActivityFilterBar> {
  final _searchController = TextEditingController();
  final _searchFocus = FocusNode();

  @override
  void dispose() {
    _searchController.dispose();
    _searchFocus.dispose();
    super.dispose();
  }

  Set<String> get _allOrgs => widget.allRepos
      .map((r) => r.contains('/') ? r.split('/').first : r)
      .toSet();

  Set<String> get _filteredRepos {
    final filters = ref.read(activityFiltersProvider);
    if (filters.orgs.isEmpty) return widget.allRepos;
    return widget.allRepos.where((r) {
      final org = r.contains('/') ? r.split('/').first : r;
      return filters.orgs.contains(org);
    }).toSet();
  }

  @override
  Widget build(BuildContext context) {
    final filters = ref.watch(activityFiltersProvider);

    if (filters.search != _searchController.text) {
      _searchController.text = filters.search;
    }

    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 8, 16, 8),
      child: Wrap(
        spacing: 6,
        runSpacing: 6,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          // ── Sort buttons ────────────────────────────────────────────
          _sortChip('Priority', Icons.sort, SortMode.priority),
          _sortChip('Newest', Icons.schedule, SortMode.newest),

          // Separator
          SizedBox(
            height: 20,
            child: VerticalDivider(width: 16, thickness: 1, color: Colors.grey.shade700),
          ),

          // ── Type chips ──────────────────────────────────────────────
          _typeChip('pr', 'PR', Colors.blue, filters),
          _typeChip('it', 'IT', Colors.orange, filters),
          _typeChip('dev', 'DEV', Colors.green, filters),

          // ── Org multi-select ────────────────────────────────────────
          if (_allOrgs.isNotEmpty)
            _multiSelectPopup(
              label: 'Org',
              icon: Icons.business,
              allValues: _allOrgs.toList()..sort(),
              selected: filters.orgs,
              displayFn: (v) => v,
              onChanged: (orgs) {
                final notifier = ref.read(activityFiltersProvider.notifier);
                var repos = filters.repos;
                if (orgs.isNotEmpty) {
                  repos = repos.where((r) {
                    final org = r.contains('/') ? r.split('/').first : r;
                    return orgs.contains(org);
                  }).toSet();
                }
                notifier.state = filters.copyWith(orgs: orgs, repos: repos);
              },
            ),

          // ── Repo multi-select ───────────────────────────────────────
          if (widget.allRepos.isNotEmpty)
            _multiSelectPopup(
              label: 'Repo',
              icon: Icons.folder_outlined,
              allValues: _filteredRepos.toList()..sort(),
              selected: filters.repos,
              displayFn: (v) => v.contains('/') ? v.split('/').last : v,
              onChanged: (repos) => ref
                  .read(activityFiltersProvider.notifier)
                  .state = filters.copyWith(repos: repos),
            ),

          // ── Search field ────────────────────────────────────────────
          SizedBox(
            width: 160,
            child: TextField(
              controller: _searchController,
              focusNode: _searchFocus,
              style: const TextStyle(fontSize: 11),
              decoration: InputDecoration(
                hintText: 'Search...',
                hintStyle: const TextStyle(fontSize: 11),
                prefixIcon: const Icon(Icons.search, size: 14),
                prefixIconConstraints:
                    const BoxConstraints(minWidth: 28, minHeight: 0),
                isDense: true,
                contentPadding:
                    const EdgeInsets.symmetric(horizontal: 8, vertical: 8),
                border: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(20),
                  borderSide: BorderSide(color: Colors.grey.shade600),
                ),
                enabledBorder: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(20),
                  borderSide: BorderSide(color: Colors.grey.shade700),
                ),
              ),
              onChanged: (v) => ref
                  .read(activityFiltersProvider.notifier)
                  .state = filters.copyWith(search: v),
            ),
          ),

          // ── Reset button ────────────────────────────────────────────
          if (filters.hasFilters)
            GestureDetector(
              onTap: () {
                ref.read(activityFiltersProvider.notifier).state =
                    const ActivityFilters();
                _searchController.clear();
              },
              child: Chip(
                avatar: Icon(Icons.clear_all, size: 14, color: Colors.red.shade400),
                label: Text('Reset', style: TextStyle(fontSize: 11, color: Colors.red.shade400)),
                visualDensity: VisualDensity.compact,
                materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
                side: BorderSide(color: Colors.red.shade400.withValues(alpha: 0.4)),
              ),
            ),
        ],
      ),
    );
  }

  // ── Sort chip ───────────────────────────────────────────────────────────────

  Widget _sortChip(String label, IconData icon, SortMode mode) {
    final selected = widget.sort == mode;
    final color = selected
        ? Theme.of(context).colorScheme.primary
        : Colors.grey.shade600;
    return GestureDetector(
      onTap: () => widget.onSortChanged(mode),
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
        decoration: BoxDecoration(
          color: selected
              ? Theme.of(context).colorScheme.primary.withValues(alpha: 0.15)
              : Colors.transparent,
          border: Border.all(color: color.withValues(alpha: 0.5)),
          borderRadius: BorderRadius.circular(20),
        ),
        child: Row(mainAxisSize: MainAxisSize.min, children: [
          Icon(icon, size: 13, color: color),
          const SizedBox(width: 4),
          Text(label, style: TextStyle(fontSize: 11, color: color,
              fontWeight: selected ? FontWeight.w600 : FontWeight.normal)),
        ]),
      ),
    );
  }

  // ── Type chip ───────────────────────────────────────────────────────────────

  Widget _typeChip(
      String type, String label, Color color, ActivityFilters filters) {
    final selected = filters.types.contains(type);
    return FilterChip(
      label: Text(label,
          style: TextStyle(
              fontSize: 11,
              fontWeight: FontWeight.w600,
              color: selected ? Colors.white : color)),
      selected: selected,
      selectedColor: color,
      backgroundColor: color.withValues(alpha: 0.1),
      side: BorderSide(color: color.withValues(alpha: 0.4)),
      checkmarkColor: Colors.white,
      showCheckmark: false,
      visualDensity: VisualDensity.compact,
      materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
      padding: const EdgeInsets.symmetric(horizontal: 4),
      onSelected: (_) {
        final types = Set<String>.from(filters.types);
        selected ? types.remove(type) : types.add(type);
        ref.read(activityFiltersProvider.notifier).state =
            filters.copyWith(types: types);
      },
    );
  }

  // ── Multi-select popup ──────────────────────────────────────────────────────

  Widget _multiSelectPopup({
    required String label,
    required IconData icon,
    required List<String> allValues,
    required Set<String> selected,
    required String Function(String) displayFn,
    required ValueChanged<Set<String>> onChanged,
  }) {
    final hasSelection = selected.isNotEmpty;
    return PopupMenuButton<String>(
      tooltip: '$label filter',
      offset: const Offset(0, 36),
      constraints: const BoxConstraints(maxHeight: 400, maxWidth: 360),
      itemBuilder: (_) => allValues.map((v) {
        final checked = selected.contains(v);
        return PopupMenuItem<String>(
          value: v,
          padding: EdgeInsets.zero,
          child: StatefulBuilder(
            builder: (ctx, setMenuState) => CheckboxListTile(
              dense: true,
              title: Text(displayFn(v), style: const TextStyle(fontSize: 12)),
              value: checked,
              onChanged: (val) {
                final next = Set<String>.from(selected);
                val == true ? next.add(v) : next.remove(v);
                onChanged(next);
                Navigator.of(ctx).pop();
              },
            ),
          ),
        );
      }).toList(),
      child: Chip(
        avatar: Icon(icon, size: 14,
            color: hasSelection
                ? Theme.of(context).colorScheme.primary
                : Colors.grey.shade600),
        label: Text(
          hasSelection ? '$label (${selected.length})' : label,
          style: TextStyle(
            fontSize: 11,
            color: hasSelection
                ? Theme.of(context).colorScheme.primary
                : Colors.grey.shade600,
          ),
        ),
        visualDensity: VisualDensity.compact,
        materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
        side: BorderSide(
          color: hasSelection
              ? Theme.of(context).colorScheme.primary.withValues(alpha: 0.5)
              : Colors.grey.shade600,
        ),
        backgroundColor: hasSelection
            ? Theme.of(context).colorScheme.primary.withValues(alpha: 0.1)
            : null,
      ),
    );
  }
}
