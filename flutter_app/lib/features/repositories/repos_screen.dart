import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:shared_preferences/shared_preferences.dart';
import '../../core/models/config_model.dart';
import '../../shared/widgets/toast.dart';
import '../config/config_providers.dart';
import 'widgets/bulk_actions_bar.dart';
import 'widgets/feature_palette.dart';
import 'widgets/filter_chips.dart';
import 'widgets/repo_grid_tile.dart';
import 'widgets/repo_list_tile.dart';

class ReposScreen extends ConsumerStatefulWidget {
  const ReposScreen({super.key});

  @override
  ConsumerState<ReposScreen> createState() => _ReposScreenState();
}

enum _SyncStatus { idle, saving, saved }

class _ReposScreenState extends ConsumerState<ReposScreen> {
  Map<String, RepoConfig> _repoConfigs = {};
  bool _initialized = false;
  String _search = '';
  String _filter = 'all'; // 'all' | 'monitored' | 'not_monitored'
  String _viewMode = 'list'; // 'list' | 'grid'
  Set<String> _orgFilter = {}; // empty = all orgs
  _SyncStatus _syncStatus = _SyncStatus.idle;
  Timer? _debounce;
  Timer? _savedResetTimer;

  final Set<String> _selected = {};
  Set<String> _dismissedNew = {};

  @override
  void initState() {
    super.initState();
    SharedPreferences.getInstance().then((p) {
      if (!mounted) return;
      setState(() {
        _viewMode = p.getString('repos_view') ?? 'list';
        _dismissedNew = (p.getStringList('repos_dismissed_new') ?? []).toSet();
      });
    });
  }

  void _setViewMode(String v) {
    setState(() => _viewMode = v);
    SharedPreferences.getInstance().then((p) => p.setString('repos_view', v));
  }

  bool _shouldShowNew(String repo, RepoConfig c) =>
      c.firstSeenAt != null && !_dismissedNew.contains(repo);

  void _dismissNew(String repo) {
    if (_dismissedNew.contains(repo)) return;
    setState(() => _dismissedNew = {..._dismissedNew, repo});
    SharedPreferences.getInstance().then(
      (p) => p.setStringList('repos_dismissed_new', _dismissedNew.toList()),
    );
  }

  void _toggleSelection(String repo) {
    setState(() {
      if (!_selected.add(repo)) _selected.remove(repo);
    });
  }

  /// Aggregate per-feature state across the current selection.
  /// Returns `true` when every selected repo has the feature on,
  /// `false` when every selected repo has it off, and `null` when
  /// repos disagree (mixed state).
  Map<Feature, bool?> _aggregate() {
    bool? agg(bool Function(RepoConfig) pick) {
      bool? result;
      for (final r in _selected) {
        final c = _repoConfigs[r];
        if (c == null) continue;
        final v = pick(c);
        if (result == null) {
          result = v;
        } else if (result != v) {
          return null;
        }
      }
      return result ?? false;
    }

    bool hasDir(RepoConfig c) =>
        c.localDir != null && c.localDir!.isNotEmpty;

    return {
      Feature.prReview:      agg((c) => c.prEnabled ?? false),
      Feature.issueTracking: agg((c) => c.itEnabled ?? false),
      Feature.develop:       agg((c) => (c.devEnabled ?? false) && hasDir(c)),
    };
  }

  void _applyBulk(Feature f, bool enable) {
    setState(() {
      for (final r in _selected) {
        final c = _repoConfigs[r];
        if (c == null) continue;
        // Develop requires a local dir to actually run. Enabling it on a
        // repo without one would write a flag the daemon can't act on, so
        // skip those silently — the row LED stays grey, making it obvious
        // the bulk apply didn't affect that repo.
        if (f == Feature.develop &&
            enable &&
            (c.localDir == null || c.localDir!.isEmpty)) {
          continue;
        }
        _repoConfigs[r] = switch (f) {
          Feature.prReview      => c.copyWith(prEnabled: enable),
          Feature.issueTracking => c.copyWith(itEnabled: enable),
          Feature.develop       => c.copyWith(devEnabled: enable),
        };
      }
    });
    _debounce?.cancel();
    _debounce = Timer(const Duration(milliseconds: 400), _autoSave);
  }

  void _clearSelection() => setState(_selected.clear);

  String _emptyStateText() {
    if (_search.isNotEmpty) return 'No repos match “$_search”.';
    return switch (_filter) {
      'monitored' =>
        'No repos currently monitored. Request a review on a PR and the daemon will add it here.',
      'not_monitored' => 'No repos in the not-monitored list.',
      _ =>
        'No repos yet. Request a review on a PR and the daemon will add it automatically.',
    };
  }

