import 'dart:async';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/compat.dart';
import '../dashboard/dashboard_providers.dart';

class HealthDetail {
  final String? version;
  final DateTime? startedAt;

  /// Daemon API revision, null while no daemon answered. Daemons that predate
  /// the field count as the baseline revision.
  final int? apiVersion;
  const HealthDetail({this.version, this.startedAt, this.apiVersion});
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
        apiVersion: effectiveDaemonApiVersion(raw['api_version']),
      );
    }
    await Future<void>.delayed(const Duration(seconds: 30));
  }
});
