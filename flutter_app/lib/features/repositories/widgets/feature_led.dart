import 'package:flutter/material.dart';
import 'feature_palette.dart';

/// Two-state LED (on / off) with a rich tooltip.
/// The on/off decision and source string are computed by the caller.
class FeatureLed extends StatelessWidget {
  final Feature feature;
  final bool isOn;
  final String sourceLine;
  final double size;

  const FeatureLed({
    super.key,
    required this.feature,
    required this.isOn,
    required this.sourceLine,
    this.size = 8,
  });

  @override
  Widget build(BuildContext context) {
    final name = FeaturePalette.labelFor(feature);
    final state = isOn ? 'On' : 'Off';
    final description = _description(feature, isOn);
    return Tooltip(
      message: '$name · $state\n$description\n$sourceLine',
      waitDuration: const Duration(milliseconds: 350),
      child: Container(
        width: size,
        height: size,
        decoration: BoxDecoration(
          shape: BoxShape.circle,
          color: isOn ? FeaturePalette.forFeature(feature) : FeaturePalette.offFill,
          border: isOn
              ? null
              : Border.all(color: FeaturePalette.offOutline, width: 1),
        ),
      ),
    );
  }

  static String _description(Feature f, bool on) => switch ((f, on)) {
        (Feature.prReview, true)       => 'The daemon auto-reviews PRs in this repo.',
        (Feature.prReview, false)      => 'The daemon will not auto-review PRs in this repo.',
        (Feature.issueTracking, true)  => 'The daemon triages new issues in this repo.',
        (Feature.issueTracking, false) => 'The daemon ignores new issues in this repo.',
        (Feature.develop, true)        => 'The daemon can auto-implement issues in this repo.',
        (Feature.develop, false)       => 'The daemon cannot auto-implement issues in this repo.',
      };
}
