import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/widgets/pr_review_state_badge.dart';

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
  testWidgets('renders nothing for an unrecognized state', (tester) async {
    await tester.pumpWidget(
      _hosted(const PRReviewStateBadge(state: 'UNKNOWN')),
    );
    await tester.pumpAndSettle();

    expect(find.byType(SizedBox), findsWidgets);
    expect(find.textContaining('PR'), findsNothing);
  });

  testWidgets('renders APPROVED', (tester) async {
    await tester.pumpWidget(
      _hosted(const PRReviewStateBadge(state: 'APPROVED')),
    );
    await tester.pumpAndSettle();

    expect(find.text('PR APPROVED'), findsOneWidget);
  });

  testWidgets('renders CHANGES_REQUESTED', (tester) async {
    await tester.pumpWidget(
      _hosted(const PRReviewStateBadge(state: 'CHANGES_REQUESTED')),
    );
    await tester.pumpAndSettle();

    expect(find.text('CHANGES REQUESTED'), findsOneWidget);
  });

  testWidgets('renders COMMENTED', (tester) async {
    await tester.pumpWidget(
      _hosted(const PRReviewStateBadge(state: 'COMMENTED')),
    );
    await tester.pumpAndSettle();

    expect(find.text('PR COMMENTED'), findsOneWidget);
  });

  testWidgets('renders FIX_PUSHED', (tester) async {
    await tester.pumpWidget(
      _hosted(const PRReviewStateBadge(state: 'FIX_PUSHED')),
    );
    await tester.pumpAndSettle();

    expect(find.text('FIX PUSHED'), findsOneWidget);
  });
}
