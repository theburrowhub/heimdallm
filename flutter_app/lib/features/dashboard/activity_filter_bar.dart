import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'activity_filters.dart';

/// Compact filter bar for the unified Activity view.
class ActivityFilterBar extends ConsumerStatefulWidget {
  /// All known repo full-names (e.g. "org/repo") from combined PR+Issue data.
  final Set<String> allRepos;

  const ActivityFilterBar({super.key, required this.allRepos});

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

  /// Repos filtered by currently selected orgs (or all repos if no org filter).
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

    // Keep search controller in sync when filters are reset externally.
    if (filters.search != _searchController.text) {
      _searchController.text = filters.search;
    }

    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
      child: Wrap(
        spacing: 8,
        runSpacing: 6,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          // ── Type chips ───────────────────────────────────────────────
          _typeChip('pr', 'PR', Colors.blue, filters),
          _typeChip('it', 'IT', Colors.orange, filters),
          _typeChip('dev', 'DEV', Colors.green, filters),

          // ── Org multi-select ─────────────────────────────────────────
          if (_allOrgs.isNotEmpty)
            _multiSelectPopup(
              label: 'Org',
              icon: Icons.business,
              allValues: _allOrgs.toList()..sort(),
              selected: filters.orgs,
              onChanged: (orgs) {
                final notifier = ref.read(activityFiltersProvider.notifier);
                // When orgs change, remove any repo selections that no longer
                // belong to the selected orgs.
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

          // ── Repo multi-select ────────────────────────────────────────
          if (widget.allRepos.isNotEmpty)
            _multiSelectPopup(
              label: 'Repo',
              icon: Icons.folder_outlined,
              allValues: _filteredRepos.toList()..sort(),
              selected: filters.repos,
              onChanged: (repos) => ref
                  .read(activityFiltersProvider.notifier)
                  .state = filters.copyWith(repos: repos),
            ),

          // ── Search field ─────────────────────────────────────────────
          SizedBox(
            width: 180,
            height: 32,
            child: TextField(
              controller: _searchController,
              focusNode: _searchFocus,
              style: const TextStyle(fontSize: 12),
              decoration: InputDecoration(
                hintText: 'Search...',
                hintStyle: const TextStyle(fontSize: 12),
                prefixIcon: const Icon(Icons.search, size: 16),
                prefixIconConstraints:
                    const BoxConstraints(minWidth: 32, minHeight: 0),
                isDense: true,
                contentPadding:
                    const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
                border: OutlineInputBorder(
                  borderRadius: BorderRadius.circular(6),
                ),
              ),
              onChanged: (v) => ref
                  .read(activityFiltersProvider.notifier)
                  .state = filters.copyWith(search: v),
            ),
          ),

          // ── Reset button ─────────────────────────────────────────────
          if (filters.hasFilters)
            IconButton(
              icon: const Icon(Icons.clear_all, size: 18),
              tooltip: 'Reset filters',
              visualDensity: VisualDensity.compact,
              onPressed: () {
                ref.read(activityFiltersProvider.notifier).state =
                    const ActivityFilters();
                _searchController.clear();
              },
            ),
        ],
      ),
    );
  }

  // ── Type chip ──────────────────────────────────────────────────────────────

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
      visualDensity: VisualDensity.compact,
      materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
      onSelected: (_) {
        final types = Set<String>.from(filters.types);
        selected ? types.remove(type) : types.add(type);
        ref.read(activityFiltersProvider.notifier).state =
            filters.copyWith(types: types);
      },
    );
  }

  // ── Multi-select popup ─────────────────────────────────────────────────────

  Widget _multiSelectPopup({
    required String label,
    required IconData icon,
    required List<String> allValues,
    required Set<String> selected,
    required ValueChanged<Set<String>> onChanged,
  }) {
    final hasSelection = selected.isNotEmpty;
    return PopupMenuButton<String>(
      tooltip: '$label filter',
      offset: const Offset(0, 36),
      constraints: const BoxConstraints(maxHeight: 320, maxWidth: 260),
      itemBuilder: (_) => allValues.map((v) {
        final checked = selected.contains(v);
        return PopupMenuItem<String>(
          value: v,
          padding: EdgeInsets.zero,
          child: StatefulBuilder(
            builder: (ctx, setMenuState) => CheckboxListTile(
              dense: true,
              title: Text(v, style: const TextStyle(fontSize: 12)),
              value: checked,
              onChanged: (val) {
                final next = Set<String>.from(selected);
                val == true ? next.add(v) : next.remove(v);
                onChanged(next);
                // Close after toggling so the filter applies immediately.
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
              : Colors.grey.shade400,
        ),
        backgroundColor: hasSelection
            ? Theme.of(context).colorScheme.primary.withValues(alpha: 0.1)
            : null,
      ),
    );
  }
}
