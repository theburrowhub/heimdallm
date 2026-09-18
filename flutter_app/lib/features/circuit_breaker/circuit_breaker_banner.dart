import 'package:flutter/material.dart';

import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';

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
    final danger = AppColors.danger.resolve(context);
    return MaterialBanner(
      backgroundColor: danger.withValues(alpha: 0.08),
      leading: Icon(Icons.warning_amber_rounded, color: danger),
      content: AppText('Review circuit breaker tripped — $message'),
      actions: [
        TextButton(
          onPressed: onDismiss,
          child: const AppText.label('Dismiss'),
        ),
      ],
    );
  }
}
