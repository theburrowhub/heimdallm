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
            Text(
              label,
              style: TextStyle(
                fontSize: compact ? 10 : 11,
                color: fg,
                fontWeight: FontWeight.w500,
              ),
            ),
          ],
        ),
      ),
    );
  }
}