  @override
  void dispose() {
    _debounce?.cancel();
    _savedResetTimer?.cancel();
    super.dispose();
  }

  void _initFrom(AppConfig config) {
    if (_initialized) return;
    _initialized = true;
    _repoConfigs = Map.from(config.repoConfigs);
  }

  /// Called whenever a repo config changes — schedules an auto-save.
  void _onChange(String repo, RepoConfig rc) {
    setState(() => _repoConfigs[repo] = rc);
    _debounce?.cancel();
    _debounce = Timer(const Duration(milliseconds: 800), _autoSave);
  }

  Future<void> _autoSave() async {
    final current = ref.read(configNotifierProvider).valueOrNull;
    if (current == null) return;
    if (mounted) setState(() => _syncStatus = _SyncStatus.saving);
    final updated = current.copyWith(repoConfigs: Map.from(_repoConfigs));
    try {
      await ref.read(configNotifierProvider.notifier).save(updated);
      if (!mounted) return;
      setState(() => _syncStatus = _SyncStatus.saved);
      _savedResetTimer?.cancel();
      _savedResetTimer = Timer(const Duration(seconds: 2),
          () { if (mounted) setState(() => _syncStatus = _SyncStatus.idle); });
    } catch (e) {
      if (mounted) {
        setState(() => _syncStatus = _SyncStatus.idle);
        showToast(context, 'Error saving: $e', isError: true);
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final configAsync = ref.watch(configNotifierProvider);

    return configAsync.when(
      loading: () => const Center(child: CircularProgressIndicator()),
      error: (_, __) => const Center(child: Text('Could not load config')),
      data: (config) {
        _initFrom(config);

        // Monitored first, disabled last; both groups sorted alphabetically
        final allRepos = _repoConfigs.keys.toList()
          ..sort((a, b) {
            final ma = _repoConfigs[a]!.isMonitored ? 0 : 1;
            final mb = _repoConfigs[b]!.isMonitored ? 0 : 1;
            if (ma != mb) return ma.compareTo(mb);
            return a.compareTo(b);
          });
        // Derive all orgs for the org filter dropdown. Only shown when more
        // than one org has at least one repo.
        final allOrgs = _repoConfigs.keys
            .map((r) => r.contains('/') ? r.split('/').first : r)
            .toSet()
            .toList()
          ..sort();

        final filtered = allRepos.where((r) {
          if (_search.isNotEmpty &&
              !r.toLowerCase().contains(_search.toLowerCase())) return false;
          if (_orgFilter.isNotEmpty) {
            final org = r.contains('/') ? r.split('/').first : r;
            if (!_orgFilter.contains(org)) return false;
          }
          final c = _repoConfigs[r]!;
          if (_filter == 'monitored' && !c.isMonitored) return false;
          if (_filter == 'not_monitored' && c.isMonitored) return false;
          return true;
        }).toList();

        return Column(
          children: [
            // Toolbar
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
              child: Row(
                children: [
                  Expanded(
                    child: TextField(
                      decoration: const InputDecoration(
                        hintText: 'Filter repos…',
                        prefixIcon: Icon(Icons.search, size: 18),
                        isDense: true,
                        border: OutlineInputBorder(),
                        contentPadding: EdgeInsets.symmetric(vertical: 8),
                      ),
                      onChanged: (v) => setState(() => _search = v),
                    ),
                  ),
                  const SizedBox(width: 8),
                  RepoFilterChips(
                    counts: {
                      'all': _repoConfigs.length,
                      'monitored': _repoConfigs.values
                          .where((c) => c.isMonitored)
                          .length,
                      'not_monitored': _repoConfigs.values
                          .where((c) => !c.isMonitored)
                          .length,
                    },
                    current: _filter,
                    onChanged: (v) => setState(() => _filter = v),
                  ),
                  const SizedBox(width: 8),
                  // Org multi-select (hidden when there's only one org).
                  if (allOrgs.length > 1) ...[
                    _OrgFilterChip(
                      orgs: allOrgs,
                      selected: _orgFilter,
                      onChanged: (s) => setState(() => _orgFilter = s),
                    ),
                    if (_orgFilter.isNotEmpty) ...[
                      const SizedBox(width: 4),
                      GestureDetector(
                        onTap: () => setState(() => _orgFilter = {}),
                        child: const Icon(Icons.clear, size: 16, color: Colors.grey),
                      ),
                    ],
                    const SizedBox(width: 8),
                  ],
                  Row(children: [
                    _ViewToggleButton(
                      icon: Icons.view_list,
                      active: _viewMode == 'list',
                      onTap: () => _setViewMode('list'),
                      buttonKey: const Key('repos_view_toggle_list'),
                    ),
                    _ViewToggleButton(
                      icon: Icons.grid_view,
                      active: _viewMode == 'grid',
                      onTap: () => _setViewMode('grid'),
                      buttonKey: const Key('repos_view_toggle_grid'),
                    ),
                  ]),
                  const SizedBox(width: 12),
                  // Auto-save status indicator
                  SizedBox(
                    width: 22, height: 22,
                    child: switch (_syncStatus) {
                      _SyncStatus.saving => const CircularProgressIndicator(strokeWidth: 2),
                      _SyncStatus.saved  => Icon(Icons.cloud_done_outlined,
                          size: 20, color: Colors.green.shade500),
                      _SyncStatus.idle   => const SizedBox.shrink(),
                    },
                  ),
                ],
              ),
            ),
            if (_selected.isNotEmpty)
              BulkActionsBar(
                selectedCount: _selected.length,
                aggregates: _aggregate(),
                onApply: _applyBulk,
                onClear: _clearSelection,
              ),
            // Repo list with section dividers
            Expanded(
              child: filtered.isEmpty
                  ? Center(child: Text(_emptyStateText()))
                  : _viewMode == 'grid'
                      ? _ReposGrid(
                          repos: filtered,
                          configs: _repoConfigs,
                          appConfig: config,
                          selected: _selected,
                          onSelectionToggle: _toggleSelection,
                          showNewFor: _shouldShowNew,
                          onDismissNew: _dismissNew,
                        )
                      : _RepoListWithSections(
                          repos: filtered,
                          configs: _repoConfigs,
                          appConfig: config,
                          onChanged: _onChange,
                          selected: _selected,
                          onSelectionToggle: _toggleSelection,
                          showNewFor: _shouldShowNew,
                          onDismissNew: _dismissNew,
                        ),
            ),
          ],
        );
      },
    );
  }
}

// ── List with section headers + org grouping ───────────────────────────────

class _RepoListWithSections extends ConsumerStatefulWidget {
  final List<String> repos;
  final Map<String, RepoConfig> configs;
  final AppConfig appConfig;
  final void Function(String repo, RepoConfig rc) onChanged;
  final Set<String> selected;
  final ValueChanged<String> onSelectionToggle;
  final bool Function(String repo, RepoConfig config) showNewFor;
  final ValueChanged<String> onDismissNew;

