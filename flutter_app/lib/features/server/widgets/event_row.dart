import 'dart:convert';

import 'package:flutter/material.dart';

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
    // Pull surface + secondary-text colours from the theme so the row
    // renders correctly in both light and dark mode (#453).
    final scheme = Theme.of(context).colorScheme;

    return InkWell(
      onTap: onTap,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              crossAxisAlignment: CrossAxisAlignment.center,
              children: [
                Icon(ev.icon, color: ev.color, size: 18),
                const SizedBox(width: 10),
                Expanded(
                  child: Text(
                    ev.label,
                    style: const TextStyle(
                      fontSize: 13,
                      fontWeight: FontWeight.w600,
                    ),
                  ),
                ),
                Text(
                  ts,
                  style: TextStyle(
                    fontFamily: 'monospace',
                    fontSize: 11,
                    color: scheme.onSurfaceVariant,
                  ),
                ),
              ],
            ),
            if (ev.target.isNotEmpty || ev.details.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(left: 28, top: 2),
                child: Wrap(
                  spacing: 8,
                  runSpacing: 2,
                  crossAxisAlignment: WrapCrossAlignment.center,
                  children: [
                    if (ev.target.isNotEmpty)
                      Text(
                        ev.target,
                        style: TextStyle(
                          fontFamily: 'monospace',
                          fontSize: 12,
                          color: scheme.onSurfaceVariant,
                        ),
                      ),
                    for (final d in ev.details) _DetailChip(text: d),
                  ],
                ),
              ),
            if (expanded)
              Container(
                margin: const EdgeInsets.only(left: 28, top: 6),
                padding: const EdgeInsets.all(8),
                decoration: BoxDecoration(
                  color: scheme.surfaceContainerHighest,
                  borderRadius: BorderRadius.circular(4),
                ),
                child: SelectableText(
                  _pretty(rawData),
                  style: TextStyle(
                    fontFamily: 'monospace',
                    fontSize: 11,
                    color: scheme.onSurface,
                  ),
                ),
              ),
          ],
        ),
      ),
    );
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

/// Subtle pill-style chip for one detail span (agent, duration, …).
/// Kept light-weight (no Material Chip) so dozens of rows stay snappy.
class _DetailChip extends StatelessWidget {
  const _DetailChip({required this.text});
  final String text;
  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
      decoration: BoxDecoration(
        color: scheme.surfaceContainerHighest,
        borderRadius: BorderRadius.circular(3),
      ),
      child: Text(
        text,
        style: TextStyle(fontSize: 11, color: scheme.onSurfaceVariant),
      ),
    );
  }
}
