import 'dart:async';
import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/models/config_model.dart';
import '../../core/models/agent.dart';
import '../../core/setup/first_run_setup.dart';
import '../../core/setup/repo_discovery.dart';
import '../../shared/widgets/toast.dart';
import '../agents/agents_screen.dart' show agentsProvider;
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
      final token = await FirstRunSetup.detectToken();
      final discovered = await RepoDiscovery.discoverFromPRs(token ?? '');
      if (!mounted) return;
      setState(() {
        for (final repo in discovered) {
          _repoConfigs.putIfAbsent(repo, () => const RepoConfig(monitored: true));
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
            final ma = _repoConfigs[a]!.monitored ? 0 : 1;
            final mb = _repoConfigs[b]!.monitored ? 0 : 1;
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
  final void Function(String repo, RepoConfig rc) onChanged;

  const _RepoListWithSections({
    required this.repos,
    required this.configs,
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
    final prompts = ref.watch(agentsProvider).valueOrNull ?? [];
    final monitored =
        widget.repos.where((r) => widget.configs[r]!.monitored).toList();
    final disabled =
        widget.repos.where((r) => !widget.configs[r]!.monitored).toList();

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
          ..._buildOrgGroups('monitored', monitored, prompts),
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
          ..._buildOrgGroups('disabled', disabled, prompts),
      ],
    );
  }

  List<Widget> _buildOrgGroups(
    String section,
    List<String> repos,
    List<ReviewPrompt> prompts,
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
            prompts: prompts,
            onChanged: (rc) => widget.onChanged(r, rc),
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

// ── Repo tile ─────────────────────────────────────────────────────────────

class _RepoTile extends StatelessWidget {
  final String repo;
  final RepoConfig config;
  final List<ReviewPrompt> prompts;
  final ValueChanged<RepoConfig> onChanged;

  const _RepoTile({
    required this.repo,
    required this.config,
    required this.prompts,
    required this.onChanged,
  });

  @override
  Widget build(BuildContext context) {
    final overrides = _buildOverrides();
    final hasDirMapping = config.localDir != null && config.localDir!.isNotEmpty;
    final dirLabel = hasDirMapping
        ? config.localDir!.split('/').last
        : null;

    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
      child: ExpansionTile(
        leading: Switch(
          value: config.monitored,
          onChanged: (v) => onChanged(config.copyWith(monitored: v)),
        ),
        title: Text(repo,
            style: TextStyle(
              fontWeight: config.monitored ? FontWeight.w600 : FontWeight.normal,
              color: config.monitored ? null : Colors.grey,
            )),
        subtitle: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            if (overrides != null)
              Text(overrides, style: const TextStyle(fontSize: 12)),
            const SizedBox(height: 2),
            // Directory mapping — always visible
            Row(children: [
              Icon(
                hasDirMapping ? Icons.folder : Icons.folder_off_outlined,
                size: 13,
                color: hasDirMapping ? Colors.green.shade500 : Colors.grey.shade600,
              ),
              const SizedBox(width: 4),
              Text(
                hasDirMapping ? dirLabel! : 'No local dir',
                style: TextStyle(
                  fontSize: 11,
                  color: hasDirMapping ? Colors.green.shade500 : Colors.grey.shade600,
                  fontStyle: hasDirMapping ? FontStyle.normal : FontStyle.italic,
                ),
              ),
            ]),
          ],
        ),
        childrenPadding: const EdgeInsets.fromLTRB(16, 0, 16, 14),
        children: [
          const Divider(height: 1),
          const SizedBox(height: 10),
          // AI overrides
          const Text('AI agent override',
              style: TextStyle(fontSize: 12, color: Colors.grey)),
          const SizedBox(height: 8),
          Row(children: [
            Expanded(child: _aiDropdown('Primary', config.aiPrimary,
                (v) => onChanged(config.copyWith(aiPrimary: v)))),
            const SizedBox(width: 10),
            Expanded(child: _aiDropdown('Fallback', config.aiFallback,
                (v) => onChanged(config.copyWith(aiFallback: v)))),
          ]),
          const SizedBox(height: 12),
          // Prompt override
          const Text('Review prompt',
              style: TextStyle(fontSize: 12, color: Colors.grey)),
          const SizedBox(height: 8),
          _promptDropdown(),
          const SizedBox(height: 12),
          // Review mode override
          const Text('Feedback mode',
              style: TextStyle(fontSize: 12, color: Colors.grey)),
          const SizedBox(height: 8),
          DropdownButtonFormField<String?>(
            // ignore: deprecated_member_use
            value: config.reviewMode,
            decoration: const InputDecoration(
                labelText: 'Override mode',
                border: OutlineInputBorder(), isDense: true),
            items: const [
              DropdownMenuItem<String?>(value: null,     child: Text('Global')),
              DropdownMenuItem<String?>(value: 'single', child: Text('Single (consolidated review)')),
              DropdownMenuItem<String?>(value: 'multi',  child: Text('Multi (one comment per issue)')),
            ],
            onChanged: (v) => onChanged(config.copyWith(reviewMode: v)),
          ),
          const SizedBox(height: 12),
          // Local directory for full-repo analysis
          const Text('Local directory',
              style: TextStyle(fontSize: 12, color: Colors.grey)),
          const SizedBox(height: 4),
          Text(
            'When set, the AI agent runs inside this directory and can read all project files.',
            style: TextStyle(fontSize: 11, color: Colors.grey.shade600),
          ),
          const SizedBox(height: 8),
          _LocalDirField(
            value: config.localDir ?? '',
            onChanged: (dir) => onChanged(config.copyWith(localDir: dir.isEmpty ? null : dir)),
          ),
        ],
      ),
    );
  }

  String? _buildOverrides() {
    final parts = <String>[];
    if (config.aiPrimary != null) parts.add('AI: ${config.aiPrimary}');
    if (config.promptId != null) {
      final p = prompts.where((p) => p.id == config.promptId).firstOrNull;
      parts.add('Prompt: ${p?.name ?? config.promptId}');
    }
    if (config.reviewMode != null) parts.add('Mode: ${config.reviewMode}');
    return parts.isEmpty ? null : parts.join(' · ');
  }

  Widget _aiDropdown(String label, String? value, ValueChanged<String?> onChanged) {
    return DropdownButtonFormField<String?>(
      // ignore: deprecated_member_use
      value: value,
      decoration: InputDecoration(labelText: label,
          border: const OutlineInputBorder(), isDense: true),
      items: const [
        DropdownMenuItem<String?>(value: null, child: Text('Global')),
        DropdownMenuItem<String?>(value: 'claude', child: Text('claude')),
        DropdownMenuItem<String?>(value: 'gemini', child: Text('gemini')),
        DropdownMenuItem<String?>(value: 'codex',  child: Text('codex')),
      ],
      onChanged: onChanged,
    );
  }

  Widget _promptDropdown() {
    return DropdownButtonFormField<String?>(
      // ignore: deprecated_member_use
      value: config.promptId,
      decoration: const InputDecoration(
          labelText: 'Override prompt',
          border: OutlineInputBorder(), isDense: true),
      items: [
        const DropdownMenuItem<String?>(value: null,
            child: Text('Global active prompt')),
        ...prompts.map((p) => DropdownMenuItem<String?>(
          value: p.id,
          child: Text('${_focusEmoji(p.focus)} ${p.name}'),
        )),
      ],
      onChanged: (v) => onChanged(config.copyWith(promptId: v)),
    );
  }

}

