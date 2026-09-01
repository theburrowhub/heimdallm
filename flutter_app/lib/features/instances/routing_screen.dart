import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/api_client.dart';
import '../../core/api/cluster_api.dart';
import '../../core/instances/instances_providers.dart';
import '../../core/instances/models.dart';
import '../config/config_providers.dart';

/// Edits which instance owns which organizations and repositories, and how
/// explicitly triggered operations are spread across the fleet.
class RoutingScreen extends ConsumerWidget {
  const RoutingScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final registryAsync = ref.watch(daemonInstancesProvider);
    final rulesAsync = ref.watch(routingRulesProvider);

    return Scaffold(
      appBar: AppBar(
        title: const Text('Routing'),
        leading: IconButton(
          icon: const Icon(Icons.arrow_back),
          onPressed: () => Navigator.of(context).maybePop(),
        ),
        actions: [
          IconButton(
            tooltip: 'Refresh',
            icon: const Icon(Icons.refresh),
            onPressed: () {
              ref.invalidate(routingRulesProvider);
              ref.invalidate(daemonInstancesProvider);
            },
          ),
        ],
      ),
      body: switch ((registryAsync, rulesAsync)) {
        (AsyncError(:final error), _) => Center(
          child: Text('Could not load instances: $error'),
        ),
        (_, AsyncError(:final error)) => Center(
          child: Text('Could not load routing: $error'),
        ),
        (AsyncData(value: final registry), AsyncData(value: final rules)) =>
          _RoutingBody(registry: registry, rules: rules),
        _ => const Center(child: CircularProgressIndicator()),
      },
    );
  }
}

class _RoutingBody extends ConsumerWidget {
  const _RoutingBody({required this.registry, required this.rules});

  final ClusterRegistry registry;
  final RoutingRules rules;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final config = ref.watch(configNotifierProvider).value;
    final repos = config?.repositories.toList() ?? const <String>[];
    final orgs = config?.knownOrganizations.toList() ?? const <String>[];
    repos.sort();
    orgs.sort();

    return ListView(
      padding: const EdgeInsets.all(12),
      children: [
        _ModeSection(registry: registry, rules: rules),
        const SizedBox(height: 16),
        _ScopeSection(
          title: 'Organizations',
          emptyHint:
              'Organizations appear here as soon as a repository from them '
              'is monitored.',
          ids: orgs,
          assignments: rules.orgs,
          registry: registry,
          onAssign: (org, instanceId) async {
            await _apply(
              context,
              ref,
              (api) => api.assignOrg(rules, org, instanceId),
            );
          },
        ),
        const SizedBox(height: 16),
        _ScopeSection(
          title: 'Repositories',
          emptyHint: 'No monitored repositories yet.',
          ids: repos,
          assignments: rules.repos,
          registry: registry,
          inheritedFrom: (repo) {
            // A repo with no rule of its own still has an owner. Showing where
            // it comes from is the difference between "unconfigured" and
            // "nobody is polling this".
            if (rules.repos.containsKey(repo)) return null;
            final owner = rules.ownerFor(repo);
            if (owner.isEmpty) return null;
            return registry.byId(owner)?.displayName ?? owner;
          },
          onAssign: (repo, instanceId) async {
            await _apply(
              context,
              ref,
              (api) => api.assignRepo(rules, repo, instanceId),
            );
          },
        ),
      ],
    );
  }

  static Future<void> _apply(
    BuildContext context,
    WidgetRef ref,
    Future<void> Function(ApiClient api) action,
  ) async {
    final messenger = ScaffoldMessenger.of(context);
    try {
      await action(ref.read(hubApiClientProvider));
    } on ApiException catch (e) {
      messenger.showSnackBar(SnackBar(content: Text(e.message)));
    } finally {
      ref.invalidate(routingRulesProvider);
      ref.invalidate(daemonInstancesProvider);
    }
  }
}

