import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:heimdallm/features/server/widgets/event_row.dart';

void main() {
  // The EventRow widget is the visual contract for the Server > Events
  // tab. The pure-function format() tests cover the data side; these
  // widget tests pin the layout so a refactor can't silently drop the
  // icon, label, target, or chip slots that #453 introduced.

  Future<void> pumpRow(
    WidgetTester tester, {
    required String type,
    required Map<String, dynamic> payload,
    bool expanded = false,
  }) async {
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: EventRow(
            timestamp: DateTime(2026, 5, 12, 9, 41, 7),
            type: type,
            payload: payload,
            rawData: '{}',
            expanded: expanded,
            onTap: () {},
          ),
        ),
      ),
    );
  }

  testWidgets('renders human label, target, and chips for review_completed',
      (tester) async {
    await pumpRow(
      tester,
      type: 'review_completed',
      payload: {
        'repo': 'acme/foo',
        'number': 42,
        'duration_ms': 1500,
      },
    );

    expect(find.text('Review completed'), findsOneWidget);
    expect(find.text('acme/foo#42'), findsOneWidget);
    expect(find.text('1.5s'), findsOneWidget);
    expect(find.text('09:41:07'), findsOneWidget);
    // Status icon is present (any IconData satisfies — colour is the
    // contract, not the specific glyph).
    expect(find.byType(Icon), findsOneWidget);
  });

  testWidgets('omits target + chip wrap when payload has neither',
      (tester) async {
    // polling_started carries no target but does carry chips; flip to
    // an unknown event with empty payload to exercise the empty wrap.
    await pumpRow(tester, type: 'mystery_event', payload: const {});

    // Label still renders (raw type as fallback).
    expect(find.text('mystery_event'), findsOneWidget);
    // No Wrap child — keeps the row compact when there's nothing extra.
    expect(find.byType(Wrap), findsNothing);
  });

  testWidgets('renders multiple detail chips for polling_started',
      (tester) async {
    await pumpRow(
      tester,
      type: 'polling_started',
      payload: {
        'kind': 'prs',
        'repos': ['acme/a', 'acme/b', 'acme/c'],
      },
    );

    expect(find.text('Polling started'), findsOneWidget);
    expect(find.text('prs'), findsOneWidget);
    expect(find.text('3 repos'), findsOneWidget);
  });

  testWidgets('shows JSON expand block when expanded is true',
      (tester) async {
    await pumpRow(
      tester,
      type: 'review_completed',
      payload: {'repo': 'acme/foo'},
      expanded: true,
    );

    // The JSON pretty-print includes the SelectableText widget; the
    // expand block is the only thing on the row that uses it.
    expect(find.byType(SelectableText), findsOneWidget);
  });

  testWidgets('hides JSON expand block when expanded is false',
      (tester) async {
    await pumpRow(
      tester,
      type: 'review_completed',
      payload: {'repo': 'acme/foo'},
    );

    expect(find.byType(SelectableText), findsNothing);
  });
}
