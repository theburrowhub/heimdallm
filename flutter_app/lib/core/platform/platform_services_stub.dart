/// Compile-time fallback for when neither dart:io nor dart:html is available.
/// This file must never actually execute at runtime — the conditional export
/// in `platform_services.dart` picks the real impl for every real build.
class PlatformServicesImpl {
  PlatformServicesImpl() {
    throw UnsupportedError(
      'PlatformServicesImpl stub: no dart:io or dart:html available. '
      'This should be impossible under `flutter run`, `flutter test`, '
      'or `flutter build web`. Check your build tooling.',
    );
  }
}
