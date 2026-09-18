import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/widgets/toast.dart';

void main() {
  testWidgets('shows a success toast with the message', (tester) async {
    late BuildContext ctx;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: Builder(
            builder: (context) {
              ctx = context;
              return const SizedBox();
            },
          ),
        ),
      ),
    );

    showToast(ctx, 'Saved successfully');
    await tester.pump();

    expect(find.text('Saved successfully'), findsOneWidget);
  });

  testWidgets('renders an action button that fires onAction and dismisses', (
    tester,
  ) async {
    late BuildContext ctx;
    var actionFired = false;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: Builder(
            builder: (context) {
              ctx = context;
              return const SizedBox();
            },
          ),
        ),
      ),
    );

    showToast(
      ctx,
      'Dismissed the PR',
      actionLabel: 'Undo',
      onAction: () => actionFired = true,
    );
    await tester.pump();

    expect(find.text('Undo'), findsOneWidget);
    await tester.tap(find.text('Undo'));
    expect(actionFired, isTrue);
  });

  testWidgets('auto-dismisses after the given duration', (tester) async {
    late BuildContext ctx;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: Builder(
            builder: (context) {
              ctx = context;
              return const SizedBox();
            },
          ),
        ),
      ),
    );

    showToast(ctx, 'Fleeting', duration: const Duration(milliseconds: 100));
    await tester.pump();
    expect(find.text('Fleeting'), findsOneWidget);

    await tester.pump(const Duration(milliseconds: 150));
    await tester.pump(const Duration(milliseconds: 250));
    expect(find.text('Fleeting'), findsNothing);
  });
}
