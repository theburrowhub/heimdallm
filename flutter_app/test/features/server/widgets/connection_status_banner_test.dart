import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/server/widgets/connection_status_banner.dart';

void main() {
  testWidgets('ConnectionStatusBanner shows reconnecting message and spinner', (
    tester,
  ) async {
    await tester.pumpWidget(
      const MaterialApp(
        home: Scaffold(body: ConnectionStatusBanner()),
      ),
    );

    expect(find.textContaining('reconnecting'), findsOneWidget);
    expect(find.byType(CircularProgressIndicator), findsOneWidget);
  });
}
