import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/design_system/components/app_badge.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

Widget _host(Widget child) => MaterialApp(
  theme: HeimdallmTheme.light(),
  builder: (context, c) => HeimdallmTheme.scope(child: c ?? const SizedBox()),
  home: Scaffold(body: Center(child: child)),
);

const _badge = AppBadge(
  label: 'Peer · Changes requested · abc1234',
  icon: Icon(Icons.error_outline),
  foreground: Colors.red,
  background: Colors.white,
);

void main() {
  testWidgets('a badge narrower than its label shortens it', (tester) async {
    await tester.pumpWidget(_host(const SizedBox(width: 120, child: _badge)));
    expect(tester.takeException(), isNull);
    expect(tester.getSize(find.byType(AppBadge)).width, lessThanOrEqualTo(120));
  });

  testWidgets('an unconstrained badge keeps its whole label', (tester) async {
    await tester.pumpWidget(_host(const UnconstrainedBox(child: _badge)));
    expect(tester.takeException(), isNull);
    expect(find.text('Peer · Changes requested · abc1234'), findsOneWidget);
    expect(tester.getSize(find.byType(AppBadge)).width, greaterThan(120));
  });
}
