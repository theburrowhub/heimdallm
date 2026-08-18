import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/platform/platform_services.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/updates/check_for_updates_button.dart';

import '../../core/platform/fake_platform_services.dart';

void main() {
  testWidgets('is hidden when native updates are unavailable', (tester) async {
    final platform = FakePlatformServices();
    await tester.pumpWidget(_app(platform));
    expect(find.byKey(const Key('check-for-updates')), findsNothing);
  });

  testWidgets('opens the native updater and prevents duplicate checks', (
    tester,
  ) async {
    final completion = Completer<void>();
    final platform = _BlockingUpdatePlatform(completion);
    await tester.pumpWidget(_app(platform));

    await tester.tap(find.byKey(const Key('check-for-updates')));
    await tester.pump();
    expect(platform.checkForAppUpdatesCalls, 1);
    expect(find.byType(CircularProgressIndicator), findsOneWidget);

    await tester.tap(find.byKey(const Key('check-for-updates')));
    expect(platform.checkForAppUpdatesCalls, 1);

    completion.complete();
    await tester.pumpAndSettle();
    expect(find.byIcon(Icons.system_update_alt), findsOneWidget);
  });
}

Widget _app(FakePlatformServices platform) => ProviderScope(
  overrides: [platformServicesProvider.overrideWithValue(platform)],
  child: const MaterialApp(home: Scaffold(body: CheckForUpdatesButton())),
);

class _BlockingUpdatePlatform extends FakePlatformServices {
  _BlockingUpdatePlatform(this.completion)
    : super(appUpdateSupport: AppUpdateSupport.native);

  final Completer<void> completion;

  @override
  Future<void> checkForAppUpdates() {
    checkForAppUpdatesCalls++;
    return completion.future;
  }
}
