import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:shared_preferences/shared_preferences.dart';
import '../../core/models/config_model.dart';
import '../../shared/widgets/toast.dart';
import '../config/config_providers.dart';

class ReposScreen extends ConsumerStatefulWidget {
  const ReposScreen({super.key});

  @override
  ConsumerState<ReposScreen> createState() => _ReposScreenState();
}

enum _SyncStatus { idle, saving, saved }
enum _FilterMode { all, monitored, notMonitored }
enum _ViewMode { list, grid }

class _ReposScreenState extends ConsumerState<ReposScreen> {
  Map<String, RepoConfig> _repoConfigs = {};
  bool _initialized = false;
  String _search = '';
  String _orgFilter = ''; // empty = all orgs
  _SyncStatus _syncStatus = _SyncStatus.idle;
  _FilterMode _filterMode = _FilterMode.all;
  _ViewMode _viewMode = _ViewMode.list;
  Timer? _debounce;
  Timer? _savedResetTimer;

  @override
  void initState() {
    super.initState();
    _loadViewPreference();
  }

  @override
  void dispose() {
    _debounce?.cancel();
    _savedResetTimer?.cancel();
    super.dispose();
  }

  void _loadViewPreference() async {
    try {
      final prefs = await SharedPreferences.getInstance();
      final value = prefs.getString('repos_view_mode');
      if (value == 'grid' && mounted) setState(() => _viewMode = _ViewMode.grid);
    } catch (_) {}
  }

  void _setViewMode(_ViewMode mode) {
    setState(() => _viewMode = mode);
    SharedPreferences.getInstance().then((prefs) {
      prefs.setString('repos_view_mode', mode.name);
    }).catchError((_) {});
  }

  void _initFrom(AppConfig config) {
    if (_initialized) return;
    _initialized = true;
    _repoConfigs = Map.from(config.repoConfigs);
  }

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

        // Sort: monitored first, then alphabetical
        final allRepos = _repoConfigs.keys.toList()
          ..sort((a, b) {
            final ma = _repoConfigs[a]!.isMonitored ? 0 : 1;
            final mb = _repoConfigs[b]!.isMonitored ? 0 : 1;
            if (ma != mb) return ma.compareTo(mb);
            return a.compareTo(b);
          });

        // Derive all orgs for the org filter dropdown
        final allOrgs = _repoConfigs.keys
            .map((r) => r.contains('/') ? r.split('/').first : r)
            .toSet()
            .toList()..sort();

        // Apply text search
        var filtered = _search.isEmpty
            ? allRepos
            : allRepos.where((r) => r.toLowerCase().contains(_search.toLowerCase())).toList();

        // Apply org filter
        if (_orgFilter.isNotEmpty) {
          filtered = filtered.where((r) {
            final org = r.contains('/') ? r.split('/').first : r;
            return org == _orgFilter;
          }).toList();
        }

        // Apply monitored/not-monitored filter
        if (_filterMode == _FilterMode.monitored) {
          filtered = filtered.where((r) => _repoConfigs[r]!.isMonitored).toList();
        } else if (_filterMode == _FilterMode.notMonitored) {
          filtered = filtered.where((r) => !_repoConfigs[r]!.isMonitored).toList();
        }

        final monitoredCount = _repoConfigs.values.where((c) => c.isMonitored).length;
        final notMonitoredCount = _repoConfigs.length - monitoredCount;

