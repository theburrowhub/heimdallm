import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../core/models/cli_agent.dart';
import '../../core/models/flow.dart' show AgentQuota, QuotaWindow;
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/toast.dart';
import '../config/config_providers.dart';
import '../dashboard/dashboard_providers.dart';
import 'cli_agents_screen.dart';

/// The daemon's installed-agent catalog.
final cliAgentCatalogProvider = FutureProvider.autoDispose<CliAgentCatalog>((
  ref,
) {
  return ref.watch(apiClientProvider).fetchCliAgents();
});

/// How often the catalog re-reads quotas. The agents branch stays mounted in
/// the shell, so this runs for as long as the app does; the daemon caches
/// each provider's reading for two minutes, so it never adds provider calls.
const kQuotaRefreshInterval = Duration(minutes: 1);

/// Every agent's remaining quota (GET /quotas), re-read every
/// [kQuotaRefreshInterval]. A failed read throws so the AsyncValue keeps the
/// last good reading instead of blanking the cards; with no reading yet
/// (older daemons) the cards show no usage.
final agentQuotasProvider = FutureProvider.autoDispose<Map<String, AgentQuota>>(
  // No automatic retry: the timer re-reads every minute anyway, and a
  // failure keeps the last reading on screen.
  retry: (_, _) => null,
  (ref) async {
    final timer = Timer(kQuotaRefreshInterval, ref.invalidateSelf);
    ref.onDispose(timer.cancel);
    final list = await ref.watch(apiClientProvider).fetchQuotas();
    return {for (final q in list) q.agent: q};
  },
);

/// Width of the window label column on a catalog card; the money detail
/// under a bar is indented by the same amount.
const _quotaLabelWidth = 56.0;

/// Readable copy for the daemon's closed set of quota errors.
String quotaErrorText(String error) => switch (error) {
  'expired' => 'Usage unavailable: sign-in expired',
  'unavailable' => 'Usage unavailable: provider not reachable',
  'unsupported' => 'This agent reports no usage',
  _ => 'Usage unavailable',
};

String agentEmoji(String id) => switch (id) {
  'claude' => '🔷',
  'codex' => '🟢',
  'gemini' => '🟡',
  'cursor' || 'cursor_cli' => '🖱️',
  'copilot' => '🐙',
  'opencode' => '⬛',
  'openrouter' => '🔀',
  _ => '🤖',
};

/// Grid of every supported agent with what the daemon found on its machine.
/// Tapping one opens that agent's settings (Cursor IDE opens Cursor CLI's).
class AgentCatalogScreen extends ConsumerStatefulWidget {
  const AgentCatalogScreen({super.key});

  @override
  ConsumerState<AgentCatalogScreen> createState() => _AgentCatalogScreenState();
}

class _AgentCatalogScreenState extends ConsumerState<AgentCatalogScreen> {
  bool _rescanning = false;

