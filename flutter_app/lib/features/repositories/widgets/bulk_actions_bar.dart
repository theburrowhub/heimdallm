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

  const BulkActionsBar({
    super.key,
    required this.selectedCount,
    required this.aggregates,
    required this.onApply,
    required this.onClear,
  });

  @override
  Widget build(BuildContext context) {
    final primary = Theme.of(context).colorScheme.primary;
    return Container(
      margin: const EdgeInsets.fromLTRB(16, 4, 16, 0),
      padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
      decoration: BoxDecoration(
        color: primary.withOpacity(0.10),
        border: Border.all(color: primary.withOpacity(0.35)),
        borderRadius: BorderRadius.circular(8),
      ),
      child: Column(
        children: [
          Row(children: [
            Container(
              padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 2),
              decoration: BoxDecoration(
                color: primary.withOpacity(0.32),
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
        ],
      ),
    );
  }

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
        color: FeaturePalette.mixed.withOpacity(0.12),
        border: Border.all(color: FeaturePalette.mixed.withOpacity(0.28)),
        borderRadius: BorderRadius.circular(8),
      ),
      child: Text(
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