        return Column(
          children: [
            // Single-line toolbar: search + filters + view toggle + status
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 10, 16, 6),
              child: Row(
                children: [
                  // Compact search
                  SizedBox(
                    width: 160,
                    child: TextField(
                      decoration: const InputDecoration(
                        hintText: 'Search…',
                        prefixIcon: Icon(Icons.search, size: 16),
                        isDense: true,
                        border: OutlineInputBorder(),
                        contentPadding: EdgeInsets.symmetric(vertical: 6),
                      ),
                      style: const TextStyle(fontSize: 13),
                      onChanged: (v) => setState(() => _search = v),
                    ),
                  ),
                  const SizedBox(width: 8),
                  // Monitored filter chips
                  _filterChip('All', _repoConfigs.length, _FilterMode.all),
                  const SizedBox(width: 4),
                  _filterChip('Monitored', monitoredCount, _FilterMode.monitored),
                  const SizedBox(width: 4),
                  _filterChip('Not mon.', notMonitoredCount, _FilterMode.notMonitored),
                  const SizedBox(width: 8),
                  // Org dropdown
                  if (allOrgs.length > 1)
                    SizedBox(
                      height: 32,
                      child: DropdownButton<String>(
                        value: _orgFilter.isEmpty ? null : _orgFilter,
                        hint: const Text('Org', style: TextStyle(fontSize: 12)),
                        style: TextStyle(fontSize: 12, color: Theme.of(context).colorScheme.onSurface),
                        isDense: true,
                        underline: const SizedBox.shrink(),
                        items: [
                          const DropdownMenuItem(value: '', child: Text('All orgs')),
                          ...allOrgs.map((o) => DropdownMenuItem(value: o, child: Text(o))),
                        ],
                        onChanged: (v) => setState(() => _orgFilter = v ?? ''),
                      ),
                    ),
                  const Spacer(),
                  // View toggle
                  SegmentedButton<_ViewMode>(
                    segments: const [
                      ButtonSegment(value: _ViewMode.list, icon: Icon(Icons.view_list, size: 16)),
                      ButtonSegment(value: _ViewMode.grid, icon: Icon(Icons.grid_view, size: 16)),
                    ],
                    selected: {_viewMode},
                    onSelectionChanged: (s) => _setViewMode(s.first),
                    showSelectedIcon: false,
                    style: const ButtonStyle(
                      visualDensity: VisualDensity.compact,
                      tapTargetSize: MaterialTapTargetSize.shrinkWrap,
                    ),
                  ),
                  const SizedBox(width: 6),
                  // Auto-save status
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
            // Content
            Expanded(
              child: filtered.isEmpty
                  ? Center(child: Text(
                      _repoConfigs.isEmpty
                          ? 'No repos yet — the daemon auto-discovers repos from your PRs.'
                          : 'No repos match the current filter.',
                      style: TextStyle(color: Colors.grey.shade500),
                    ))
                  : _viewMode == _ViewMode.list
                      ? _RepoListWithSections(
                          repos: filtered,
                          configs: _repoConfigs,
                          appConfig: config,
                          onChanged: _onChange,
                        )
                      : _RepoGrid(
                          repos: filtered,
                          configs: _repoConfigs,
                          appConfig: config,
                        ),
            ),
          ],
        );
      },
    );
  }

  Widget _filterChip(String label, int count, _FilterMode mode) {
    final active = _filterMode == mode;
    return FilterChip(
      label: Text('$label ($count)', style: const TextStyle(fontSize: 12)),
      selected: active,
      onSelected: (_) => setState(() => _filterMode = mode),
      visualDensity: VisualDensity.compact,
      showCheckmark: false,
    );
  }
}

// ── Grid view ────────────────────────────────────────────────────────────────

class _RepoGrid extends StatelessWidget {
  final List<String> repos;
  final Map<String, RepoConfig> configs;
  final AppConfig appConfig;

  const _RepoGrid({
    required this.repos,
    required this.configs,
    required this.appConfig,
  });

