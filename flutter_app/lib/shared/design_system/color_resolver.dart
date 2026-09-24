import 'package:flutter/material.dart';
import 'package:mix/mix.dart';

import 'tokens.dart';

/// Resolves a design-system colour token when a MixScope is present, while
/// preserving legacy callers/tests that still render some leaf widgets inside a
/// plain MaterialApp.
Color resolveAppColor(BuildContext context, ColorToken token) {
  try {
    return token.resolve(context);
  } catch (_) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final isDark = theme.brightness == Brightness.dark;

    if (identical(token, AppColors.success)) {
      return isDark ? const Color(0xFF3FB950) : const Color(0xFF2E7D32);
    }
    if (identical(token, AppColors.warning)) {
      return isDark ? const Color(0xFFE3B341) : const Color(0xFFB26A00);
    }
    if (identical(token, AppColors.danger)) {
      return scheme.error;
    }
    if (identical(token, AppColors.info)) {
      return scheme.secondary;
    }
    if (identical(token, AppColors.textMuted)) {
      return scheme.onSurfaceVariant;
    }
    if (identical(token, AppColors.featurePrReview)) {
      return const Color(0xFF58A6FF);
    }
    if (identical(token, AppColors.featureMergeTracking)) {
      return const Color(0xFF3FB950);
    }
    if (identical(token, AppColors.featureMixed)) {
      return const Color(0xFFE3B341);
    }
    if (identical(token, AppColors.featureOffFill)) {
      return const Color(0xFF2E333B);
    }
    if (identical(token, AppColors.featureOffOutline)) {
      return const Color(0xFF3B424C);
    }

    rethrow;
  }
}
