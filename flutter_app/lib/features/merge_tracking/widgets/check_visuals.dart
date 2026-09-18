import 'package:flutter/material.dart';

import '../../../core/models/merge_tracking.dart';
import '../../../shared/design_system/color_resolver.dart';
import '../../../shared/design_system/components/components.dart';
import '../../../shared/design_system/tokens.dart';

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
    switch (check.state) {
      case 'failure':
        return CheckVisuals(
          icon: Icons.cancel,
          color: resolveAppColor(context, AppColors.danger),
          label: 'Failed',
        );
      case 'pending':
        return CheckVisuals(
          icon: Icons.hourglass_top,
          color: resolveAppColor(context, AppColors.warning),
          label: 'Running',
        );
      case 'neutral':
        return CheckVisuals(
          icon: Icons.remove_circle_outline,
          color: resolveAppColor(context, AppColors.textMuted),
          label: 'Skipped',
        );
      default:
        return CheckVisuals(
          icon: Icons.check_circle,
          color: resolveAppColor(context, AppColors.success),
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
    final danger = resolveAppColor(context, AppColors.danger);
    final warning = resolveAppColor(context, AppColors.warning);
    if (failing == 0 && pending == 0) return const SizedBox.shrink();
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        if (failing > 0)
          _chip(
            context,
            icon: Icons.cancel,
            count: failing,
            color: danger,
            semantics: '$failing required checks failing',
          ),
        if (failing > 0 && pending > 0) const SizedBox(width: 4),
        if (pending > 0)
          _chip(
            context,
            icon: Icons.hourglass_top,
            count: pending,
            color: warning,
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
      child: AppBadge(
        label: '$count',
        foreground: color,
        background: color.withValues(alpha: 0.12),
        border: color.withValues(alpha: 0.4),
        icon: Icon(icon, size: 12),
        padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
        radius: 4,
        fontSize: 11,
        fontWeight: FontWeight.w700,
        letterSpacing: 0,
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

    return DecoratedBox(
      decoration: BoxDecoration(
        color: bg,
        borderRadius: BorderRadius.circular(6),
        border: Border(left: BorderSide(color: fg, width: 4)),
      ),
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Icon(icon, size: 18, color: fg),
            const SizedBox(width: 8),
            Expanded(child: AppText(detail, color: fg)),
          ],
        ),
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