  @override
  Widget build(BuildContext context) {
    return LayoutBuilder(
      builder: (context, constraints) {
        final crossCount = constraints.maxWidth > 900 ? 4
            : constraints.maxWidth > 600 ? 3
            : 2;
        return GridView.builder(
          padding: const EdgeInsets.all(12),
          gridDelegate: SliverGridDelegateWithFixedCrossAxisCount(
            crossAxisCount: crossCount,
            mainAxisSpacing: 8,
            crossAxisSpacing: 8,
            childAspectRatio: 2.0,
          ),
          itemCount: repos.length,
          itemBuilder: (context, i) {
            final repo = repos[i];
            final config = configs[repo]!;
            final shortName = repo.contains('/') ? repo.split('/').last : repo;
            final org = repo.contains('/') ? repo.split('/').first : '';
            final configuredDir = (config.localDir ?? '').isNotEmpty ? config.localDir : null;
            final detectedDir = appConfig.localDirsDetected[repo];
            final effectiveDir = configuredDir ?? detectedDir;
            final hasDirMapping = effectiveDir != null && effectiveDir.isNotEmpty;
            final isAutoDetected = configuredDir == null && detectedDir != null;

            return Card(
              child: InkWell(
                borderRadius: BorderRadius.circular(12),
                onTap: () => context.push('/repos/${Uri.encodeComponent(repo)}'),
                child: Padding(
                  padding: const EdgeInsets.all(10),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    mainAxisAlignment: MainAxisAlignment.center,
                    children: [
                      Row(
                        children: [
                          _Led(
                            status: config.prLedStatus(appConfig.repositories.contains(repo)),
                            tooltip: 'PR',
                          ),
                          const SizedBox(width: 3),
                          _Led(
                            status: config.itLedStatus(appConfig.issueTracking.enabled),
                            tooltip: 'IT',
                          ),
                          const SizedBox(width: 3),
                          _Led(
                            status: config.devLedStatus(appConfig.issueTracking.enabled, hasDirMapping),
                            tooltip: 'Dev',
                          ),
                          const Spacer(),
                          Icon(Icons.chevron_right, size: 14, color: Colors.grey.shade600),
                        ],
                      ),
                      const SizedBox(height: 6),
                      Text(shortName,
                          style: TextStyle(
                            fontSize: 13,
                            fontWeight: config.isMonitored ? FontWeight.w600 : FontWeight.normal,
                            color: config.isMonitored ? null : Colors.grey,
                          ),
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis),
                      if (org.isNotEmpty)
                        Text(org,
                            style: TextStyle(fontSize: 11, color: Colors.grey.shade500),
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis),
                      const SizedBox(height: 2),
                      Row(children: [
                        Icon(
                          hasDirMapping ? Icons.folder : Icons.folder_off_outlined,
                          size: 11,
                          color: hasDirMapping
                              ? (isAutoDetected ? Colors.blue.shade400 : Colors.green.shade500)
                              : Colors.grey.shade600,
                        ),
                        const SizedBox(width: 3),
                        Expanded(
                          child: Text(
                            hasDirMapping
                                ? (isAutoDetected
                                    ? 'Auto: ${effectiveDir.split('/').last}'
                                    : effectiveDir.split('/').last)
                                : 'No local dir',
                            style: TextStyle(
                              fontSize: 10,
                              color: hasDirMapping
                                  ? (isAutoDetected ? Colors.blue.shade400 : Colors.green.shade500)
                                  : Colors.grey.shade600,
                            ),
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                          ),
                        ),
                      ]),
                    ],
                  ),
                ),
              ),
            );
          },
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

  const _RepoListWithSections({
    required this.repos,
    required this.configs,
    required this.appConfig,
    required this.onChanged,
  });

  @override
  ConsumerState<_RepoListWithSections> createState() =>
      _RepoListWithSectionsState();
}

class _RepoListWithSectionsState extends ConsumerState<_RepoListWithSections> {
  final _expanded = <String, bool>{};

  bool _isExpanded(String key) => _expanded[key] ?? true;
  void _toggle(String key) =>
      setState(() => _expanded[key] = !_isExpanded(key));

  Map<String, List<String>> _groupByOrg(List<String> repos) {
    final groups = <String, List<String>>{};
    for (final r in repos) {
      final org = r.contains('/') ? r.split('/').first : r;
      groups.putIfAbsent(org, () => []).add(r);
    }
    for (final list in groups.values) {
      list.sort();
    }
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
          _sectionHeader(context, 'Monitored — auto-review enabled',
              monitored.length, Colors.green.shade700),
          ..._buildOrgGroups('monitored', monitored),
        ],
        if (disabled.isNotEmpty) ...[
          _sectionHeader(context, 'Not monitored — PRs visible, no auto-review',
              disabled.length, Colors.grey.shade600),
          ..._buildOrgGroups('disabled', disabled),
        ],
      ],
    );
  }