  Future<void> _rescan() async {
    setState(() => _rescanning = true);
    try {
      await ref.read(apiClientProvider).rescanCliAgents();
      ref.invalidate(cliAgentCatalogProvider);
      if (mounted) showToast(context, 'Agents rescanned');
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    } finally {
      if (mounted) setState(() => _rescanning = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final async = ref.watch(cliAgentCatalogProvider);
    final quotas = ref.watch(agentQuotasProvider).value ?? const {};
    final agentConfigs = ref.watch(configNotifierProvider).value?.agentConfigs;
    return ListView(
      padding: const EdgeInsets.all(16),
      children: [
        Row(
          children: [
            Expanded(
              child: AppText.muted(
                'Agents Heimdallm can review with, and what it found on the '
                'daemon\'s machine. Open one to configure it.',
              ),
            ),
            const SizedBox(width: 12),
            OutlinedButton.icon(
              key: const ValueKey('agents-add-custom'),
              onPressed: () => context.go('/cli-agents/custom/new'),
              icon: const Icon(Icons.add, size: 18),
              label: const Text('Custom agent'),
            ),
            const SizedBox(width: 8),
            OutlinedButton.icon(
              key: const ValueKey('agents-rescan'),
              onPressed: _rescanning ? null : _rescan,
              icon: _rescanning
                  ? const SizedBox(
                      width: 16,
                      height: 16,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    )
                  : const Icon(Icons.refresh, size: 18),
              label: const Text('Rescan'),
            ),
          ],
        ),
        const SizedBox(height: 16),
        async.when(
          loading: () => const Padding(
            padding: EdgeInsets.all(32),
            child: Center(child: CircularProgressIndicator()),
          ),
          error: (e, _) => AppText('Could not load agents: $e'),
          data: (catalog) => LayoutBuilder(
            builder: (context, constraints) {
              final columns = (constraints.maxWidth / 320).floor().clamp(1, 4);
              final width =
                  (constraints.maxWidth - (columns - 1) * 12) / columns;
              return Wrap(
                spacing: 12,
                runSpacing: 12,
                children: [
                  for (final a in catalog.agents)
                    SizedBox(
                      width: width,
                      child: AgentCatalogCard(
                        agent: a,
                        quota: quotas[a.id],
                        // A provider has no default model: until one is
                        // chosen it cannot review. Without a key the card
                        // already says "No API key", the first thing to fix.
                        needsModel:
                            a.isProvider &&
                            a.configured &&
                            agentConfigs != null &&
                            (agentConfigs[a.configAgent]?.model ?? '')
                                .trim()
                                .isEmpty,
                        onTap: () => context.go('/cli-agents/${a.configAgent}'),
                      ),
                    ),
                ],
              );
            },
          ),
        ),
      ],
    );
  }
}

/// One catalog entry.
class AgentCatalogCard extends StatelessWidget {
  final CliAgentInfo agent;
  final AgentQuota? quota;
  final VoidCallback onTap;

  /// The agent has no model and no default to fall back to (a provider).
  final bool needsModel;

  const AgentCatalogCard({
    super.key,
    required this.agent,
    required this.onTap,
    this.quota,
    this.needsModel = false,
  });

  @override
  Widget build(BuildContext context) {
    final ok = AppColors.success.resolve(context);
    final muted = AppColors.textMuted.resolve(context);
    final border = AppColors.border.resolve(context);
    return InkWell(
      key: ValueKey('agent-card-${agent.id}'),
      borderRadius: BorderRadius.circular(12),
      onTap: onTap,
      child: AppSurface(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Text(
                  agentEmoji(agent.id),
                  style: const TextStyle(fontSize: 22),
                ),
                const SizedBox(width: 8),
                Expanded(child: AppText.sectionTitle(agent.name)),
                const Icon(Icons.chevron_right, size: 18),
              ],
            ),
            const SizedBox(height: 10),
            Wrap(
              spacing: 6,
              runSpacing: 6,
              children: [
                AppBadge(
                  label: agent.isProvider
                      ? (agent.installed ? 'API key set' : 'No API key')
                      : agent.installed
                      ? (agent.version.isEmpty
                            ? 'Installed'
                            : 'Installed ${agent.version}')
                      : 'Not installed',
                  foreground: agent.installed ? ok : muted,
                  background: agent.installed
                      ? ok.withValues(alpha: 0.14)
                      : AppColors.surfaceRaised.resolve(context),
                  border: agent.installed ? ok.withValues(alpha: 0.35) : border,
                ),
                if (agent.configured && !agent.isProvider && !agent.isCustom)
                  AppBadge(
                    label: 'Signed in / configured',
                    foreground: muted,
                    background: AppColors.surfaceRaised.resolve(context),
                    border: border,
                  ),
                if (agent.isCustom)
                  AppBadge(
                    key: ValueKey('agent-custom-${agent.id}'),
                    label: 'Custom · ${agent.binary}',
                    foreground: muted,
                    background: AppColors.surfaceRaised.resolve(context),
                    border: border,
                  ),
                if (agent.isProvider)
                  AppBadge(
                    label: 'Runs in Heimdallm',
                    foreground: muted,
                    background: AppColors.surfaceRaised.resolve(context),
                    border: border,
                  ),
                if (needsModel)
                  AppBadge(
                    key: ValueKey('agent-needs-model-${agent.id}'),
                    label: 'Choose a model',
                    foreground: AppColors.warning.resolve(context),
                    background: AppColors.warning
                        .resolve(context)
                        .withValues(alpha: 0.14),
                    border: AppColors.warning
                        .resolve(context)
                        .withValues(alpha: 0.35),
                  ),
                if (agent.isIde)
                  AppBadge(
                    label: 'Reviews via Cursor CLI',
                    foreground: muted,
                    background: AppColors.surfaceRaised.resolve(context),
                    border: border,
                  ),
              ],
            ),
            if (quota != null &&
                !quota!.available &&
                quota!.error.isNotEmpty &&
                quota!.error != 'not_configured') ...[
              const SizedBox(height: 8),
              Text(
                quotaErrorText(quota!.error),
                key: ValueKey('quota-${agent.id}-unavailable'),
                style: TextStyle(fontSize: 11, color: muted),
              ),
            ],
            if (quota != null &&
                quota!.available &&
                quota!.detail.isNotEmpty) ...[
              const SizedBox(height: 8),
              Text(
                quota!.detail,
                key: ValueKey('quota-${agent.id}-detail'),
                style: const TextStyle(fontSize: 11),
              ),
            ],
            if (quota != null &&
                quota!.available &&
                quota!.windows.isNotEmpty) ...[
              const SizedBox(height: 8),
              for (final w in quota!.windows) ...[
                Padding(
                  padding: const EdgeInsets.only(top: 3),
                  child: Row(
                    children: [
                      SizedBox(
                        width: _quotaLabelWidth,
                        child: Text(
                          w.shortLabel,
                          overflow: TextOverflow.ellipsis,
                          style: const TextStyle(fontSize: 11),
                        ),
                      ),
                      Expanded(
                        child: ClipRRect(
                          borderRadius: BorderRadius.circular(4),
                          child: LinearProgressIndicator(
                            key: ValueKey(
                              'quota-${agent.id}-${_quotaRowKey(w)}',
                            ),
                            value: w.usedPercent / 100,
                            minHeight: 5,
                            color: w.usedPercent >= 90
                                ? Colors.red.shade400
                                : null,
                          ),
                        ),
                      ),
                      const SizedBox(width: 6),
                      Text(
                        '${w.usedPercent.round()}%',
                        style: const TextStyle(fontSize: 11),
                      ),
                    ],
                  ),
                ),
                if (w.detail.isNotEmpty)
                  Padding(
                    padding: const EdgeInsets.only(
                      left: _quotaLabelWidth,
                      top: 1,
                    ),
                    child: Text(
                      w.detail,
                      key: ValueKey(
                        'quota-${agent.id}-${_quotaRowKey(w)}-detail',
                      ),
                      overflow: TextOverflow.ellipsis,
                      style: TextStyle(fontSize: 10, color: muted),
                    ),
                  ),
              ],
            ],
          ],
        ),
      ),
    );
  }
}