  const _RepoListWithSections({
    required this.repos,
    required this.configs,
    required this.appConfig,
    required this.onChanged,
    required this.selected,
    required this.onSelectionToggle,
    required this.showNewFor,
    required this.onDismissNew,
  });

  @override
  ConsumerState<_RepoListWithSections> createState() =>
      _RepoListWithSectionsState();
}

class _RepoListWithSectionsState extends ConsumerState<_RepoListWithSections> {
  // Collapse state per "section:org" key — default expanded
  final _expanded = <String, bool>{};

  bool _isExpanded(String key) => _expanded[key] ?? true;

  void _toggle(String key) =>
      setState(() => _expanded[key] = !_isExpanded(key));

  /// Groups repos by the org part ("org" in "org/repo") and sorts within each org.
  Map<String, List<String>> _groupByOrg(List<String> repos) {
    final groups = <String, List<String>>{};
    for (final r in repos) {
      final org = r.contains('/') ? r.split('/').first : r;
      groups.putIfAbsent(org, () => []).add(r);
    }
    // Sort repos within each org alphabetically
    for (final list in groups.values) {
      list.sort();
    }
    // Return sorted by org name
    return Map.fromEntries(
        groups.entries.toList()..sort((a, b) => a.key.compareTo(b.key)));
  }

