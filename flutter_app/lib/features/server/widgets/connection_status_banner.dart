import 'package:flutter/material.dart';

/// Thin in-tab banner shown when the Events tab's own SSE stream has dropped.
///
/// The tab owns a separate [SseClient] from the shared stream that drives the
/// global connection indicator, so a drop isolated to this connection would
/// otherwise leave the tab silently stalled while the global indicator still
/// reads "connected" (#572). The client auto-reconnects, hence the
/// "reconnecting" wording rather than a hard "offline".
class ConnectionStatusBanner extends StatelessWidget {
  const ConnectionStatusBanner({super.key});

  @override
  Widget build(BuildContext context) {
    const amber = Color(0xFFFFB347);
    // liveRegion so assistive tech announces the drop/recovery as it toggles.
    return Semantics(
      liveRegion: true,
      label: 'Live stream disconnected, reconnecting',
      child: Container(
        width: double.infinity,
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
        color: amber.withValues(alpha: 0.15),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            const SizedBox(
              width: 12,
              height: 12,
              child: CircularProgressIndicator(strokeWidth: 2, color: amber),
            ),
            const SizedBox(width: 8),
            Flexible(
              child: Text(
                'Live stream disconnected — reconnecting…',
                style: TextStyle(
                  fontSize: 12,
                  color: Theme.of(context).colorScheme.onSurface,
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}
