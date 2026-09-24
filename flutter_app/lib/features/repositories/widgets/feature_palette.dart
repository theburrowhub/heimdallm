import 'package:flutter/material.dart';

/// The features the user can toggle per repo.
enum Feature { prReview, mergeTracking }

/// Palette used everywhere a feature is rendered: LEDs, detail section
/// headers + switches, bulk bar. Grey (hollow) is shared for "off".
class FeaturePalette {
  static const prReview = Color(0xFF58A6FF);
  static const mergeTracking = Color(0xFF3FB950);

  /// Mixed state in the bulk bar (switch thumb / MIXED tag).
  static const mixed = Color(0xFFE3B341);

  /// Off LED fill + outline.
  static const offFill = Color(0xFF2E333B);
  static const offOutline = Color(0xFF3B424C);

  static Color forFeature(Feature f) => switch (f) {
    Feature.prReview => prReview,
    Feature.mergeTracking => mergeTracking,
  };

  static String labelFor(Feature f) => switch (f) {
    Feature.prReview => 'PR Review',
    Feature.mergeTracking => 'Merge Tracking',
  };
}
