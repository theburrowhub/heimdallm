import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/main.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'core/platform/fake_platform_services.dart';

void main() {
  testWidgets('HeimdallmApp builds the root router', (tester) async {
    // Use IgnoreTimers to avoid failures from SSE reconnect timers that start
    // when the router mounts the DashboardScreen inside HeimdallmApp.
    await tester.runAsync(() async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            platformServicesProvider.overrideWithValue(FakePlatformServices()),
          ],
          child: const HeimdallmApp(),
        ),
      );
      // Pump a single frame — we only assert the tree builds, not that it
      // settles (providers fire async requests that cannot settle in tests).
      await tester.pump(Duration.zero);
    });
    // Splash or dashboard — we're not asserting the content, only that
    // the app bootstraps without throwing on a web-shape platform.
    expect(tester.takeException(), isNull);
  });
}
