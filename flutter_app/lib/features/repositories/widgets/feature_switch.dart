import 'package:flutter/material.dart';
import 'feature_palette.dart';

/// Material switch coloured per feature. Supports a third "mixed" state
/// (value == null) rendered as an amber placeholder with a dash on the
/// thumb — used by the bulk actions bar when the selection is mixed.
class FeatureSwitch extends StatelessWidget {
  final Feature feature;
  final bool? value; // null = mixed aggregate
  final ValueChanged<bool> onChanged;

  const FeatureSwitch({
    super.key,
    required this.feature,
    required this.value,
    required this.onChanged,
  });

  @override
  Widget build(BuildContext context) {
    if (value == null) {
      return _MixedSwitch(onTap: () => onChanged(true));
    }
    final color = FeaturePalette.forFeature(feature);
    return Switch(
      value: value!,
      activeThumbColor: color,
      activeTrackColor: color.withValues(alpha: 0.55),
      onChanged: onChanged,
    );
  }
}

/// Drawn to match Material Switch geometry but tinted amber with a
/// centred dash to signal "aggregate is mixed".
class _MixedSwitch extends StatelessWidget {
  final VoidCallback onTap;
  const _MixedSwitch({required this.onTap});

  @override
  Widget build(BuildContext context) {
    return GestureDetector(
      key: const Key('FeatureSwitch_mixed'),
      onTap: onTap,
      child: Container(
        width: 36,
        height: 20,
        decoration: BoxDecoration(
          color: FeaturePalette.mixed.withValues(alpha: 0.22),
          borderRadius: BorderRadius.circular(12),
          border: Border.all(
            color: FeaturePalette.mixed.withValues(alpha: 0.45),
            width: 1,
          ),
        ),
        child: Stack(
          alignment: Alignment.center,
          children: [
            Positioned(
              left: 10,
              top: 2,
              child: Container(
                width: 16,
                height: 16,
                decoration: BoxDecoration(
                  color: FeaturePalette.mixed,
                  shape: BoxShape.circle,
                ),
              ),
            ),
            Container(width: 6, height: 1.5, color: const Color(0xFF1C1F24)),
          ],
        ),
      ),
    );
  }
}
