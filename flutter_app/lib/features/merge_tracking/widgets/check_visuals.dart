import 'package:flutter/material.dart';

import '../../../core/models/merge_tracking.dart';

/// The one place that maps a check state to an icon and a colour, so the
/// listing warning, the badge and the detail table can never disagree about
/// what red means.
class CheckVisuals {
  final IconData icon;
  final Color color;
  final String label;

  const CheckVisuals({
    required this.icon,
    required this.color,
    required this.label,
  });

  static CheckVisuals forCheck(BuildContext context, MergeCheck check) {
    final scheme = Theme.of(context).colorScheme;
    switch (check.state) {
      case 'failure':
        return CheckVisuals(
          icon: Icons.cancel,
          color: scheme.error,
          label: 'Failed',
        );
      case 'pending':
        return const CheckVisuals(
          icon: Icons.hourglass_top,
          color: Color(0xFFB26A00),
          label: 'Running',
        );
      case 'neutral':
        return CheckVisuals(
          icon: Icons.remove_circle_outline,
          color: scheme.onSurfaceVariant,
          label: 'Skipped',
        );
      default:
        return const CheckVisuals(
          icon: Icons.check_circle,
          color: Color(0xFF2E7D32),
          label: 'Passed',
        );
    }
  }
}

/// A compact counter of the check problems on a PR: `2✕ 1⏳`.
///
/// Sits next to the phase badge so the state of CI is legible at a glance even
/// when the row is collapsed and the full warning text is not visible.
class CheckCountChips extends StatelessWidget {
  final int failing;
  final int pending;

  const CheckCountChips({
    super.key,
    required this.failing,
    required this.pending,
  });

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    if (failing == 0 && pending == 0) return const SizedBox.shrink();
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        if (failing > 0)
          _chip(
            context,
            icon: Icons.cancel,
            count: failing,
            color: scheme.error,
            semantics: '$failing required checks failing',
          ),
        if (failing > 0 && pending > 0) const SizedBox(width: 4),
        if (pending > 0)
          _chip(
            context,
            icon: Icons.hourglass_top,
            count: pending,
            color: const Color(0xFFB26A00),
            semantics: '$pending required checks running',
          ),
      ],
    );
  }

  Widget _chip(
    BuildContext context, {
    required IconData icon,
    required int count,
    required Color color,
    required String semantics,
  }) {
    return Semantics(
      // container + excludeSemantics: the chip is one node reading "2 required
      // checks failing", not a bare "2" that a screen reader cannot place.
      container: true,
      excludeSemantics: true,
      label: semantics,
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
        decoration: BoxDecoration(
          color: color.withValues(alpha: 0.12),
          borderRadius: BorderRadius.circular(4),
          border: Border.all(color: color.withValues(alpha: 0.4)),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(icon, size: 12, color: color),
            const SizedBox(width: 3),
            Text(
              '$count',
              style: TextStyle(
                color: color,
                fontSize: 11,
                fontWeight: FontWeight.w700,
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// The prominent warning shown on a listing row whose merge is held up by CI.
///
/// A full-width coloured band rather than a subtle icon: a merge blocked by a
/// failing check is the single thing on this screen that needs a human, and it
/// has to be impossible to scroll past. The text is the daemon's block detail,
/// which always names the check ("1 required check is failing: build (GitHub
/// Actions)") rather than reporting a count.
class ChecksWarningBanner extends StatelessWidget {
  final MergeTrackingEntry entry;

  const ChecksWarningBanner({super.key, required this.entry});

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final failing = entry.hasFailingChecks;

    final Color fg = failing
        ? scheme.onErrorContainer
        : const Color(0xFF6B4300);
    final Color bg = failing ? scheme.errorContainer : const Color(0xFFFFF3CD);
    final IconData icon = failing ? Icons.error : Icons.hourglass_top;

    // The daemon's detail names the check, but only when CI is the primary
    // blocker. When something else is (a PR both behind its base and failing a
    // check), the detail describes that instead — so fall back to the counts,
    // which always describe the checks.
    final detail = entry.blockReasonIsChecks && entry.blockDetail.isNotEmpty
        ? entry.blockDetail
        : _fallbackDetail();

    return Container(
      width: double.infinity,
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
      decoration: BoxDecoration(
        color: bg,
        borderRadius: BorderRadius.circular(6),
        border: Border(left: BorderSide(color: fg, width: 4)),
      ),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Icon(icon, size: 18, color: fg),
          const SizedBox(width: 8),
          Expanded(
            child: Text(
              detail,
              style: TextStyle(
                color: fg,
                fontSize: 13,
                fontWeight: failing ? FontWeight.w600 : FontWeight.w500,
              ),
            ),
          ),
        ],
      ),
    );
  }

  /// Used only if the daemon somehow recorded no detail; the counts are always
  /// present, so the reader still learns something actionable.
  String _fallbackDetail() {
    if (entry.checksRequiredFailing > 0) {
      final n = entry.checksRequiredFailing;
      return '$n required ${n == 1 ? 'check is' : 'checks are'} failing.';
    }
    final n = entry.checksRequiredPending;
    return 'Waiting on $n required ${n == 1 ? 'check' : 'checks'}.';
  }
}
