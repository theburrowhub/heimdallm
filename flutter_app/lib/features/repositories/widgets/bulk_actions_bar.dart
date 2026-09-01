import 'package:flutter/material.dart';
import 'feature_palette.dart';
import 'feature_switch.dart';

/// Floats above the repo list when >=1 repo is selected.
/// Shows the aggregate state of the 3 features across the selection;
/// flipping a switch applies to every selected repo.
class BulkActionsBar extends StatelessWidget {
  final int selectedCount;
  /// true = all selected on; false = all off; null = mixed.
  final Map<Feature, bool?> aggregates;
  final void Function(Feature feature, bool enable) onApply;
  final VoidCallback onClear;

  /// Instances the selection can be routed to. Empty on a single-daemon
  /// install, where the whole row is hidden.
  final List<({String id, String name})> instances;

  /// Routes every selected repository to an instance. Null clears the rules so
  /// they fall back to the default instance.
  final void Function(String? instanceId)? onAssignInstance;

  const BulkActionsBar({
    super.key,
    required this.selectedCount,
    required this.aggregates,
    required this.onApply,
    required this.onClear,
    this.instances = const [],
    this.onAssignInstance,
  });

  @override
  Widget build(BuildContext context) {
    final primary = Theme.of(context).colorScheme.primary;
    return Container(
      margin: const EdgeInsets.fromLTRB(16, 4, 16, 0),
      padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
      decoration: BoxDecoration(
        color: primary.withValues(alpha:0.10),
        border: Border.all(color: primary.withValues(alpha:0.35)),
        borderRadius: BorderRadius.circular(8),
      ),
      child: Column(
        children: [
          Row(children: [
            Container(
              padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 2),
              decoration: BoxDecoration(
                color: primary.withValues(alpha:0.32),
                borderRadius: BorderRadius.circular(10),
              ),
              child: Text(
                '$selectedCount selected',
                style: TextStyle(
                  color: primary, fontWeight: FontWeight.w600, fontSize: 11,
                ),
              ),
            ),
            const SizedBox(width: 10),
            Text('Bulk actions',
                style: TextStyle(color: primary, fontWeight: FontWeight.w600, fontSize: 13)),
            const Spacer(),
            TextButton(
              onPressed: onClear,
              child: const Text('Clear'),
            ),
          ]),
          const Divider(height: 14, thickness: 0.5),
          for (final f in Feature.values) _row(f),
          if (instances.isNotEmpty && onAssignInstance != null) ...[
            const Divider(height: 14, thickness: 0.5),
            _instanceRow(context),
          ],
        ],
      ),
    );
  }

  Widget _instanceRow(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Row(
        children: [
          const Icon(Icons.dns_outlined, size: 14),
          const SizedBox(width: 8),
          const Text(
            'Route to instance',
            style: TextStyle(fontWeight: FontWeight.w600, fontSize: 12.5),
          ),
          const Spacer(),
          PopupMenuButton<String>(
            tooltip: 'Route the selected repositories',
            onSelected: (value) =>
                onAssignInstance!(value == _inheritValue ? null : value),
            itemBuilder: (context) => [
              const PopupMenuItem(
                value: _inheritValue,
                child: Text('Inherit (default instance)'),
              ),
              const PopupMenuDivider(),
              for (final instance in instances)
                PopupMenuItem(value: instance.id, child: Text(instance.name)),
            ],
            child: const Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                Text('Choose…', style: TextStyle(fontSize: 12.5)),
                Icon(Icons.arrow_drop_down, size: 18),
              ],
            ),
          ),
        ],
      ),
    );
  }

  static const _inheritValue = '__inherit__';

  Widget _row(Feature f) {
    final v = aggregates[f];
    final color = FeaturePalette.forFeature(f);
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Row(children: [
        Container(
          width: 10, height: 10,
          decoration: BoxDecoration(color: color, shape: BoxShape.circle),
        ),
        const SizedBox(width: 10),
        Text(FeaturePalette.labelFor(f),
            style: TextStyle(fontWeight: FontWeight.w600, color: color, fontSize: 12.5)),
        const SizedBox(width: 10),
        if (v == null) const _MixedTag(),
        const Spacer(),
        FeatureSwitch(
          feature: f,
          value: v,
          onChanged: (newValue) => onApply(f, newValue),
        ),
      ]),
    );
  }
}

class _MixedTag extends StatelessWidget {
  const _MixedTag();
  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 1),
      decoration: BoxDecoration(
        color: FeaturePalette.mixed.withValues(alpha:0.12),
        border: Border.all(color: FeaturePalette.mixed.withValues(alpha:0.28)),
        borderRadius: BorderRadius.circular(8),
      ),
      child: const Text(
        'MIXED',
        style: TextStyle(
          color: FeaturePalette.mixed,
          fontSize: 10.5,
          fontWeight: FontWeight.w700,
          letterSpacing: 0.3,
        ),
      ),
    );
  }
}
