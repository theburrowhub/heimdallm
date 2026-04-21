import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/repositories/widgets/filter_chips.dart';

Widget _host(Widget child) =>
    MaterialApp(home: Scaffold(body: child));

void main() {
  testWidgets('shows All / Monitored / Not monitored with counts',
      (tester) async {
    await tester.pumpWidget(_host(RepoFilterChips(
      counts: const {'all': 11, 'monitored': 8, 'not_monitored': 3},
      current: 'all',
      onChanged: (_) {},
    )));
    expect(find.text('All'), findsOneWidget);
    expect(find.text('Monitored'), findsOneWidget);
    expect(find.text('Not monitored'), findsOneWidget);
    expect(find.text('11'), findsOneWidget);
    expect(find.text('8'), findsOneWidget);
    expect(find.text('3'), findsOneWidget);
  });

  testWidgets('tapping a chip calls onChanged with its key', (tester) async {
    String? selected;
    await tester.pumpWidget(_host(RepoFilterChips(
      counts: const {'all': 1, 'monitored': 1, 'not_monitored': 0},
      current: 'all',
      onChanged: (v) => selected = v,
    )));
    await tester.tap(find.text('Monitored'));
    expect(selected, 'monitored');
  });
}