/// A window's row key on a catalog card. A model's label can equal another
/// kind's short label (a model named "credit"), so model rows say their kind.
String _quotaRowKey(QuotaWindow w) =>
    w.kind == 'model' ? 'model:${w.label}' : w.shortLabel;

/// Settings page of one agent: detection details plus the execution editor.
class AgentConfigScreen extends ConsumerWidget {
  final String agentId;

  const AgentConfigScreen({super.key, required this.agentId});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final catalogAsync = ref.watch(cliAgentCatalogProvider);
    final info = catalogAsync.value?.byId(agentId);
    // The catalog's kind decides; the known ids only cover a catalog that
    // has not loaded, so OpenRouter never flashes a "CLI default".
    final isProvider =
        info?.isProvider ?? knownProviderAgentIds.contains(agentId);
    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(8, 8, 16, 0),
          child: Row(
            children: [
              IconButton(
                key: const ValueKey('agent-config-back'),
                tooltip: 'All agents',
                icon: const Icon(Icons.arrow_back),
                onPressed: () => context.go('/cli-agents'),
              ),
              Text(agentEmoji(agentId), style: const TextStyle(fontSize: 20)),
              const SizedBox(width: 8),
              Expanded(child: AppText.sectionTitle(info?.name ?? agentId)),
              if (info != null && info.isCustom)
                OutlinedButton.icon(
                  key: const ValueKey('agent-edit-definition'),
                  onPressed: () => context.go('/cli-agents/custom/$agentId'),
                  icon: const Icon(Icons.edit_outlined, size: 18),
                  label: const Text('Edit definition'),
                ),
            ],
          ),
        ),
        Expanded(
          child: CLIAgentsScreen(
            key: ValueKey('agent-config-$agentId'),
            agentIds: [agentId],
            discoveredModels: {
              if (info != null && info.models.isNotEmpty) agentId: info.models,
            },
            providerIds: {if (isProvider) agentId},
            modelsLoading: catalogAsync.isLoading,
            // A provider's catalog entry is configured once its key is set.
            providersWithoutKey: {
              if (info != null && info.isProvider && !info.configured) agentId,
            },
            onReloadModels: () async {
              // The page can be left while the rescan runs: invalidate
              // through the container, not this widget's ref.
              final container = ProviderScope.containerOf(
                context,
                listen: false,
              );
              await container.read(apiClientProvider).rescanCliAgents();
              container.invalidate(cliAgentCatalogProvider);
            },
            header: info == null
                ? null
                : info.isProvider
                ? ProviderKeyCard(agent: info)
                : AgentDetectionCard(agent: info),
          ),
        ),
      ],
    );
  }
}

