import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/widgets/restart_required_banner.dart';

Widget _app(Widget child) => MaterialApp(home: Scaffold(body: child));

void main() {
  testWidgets('renders the message and calls onRestart when tapped', (
    tester,
  ) async {
    var restarted = false;
    await tester.pumpWidget(
      _app(
        RestartRequiredBanner(
          message: 'Something changed. Restart for it to take effect.',
          onRestart: () => restarted = true,
          starting: false,
        ),
      ),
    );

    expect(
      find.text('Something changed. Restart for it to take effect.'),
      findsOneWidget,
    );
    await tester.tap(find.text('Restart server'));
    expect(restarted, isTrue);
  });

  testWidgets('renders no second line when detail is null', (tester) async {
    await tester.pumpWidget(
      _app(
        RestartRequiredBanner(
          message: 'Changed.',
          detail: null,
          onRestart: () {},
          starting: false,
        ),
      ),
    );
    // Only the primary message and the button's own label — no detail line.
    expect(find.byType(Text), findsNWidgets(2));
  });

  testWidgets('renders the detail line when provided', (tester) async {
    await tester.pumpWidget(
      _app(
        RestartRequiredBanner(
          message: 'Changed.',
          detail: 'Also restart the desktop app.',
          onRestart: () {},
          starting: false,
        ),
      ),
    );
    expect(find.text('Also restart the desktop app.'), findsOneWidget);
  });

  testWidgets('disables the button while starting', (tester) async {
    var restarted = false;
    await tester.pumpWidget(
      _app(
        RestartRequiredBanner(
          message: 'Changed.',
          onRestart: () => restarted = true,
          starting: true,
        ),
      ),
    );

    final button = tester.widget<FilledButton>(find.byType(FilledButton));
    expect(button.onPressed, isNull);
    await tester.tap(find.text('Restart server'), warnIfMissed: false);
    expect(restarted, isFalse);
  });

  testWidgets('honours a custom button label', (tester) async {
    await tester.pumpWidget(
      _app(
        RestartRequiredBanner(
          message: 'Changed.',
          onRestart: () {},
          starting: false,
          buttonLabel: 'Restart now',
        ),
      ),
    );
    expect(find.text('Restart now'), findsOneWidget);
    expect(find.text('Restart server'), findsNothing);
  });
}