class _ModeSection extends ConsumerWidget {
  const _ModeSection({required this.registry, required this.rules});

  final ClusterRegistry registry;
  final RoutingRules rules;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final scheme = Theme.of(context).colorScheme;
    return Card(
      margin: EdgeInsets.zero,
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('Mode', style: Theme.of(context).textTheme.titleSmall),
            const SizedBox(height: 8),
            SegmentedButton<String>(
              segments: const [
                ButtonSegment(
                  value: RoutingMode.assignment,
                  label: Text('Assignment'),
                  icon: Icon(Icons.call_split, size: 16),
                ),
                ButtonSegment(
                  value: RoutingMode.dispatch,
                  label: Text('Dispatch'),
                  icon: Icon(Icons.loop, size: 16),
                ),
              ],
              selected: {rules.mode},
              onSelectionChanged: (selection) =>
                  _setMode(context, ref, selection.first),
            ),
            const SizedBox(height: 8),
            Text(
              rules.mode == RoutingMode.dispatch
                  ? 'Repositories stay partitioned for polling, and each '
                        'operation you trigger by hand also rotates across the '
                        'pool below — regardless of which instance owns the repo.'
                  : 'Repositories are partitioned across instances: each daemon '
                        'polls, reviews and merges only what is routed to it. '
                        'Unrouted repositories are handed out round-robin and '
                        'then stay put.',
              style: TextStyle(fontSize: 12, color: scheme.onSurfaceVariant),
            ),
            if (rules.mode == RoutingMode.dispatch) ...[
              const SizedBox(height: 12),
              Text(
                'Rotate these operations',
                style: Theme.of(context).textTheme.labelLarge,
              ),
              const SizedBox(height: 4),
              Wrap(
                spacing: 8,
                children: [
                  for (final op in RoutingOp.all)
                    FilterChip(
                      label: Text(op),
                      selected:
                          rules.roundRobinOps.isEmpty ||
                          rules.roundRobinOps.contains(op),
                      onSelected: (selected) =>
                          _toggleOp(context, ref, op, selected),
                    ),
                ],
              ),
            ],
            const SizedBox(height: 12),
            Text(
              'Round-robin pool',
              style: Theme.of(context).textTheme.labelLarge,
            ),
            const SizedBox(height: 4),
            Text(
              'Empty means every enabled instance takes part.',
              style: TextStyle(fontSize: 11, color: scheme.onSurfaceVariant),
            ),
            const SizedBox(height: 6),
            Wrap(
              spacing: 8,
              children: [
                for (final instance in registry.instances)
                  FilterChip(
                    label: Text(instance.displayName),
                    selected:
                        rules.roundRobinPool.isEmpty ||
                        rules.roundRobinPool.contains(instance.id),
                    onSelected: instance.usable
                        ? (selected) =>
                              _togglePool(context, ref, instance.id, selected)
                        : null,
                  ),
              ],
            ),
            const SizedBox(height: 12),
            Text(
              'Owner of everything unrouted',
              style: Theme.of(context).textTheme.labelLarge,
            ),
            const SizedBox(height: 4),
            _InstanceDropdown(
              registry: registry,
              value: rules.defaultInstance,
              allowNone: false,
              onChanged: (id) => _setDefault(context, ref, id),
            ),
          ],
        ),
      ),
    );
  }

  Future<void> _setMode(BuildContext context, WidgetRef ref, String mode) =>
      _RoutingBody._apply(context, ref, (api) => api.putRouting(mode: mode));

  Future<void> _setDefault(BuildContext context, WidgetRef ref, String? id) =>
      _RoutingBody._apply(
        context,
        ref,
        (api) => api.putRouting(defaultInstance: id ?? ''),
      );

  Future<void> _toggleOp(
    BuildContext context,
    WidgetRef ref,
    String op,
    bool selected,
  ) {
    // An empty list means "all ops", so the first deselection has to be
    // expanded into the explicit full set minus that op.
    final current = rules.roundRobinOps.isEmpty
        ? List<String>.from(RoutingOp.all)
        : List<String>.from(rules.roundRobinOps);
    if (selected) {
      if (!current.contains(op)) current.add(op);
    } else {
      current.remove(op);
    }
    return _RoutingBody._apply(
      context,
      ref,
      (api) => api.putRouting(roundRobinOps: current),
    );
  }

  Future<void> _togglePool(
    BuildContext context,
    WidgetRef ref,
    String id,
    bool selected,
  ) {
    final current = rules.roundRobinPool.isEmpty
        ? registry.usable.map((i) => i.id).toList()
        : List<String>.from(rules.roundRobinPool);
    if (selected) {
      if (!current.contains(id)) current.add(id);
    } else {
      current.remove(id);
    }
    return _RoutingBody._apply(
      context,
      ref,
      (api) => api.putRouting(roundRobinPool: current),
    );
  }
}