  @override
  Widget build(BuildContext context) {
    final monitored =
        widget.repos.where((r) => widget.configs[r]!.isMonitored).toList();
    final disabled =
        widget.repos.where((r) => !widget.configs[r]!.isMonitored).toList();

    return ListView(
      padding: const EdgeInsets.symmetric(vertical: 4),
      children: [
        if (monitored.isNotEmpty) ...[
          _sectionHeader(
            context,
            'Monitored — auto-review enabled',
            monitored.length,
            Colors.green.shade700,
          ),
          ..._buildOrgGroups('monitored', monitored),
        ],
        _sectionHeader(
          context,
          'Not monitored — PRs visible, no auto-review',
          disabled.length,
          Colors.grey.shade600,
        ),
        if (disabled.isEmpty)
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 4, 16, 8),
            child: Text(
              'No repos disabled. Toggle the switch on any repo above to stop auto-reviewing it.',
              style: TextStyle(fontSize: 12, color: Colors.grey.shade500),
            ),
          )
        else
          ..._buildOrgGroups('disabled', disabled),
      ],
    );
  }

  List<Widget> _buildOrgGroups(
    String section,
    List<String> repos,
  ) {
    final groups = _groupByOrg(repos);
    final items = <Widget>[];
    for (final entry in groups.entries) {
      final org = entry.key;
      final orgRepos = entry.value;
      final key = '$section:$org';
      final expanded = _isExpanded(key);

      items.add(_orgHeader(org, orgRepos.length, expanded, () => _toggle(key)));
      if (expanded) {
        for (final r in orgRepos) {
          items.add(RepoListTile(
            repo: r,
            config: widget.configs[r]!,
            appConfig: widget.appConfig,
            selected: widget.selected.contains(r),
            showNew: widget.showNewFor(r, widget.configs[r]!),
            onSelectionToggle: () => widget.onSelectionToggle(r),
            onTap: () {
              widget.onDismissNew(r);
              context.push('/repos/${Uri.encodeComponent(r)}');
            },
          ));
        }
      }
    }
    return items;
  }

  Widget _sectionHeader(
      BuildContext ctx, String label, int count, Color color) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 14, 16, 4),
      child: Row(children: [
        Container(
            width: 8,
            height: 8,
            decoration: BoxDecoration(color: color, shape: BoxShape.circle)),
        const SizedBox(width: 6),
        Text(label,
            style: TextStyle(
                fontSize: 12,
                color: Colors.grey.shade400,
                fontWeight: FontWeight.w600)),
        const SizedBox(width: 6),
        Text('$count',
            style: TextStyle(fontSize: 11, color: Colors.grey.shade500)),
      ]),
    );
  }

  Widget _orgHeader(
      String org, int count, bool expanded, VoidCallback onTap) {
    return InkWell(
      onTap: onTap,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(24, 6, 16, 2),
        child: Row(children: [
          Icon(
            expanded ? Icons.expand_less : Icons.expand_more,
            size: 16,
            color: Colors.grey.shade500,
          ),
          const SizedBox(width: 4),
          Text(org,
              style: TextStyle(
                  fontSize: 12,
                  color: Colors.grey.shade400,
                  fontWeight: FontWeight.w500)),
          const SizedBox(width: 6),
          Text('$count',
              style: TextStyle(fontSize: 11, color: Colors.grey.shade600)),
        ]),
      ),
    );
  }
}

class _ViewToggleButton extends StatelessWidget {
  final IconData icon;
  final bool active;
  final VoidCallback onTap;
  final Key buttonKey;
  const _ViewToggleButton({
    required this.icon,
    required this.active,
    required this.onTap,
    required this.buttonKey,
  });
  @override
  Widget build(BuildContext context) {
    final primary = Theme.of(context).colorScheme.primary;
    return InkWell(
      key: buttonKey,
      onTap: onTap,
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
        color: active ? primary.withOpacity(0.22) : null,
        child: Icon(icon,
            size: 18, color: active ? primary : Colors.grey.shade500),
      ),
    );
  }
}

class _ReposGrid extends StatelessWidget {
  final List<String> repos;
  final Map<String, RepoConfig> configs;
  final AppConfig appConfig;
  final Set<String> selected;
  final ValueChanged<String> onSelectionToggle;
  final bool Function(String repo, RepoConfig config) showNewFor;
  final ValueChanged<String> onDismissNew;

  const _ReposGrid({
    required this.repos,
    required this.configs,
    required this.appConfig,
    required this.selected,
    required this.onSelectionToggle,
    required this.showNewFor,
    required this.onDismissNew,
  });

