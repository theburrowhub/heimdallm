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

class _ReposScreenState extends ConsumerState<ReposScreen> {
  Map<String, RepoConfig> _repoConfigs = {};
  bool _initialized = false;
  bool _discovering = false;
  String? _discoverError;
  String _search = '';

  void _initFrom(AppConfig config) {
    if (_initialized) return;
    _initialized = true;
    _repoConfigs = Map.from(config.repoConfigs);
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
    } catch (e) {
      if (!mounted) return;
      setState(() { _discovering = false; _discoverError = '$e'; });
    }
  }

  Future<void> _save(AppConfig current) async {
    final updated = current.copyWith(repoConfigs: Map.from(_repoConfigs));
    try {
      await ref.read(configNotifierProvider.notifier).save(updated);
      if (mounted) showToast(context, 'Saved');
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
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
                  const SizedBox(width: 8),
                  FilledButton(
                    onPressed: () => _save(config),
                    child: const Text('Save'),
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
                      onChanged: (repo, rc) =>
                          setState(() => _repoConfigs[repo] = rc),
                    ),
            ),
          ],
        );
      },
    );
  }
}

// ── List with section headers ──────────────────────────────────────────────

class _RepoListWithSections extends ConsumerWidget {
  final List<String> repos;
  final Map<String, RepoConfig> configs;
  final void Function(String repo, RepoConfig rc) onChanged;

  const _RepoListWithSections({
    required this.repos,
    required this.configs,
    required this.onChanged,
  });

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final prompts = ref.watch(agentsProvider).valueOrNull ?? [];
    final monitored = repos.where((r) => configs[r]!.monitored).toList();
    final disabled  = repos.where((r) => !configs[r]!.monitored).toList();

    return ListView(
      padding: const EdgeInsets.symmetric(vertical: 4),
      children: [
        if (monitored.isNotEmpty) ...[
          _header(context, 'Monitored — auto-review enabled', monitored.length,
              Colors.green.shade700),
          ...monitored.map((r) => _RepoTile(
            repo: r,
            config: configs[r]!,
            prompts: prompts,
            onChanged: (rc) => onChanged(r, rc),
          )),
        ],
        if (disabled.isNotEmpty) ...[
          _header(context, 'Watching — PRs visible, no auto-review', disabled.length,
              Colors.grey.shade600),
          ...disabled.map((r) => _RepoTile(
            repo: r,
            config: configs[r]!,
            prompts: prompts,
            onChanged: (rc) => onChanged(r, rc),
          )),
        ],
      ],
    );
  }

  Widget _header(BuildContext ctx, String label, int count, Color color) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
      child: Row(children: [
        Container(width: 8, height: 8,
            decoration: BoxDecoration(color: color, shape: BoxShape.circle)),
        const SizedBox(width: 6),
        Text(label, style: TextStyle(fontSize: 12, color: Colors.grey.shade400,
            fontWeight: FontWeight.w500)),
        const SizedBox(width: 6),
        Text('$count', style: TextStyle(fontSize: 11, color: Colors.grey.shade500)),
      ]),
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
    final subtitle = _buildSubtitle();
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
        subtitle: subtitle != null
            ? Text(subtitle, style: const TextStyle(fontSize: 12))
            : null,
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
        ],
      ),
    );
  }

  String? _buildSubtitle() {
    final parts = <String>[];
    if (config.aiPrimary != null) parts.add('AI: ${config.aiPrimary}');
    if (config.promptId != null) {
      final p = prompts.where((p) => p.id == config.promptId).firstOrNull;
      parts.add('Prompt: ${p?.name ?? config.promptId}');
    }
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

  String _focusEmoji(String f) {
    switch (f) {
      case 'security':     return '🔒';
      case 'performance':  return '⚡';
      case 'architecture': return '🏛️';
      case 'docs':         return '📝';
      default:             return '🔍';
    }
  }
}
