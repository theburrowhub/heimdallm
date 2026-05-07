import 'dart:async';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../dashboard/dashboard_providers.dart';

class HealthDetail {
  final String? version;
  final DateTime? startedAt;
  const HealthDetail({this.version, this.startedAt});
}

final serverHealthDetailProvider =
    StreamProvider.autoDispose<HealthDetail>((ref) async* {
  final api = ref.read(apiClientProvider);
  while (true) {
    final raw = await api.fetchHealth();
    if (raw == null) {
      yield const HealthDetail();
    } else {
      DateTime? started;
      final s = raw['started_at'];
      if (s is String) {
        started = DateTime.tryParse(s);
      }
      yield HealthDetail(
        version: raw['version'] as String?,
        startedAt: started,
      );
    }
    await Future<void>.delayed(const Duration(seconds: 30));
  }
});
