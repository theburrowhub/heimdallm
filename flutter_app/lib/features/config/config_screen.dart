import 'dart:io';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/daemon/daemon_lifecycle.dart';
import '../../core/models/config_model.dart';
import '../../core/setup/first_run_setup.dart';
import '../../core/setup/gh_cli.dart';
import '../../core/setup/repo_discovery.dart';
import '../../shared/widgets/toast.dart';
import 'config_providers.dart';

const _aiOptions = ['claude', 'gemini', 'codex'];

class ConfigScreen extends ConsumerStatefulWidget {
  const ConfigScreen({super.key});

  @override
  ConsumerState<ConfigScreen> createState() => _ConfigScreenState();
}

class _ConfigScreenState extends ConsumerState<ConfigScreen> {
  final _tokenController = TextEditingController();
  bool _obscureToken = true;
  bool _tokenFromGh = false; // true = auto-detected from gh CLI

  String _pollInterval = '5m';
  String _aiPrimary = 'claude';
  String _aiFallback = '';
  int _retentionDays = 90;

  // All known repos. Key = "org/repo", Value = per-repo settings.
  Map<String, RepoConfig> _repoConfigs = {};

  bool _initialized = false;
  bool _discovering = false;
  String? _discoverError;

  @override
  void initState() {
    super.initState();
    _detectToken().then((_) => _autoDiscoverRepos());
  }

  @override
  void dispose() {
    _tokenController.dispose();
    super.dispose();
  }

  Future<void> _detectToken() async {
    // 1. Try gh CLI first — if it works, show the chip (no manual entry needed)
    final ghToken = await _getGhCliToken();
    if (!mounted) return;
    if (ghToken != null) {
      setState(() {
        _tokenController.text = ghToken;
        _tokenFromGh = true;
      });
      return;
    }

    // 2. Fall back to Keychain / GITHUB_TOKEN env var
    final stored = await FirstRunSetup.getToken()
        ?? Platform.environment['GITHUB_TOKEN'];
    if (!mounted || stored == null || stored.isEmpty) return;
    setState(() => _tokenController.text = stored);
  }

  Future<String?> _getGhCliToken() => GhCli.authToken();

  void _initFromConfig(AppConfig config) {
    if (_initialized) return;
    _initialized = true;
    _pollInterval = config.pollInterval;
    _aiPrimary = config.aiPrimary;
    _aiFallback = config.aiFallback;
    _retentionDays = config.retentionDays;
    _repoConfigs = Map.from(config.repoConfigs);
  }

