import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/review_status.dart';

void main() {
  test('durable review execution status round-trips through JSON', () {
    final json = <String, dynamic>{
      'head_sha': 'abc123',
      'attempts': 2,
      'failed_at': '2026-08-24T10:30:00.000Z',
      'retry_at': '2026-08-24T10:40:00.000Z',
      'error': 'Review timed out before completion.',
      'active': false,
    };

    final status = ReviewExecutionStatus.fromJson(json);

    expect(status.headSha, 'abc123');
    expect(status.attempts, 2);
    expect(status.failedAt, DateTime.utc(2026, 8, 24, 10, 30));
    expect(status.retryAt, DateTime.utc(2026, 8, 24, 10, 40));
    expect(status.error, 'Review timed out before completion.');
    expect(status.active, isFalse);
    expect(status.toJson(), json);
  });
}
