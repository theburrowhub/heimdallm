import 'package:flutter/material.dart';
import 'package:mix/mix.dart';
import 'package:url_launcher/url_launcher.dart';

import '../../core/models/review.dart';
import '../design_system/color_resolver.dart';
import '../design_system/components/app_badge.dart';
import '../design_system/tokens.dart';
import 'toast.dart';

/// Whether a review reached GitHub: its verdict and commit once posted, or why
/// it was not. A posted review links to itself on GitHub.
class ReviewPublicationBadge extends StatelessWidget {
  final Review review;

  /// The PR's GitHub URL. The badge links to the review only when this is an
  /// https URL on github.com, like the screen's "Open on GitHub" button.
  final String prUrl;

  /// Opens the review and says whether it could. Injectable for tests;
  /// defaults to the platform browser.
  final Future<bool> Function(Uri uri) onOpen;

  /// Whether a posted review's label names its verdict. Off where the verdict
  /// is already on screen beside the badge, as in the PR detail's verdict hero.
  final bool showVerdict;

  const ReviewPublicationBadge({
    super.key,
    required this.review,
    required this.prUrl,
    this.onOpen = launchUrl,
    this.showVerdict = true,
  });

  /// Opens [uri], with a toast when it cannot: no browser, or a refused launch.
  Future<void> _open(BuildContext context, Uri uri) async {
    var opened = false;
    try {
      opened = await onOpen(uri);
    } catch (_) {
      // No handler for the link (PlatformException): reported below.
    }
    if (!opened && context.mounted) {
      showToast(context, 'Could not open the review on GitHub.', isError: true);
    }
  }

  /// The review's own page on GitHub, or null when it is not posted or the PR
  /// URL is not a github.com https URL.
  Uri? get reviewUri {
    if (review.publication != ReviewPublication.posted) return null;
    final pr = Uri.tryParse(prUrl);
    if (pr == null || pr.scheme != 'https' || pr.host != 'github.com') {
      return null;
    }
    return pr.replace(fragment: 'pullrequestreview-${review.githubReviewId}');
  }

  @override
  Widget build(BuildContext context) {
    final look = _look(review, showVerdict: showVerdict);
    final color = resolveAppColor(context, look.color);
    final uri = reviewUri;
    final badge = AppBadge(
      label: look.label,
      icon: Icon(look.icon),
      foreground: color,
      background: color.withValues(alpha: 0.12),
      border: color.withValues(alpha: 0.4),
      radius: 4,
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      letterSpacing: 0.2,
    );
    final onTap = uri == null ? null : () => _open(context, uri);
    // The node carries the tap itself: excludeSemantics drops the InkWell's.
    // The InkWell gives keyboard focus and Enter/Space activation.
    return Semantics(
      label: '${look.label}. ${look.tooltip}',
      link: onTap != null,
      onTap: onTap,
      excludeSemantics: true,
      child: Tooltip(
        message: look.tooltip,
        child: onTap == null
            ? badge
            : InkWell(
                onTap: onTap,
                borderRadius: BorderRadius.circular(4),
                child: badge,
              ),
      ),
    );
  }
}

class _Look {
  final String label;
  final String tooltip;
  final IconData icon;
  final ColorToken color;

  const _Look(this.label, this.tooltip, this.icon, this.color);
}

_Look _look(Review review, {required bool showVerdict}) {
  switch (review.publication) {
    case ReviewPublication.pending:
      return const _Look(
        'Not posted yet',
        'Waiting to be published: Heimdallm retries pending reviews on every '
            'poll while the repository is enabled.',
        Icons.schedule,
        AppColors.warning,
      );
    case ReviewPublication.abandoned:
      return const _Look(
        'Not posted',
        'Heimdallm will not retry this review, for example because the PR '
            'was closed or merged first, or GitHub rejected it for good.',
        Icons.block,
        AppColors.textMuted,
      );
    case ReviewPublication.superseded:
      return const _Look(
        'Not posted: superseded',
        'A newer commit arrived before this review was posted, so the review '
            'of the newer commit replaces it.',
        Icons.update,
        AppColors.textMuted,
      );
    case ReviewPublication.posted:
      final (verdict, icon, color) = switch (review.verdict) {
        ReviewVerdict.approved => (
          'Approved',
          Icons.check_circle_outline,
          AppColors.success,
        ),
        ReviewVerdict.changesRequested => (
          'Changes requested',
          Icons.error_outline,
          AppColors.danger,
        ),
        ReviewVerdict.commented => (
          'Commented',
          Icons.chat_bubble_outline,
          AppColors.info,
        ),
        ReviewVerdict.dismissed => (
          'Dismissed',
          Icons.remove_circle_outline,
          AppColors.textMuted,
        ),
        null => ('Posted', Icons.cloud_done_outlined, AppColors.info),
      };
      final sha = review.shortSha.isEmpty ? '' : ' · ${review.shortSha}';
      final by = review.isPeer
          ? 'A peer Heimdallm instance posted this review on GitHub'
          : 'Posted on GitHub';
      final commit = review.headSha.isEmpty
          ? ''
          : ' for commit ${review.headSha}';
      if (!showVerdict) {
        return _Look(
          review.isPeer ? 'Posted by a peer$sha' : 'Posted$sha',
          '$by$commit.',
          Icons.cloud_done_outlined,
          AppColors.info,
        );
      }
      final who = review.isPeer ? 'Peer · ' : '';
      return _Look('$who$verdict$sha', '$by$commit.', icon, color);
  }
}
