import 'package:flutter/material.dart';
import 'package:mix/mix.dart';
import 'package:url_launcher/url_launcher.dart';

import '../../core/models/review.dart';
import '../design_system/color_resolver.dart';
import '../design_system/components/app_badge.dart';
import '../design_system/tokens.dart';

/// Whether a review reached GitHub: its verdict and commit once posted, or why
/// it was not. A posted review links to itself on GitHub.
class ReviewPublicationBadge extends StatelessWidget {
  final Review review;

  /// The PR's GitHub URL. The badge links to the review only when this is an
  /// https URL on github.com, like the screen's "Open on GitHub" button.
  final String prUrl;

  /// Opens the review. Injectable for tests; defaults to the platform browser.
  final void Function(Uri uri) onOpen;

  const ReviewPublicationBadge({
    super.key,
    required this.review,
    required this.prUrl,
    this.onOpen = _launch,
  });

  static void _launch(Uri uri) => launchUrl(uri);

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
    final look = _look(review);
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
    return Semantics(
      label: '${look.label}. ${look.tooltip}',
      button: uri != null,
      excludeSemantics: true,
      child: Tooltip(
        message: look.tooltip,
        child: uri == null
            ? badge
            : MouseRegion(
                cursor: SystemMouseCursors.click,
                child: GestureDetector(onTap: () => onOpen(uri), child: badge),
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

_Look _look(Review review) {
  switch (review.publication) {
    case ReviewPublication.pending:
      return const _Look(
        'Not posted yet',
        'Heimdallm will publish this review on its next publish attempt.',
        Icons.schedule,
        AppColors.warning,
      );
    case ReviewPublication.abandoned:
      return const _Look(
        'Not posted',
        'Heimdallm will not retry this review: the PR was closed or merged '
            'first, or GitHub rejected it for good.',
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
      final who = review.isPeer ? 'Peer · ' : '';
      final sha = review.shortSha.isEmpty ? '' : ' · ${review.shortSha}';
      final commit = review.headSha.isEmpty
          ? ''
          : ' for commit ${review.headSha}';
      final by = review.isPeer
          ? 'A peer Heimdallm instance posted this review on GitHub'
          : 'Posted on GitHub';
      return _Look('$who$verdict$sha', '$by$commit.', icon, color);
  }
}
