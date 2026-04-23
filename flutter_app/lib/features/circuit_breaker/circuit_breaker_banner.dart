import 'package:flutter/material.dart';

/// Banner shown when the daemon's review circuit breaker has tripped.
/// Dismiss is explicit — the user must acknowledge seeing the warning so
/// they can't miss a cost event. The message is sourced from the SSE
/// event payload.
class CircuitBreakerBanner extends StatelessWidget {
  final String message;
  final VoidCallback onDismiss;
  const CircuitBreakerBanner({
    super.key,
    required this.message,
    required this.onDismiss,
  });

  @override
  Widget build(BuildContext context) {
    return MaterialBanner(
      backgroundColor: Colors.red.shade50,
      leading: const Icon(Icons.warning_amber_rounded, color: Colors.red),
      content: Text(
        'Review circuit breaker tripped — $message',
        style: const TextStyle(color: Colors.black87),
      ),
      actions: [TextButton(onPressed: onDismiss, child: const Text('Dismiss'))],
    );
  }
}
