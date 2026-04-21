import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/platform/platform_services.dart';

void main() {
  test('PlatformServices.create() returns a DesktopPlatformServices on the VM', () {
    final services = PlatformServices.create();
    // DesktopPlatformServices declares runtimeType.toString() == 'DesktopPlatformServices'.
    expect(services.runtimeType.toString(), 'DesktopPlatformServices');
  });

  test('DesktopPlatformServices.apiBaseUrl defaults to http://127.0.0.1:7842', () {
    final services = PlatformServices.create();
    expect(services.apiBaseUrl, 'http://127.0.0.1:7842');
  });
}