// ── Local directory picker ─────────────────────────────────────────────────

class _LocalDirField extends StatefulWidget {
  final String value;
  final ValueChanged<String> onChanged;
  const _LocalDirField({required this.value, required this.onChanged});

  @override
  State<_LocalDirField> createState() => _LocalDirFieldState();
}

class _LocalDirFieldState extends State<_LocalDirField> {
  late final TextEditingController _ctrl;

  @override
  void initState() {
    super.initState();
    _ctrl = TextEditingController(text: widget.value);
  }

  @override
  void dispose() {
    _ctrl.dispose();
    super.dispose();
  }

  Future<void> _pick() async {
    final dir = await FilePicker.platform.getDirectoryPath(
      dialogTitle: 'Select local repository directory',
      lockParentWindow: true,
    );
    if (dir == null) return;
    setState(() => _ctrl.text = dir);
    widget.onChanged(dir);
  }

  @override
  Widget build(BuildContext context) {
    return Row(children: [
      Expanded(
        child: TextFormField(
          controller: _ctrl,
          decoration: const InputDecoration(
            hintText: '/path/to/local/repo',
            border: OutlineInputBorder(),
            isDense: true,
          ),
          onChanged: widget.onChanged,
        ),
      ),
      const SizedBox(width: 8),
      OutlinedButton.icon(
        icon: const Icon(Icons.folder_open, size: 16),
        label: const Text('Browse'),
        onPressed: _pick,
        style: OutlinedButton.styleFrom(
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 12),
        ),
      ),
      if (_ctrl.text.isNotEmpty) ...[
        const SizedBox(width: 4),
        IconButton(
          icon: const Icon(Icons.clear, size: 16),
          tooltip: 'Clear',
          onPressed: () {
            setState(() => _ctrl.clear());
            widget.onChanged('');
          },
        ),
      ],
    ]);
  }
}

// ── (end of _LocalDirField) ────────────────────────────────────────────────

String _focusEmoji(String f) {
  switch (f) {
    case 'security':     return '🔒';
    case 'performance':  return '⚡';
    case 'architecture': return '🏛️';
    case 'docs':         return '📝';
    default:             return '🔍';
  }
}
