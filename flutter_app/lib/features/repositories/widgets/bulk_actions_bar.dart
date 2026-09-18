import 'package:flutter/material.dart';
import 'package:mix/mix.dart';

import '../../../shared/design_system/components/components.dart';
import '../../../shared/design_system/tokens.dart';
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
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 4, 16, 0),
      child: Box(
        style: BoxStyler()
            .color(primary.withValues(alpha: 0.10))
            .borderAll(color: primary.withValues(alpha: 0.35), width: 1)
            .borderRadiusAll(AppRadius.md())
            .padding(
              EdgeInsetsGeometryMix.value(
                const EdgeInsets.fromLTRB(14, 12, 14, 12),
              ),
            ),
        child: Column(
          children: [
            Row(
              children: [
                AppBadge(
                  label: '$selectedCount selected',
                  foreground: primary,
                  background: primary.withValues(alpha: 0.18),
                  padding: const EdgeInsets.symmetric(
                    horizontal: 9,
                    vertical: 2,
                  ),
                  radius: 10,
                  fontSize: 11,
                  letterSpacing: 0,
                ),
                const SizedBox(width: 10),
                AppText.label('Bulk actions', color: primary),
                const Spacer(),
                TextButton(onPressed: onClear, child: const Text('Clear')),
              ],
            ),
            const Divider(height: 14, thickness: 0.5),
            for (final f in Feature.values) _row(f),
            if (instances.isNotEmpty && onAssignInstance != null) ...[
              const Divider(height: 14, thickness: 0.5),
              _instanceRow(context),
            ],
          ],
        ),
      ),
    );
  }

  Widget _instanceRow(BuildContext context) {
    final muted = Theme.of(context).colorScheme.onSurfaceVariant;
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Row(
        children: [
          const Icon(Icons.dns_outlined, size: 14),
          const SizedBox(width: 8),
          const AppText.label('Route to instance'),
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
            child: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                const AppText.label('Choose…'),
                Icon(Icons.arrow_drop_down, size: 18, color: muted),
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
      child: Row(
        children: [
          Box(
            style: BoxStyler()
                .width(10)
                .height(10)
                .borderRadiusAll(Radius.circular(999))
                .color(color),
          ),
          const SizedBox(width: 10),
          AppText.label(FeaturePalette.labelFor(f), color: color),
          const SizedBox(width: 10),
          if (v == null) const _MixedTag(),
          const Spacer(),
          FeatureSwitch(
            feature: f,
            value: v,
            onChanged: (newValue) => onApply(f, newValue),
          ),
        ],
      ),
    );
  }
}

class _MixedTag extends StatelessWidget {
  const _MixedTag();
  @override
  Widget build(BuildContext context) {
    return AppBadge(
      label: 'MIXED',
      foreground: FeaturePalette.mixed,
      background: FeaturePalette.mixed.withValues(alpha: 0.12),
      border: FeaturePalette.mixed.withValues(alpha: 0.28),
      padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 1),
      radius: 8,
      fontSize: 10.5,
      fontWeight: FontWeight.w700,
      letterSpacing: 0.3,
    );
  }
}
