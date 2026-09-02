import 'package:flutter/material.dart';

/// An inline "this change needs a daemon restart to take effect" banner.
///
/// Extracted from the Server screen's Listen-URL editor (the first place that
/// needed it) so the Config screen's Cluster section and the Instances tab
/// can show the exact same affordance instead of growing their own copies.
class RestartRequiredBanner extends StatelessWidget {
  const RestartRequiredBanner({
    super.key,
    required this.message,
    required this.onRestart,
    required this.starting,
    this.detail,
    this.buttonLabel = 'Restart server',
  });

  /// The primary explanation, e.g. "Listen URL changed. Restart the server
  /// for it to take effect."
  final String message;

  /// An optional second line for extra context, e.g. a note that the desktop
  /// app itself also needs restarting after a port change.
  final String? detail;

  final VoidCallback onRestart;

  /// Disables the button while a restart is already underway.
  final bool starting;

  final String buttonLabel;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: const Color(0xFFFFF4D6),
        border: Border.all(color: const Color(0xFFE8C547)),
        borderRadius: BorderRadius.circular(6),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              const Icon(Icons.warning_amber, size: 18),
              const SizedBox(width: 8),
              Expanded(
                child: Text(message, style: const TextStyle(fontSize: 13)),
              ),
              FilledButton(
                onPressed: starting ? null : onRestart,
                child: Text(buttonLabel),
              ),
            ],
          ),
          if (detail != null) ...[
            const SizedBox(height: 6),
            Text(detail!, style: const TextStyle(fontSize: 12)),
          ],
        ],
      ),
    );
  }
}
