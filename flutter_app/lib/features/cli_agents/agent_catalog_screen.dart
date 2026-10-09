import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../core/models/cli_agent.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/toast.dart';
import '../dashboard/dashboard_providers.dart';
import 'cli_agents_screen.dart';

/// The daemon's installed-agent catalog.
final cliAgentCatalogProvider = FutureProvider.autoDispose<CliAgentCatalog>((
  ref,
) {
  return ref.watch(apiClientProvider).fetchCliAgents();
});

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
  final VoidCallback onTap;

  const AgentCatalogCard({super.key, required this.agent, required this.onTap});

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
                Text(agentEmoji(agent.id), style: const TextStyle(fontSize: 22)),
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
                  label: agent.installed
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
                if (agent.configured)
                  AppBadge(
                    label: 'Signed in / configured',
                    foreground: muted,
                    background: AppColors.surfaceRaised.resolve(context),
                    border: border,
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
          ],
        ),
      ),
    );
  }
}

/// Settings page of one agent: detection details plus the execution editor.
class AgentConfigScreen extends ConsumerWidget {
  final String agentId;

  const AgentConfigScreen({super.key, required this.agentId});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final catalog = ref.watch(cliAgentCatalogProvider).value;
    final info = catalog?.byId(agentId);
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
              AppText.sectionTitle(info?.name ?? agentId),
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
            header: info == null ? null : AgentDetectionCard(agent: info),
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
      if (agent.models.isNotEmpty) ('Models', '${agent.models.length} available'),
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
