import 'package:flutter/material.dart';
import 'package:mix/mix.dart';

import '../../../shared/design_system/components/components.dart';
import '../../../shared/design_system/tokens.dart';

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
    final accent = AppColors.warning.resolve(context);
    return Semantics(
      liveRegion: true,
      label: 'Live stream disconnected, reconnecting',
      child: Box(
        style: BoxStyler()
            .color(accent.withValues(alpha: 0.12))
            .borderAll(color: accent.withValues(alpha: 0.24), width: 1)
            .borderRadiusAll(AppRadius.md())
            .padding(
              EdgeInsetsGeometryMix.value(
                const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
              ),
            ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            SizedBox(
              width: 12,
              height: 12,
              child: CircularProgressIndicator(strokeWidth: 2, color: accent),
            ),
            const SizedBox(width: 8),
            const Flexible(
              child: AppText('Live stream disconnected — reconnecting…'),
            ),
          ],
        ),
      ),
    );
  }
}