  @override
  Widget build(BuildContext context) {
    final monitored = repos.where((r) => configs[r]!.isMonitored).toList();
    final disabled = repos.where((r) => !configs[r]!.isMonitored).toList();

    return CustomScrollView(
      slivers: [
        if (monitored.isNotEmpty) ...[
          SliverToBoxAdapter(
            child: Padding(
              padding: const EdgeInsets.fromLTRB(16, 14, 16, 4),
              child: Row(children: [
                Container(
                  width: 8, height: 8,
                  decoration: const BoxDecoration(color: Color(0xFF3FB950), shape: BoxShape.circle),
                ),
                const SizedBox(width: 6),
                Text('MONITORED · ${monitored.length}',
                    style: TextStyle(fontSize: 11.5, color: Colors.grey.shade400, fontWeight: FontWeight.w600)),
              ]),
            ),
          ),
          SliverPadding(
            padding: const EdgeInsets.fromLTRB(16, 4, 16, 12),
            sliver: SliverGrid(
              gridDelegate: const SliverGridDelegateWithMaxCrossAxisExtent(
                maxCrossAxisExtent: 200,
                mainAxisSpacing: 10,
                crossAxisSpacing: 10,
                childAspectRatio: 1.0,
              ),
              delegate: SliverChildBuilderDelegate(
                (ctx, i) {
                  final r = monitored[i];
                  return RepoGridTile(
                    repo: r,
                    config: configs[r]!,
                    appConfig: appConfig,
                    selected: selected.contains(r),
                    showNew: showNewFor(r, configs[r]!),
                    onSelectionToggle: () => onSelectionToggle(r),
                    onTap: () {
                      onDismissNew(r);
                      ctx.push('/repos/${Uri.encodeComponent(r)}');
                    },
                  );
                },
                childCount: monitored.length,
              ),
            ),
          ),
        ],
        if (disabled.isNotEmpty) ...[
          SliverToBoxAdapter(
            child: Padding(
              padding: const EdgeInsets.fromLTRB(16, 8, 16, 4),
              child: Row(children: [
                Container(
                  width: 8, height: 8,
                  decoration: BoxDecoration(color: Colors.grey.shade600, shape: BoxShape.circle),
                ),
                const SizedBox(width: 6),
                Text('NOT MONITORED · ${disabled.length}',
                    style: TextStyle(fontSize: 11.5, color: Colors.grey.shade400, fontWeight: FontWeight.w600)),
              ]),
            ),
          ),
          SliverPadding(
            padding: const EdgeInsets.fromLTRB(16, 4, 16, 20),
            sliver: SliverGrid(
              gridDelegate: const SliverGridDelegateWithMaxCrossAxisExtent(
                maxCrossAxisExtent: 200,
                mainAxisSpacing: 10,
                crossAxisSpacing: 10,
                childAspectRatio: 1.0,
              ),
              delegate: SliverChildBuilderDelegate(
                (ctx, i) {
                  final r = disabled[i];
                  return RepoGridTile(
                    repo: r,
                    config: configs[r]!,
                    appConfig: appConfig,
                    selected: selected.contains(r),
                    showNew: showNewFor(r, configs[r]!),
                    onSelectionToggle: () => onSelectionToggle(r),
                    onTap: () {
                      onDismissNew(r);
                      ctx.push('/repos/${Uri.encodeComponent(r)}');
                    },
                  );
                },
                childCount: disabled.length,
              ),
            ),
          ),
        ],
      ],
    );
  }
}


/// Multi-select org filter chip. Opens a checkbox dialog when tapped.
/// Matches the Activity screen's org filter UX (issue #112 / PR #133).
class _OrgFilterChip extends StatelessWidget {
  final List<String> orgs;
  final Set<String> selected;
  final ValueChanged<Set<String>> onChanged;

  const _OrgFilterChip({
    required this.orgs,
    required this.selected,
    required this.onChanged,
  });

  @override
  Widget build(BuildContext context) {
    final primary = Theme.of(context).colorScheme.primary;
    final isActive = selected.isNotEmpty;
    return GestureDetector(
      onTap: () async {
        final result = await showDialog<Set<String>>(
          context: context,
          builder: (_) => _MultiSelectDialog(
            title: 'Filter by Org',
            items: orgs,
            selected: selected,
          ),
        );
        if (result != null) onChanged(result);
      },
      child: Chip(
        avatar: Icon(Icons.business, size: 14, color: isActive ? primary : Colors.grey),
        label: Text(
          isActive ? 'Org (${selected.length})' : 'Org',
          style: TextStyle(fontSize: 12, color: isActive ? primary : null),
        ),
        visualDensity: VisualDensity.compact,
        side: isActive
            ? BorderSide(color: primary.withOpacity(0.5))
            : const BorderSide(color: Colors.transparent),
      ),
    );
  }
}

/// Simple checkbox-list dialog. Reused from #133; kept here rather than in
/// a shared location because the only caller is the org filter above.
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
