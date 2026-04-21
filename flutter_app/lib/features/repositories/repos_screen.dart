import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/models/config_model.dart';
import '../../core/platform/platform_services_provider.dart';
import '../../shared/widgets/toast.dart';
import '../config/config_providers.dart';

class ReposScreen extends ConsumerStatefulWidget {
  const ReposScreen({super.key});

  @override
  ConsumerState<ReposScreen> createState() => _ReposScreenState();
}

enum _SyncStatus { idle, saving, saved }

class _ReposScreenState extends ConsumerState<ReposScreen> {
  Map<String, RepoConfig> _repoConfigs = {};
  bool _initialized = false;
  bool _discovering = false;
  String? _discoverError;
  String _search = '';
  _SyncStatus _syncStatus = _SyncStatus.idle;
  Timer? _debounce;
  Timer? _savedResetTimer;

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

  Future<void> _discover() async {
    setState(() { _discovering = true; _discoverError = null; });
    try {
      final token = await ref.read(platformServicesProvider).detectGitHubToken();
      final discovered = await ref.read(platformServicesProvider).discoverReposFromPRs(token ?? '');
      if (!mounted) return;
      setState(() {
        for (final repo in discovered) {
          _repoConfigs.putIfAbsent(repo, () => const RepoConfig(prEnabled: true));
        }
        _discovering = false;
        if (discovered.isEmpty) _discoverError = 'No active PRs found.';
      });
      _debounce?.cancel();
      _debounce = Timer(const Duration(milliseconds: 400), _autoSave);
    } catch (e) {
      if (!mounted) return;
      setState(() { _discovering = false; _discoverError = '$e'; });
    }
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
        final filtered = _search.isEmpty
            ? allRepos
            : allRepos.where((r) => r.toLowerCase().contains(_search.toLowerCase())).toList();

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
                  FilledButton.tonalIcon(
                    icon: _discovering
                        ? const SizedBox(width: 14, height: 14,
                            child: CircularProgressIndicator(strokeWidth: 2))
                        : const Icon(Icons.sync, size: 16),
                    label: const Text('Discover'),
                    onPressed: _discovering ? null : _discover,
                  ),
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
            if (_discoverError != null)
              Padding(
                padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 4),
                child: Text(_discoverError!, style: const TextStyle(color: Colors.orange)),
              ),
            // Repo list with section dividers
            Expanded(
              child: filtered.isEmpty
                  ? const Center(child: Text('No repos yet. Tap Discover.'))
                  : _RepoListWithSections(
                      repos: filtered,
                      configs: _repoConfigs,
                      appConfig: config,
                      onChanged: _onChange,
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

// ── LED indicator ────────────────────────────────────────────────────────────

class _Led extends StatelessWidget {
  final String status; // 'off', 'global', 'repo'
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
    final hasDirMapping = config.localDir != null && config.localDir!.isNotEmpty;

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
                        color: hasDirMapping ? Colors.green.shade500 : Colors.grey.shade600,
                      ),
                      const SizedBox(width: 4),
                      Text(
                        hasDirMapping
                            ? config.localDir!.split('/').last
                            : 'No local dir',
                        style: TextStyle(
                          fontSize: 11,
                          color: hasDirMapping ? Colors.green.shade500 : Colors.grey.shade600,
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
