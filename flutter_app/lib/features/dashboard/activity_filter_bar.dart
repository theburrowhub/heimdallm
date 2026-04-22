import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'activity_filters.dart';

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

  Set<String> _filteredRepos(ActivityFilters filters) {
    if (filters.orgs.isEmpty) return widget.allRepos;
    return widget.allRepos.where((r) {
      final org = r.contains('/') ? r.split('/').first : r;
      return filters.orgs.contains(org);
    }).toSet();
  }

  @override
  Widget build(BuildContext context) {
    final filters = ref.watch(activityFiltersProvider);

    // Sync controller on external reset (e.g. Reset button) without
    // clobbering cursor position during normal typing.
    if (filters.search != _searchController.text && !_searchFocus.hasFocus) {
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
                notifier.update(filters.copyWith(orgs: orgs, repos: repos));
              },
            ),

          // ── Repo multi-select ───────────────────────────────────────
          if (widget.allRepos.isNotEmpty)
            _multiSelectPopup(
              label: 'Repo',
              icon: Icons.folder_outlined,
              allValues: _filteredRepos(filters).toList()..sort(),
              selected: filters.repos,
              displayFn: (v) => v.contains('/') ? v.split('/').last : v,
              onChanged: (repos) => ref
                  .read(activityFiltersProvider.notifier)
                  .update(filters.copyWith(repos: repos)),
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
                  .update(filters.copyWith(search: v)),
            ),
          ),

          // ── Reset button ────────────────────────────────────────────
          if (filters.hasFilters)
            GestureDetector(
              onTap: () {
                ref.read(activityFiltersProvider.notifier).update(
                    const ActivityFilters());
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
        ref.read(activityFiltersProvider.notifier).update(
            filters.copyWith(types: types));
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
    return GestureDetector(
      onTap: () => _showMultiSelectDialog(
        label: label,
        allValues: allValues,
        selected: selected,
        displayFn: displayFn,
        onChanged: onChanged,
      ),
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

  void _showMultiSelectDialog({
    required String label,
    required List<String> allValues,
    required Set<String> selected,
    required String Function(String) displayFn,
    required ValueChanged<Set<String>> onChanged,
  }) {
    var current = Set<String>.from(selected);
    showDialog(
      context: context,
      builder: (ctx) => StatefulBuilder(
        builder: (ctx, setDialogState) => Dialog(
          backgroundColor: Theme.of(context).colorScheme.surface,
          shape: RoundedRectangleBorder(
            borderRadius: BorderRadius.circular(12),
            side: BorderSide(color: Colors.grey.shade700),
          ),
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 340, maxHeight: 420),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                // Header
                Padding(
                  padding: const EdgeInsets.fromLTRB(20, 16, 12, 8),
                  child: Row(
                    children: [
                      Text('Filter by $label',
                          style: const TextStyle(fontSize: 14, fontWeight: FontWeight.w600)),
                      const Spacer(),
                      if (current.isNotEmpty)
                        TextButton(
                          onPressed: () {
                            setDialogState(() => current = {});
                          },
                          child: const Text('Clear', style: TextStyle(fontSize: 12)),
                        ),
                    ],
                  ),
                ),
                const Divider(height: 1),
                // List
                Flexible(
                  child: ListView.builder(
                    shrinkWrap: true,
                    padding: const EdgeInsets.symmetric(vertical: 4),
                    itemCount: allValues.length,
                    itemBuilder: (_, i) {
                      final v = allValues[i];
                      final checked = current.contains(v);
                      return InkWell(
                        onTap: () {
                          setDialogState(() {
                            checked ? current.remove(v) : current.add(v);
                          });
                        },
                        child: Padding(
                          padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 10),
                          child: Row(
                            children: [
                              Expanded(
                                child: Text(displayFn(v),
                                    style: TextStyle(
                                      fontSize: 13,
                                      color: checked ? Theme.of(context).colorScheme.primary : null,
                                      fontWeight: checked ? FontWeight.w600 : FontWeight.normal,
                                    )),
                              ),
                              Icon(
                                checked ? Icons.check_box : Icons.check_box_outline_blank,
                                size: 20,
                                color: checked
                                    ? Theme.of(context).colorScheme.primary
                                    : Colors.grey.shade600,
                              ),
                            ],
                          ),
                        ),
                      );
                    },
                  ),
                ),
                const Divider(height: 1),
                // Apply button
                Padding(
                  padding: const EdgeInsets.all(12),
                  child: SizedBox(
                    width: double.infinity,
                    child: FilledButton(
                      onPressed: () {
                        onChanged(current);
                        Navigator.of(ctx).pop();
                      },
                      child: Text('Apply${current.isNotEmpty ? ' (${current.length})' : ''}'),
                    ),
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