/// What the daemon found for an agent, and how to install it when missing.
class AgentDetectionCard extends StatelessWidget {
  final CliAgentInfo agent;

  const AgentDetectionCard({super.key, required this.agent});

  @override
  Widget build(BuildContext context) {
    final rows = <(String, String)>[
      ('Status', agent.installed ? 'Installed' : 'Not installed'),
      if (agent.version.isNotEmpty) ('Version', agent.version),
      if (agent.path.isNotEmpty) ('Executable', agent.path),
      ('Configuration', agent.configured ? 'Found' : 'Not found'),
      if (agent.models.isNotEmpty)
        ('Models', '${agent.models.length} available'),
    ];
    return AppSurface(
      elevation: AppSurfaceElevation.canvas,
      padding: const EdgeInsets.all(12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          for (final (label, value) in rows)
            Padding(
              padding: const EdgeInsets.symmetric(vertical: 2),
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  SizedBox(width: 120, child: AppText.muted(label)),
                  Expanded(child: SelectableText(value)),
                ],
              ),
            ),
          if (!agent.installed && agent.installHint.isNotEmpty) ...[
            const SizedBox(height: 8),
            AppText.muted('Install'),
            const SizedBox(height: 4),
            Row(
              children: [
                Expanded(
                  child: SelectableText(
                    agent.installHint,
                    style: const TextStyle(fontFamily: 'monospace'),
                  ),
                ),
                IconButton(
                  key: const ValueKey('agent-copy-install'),
                  tooltip: 'Copy',
                  icon: const Icon(Icons.copy, size: 16),
                  onPressed: () async {
                    await Clipboard.setData(
                      ClipboardData(text: agent.installHint),
                    );
                    if (context.mounted) showToast(context, 'Copied');
                  },
                ),
              ],
            ),
          ],
        ],
      ),
    );
  }
}

/// Provider account usage (OpenRouter GET /key), refreshed with the page.
final agentUsageProvider = FutureProvider.autoDispose
    .family<Map<String, dynamic>, String>((ref, agentId) {
      return ref.watch(apiClientProvider).fetchAgentUsage(agentId);
    });

/// API key management for an in-process provider. The key is write-only: the
/// daemon only ever reports whether one is set and where it comes from.
class ProviderKeyCard extends ConsumerStatefulWidget {
  final CliAgentInfo agent;

  const ProviderKeyCard({super.key, required this.agent});

  @override
  ConsumerState<ProviderKeyCard> createState() => _ProviderKeyCardState();
}

class _ProviderKeyCardState extends ConsumerState<ProviderKeyCard> {
  final _keyCtrl = TextEditingController();
  AgentKeyStatus? _status;
  bool _busy = false;

