import 'package:flutter/material.dart';
import 'package:flutter/semantics.dart';
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
  testWidgets('AppSegmentedFilter renders every segment with its count', (
    tester,
  ) async {
    String? selected;
    await tester.pumpWidget(
      _hosted(
        AppSegmentedFilter<String>(
          segments: const [
            AppSegment(value: 'all', label: 'All', count: 151),
            AppSegment(value: 'monitored', label: 'Monitored', count: 107),
            AppSegment(
              value: 'not_monitored',
              label: 'Not monitored',
              count: 44,
            ),
          ],
          current: 'all',
          onChanged: (v) => selected = v,
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('All'), findsOneWidget);
    expect(find.text('151'), findsOneWidget);
    expect(find.text('Monitored'), findsOneWidget);
    expect(find.text('107'), findsOneWidget);
    expect(find.text('Not monitored'), findsOneWidget);
    expect(find.text('44'), findsOneWidget);

    await tester.tap(find.text('Monitored'));
    await tester.pump();
    expect(selected, 'monitored');
  });

  testWidgets('AppSegmentedFilter renders without a count', (tester) async {
    await tester.pumpWidget(
      _hosted(
        AppSegmentedFilter<String>(
          segments: const [
            AppSegment(value: 'a', label: 'A'),
            AppSegment(value: 'b', label: 'B'),
          ],
          current: 'a',
          onChanged: (_) {},
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('A'), findsOneWidget);
    expect(find.text('B'), findsOneWidget);
  });

  testWidgets('AppSegmentedFilter announces the current segment as selected', (
    tester,
  ) async {
    final handle = tester.ensureSemantics();

    await tester.pumpWidget(
      _hosted(
        AppSegmentedFilter<String>(
          segments: const [
            AppSegment(value: 'all', label: 'All'),
            AppSegment(value: 'monitored', label: 'Monitored'),
          ],
          current: 'all',
          onChanged: (_) {},
        ),
      ),
    );
    await tester.pumpAndSettle();

    final selectedNode = tester.getSemantics(find.text('All'));
    final unselectedNode = tester.getSemantics(find.text('Monitored'));

    // ignore: deprecated_member_use
    expect(selectedNode.hasFlag(SemanticsFlag.isSelected), isTrue);
    // ignore: deprecated_member_use
    expect(unselectedNode.hasFlag(SemanticsFlag.isSelected), isFalse);

    handle.dispose();
  });
}
