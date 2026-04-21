import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'stats_filters.dart';

/// Compact filter bar for Stats: org and repo multi-select + reset.
class StatsFilterBar extends ConsumerWidget {
  final Set<String> allRepos;

  const StatsFilterBar({super.key, required this.allRepos});

  Set<String> get _allOrgs =>
      allRepos.map((r) => r.contains('/') ? r.split('/').first : r).toSet();

  Set<String> _filteredRepos(StatsFilters filters) {
    if (filters.orgs.isEmpty) return allRepos;
    return allRepos.where((r) {
      final org = r.contains('/') ? r.split('/').first : r;
      return filters.orgs.contains(org);
    }).toSet();
  }

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final filters = ref.watch(statsFiltersProvider);
    final orgs = _allOrgs;
    final visibleRepos = _filteredRepos(filters);

    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 8, 16, 8),
      child: Wrap(
        spacing: 6,
        runSpacing: 6,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          Text('Filter:', style: TextStyle(fontSize: 12, color: Colors.grey.shade500)),

          // Org multi-select
          if (orgs.isNotEmpty)
            _filterChip(
              context: context,
              label: 'Org',
              icon: Icons.business,
              allItems: orgs.toList()..sort(),
              selected: filters.orgs,
              onChanged: (selectedOrgs) {
                final validRepos = filters.repos.where((r) {
                  final org = r.contains('/') ? r.split('/').first : r;
                  return selectedOrgs.isEmpty || selectedOrgs.contains(org);
                }).toSet();
                ref.read(statsFiltersProvider.notifier).state =
                    filters.copyWith(orgs: selectedOrgs, repos: validRepos);
              },
            ),

          // Repo multi-select
          if (visibleRepos.isNotEmpty)
            _filterChip(
              context: context,
              label: 'Repo',
              icon: Icons.folder_outlined,
              allItems: visibleRepos.toList()..sort(),
              selected: filters.repos,
              onChanged: (repos) {
                ref.read(statsFiltersProvider.notifier).state =
                    filters.copyWith(repos: repos);
              },
            ),

          // Reset
          if (filters.hasFilters)
            ActionChip(
              avatar: const Icon(Icons.clear, size: 14),
              label: const Text('Reset', style: TextStyle(fontSize: 12)),
              visualDensity: VisualDensity.compact,
              onPressed: () {
                ref.read(statsFiltersProvider.notifier).state =
                    const StatsFilters();
              },
            ),
        ],
      ),
    );
  }

  Widget _filterChip({
    required BuildContext context,
    required String label,
    required IconData icon,
    required List<String> allItems,
    required Set<String> selected,
    required ValueChanged<Set<String>> onChanged,
  }) {
    final isActive = selected.isNotEmpty;
    return GestureDetector(
      onTap: () async {
        final result = await showDialog<Set<String>>(
          context: context,
          builder: (_) => _MultiSelectDialog(
            title: label,
            items: allItems,
            selected: selected,
          ),
        );
        if (result != null) onChanged(result);
      },
      child: Chip(
        avatar: Icon(icon, size: 14,
            color: isActive ? Theme.of(context).colorScheme.primary : Colors.grey),
        label: Text(
          isActive ? '$label (${selected.length})' : label,
          style: TextStyle(
            fontSize: 12,
            color: isActive ? Theme.of(context).colorScheme.primary : null,
          ),
        ),
        visualDensity: VisualDensity.compact,
        side: isActive
            ? BorderSide(color: Theme.of(context).colorScheme.primary.withValues(alpha: 0.5))
            : const BorderSide(color: Colors.transparent),
      ),
    );
  }
}

class _MultiSelectDialog extends StatefulWidget {
  final String title;
  final List<String> items;
  final Set<String> selected;

  const _MultiSelectDialog({
    required this.title,
    required this.items,
    required this.selected,
  });

  @override
  State<_MultiSelectDialog> createState() => _MultiSelectDialogState();
}

class _MultiSelectDialogState extends State<_MultiSelectDialog> {
  late Set<String> _selected;

  @override
  void initState() {
    super.initState();
    _selected = Set<String>.from(widget.selected);
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: Text(widget.title, style: const TextStyle(fontSize: 16)),
      contentPadding: const EdgeInsets.only(top: 12),
      content: SizedBox(
        width: 300,
        child: ListView.builder(
          shrinkWrap: true,
          itemCount: widget.items.length,
          itemBuilder: (_, i) {
            final item = widget.items[i];
            return CheckboxListTile(
              dense: true,
              title: Text(item, style: const TextStyle(fontSize: 13)),
              value: _selected.contains(item),
              onChanged: (val) {
                setState(() {
                  if (val == true) {
                    _selected.add(item);
                  } else {
                    _selected.remove(item);
                  }
                });
              },
            );
          },
        ),
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context, null),
          child: const Text('Cancel'),
        ),
        FilledButton(
          onPressed: () => Navigator.pop(context, _selected),
          child: const Text('Apply'),
        ),
      ],
    );
  }
}
