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
}
