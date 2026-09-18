import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';

Widget _hostedInApp(ThemeData theme, Widget child) {
  return MaterialApp(
    theme: theme,
    debugShowCheckedModeBanner: false,
    builder: (context, navigatorChild) =>
        HeimdallmTheme.scope(child: navigatorChild ?? const SizedBox.shrink()),
    home: child,
  );
}

void main() {
  testWidgets('light theme resolves accent to the brand seed-derived primary', (
    tester,
  ) async {
    late BuildContext captured;
    await tester.pumpWidget(
      _hostedInApp(
        HeimdallmTheme.light(),
        Builder(
          builder: (context) {
            captured = context;
            return const SizedBox();
          },
        ),
      ),
    );
    await tester.pumpAndSettle();

    final resolvedAccent = AppColors.accent.resolve(captured);
    final materialPrimary = Theme.of(captured).colorScheme.primary;
    expect(resolvedAccent, materialPrimary);
  });

  testWidgets('dark theme resolves text/surface tokens distinctly from light', (
    tester,
  ) async {
    late BuildContext lightCtx;
    late BuildContext darkCtx;

    await tester.pumpWidget(
      _hostedInApp(
        HeimdallmTheme.light(),
        Builder(builder: (context) {
          lightCtx = context;
          return const SizedBox();
        }),
      ),
    );
    await tester.pumpAndSettle();
    final lightSurface = AppColors.surface.resolve(lightCtx);

    await tester.pumpWidget(
      _hostedInApp(
        HeimdallmTheme.dark(),
        Builder(builder: (context) {
          darkCtx = context;
          return const SizedBox();
        }),
      ),
    );
    await tester.pumpAndSettle();
    final darkSurface = AppColors.surface.resolve(darkCtx);

    expect(lightSurface, isNot(darkSurface));
  });

  testWidgets('feature palette tokens match FeaturePalette hex values', (
    tester,
  ) async {
    late BuildContext captured;
    await tester.pumpWidget(
      _hostedInApp(
        HeimdallmTheme.light(),
        Builder(builder: (context) {
          captured = context;
          return const SizedBox();
        }),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      AppColors.featurePrReview.resolve(captured),
      const Color(0xFF58A6FF),
    );
    expect(
      AppColors.featureMergeTracking.resolve(captured),
      const Color(0xFF3FB950),
    );
  });

  testWidgets('space and radius tokens resolve to the documented scale', (
    tester,
  ) async {
    late BuildContext captured;
    await tester.pumpWidget(
      _hostedInApp(
        HeimdallmTheme.light(),
        Builder(builder: (context) {
          captured = context;
          return const SizedBox();
        }),
      ),
    );
    await tester.pumpAndSettle();

    expect(AppSpace.md.resolve(captured), 12);
    expect(AppRadius.lg.resolve(captured), const Radius.circular(12));
  });

  testWidgets('throws a clear error when resolved outside a MixScope', (
    tester,
  ) async {
    late BuildContext captured;
    await tester.pumpWidget(
      MaterialApp(
        home: Builder(
          builder: (context) {
            captured = context;
            return const SizedBox();
          },
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(() => AppColors.accent.resolve(captured), throwsFlutterError);
  });
}
