import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/instances/widgets/instance_badge.dart';

void main() {
  Future<void> pump(WidgetTester tester, Widget child, {double width = 400}) {
    return tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: Align(
            alignment: Alignment.topLeft,
            child: SizedBox(width: width, child: child),
          ),
        ),
      ),
    );
  }

  testWidgets('empty list renders nothing', (tester) async {
    await pump(tester, const InstanceBadges(instances: []));
    expect(find.byType(InstanceBadge), findsNothing);
    expect(find.byType(SizedBox), findsWidgets); // the shell SizedBox is fine
  });

  testWidgets('a single blank id renders nothing, matching InstanceBadge', (
    tester,
  ) async {
    await pump(tester, const InstanceBadges(instances: [(id: '', name: '')]));
    expect(find.byType(InstanceBadge), findsNothing);
  });

  testWidgets('two instances render two badges, no overflow chip', (
    tester,
  ) async {
    await pump(
      tester,
      const InstanceBadges(
        instances: [(id: 'hub-1', name: 'Local hub'), (id: 'srv-a', name: 'Server A')],
      ),
    );
    expect(find.byType(InstanceBadge), findsNWidgets(2));
    expect(find.textContaining('+'), findsNothing);
  });

  testWidgets('four instances compact: two badges plus a +2 overflow chip', (
    tester,
  ) async {
    await pump(
      tester,
      const InstanceBadges(
        compact: true,
        instances: [
          (id: 'a', name: 'Alpha'),
          (id: 'b', name: 'Bravo'),
          (id: 'c', name: 'Charlie'),
          (id: 'd', name: 'Delta'),
        ],
      ),
    );
    expect(find.byType(InstanceBadge), findsNWidgets(2));
    expect(find.text('+2'), findsOneWidget);
  });

  testWidgets('the winner is always first and always visible', (tester) async {
    await pump(
      tester,
      const InstanceBadges(
        compact: true,
        instances: [
          (id: 'a', name: 'Alpha'),
          (id: 'b', name: 'Bravo'),
          (id: 'c', name: 'Charlie'),
        ],
      ),
    );
    final texts = tester
        .widgetList<InstanceBadge>(find.byType(InstanceBadge))
        .map((b) => b.instanceName)
        .toList();
    expect(texts.first, 'Alpha');
  });

  testWidgets('the overflow tooltip names the hidden instances', (
    tester,
  ) async {
    await pump(
      tester,
      const InstanceBadges(
        compact: true,
        instances: [
          (id: 'a', name: 'Alpha'),
          (id: 'b', name: 'Bravo'),
          (id: 'c', name: 'Charlie'),
        ],
      ),
    );
    final tooltip = tester.widget<Tooltip>(
      find.ancestor(
        of: find.text('+1'),
        matching: find.byType(Tooltip),
      ),
    );
    expect(tooltip.message, contains('Charlie'));
  });

  testWidgets('no overflow with long names inside a narrow box', (
    tester,
  ) async {
    await pump(
      tester,
      const InstanceBadges(
        compact: true,
        instances: [
          (id: 'lt16a-10006-a6b35523', name: 'LT16A-10006-FR'),
          (id: '192-168-1-100-3000', name: 'Friday Server'),
          (id: 'srv-c', name: 'A Third Very Long Instance Name'),
          (id: 'srv-d', name: 'A Fourth Very Long Instance Name'),
        ],
      ),
      width: 180,
    );
    expect(tester.takeException(), isNull);
  });
}