class _ScopeSection extends StatelessWidget {
  const _ScopeSection({
    required this.title,
    required this.emptyHint,
    required this.ids,
    required this.assignments,
    required this.registry,
    required this.onAssign,
    this.inheritedFrom,
  });

  final String title;
  final String emptyHint;
  final List<String> ids;
  final Map<String, String> assignments;
  final ClusterRegistry registry;
  final Future<void> Function(String id, String? instanceId) onAssign;
  final String? Function(String id)? inheritedFrom;

  @override
  Widget build(BuildContext context) {
    return Card(
      margin: EdgeInsets.zero,
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(title, style: Theme.of(context).textTheme.titleSmall),
            const SizedBox(height: 8),
            if (ids.isEmpty)
              Text(emptyHint, style: const TextStyle(fontSize: 12))
            else
              for (final id in ids)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 3),
                  child: Row(
                    children: [
                      Expanded(
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Text(id, style: const TextStyle(fontSize: 13)),
                            if (assignments[id] == null &&
                                inheritedFrom?.call(id) != null)
                              Text(
                                'inherits ${inheritedFrom!(id)}',
                                style: TextStyle(
                                  fontSize: 10,
                                  color: Theme.of(
                                    context,
                                  ).colorScheme.onSurfaceVariant,
                                ),
                              ),
                          ],
                        ),
                      ),
                      SizedBox(
                        width: 220,
                        child: _InstanceDropdown(
                          registry: registry,
                          value: assignments[id],
                          allowNone: true,
                          onChanged: (instanceId) => onAssign(id, instanceId),
                        ),
                      ),
                    ],
                  ),
                ),
          ],
        ),
      ),
    );
  }
}

class _InstanceDropdown extends StatelessWidget {
  const _InstanceDropdown({
    required this.registry,
    required this.value,
    required this.allowNone,
    required this.onChanged,
  });

  final ClusterRegistry registry;
  final String? value;
  final bool allowNone;
  final ValueChanged<String?> onChanged;

  static const _noneValue = '__none__';

  @override
  Widget build(BuildContext context) {
    final known = registry.instances.map((i) => i.id).toSet();
    final current = (value != null && known.contains(value))
        ? value
        : (allowNone ? _noneValue : null);

    return DropdownButtonFormField<String>(
      initialValue: current,
      isDense: true,
      decoration: const InputDecoration(
        border: OutlineInputBorder(),
        contentPadding: EdgeInsets.symmetric(horizontal: 8, vertical: 6),
      ),
      style: const TextStyle(fontSize: 12),
      items: [
        if (allowNone)
          const DropdownMenuItem(value: _noneValue, child: Text('Inherit')),
        for (final instance in registry.instances)
          DropdownMenuItem(
            value: instance.id,
            enabled: instance.usable,
            child: Text(
              instance.displayName,
              overflow: TextOverflow.ellipsis,
            ),
          ),
      ],
      onChanged: (selected) =>
          onChanged(selected == _noneValue ? null : selected),
    );
  }
}
