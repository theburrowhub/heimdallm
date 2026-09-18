import 'dart:convert';

import 'package:flutter/material.dart';

import '../../../shared/design_system/components/components.dart';
import '../../../shared/design_system/tokens.dart';
import '../event_summary.dart';

/// One row of the Server > Events tab. Renders the structured fields of
/// the underlying [FormattedEvent] (label + icon up top, target + chip-
/// style details below) and, when [expanded] is true, the pretty-printed
/// raw JSON payload as a debugging fallback.
///
/// Kept as a public widget so widget tests can exercise the visual
/// contract (icon, label, target, chips) without standing up the full
/// events tab + SSE client.
class EventRow extends StatelessWidget {
  const EventRow({
    super.key,
    required this.timestamp,
    required this.type,
    required this.payload,
    required this.rawData,
    required this.expanded,
    required this.onTap,
  });

  final DateTime timestamp;
  final String type;
  final Map<String, dynamic> payload;
  final String rawData;
  final bool expanded;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final ev = format(type, payload);
    final ts = formatTimestamp(timestamp);

    return InkWell(
      onTap: onTap,
      child: AppSurface(
        elevation: AppSurfaceElevation.surface,
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
        bordered: false,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              crossAxisAlignment: CrossAxisAlignment.center,
              children: [
                Icon(ev.icon, color: ev.color, size: 18),
                const SizedBox(width: 10),
                Expanded(
                  child: AppText(
                    ev.label,
                    role: AppTextRole.label,
                    color: AppColors.text.resolve(context),
                  ),
                ),
                AppText.mono(ts, color: AppColors.textMuted.resolve(context)),
              ],
            ),
            if (ev.target.isNotEmpty || ev.details.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(left: 28, top: 4),
                child: Wrap(
                  spacing: 8,
                  runSpacing: 4,
                  crossAxisAlignment: WrapCrossAlignment.center,
                  children: [
                    if (ev.target.isNotEmpty)
                      AppText.mono(
                        ev.target,
                        color: AppColors.textMuted.resolve(context),
                      ),
                    for (final d in ev.details)
                      _DetailChip(
                        text: d,
                        color: _detailColor(ev.status, context),
                      ),
                  ],
                ),
              ),
            if (expanded)
              Padding(
                padding: const EdgeInsets.only(left: 28, top: 8),
                child: AppSurface(
                  elevation: AppSurfaceElevation.raised,
                  padding: const EdgeInsets.all(8),
                  child: SelectableText(
                    _pretty(rawData),
                    style: TextStyle(
                      fontFamily: 'monospace',
                      fontSize: 11,
                      color: AppColors.text.resolve(context),
                    ),
                  ),
                ),
              ),
          ],
        ),
      ),
    );
  }

  Color _detailColor(EventStatus status, BuildContext context) {
    return switch (status) {
      EventStatus.started => AppColors.warning.resolve(context),
      EventStatus.succeeded => AppColors.success.resolve(context),
      EventStatus.failed => AppColors.danger.resolve(context),
      EventStatus.skipped => AppColors.textMuted.resolve(context),
      EventStatus.info => AppColors.info.resolve(context),
      EventStatus.warning => AppColors.warning.resolve(context),
    };
  }

  static String _pretty(String raw) {
    try {
      return const JsonEncoder.withIndent('  ').convert(jsonDecode(raw));
    } catch (_) {
      return raw;
    }
  }
}

/// `hh:mm:ss` for the right-aligned timestamp column.
String formatTimestamp(DateTime t) {
  final hh = t.hour.toString().padLeft(2, '0');
  final mm = t.minute.toString().padLeft(2, '0');
  final ss = t.second.toString().padLeft(2, '0');
  return '$hh:$mm:$ss';
}

class _DetailChip extends StatelessWidget {
  const _DetailChip({required this.text, required this.color});

  final String text;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return AppBadge(
      label: text,
      foreground: color,
      background: color.withValues(alpha: 0.12),
      border: color.withValues(alpha: 0.18),
      padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
      radius: 6,
      fontSize: 11,
      letterSpacing: 0,
      fontWeight: FontWeight.w500,
    );
  }
}
