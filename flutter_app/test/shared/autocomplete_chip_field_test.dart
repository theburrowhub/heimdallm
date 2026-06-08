import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/widgets/autocomplete_chip_field.dart';

void main() {
  testWidgets('shows available options when focused with empty query', (
    tester,
  ) async {
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: AutocompleteChipField(
            label: 'Organizations',
            selectedValues: const [],
            availableOptions: const ['acme', 'platform'],
            onChanged: (_) {},
          ),
        ),
      ),
    );

    expect(find.text('acme'), findsNothing);
    expect(find.text('platform'), findsNothing);

    await tester.tap(find.byType(TextFormField));
    await tester.pump();

    expect(find.text('acme'), findsOneWidget);
    expect(find.text('platform'), findsOneWidget);
  });
}
