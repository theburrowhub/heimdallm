import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'platform_services.dart';

/// Singleton `PlatformServices` for the running build. Override this in
/// tests to inject a fake.
final platformServicesProvider = Provider<PlatformServices>((ref) {
  return PlatformServices.create();
});
