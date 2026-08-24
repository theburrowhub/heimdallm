class ReviewExecutionStatus {
  static const manualCancellationError = 'Review cancelled manually.';

  final String headSha;
  final int attempts;
  final DateTime failedAt;
  final DateTime retryAt;
  final String error;
  final bool active;

  const ReviewExecutionStatus({
    required this.headSha,
    required this.attempts,
    required this.failedAt,
    required this.retryAt,
    required this.error,
    required this.active,
  });

  factory ReviewExecutionStatus.fromJson(Map<String, dynamic> json) =>
      ReviewExecutionStatus(
        headSha: json['head_sha'] as String? ?? '',
        attempts: (json['attempts'] as num?)?.toInt() ?? 0,
        failedAt: DateTime.parse(json['failed_at'] as String),
        retryAt: DateTime.parse(json['retry_at'] as String),
        error:
            json['error'] as String? ??
            'Review failed before producing a result.',
        active: json['active'] as bool? ?? false,
      );

  Map<String, dynamic> toJson() => <String, dynamic>{
    'head_sha': headSha,
    'attempts': attempts,
    'failed_at': failedAt.toIso8601String(),
    'retry_at': retryAt.toIso8601String(),
    'error': error,
    'active': active,
  };

  bool get isCancelled => error == manualCancellationError;
}

String reviewFailureSummary(ReviewExecutionStatus failure, {DateTime? now}) {
  final current = (now ?? DateTime.now()).toLocal();
  final failed = failure.failedAt.toLocal();
  final retry = failure.retryAt.toLocal();
  final retryText = current.isBefore(retry)
      ? 'auto retry ${_formatLocalMinute(retry)} '
            '(in ${_compactDuration(retry.difference(current))})'
      : 'auto retry due now';
  final terminalVerb = failure.isCancelled ? 'cancelled' : 'failed';
  return '${failure.error} · $terminalVerb ${_formatLocalMinute(failed)} · '
      '$retryText';
}

String _formatLocalMinute(DateTime value) {
  String two(int n) => n.toString().padLeft(2, '0');
  return '${value.year}-${two(value.month)}-${two(value.day)} '
      '${two(value.hour)}:${two(value.minute)}';
}

String _compactDuration(Duration value) {
  final totalMinutes = value.inMinutes < 1 ? 1 : value.inMinutes;
  if (totalMinutes < 60) return '${totalMinutes}m';
  final hours = totalMinutes ~/ 60;
  final minutes = totalMinutes % 60;
  return minutes == 0 ? '${hours}h' : '${hours}h ${minutes}m';
}