  /// Auto-discovers repos from the user's PRs. Runs silently on init.
  Future<void> _autoDiscoverRepos() async {
    final token = _tokenController.text.trim();
    if (token.isEmpty) return;
    if (!mounted) return;
    setState(() { _discovering = true; _discoverError = null; });
    try {
      final discovered = await RepoDiscovery.discoverFromPRs(token);
      if (!mounted) return;
      setState(() {
        for (final repo in discovered) {
          // Keep existing toggle state; default new ones to monitored
          _repoConfigs.putIfAbsent(repo, () => const RepoConfig(monitored: true));
        }
        _discovering = false;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _discovering = false;
        _discoverError = 'Could not discover repos: $e';
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final configAsync = ref.watch(configNotifierProvider);
    final daemonRunning = ref.watch(daemonHealthProvider).valueOrNull ?? false;

    return Scaffold(
      appBar: AppBar(
        title: const Text('Settings'),
        leading: IconButton(
            icon: const Icon(Icons.arrow_back),
            onPressed: () => context.canPop() ? context.pop() : context.go('/')),
      ),
      body: configAsync.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (_, __) => _buildForm(context, const AppConfig(), daemonRunning),
        data: (config) {
          _initFromConfig(config);
          return _buildForm(context, config, daemonRunning);
        },
      ),
    );
  }

  Widget _buildForm(BuildContext context, AppConfig config, bool daemonRunning) {
    return SingleChildScrollView(
      padding: const EdgeInsets.all(24),
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 680),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            if (!daemonRunning) _setupBanner(),
            _tokenSection(),
            const SizedBox(height: 20),
            if (!daemonRunning) ...[_repoSection(), const SizedBox(height: 20)],
            _globalSection(),
            const SizedBox(height: 20),
            _retentionSection(),
            const SizedBox(height: 28),
            _saveButton(context, config, daemonRunning),
          ],
        ),
      ),
    );
  }

  // ── Token ───────────────────────────────────────────────────────────────

  Widget _tokenSection() {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _sectionHeader('GitHub Token'),
        if (_tokenFromGh)
          _infoChip(
            Icons.check_circle,
            'Auto-detected from gh CLI',
            Colors.green,
          )
        else
          TextFormField(
            controller: _tokenController,
            obscureText: _obscureToken,
            decoration: InputDecoration(
              labelText: 'Personal Access Token',
              hintText: 'ghp_...',
              helperText: 'Required scopes: repo, read:org',
              border: const OutlineInputBorder(),
              suffixIcon: IconButton(
                icon: Icon(_obscureToken ? Icons.visibility : Icons.visibility_off),
                onPressed: () => setState(() => _obscureToken = !_obscureToken),
              ),
            ),
          ),
        if (_tokenFromGh)
          TextButton.icon(
            icon: const Icon(Icons.edit, size: 14),
            label: const Text('Use a different token'),
            onPressed: () => setState(() {
              _tokenFromGh = false;
              _tokenController.clear();
            }),
          ),
      ],
    );
  }

  // ── Repos ───────────────────────────────────────────────────────────────

  Widget _repoSection() {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            _sectionHeaderInline('Repos with active PRs'),
            if (_discovering) ...[
              const SizedBox(width: 10),
              const SizedBox(
                width: 14, height: 14,
                child: CircularProgressIndicator(strokeWidth: 2),
              ),
            ],
          ],
        ),
        if (_discoverError != null) ...[
          const SizedBox(height: 6),
          _infoChip(Icons.warning_amber, _discoverError!, Colors.orange),
        ],
        const SizedBox(height: 8),
        if (_repoConfigs.isEmpty && !_discovering)
          const Padding(
            padding: EdgeInsets.symmetric(vertical: 8),
            child: Text(
              'No active PRs found assigned to you.',
              style: TextStyle(color: Colors.grey),
            ),
          )
        else
          _repoList(),
      ],
    );
  }

  Widget _repoList() {
    final sorted = _repoConfigs.keys.toList()..sort();
    return Column(
      children: sorted.map((repo) => _repoTile(repo)).toList(),
    );
  }

  Widget _repoTile(String repo) {
    final rc = _repoConfigs[repo]!;
    return Card(
      margin: const EdgeInsets.only(bottom: 4),
      child: ExpansionTile(
        leading: Switch(
          value: rc.monitored,
          onChanged: (v) => setState(() {
            _repoConfigs[repo] = rc.copyWith(monitored: v);
          }),
        ),
        title: Text(repo,
            style: TextStyle(
              color: rc.monitored ? null : Colors.grey,
              fontWeight: rc.monitored ? FontWeight.w600 : FontWeight.normal,
            )),
        subtitle: rc.hasAiOverride
            ? Text('AI: ${rc.aiPrimary ?? "global"}', style: const TextStyle(fontSize: 12))
            : null,
        childrenPadding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
        children: [
          const Divider(height: 1),
          const SizedBox(height: 10),
          const Text('AI overrides for this repo',
              style: TextStyle(fontSize: 12, color: Colors.grey)),
          const SizedBox(height: 8),
          Row(
            children: [
              Expanded(
                child: _overrideDropdown(
                  label: 'Primary agent',
                  value: rc.aiPrimary,
                  onChanged: (v) => setState(() {
                    _repoConfigs[repo] = rc.copyWith(aiPrimary: v);
                  }),
                ),
              ),
              const SizedBox(width: 12),
              Expanded(
                child: _overrideDropdown(
                  label: 'Fallback',
                  value: rc.aiFallback,
                  onChanged: (v) => setState(() {
                    _repoConfigs[repo] = rc.copyWith(aiFallback: v);
                  }),
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }

  Widget _overrideDropdown({
    required String label,
    required String? value,
    required ValueChanged<String?> onChanged,
  }) {
    return DropdownButtonFormField<String?>(
      // ignore: deprecated_member_use
      value: value,
      decoration: InputDecoration(
        labelText: label,
        border: const OutlineInputBorder(),
        isDense: true,
      ),
      items: [
        const DropdownMenuItem<String?>(value: null, child: Text('Global (no override)')),
        ..._aiOptions.map((v) => DropdownMenuItem<String?>(value: v, child: Text(v))),
      ],
      onChanged: onChanged,
    );
  }

  // ── Global AI & Polling ──────────────────────────────────────────────────

  Widget _globalSection() {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _sectionHeader('Global defaults'),
        DropdownButtonFormField<String>(
          // ignore: deprecated_member_use
          value: _pollInterval,
          decoration: const InputDecoration(
            labelText: 'Poll interval',
            border: OutlineInputBorder(),
          ),
          items: ['1m', '5m', '30m', '1h']
              .map((v) => DropdownMenuItem(value: v, child: Text(v)))
              .toList(),
          onChanged: (v) => setState(() => _pollInterval = v!),
        ),
        const SizedBox(height: 12),
        DropdownButtonFormField<String>(
          // ignore: deprecated_member_use
          value: _aiPrimary,
          decoration: const InputDecoration(
            labelText: 'Primary AI agent',
            helperText: 'Can be overridden per repo',
            border: OutlineInputBorder(),
          ),
          items: _aiOptions.map((v) => DropdownMenuItem(value: v, child: Text(v))).toList(),
          onChanged: (v) => setState(() => _aiPrimary = v!),
        ),
        const SizedBox(height: 12),
        DropdownButtonFormField<String?>(
          // ignore: deprecated_member_use
          value: _aiFallback.isEmpty ? null : _aiFallback,
          decoration: const InputDecoration(
            labelText: 'Fallback AI agent (optional)',
            border: OutlineInputBorder(),
          ),
          items: [
            const DropdownMenuItem<String?>(value: null, child: Text('None')),
            ..._aiOptions.map((v) => DropdownMenuItem<String?>(value: v, child: Text(v))),
          ],
          onChanged: (v) => setState(() => _aiFallback = v ?? ''),
        ),
      ],
    );
  }

  // ── Retention ────────────────────────────────────────────────────────────

  Widget _retentionSection() {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _sectionHeader('Retention'),
        TextFormField(
          initialValue: _retentionDays.toString(),
          decoration: const InputDecoration(
            labelText: 'Keep reviews for (days, 0 = forever)',
            border: OutlineInputBorder(),
          ),
          keyboardType: TextInputType.number,
          onChanged: (v) => _retentionDays = int.tryParse(v) ?? 90,
        ),
      ],
    );
  }

  // ── Save button ──────────────────────────────────────────────────────────

  Widget _saveButton(BuildContext context, AppConfig base, bool daemonRunning) {
    final isLoading = ref.watch(configNotifierProvider).isLoading;
    final updated = _buildConfig(base);

    if (daemonRunning) {
      return SizedBox(
        width: double.infinity,
        child: ElevatedButton(
          onPressed: () async {
            try {
              final token = _tokenController.text.trim();
              if (token.isNotEmpty && !_tokenFromGh) {
                await FirstRunSetup.storeToken(token);
              }
              await ref.read(configNotifierProvider.notifier).save(updated);
              if (context.mounted) showToast(context, 'Settings saved');
            } catch (e) {
              if (context.mounted) showToast(context, 'Error: $e', isError: true);
            }
          },
          child: const Text('Save'),
        ),
      );
    }

    return SizedBox(
      width: double.infinity,
      child: FilledButton.icon(
        icon: isLoading
            ? const SizedBox(width: 16, height: 16,
                child: CircularProgressIndicator(strokeWidth: 2, color: Colors.white))
            : const Icon(Icons.rocket_launch),
        label: Text(isLoading ? 'Starting…' : 'Save and start Heimdallr'),
        onPressed: isLoading ? null : () async {
          final token = _tokenController.text.trim();
          if (!_tokenFromGh && token.isEmpty) {
            showToast(context, 'GitHub token is required', isError: true);
            return;
          }
          await ref.read(configNotifierProvider.notifier).saveAndStartDaemon(
            token: _tokenFromGh ? (_tokenController.text.trim()) : token,
            config: updated,
            daemonBinaryPath: DaemonLifecycle.defaultBinaryPath(),
          );
          if (context.mounted) {
            final state = ref.read(configNotifierProvider);
            if (state.hasError) {
              showToast(context, '${state.error}', isError: true);
            } else {
              ref.invalidate(daemonHealthProvider);
              context.canPop() ? context.pop() : context.go('/');
            }
          }
        },
      ),
    );
  }

  AppConfig _buildConfig(AppConfig base) => base.copyWith(
    pollInterval: _pollInterval,
    aiPrimary: _aiPrimary,
    aiFallback: _aiFallback,
    retentionDays: _retentionDays,
    repoConfigs: Map.from(_repoConfigs),
  );

  // ── Helpers ──────────────────────────────────────────────────────────────

  Widget _setupBanner() => Padding(
    padding: const EdgeInsets.only(bottom: 20),
    child: Container(
      width: double.infinity,
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: Colors.orange.shade700.withValues(alpha: 0.15),
        border: Border.all(color: Colors.orange.shade700),
        borderRadius: BorderRadius.circular(8),
      ),
      child: Row(children: [
        Icon(Icons.info_outline, color: Colors.orange.shade700),
        const SizedBox(width: 8),
        const Expanded(
          child: Text('Heimdallr is not running. Configure and tap "Save and start".'),
        ),
      ]),
    ),
  );

  Widget _infoChip(IconData icon, String text, Color color) => Container(
    width: double.infinity,
    padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
    decoration: BoxDecoration(
      color: color.withValues(alpha: 0.12),
      border: Border.all(color: color.withValues(alpha: 0.4)),
      borderRadius: BorderRadius.circular(6),
    ),
    child: Row(children: [
      Icon(icon, size: 16, color: color),
      const SizedBox(width: 6),
      Expanded(child: Text(text, style: TextStyle(fontSize: 13, color: color))),
    ]),
  );

  Widget _sectionHeader(String title) => Padding(
    padding: const EdgeInsets.only(bottom: 10),
    child: Text(title, style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 15)),
  );

  Widget _sectionHeaderInline(String title) => Text(
    title, style: const TextStyle(fontWeight: FontWeight.bold, fontSize: 15),
  );
}
