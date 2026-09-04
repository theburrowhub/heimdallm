import 'package:flutter/material.dart';

/// Small chip identifying which instance a row came from.
///
/// Renders nothing when [instanceId] is empty: on a single-daemon install every
/// row would carry the same badge, which is pure noise.
class InstanceBadge extends StatelessWidget {
  const InstanceBadge({
    super.key,
    required this.instanceId,
    this.instanceName = '',
    this.reachable = true,
    this.compact = false,
  });

  final String instanceId;
  final String instanceName;
  final bool reachable;

  /// Drops the icon and tightens the padding, for dense list rows.
  final bool compact;

  @override
  Widget build(BuildContext context) {
    if (instanceId.isEmpty) return const SizedBox.shrink();
    final scheme = Theme.of(context).colorScheme;
    final label = instanceName.isNotEmpty ? instanceName : instanceId;
    final fg = reachable ? scheme.onSurfaceVariant : scheme.error;

    return Tooltip(
      message: reachable
          ? 'Served by $label'
          : 'Served by $label — currently unreachable',
      child: Container(
        constraints: const BoxConstraints(maxWidth: 140),
        padding: EdgeInsets.symmetric(
          horizontal: compact ? 5 : 7,
          vertical: compact ? 1 : 2,
        ),
        decoration: BoxDecoration(
          color: scheme.surfaceContainerHighest,
          borderRadius: BorderRadius.circular(4),
          border: Border.all(color: fg.withValues(alpha: 0.35)),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            if (!compact) ...[
              Icon(
                reachable ? Icons.dns_outlined : Icons.cloud_off_outlined,
                size: 11,
                color: fg,
              ),
              const SizedBox(width: 3),
            ],
            Flexible(
              child: Text(
                label,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: TextStyle(
                  fontSize: compact ? 10 : 11,
                  color: fg,
                  fontWeight: FontWeight.w500,
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// Renders one [InstanceBadge] per instance that reported the same record,
/// collapsing the rest behind a "+N" chip once there are more than
/// [maxVisible].
///
/// This is the other half of fixing theburrowhub/heimdallm#769's visual
/// symptom: grouping the duplicate rows (see `groupByIdentity` in
/// core/instances/aggregation.dart) still leaves one row that came from
/// several instances, and a single [InstanceBadge] can only ever name one of
/// them.
class InstanceBadges extends StatelessWidget {
  const InstanceBadges({
    super.key,
    required this.instances,
    this.compact = false,
    this.maxVisible,
  });

  /// The instances that reported this record, winner first. Entries with a
  /// blank id are dropped — the same "no badge on a single-daemon install"
  /// rule [InstanceBadge] follows.
  final List<({String id, String name})> instances;
  final bool compact;

  /// How many badges to show before collapsing the rest into "+N". Defaults
  /// to 2 in [compact] mode (dense list rows) and 3 otherwise.
  final int? maxVisible;

  @override
  Widget build(BuildContext context) {
    final named = instances.where((i) => i.id.isNotEmpty).toList();
    if (named.isEmpty) return const SizedBox.shrink();

    final limit = maxVisible ?? (compact ? 2 : 3);
    final visible = named.length > limit ? named.sublist(0, limit) : named;
    final hidden = named.length > limit
        ? named.sublist(limit)
        : const <({String id, String name})>[];

    return Wrap(
      spacing: 4,
      runSpacing: 2,
      crossAxisAlignment: WrapCrossAlignment.center,
      children: [
        for (final instance in visible)
          InstanceBadge(
            instanceId: instance.id,
            instanceName: instance.name,
            compact: compact,
          ),
        if (hidden.isNotEmpty) _OverflowChip(hidden: hidden, compact: compact),
      ],
    );
  }
}

/// The "+N" chip for instances [InstanceBadges] could not fit. A plain
/// [InstanceBadge] with a "+N" label would work visually but would also
/// count as a real badge to any `find.byType(InstanceBadge)` — this is a
/// distinct type so a test can tell "N real badges" apart from "and M more".
class _OverflowChip extends StatelessWidget {
  const _OverflowChip({required this.hidden, required this.compact});

  final List<({String id, String name})> hidden;
  final bool compact;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final fg = scheme.onSurfaceVariant;
    final names = hidden
        .map((i) => i.name.isNotEmpty ? i.name : i.id)
        .join(', ');

    return Tooltip(
      message: 'Also on: $names',
      child: Container(
        padding: EdgeInsets.symmetric(
          horizontal: compact ? 5 : 7,
          vertical: compact ? 1 : 2,
        ),
        decoration: BoxDecoration(
          color: scheme.surfaceContainerHighest,
          borderRadius: BorderRadius.circular(4),
          border: Border.all(color: fg.withValues(alpha: 0.35)),
        ),
        child: Text(
          '+${hidden.length}',
          style: TextStyle(
            fontSize: compact ? 10 : 11,
            color: fg,
            fontWeight: FontWeight.w500,
          ),
        ),
      ),
    );
  }
}
