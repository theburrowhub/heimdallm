import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/widgets/attention_badge.dart';

Widget _hosted(Widget child) {
  return MaterialApp(
    theme: HeimdallmTheme.light(),
    debugShowCheckedModeBanner: false,
    builder: (context, navigatorChild) =>
        HeimdallmTheme.scope(child: navigatorChild ?? const SizedBox.shrink()),
    home: Scaffold(body: Center(child: child)),
  );
}

void main() {
  testWidgets('renders the default label', (tester) async {
    await tester.pumpWidget(_hosted(const AttentionBadge()));
    await tester.pumpAndSettle();

    expect(find.text('NEEDS ATTENTION'), findsOneWidget);
  });

  testWidgets('renders a custom label', (tester) async {
    await tester.pumpWidget(
      _hosted(const AttentionBadge(label: 'MANUAL FOLLOW-UP')),
    );
    await tester.pumpAndSettle();

    expect(find.text('MANUAL FOLLOW-UP'), findsOneWidget);
  });
}