  List<Widget> _buildOrgGroups(String section, List<String> repos) {
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
          items.add(_RepoTile(
            repo: r,
            config: widget.configs[r]!,
            appConfig: widget.appConfig,
          ));
        }
      }
    }
    return items;
  }

  Widget _sectionHeader(BuildContext ctx, String label, int count, Color color) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 14, 16, 4),
      child: Row(children: [
        Container(
            width: 8, height: 8,
            decoration: BoxDecoration(color: color, shape: BoxShape.circle)),
        const SizedBox(width: 6),
        Text(label,
            style: TextStyle(
                fontSize: 12, color: Colors.grey.shade400, fontWeight: FontWeight.w600)),
        const SizedBox(width: 6),
        Text('$count',
            style: TextStyle(fontSize: 11, color: Colors.grey.shade500)),
      ]),
    );
  }

  Widget _orgHeader(String org, int count, bool expanded, VoidCallback onTap) {
    return InkWell(
      onTap: onTap,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(24, 6, 16, 2),
        child: Row(children: [
          Icon(expanded ? Icons.expand_less : Icons.expand_more,
              size: 16, color: Colors.grey.shade500),
          const SizedBox(width: 4),
          Text(org,
              style: TextStyle(
                  fontSize: 12, color: Colors.grey.shade400, fontWeight: FontWeight.w500)),
          const SizedBox(width: 6),
          Text('$count',
              style: TextStyle(fontSize: 11, color: Colors.grey.shade600)),
        ]),
      ),
    );
  }
}

// ── LED indicator ────────────────────────────────────────────────────────────

class _Led extends StatelessWidget {
  final String status;
  final String tooltip;
  const _Led({required this.status, required this.tooltip});

  @override
  Widget build(BuildContext context) {
    final color = switch (status) {
      'repo'   => Colors.green.shade500,
      'global' => Colors.blue.shade500,
      _        => Colors.red.shade800,
    };
    return Tooltip(
      message: '$tooltip: ${_label()}',
      child: Container(
        width: 8, height: 8,
        decoration: BoxDecoration(color: color, shape: BoxShape.circle),
      ),
    );
  }

  String _label() => switch (status) {
    'repo'   => 'active (repo)',
    'global' => 'active (global)',
    _        => 'inactive',
  };
}

// ── Repo tile ─────────────────────────────────────────────────────────────

class _RepoTile extends StatelessWidget {
  final String repo;
  final RepoConfig config;
  final AppConfig appConfig;

  const _RepoTile({
    required this.repo,
    required this.config,
    required this.appConfig,
  });

  @override
  Widget build(BuildContext context) {
    final configuredDir = (config.localDir ?? '').isNotEmpty ? config.localDir : null;
    final detectedDir = appConfig.localDirsDetected[repo];
    final effectiveDir = configuredDir ?? detectedDir;
    final hasDirMapping = effectiveDir != null && effectiveDir.isNotEmpty;
    final isAutoDetected = configuredDir == null && detectedDir != null;

    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
      child: InkWell(
        borderRadius: BorderRadius.circular(12),
        onTap: () => context.push('/repos/${Uri.encodeComponent(repo)}'),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
          child: Row(
            children: [
              Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  _Led(
                    status: config.prLedStatus(appConfig.repositories.contains(repo)),
                    tooltip: 'PR Review',
                  ),
                  const SizedBox(height: 3),
                  _Led(
                    status: config.itLedStatus(appConfig.issueTracking.enabled),
                    tooltip: 'Issue Tracking',
                  ),
                  const SizedBox(height: 3),
                  _Led(
                    status: config.devLedStatus(
                        appConfig.issueTracking.enabled, hasDirMapping),
                    tooltip: 'Develop',
                  ),
                ],
              ),
              const SizedBox(width: 10),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(repo,
                        style: TextStyle(
                          fontWeight: config.isMonitored ? FontWeight.w600 : FontWeight.normal,
                          color: config.isMonitored ? null : Colors.grey,
                        )),
                    const SizedBox(height: 2),
                    Row(children: [
                      Icon(
                        hasDirMapping ? Icons.folder : Icons.folder_off_outlined,
                        size: 13,
                        color: hasDirMapping
                            ? (isAutoDetected ? Colors.blue.shade400 : Colors.green.shade500)
                            : Colors.grey.shade600,
                      ),
                      const SizedBox(width: 4),
                      Text(
                        hasDirMapping
                            ? (isAutoDetected
                                ? 'Auto: ${effectiveDir.split('/').last}'
                                : effectiveDir.split('/').last)
                            : 'No local dir',
                        style: TextStyle(
                          fontSize: 11,
                          color: hasDirMapping
                              ? (isAutoDetected ? Colors.blue.shade400 : Colors.green.shade500)
                              : Colors.grey.shade600,
                        ),
                      ),
                    ]),
                  ],
                ),
              ),
              Icon(Icons.chevron_right, size: 18, color: Colors.grey.shade600),
            ],
          ),
        ),
      ),
    );
  }
}
