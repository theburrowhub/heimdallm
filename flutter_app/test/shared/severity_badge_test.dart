import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/design_system/components/app_badge.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';
import 'package:heimdallm/shared/widgets/severity_badge.dart';

Widget _host(String severity) => MaterialApp(
  builder: (context, child) =>
      HeimdallmTheme.scope(child: child ?? const SizedBox.shrink()),
  home: Scaffold(body: SeverityBadge(severity: severity)),
);

Color _badgeColor(WidgetTester tester) =>
    tester.widget<AppBadge>(find.byType(AppBadge)).background;

void main() {
  testWidgets('SeverityBadge shows correct color for high', (tester) async {
    await tester.pumpWidget(_host('high'));
    expect(
      _badgeColor(tester),
      AppColors.danger.resolve(tester.element(find.byType(AppBadge))),
    );
  });

  testWidgets('SeverityBadge shows correct color for medium', (tester) async {
    await tester.pumpWidget(_host('medium'));
    expect(
      _badgeColor(tester),
      AppColors.warning.resolve(tester.element(find.byType(AppBadge))),
    );
  });

  testWidgets('SeverityBadge shows correct color for low', (tester) async {
    await tester.pumpWidget(_host('low'));
    expect(
      _badgeColor(tester),
      AppColors.success.resolve(tester.element(find.byType(AppBadge))),
    );
  });

  // Dark mode's status colours are light: the badge is tinted there so its
  // text stays readable, and light mode keeps white on the solid colour.
  testWidgets('SeverityBadge is tinted in dark mode', (tester) async {
    await tester.pumpWidget(
      MaterialApp(
        theme: HeimdallmTheme.light(),
        darkTheme: HeimdallmTheme.dark(),
        themeMode: ThemeMode.dark,
        builder: (context, child) =>
            HeimdallmTheme.scope(child: child ?? const SizedBox.shrink()),
        home: const Scaffold(body: SeverityBadge(severity: 'high')),
      ),
    );
    final badge = tester.widget<AppBadge>(find.byType(AppBadge));
    final danger = AppColors.danger.resolve(
      tester.element(find.byType(AppBadge)),
    );
    expect(badge.foreground, danger);
    expect(badge.background, danger.withValues(alpha: 0.16));
  });

  testWidgets('SeverityBadge keeps white on solid colour in light mode', (
    tester,
  ) async {
    await tester.pumpWidget(_host('medium'));
    expect(
      tester.widget<AppBadge>(find.byType(AppBadge)).foreground,
      Colors.white,
    );
  });
}
