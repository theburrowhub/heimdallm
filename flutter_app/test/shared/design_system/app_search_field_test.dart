import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

Widget _hosted(Widget child) {
  return MaterialApp(
    theme: HeimdallmTheme.light(),
    debugShowCheckedModeBanner: false,
    builder: (context, navigatorChild) =>
        HeimdallmTheme.scope(child: navigatorChild ?? const SizedBox.shrink()),
    home: Scaffold(body: child),
  );
}

void main() {
  testWidgets('AppSearchField reports changes as the user types', (
    tester,
  ) async {
    var value = '';
    await tester.pumpWidget(
      _hosted(
        StatefulBuilder(
          builder: (context, setState) => AppSearchField(
            value: value,
            onChanged: (v) => setState(() => value = v),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    await tester.enterText(find.byType(TextField), 'hello');
    await tester.pump();

    expect(value, 'hello');
  });

  testWidgets('AppSearchField resyncs from an external reset when unfocused', (
    tester,
  ) async {
    var value = 'stale';
    late StateSetter setState;
    await tester.pumpWidget(
      _hosted(
        StatefulBuilder(
          builder: (context, setter) {
            setState = setter;
            return AppSearchField(value: value, onChanged: (v) => value = v);
          },
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('stale'), findsOneWidget);

    setState(() => value = '');
    await tester.pumpAndSettle();

    expect(find.text('stale'), findsNothing);
  });

  testWidgets('AppSearchField does not clobber the cursor while focused', (
    tester,
  ) async {
    var value = 'existing';
    late StateSetter setState;
    await tester.pumpWidget(
      _hosted(
        StatefulBuilder(
          builder: (context, setter) {
            setState = setter;
            return AppSearchField(value: value, onChanged: (v) => value = v);
          },
        ),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byType(TextField));
    await tester.pump();

    // An external state change while focused (e.g. a sibling rebuild that
    // still reports the stale value) must not overwrite what's on screen.
    setState(() {});
    await tester.pump();

    expect(find.text('existing'), findsOneWidget);
  });
}
