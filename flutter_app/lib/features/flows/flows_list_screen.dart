import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../core/models/flow.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/toast.dart';
import '../dashboard/dashboard_providers.dart';

/// Every review flow and the global selection.
final flowsProvider = FutureProvider.autoDispose<FlowListing>((ref) {
  return ref.watch(apiClientProvider).fetchFlows();
});

/// List of flows.
class FlowsScreen extends ConsumerWidget {
  const FlowsScreen({super.key});

  Future<void> _create(BuildContext context, FlowListing listing) async {
    final ctrl = TextEditingController();
    final id = await showDialog<String>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('New flow'),
        content: TextField(
          key: const ValueKey('new-flow-id'),
          controller: ctrl,
          autofocus: true,
          decoration: const InputDecoration(
            labelText: 'Flow id',
            hintText: 'e.g. weekday-copilot',
            helperText: 'Lowercase letters, digits, - and _',
          ),
          inputFormatters: [
            FilteringTextInputFormatter.allow(RegExp(r'[a-z0-9_-]')),
            LengthLimitingTextInputFormatter(64),
          ],
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('Cancel'),
          ),
          FilledButton(
            key: const ValueKey('new-flow-create'),
            onPressed: () => Navigator.pop(ctx, ctrl.text.trim()),
            child: const Text('Create'),
          ),
        ],
      ),
    );
    if (id == null || id.isEmpty || !context.mounted) return;
    if (id == listing.defaultId || listing.flows.containsKey(id)) {
      showToast(context, 'A flow named "$id" already exists', isError: true);
      return;
    }
    context.go('/flows/$id');
  }

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final async = ref.watch(flowsProvider);
    return async.when(
      loading: () => const Center(child: CircularProgressIndicator()),
      error: (e, _) => Center(child: AppText('Could not load flows: $e')),
      data: (listing) => ListView(
        padding: const EdgeInsets.all(16),
        children: [
          Row(
            children: [
              Expanded(
                child: AppText.muted(
                  'A flow is a decision graph that picks which agents review a PR: '
                  'conditions branch on schedule and quota, and each agent '
                  'step falls back to the next one if it is unavailable.',
                ),
              ),
              const SizedBox(width: 12),
              FilledButton.icon(
                key: const ValueKey('flows-new'),
                onPressed: () => _create(context, listing),
                icon: const Icon(Icons.add, size: 18),
                label: const Text('New flow'),
              ),
            ],
          ),
          const SizedBox(height: 16),
          for (final id in listing.ids)
            Padding(
              padding: const EdgeInsets.only(bottom: 12),
              child: _FlowCard(
                id: id,
                flow: listing.flows[id]!,
                isDefault: id == listing.defaultId,
                selected: id == listing.selected,
              ),
            ),
        ],
      ),
    );
  }
}

class _FlowCard extends StatelessWidget {
  final String id;
  final ReviewFlow flow;
  final bool isDefault;
  final bool selected;

  const _FlowCard({
    required this.id,
    required this.flow,
    required this.isDefault,
    required this.selected,
  });

  @override
  Widget build(BuildContext context) {
    final ok = AppColors.success.resolve(context);
    final muted = AppColors.textMuted.resolve(context);
    final border = AppColors.border.resolve(context);
    return InkWell(
      key: ValueKey('flow-card-$id'),
      borderRadius: BorderRadius.circular(12),
      onTap: () => context.go('/flows/$id'),
      child: AppSurface(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                const Icon(Icons.alt_route, size: 20),
                const SizedBox(width: 8),
                Expanded(
                  child: AppText.sectionTitle(
                    flow.name.isEmpty ? id : flow.name,
                  ),
                ),
                if (selected)
                  AppBadge(
                    label: 'Used globally',
                    foreground: ok,
                    background: ok.withValues(alpha: 0.14),
                    border: ok.withValues(alpha: 0.35),
                  ),
                if (isDefault) ...[
                  const SizedBox(width: 6),
                  AppBadge(
                    label: 'Primary / fallback',
                    foreground: muted,
                    background: AppColors.surfaceRaised.resolve(context),
                    border: border,
                  ),
                ],
                const Icon(Icons.chevron_right, size: 18),
              ],
            ),
            const SizedBox(height: 8),
            if (flow.nodes.isEmpty)
              AppText.muted('No steps yet.')
            else
              Text(_summary(flow)),
          ],
        ),
      ),
    );
  }
}

/// '3 steps · claude, codex': the distinct agents of the flow's agent nodes,
/// in node id order.
String _summary(ReviewFlow flow) {
  final ids = flow.nodes.keys.toList()..sort();
  final agents = <String>{
    for (final id in ids)
      if (!flow.nodes[id]!.isCondition && flow.nodes[id]!.agent.isNotEmpty)
        flow.nodes[id]!.agent,
  };
  return '${flow.nodes.length} steps · ${agents.join(', ')}';
}