  @override
  void initState() {
    super.initState();
    // After the first frame: a failure shows a toast, which must not happen
    // while the tree is still building.
    Future.microtask(_load);
  }

  @override
  void dispose() {
    _keyCtrl.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final s = await ref
          .read(apiClientProvider)
          .fetchAgentKey(widget.agent.id);
      if (mounted) setState(() => _status = s);
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _run(
    Future<AgentKeyStatus> Function() action,
    String done,
  ) async {
    setState(() => _busy = true);
    try {
      final s = await action();
      if (!mounted) return;
      _keyCtrl.clear();
      setState(() => _status = s);
      ref.invalidate(agentUsageProvider(widget.agent.id));
      showToast(context, done);
      // Rescan so the model list reflects the new key, without holding the
      // button: a rescan probes every agent and can take seconds. The
      // daemon's own background refresh covers a failed rescan.
      final api = ref.read(apiClientProvider);
      unawaited(
        Future<CliAgentCatalog>(api.rescanCliAgents)
            .then<void>(
              (_) {},
              onError: (Object e) {
                debugPrint('agents rescan after a key change failed: $e');
              },
            )
            .whenComplete(() {
              if (mounted) ref.invalidate(cliAgentCatalogProvider);
            }),
      );
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final api = ref.read(apiClientProvider);
    final id = widget.agent.id;
    final status = _status;
    final sourceText = switch (status?.source) {
      'stored' => 'Key saved in Heimdallm',
      'env' => 'Key from the daemon environment (OPENROUTER_API_KEY)',
      _ => 'No API key',
    };
    return AppSurface(
      elevation: AppSurfaceElevation.canvas,
      padding: const EdgeInsets.all(12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(
                status?.configured == true ? Icons.key : Icons.key_off,
                size: 18,
              ),
              const SizedBox(width: 8),
              Expanded(child: Text(status == null ? 'Checking…' : sourceText)),
              if (status?.source == 'stored')
                TextButton(
                  key: const ValueKey('provider-key-remove'),
                  onPressed: _busy
                      ? null
                      : () => _run(() => api.deleteAgentKey(id), 'Key removed'),
                  child: const Text('Remove'),
                ),
            ],
          ),
          const SizedBox(height: 10),
          Row(
            children: [
              Expanded(
                child: TextField(
                  key: const ValueKey('provider-key-field'),
                  controller: _keyCtrl,
                  obscureText: true,
                  enableSuggestions: false,
                  autocorrect: false,
                  decoration: InputDecoration(
                    labelText: status?.configured == true
                        ? 'Replace API key'
                        : 'API key',
                    hintText: 'sk-or-…',
                    border: const OutlineInputBorder(),
                    isDense: true,
                  ),
                ),
              ),
              const SizedBox(width: 8),
              FilledButton(
                key: const ValueKey('provider-key-save'),
                onPressed: _busy
                    ? null
                    : () {
                        final key = _keyCtrl.text.trim();
                        if (key.isEmpty) return;
                        _run(() => api.setAgentKey(id, key), 'Key saved');
                      },
                child: const Text('Save key'),
              ),
            ],
          ),
          if (status?.configured == true) ...[
            const SizedBox(height: 10),
            _ProviderUsage(agentId: id),
          ],
        ],
      ),
    );
  }
}

class _ProviderUsage extends ConsumerWidget {
  final String agentId;

  const _ProviderUsage({required this.agentId});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final async = ref.watch(agentUsageProvider(agentId));
    return async.when(
      loading: () => const LinearProgressIndicator(minHeight: 2),
      error: (e, _) => AppText.muted('Usage unavailable: $e'),
      data: (u) {
        String money(Object? v) =>
            v is num ? '\$${v.toStringAsFixed(2)}' : 'no limit';
        final parts = [
          'Spent ${money(u['usage'])}',
          if (u['usage_daily'] is num) 'today ${money(u['usage_daily'])}',
          'limit ${money(u['limit'])}',
          if (u['limit_remaining'] is num)
            '${money(u['limit_remaining'])} left',
        ];
        return AppText.muted(parts.join(' · '));
      },
    );
  }
}
