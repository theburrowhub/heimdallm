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
  testWidgets('AppViewToggle reports mode changes and preserves keys', (
    tester,
  ) async {
    AppViewMode? changedTo;
    await tester.pumpWidget(
      _hosted(
        AppViewToggle(
          mode: AppViewMode.list,
          onChanged: (m) => changedTo = m,
          listKey: const Key('repos_view_toggle_list'),
          gridKey: const Key('repos_view_toggle_grid'),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('repos_view_toggle_list')), findsOneWidget);
    expect(find.byKey(const Key('repos_view_toggle_grid')), findsOneWidget);

    await tester.tap(find.byKey(const Key('repos_view_toggle_grid')));
    await tester.pump();
    expect(changedTo, AppViewMode.grid);
  });
}
