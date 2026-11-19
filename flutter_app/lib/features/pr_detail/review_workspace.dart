import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:mix/mix.dart' show ColorToken;
import 'package:url_launcher/url_launcher.dart';

import '../../core/models/issue.dart';
import '../../core/models/pr.dart';
import '../../core/models/review.dart';
import '../../shared/design_system/color_resolver.dart';
import '../../shared/design_system/components/app_badge.dart';
import '../../shared/design_system/components/app_segmented_filter.dart';
import '../../shared/design_system/components/app_surface.dart';
import '../../shared/design_system/components/app_text.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/review_publication_badge.dart';
import '../../shared/widgets/severity_badge.dart';
import '../../shared/widgets/state_badge.dart';
import '../../shared/widgets/toast.dart';

/// The PR detail body as a review workspace (#900): the PR, its latest review
/// as a verdict, the findings grouped by file, the earlier reviews, and an
/// inspector with merge status and details.
///
/// Everything here comes from the `/prs/{id}` payload the screen already
/// loads; links to GitHub are opened by the browser, never fetched.
class ReviewWorkspace extends StatelessWidget {
  final PR pr;
  final List<Review> reviews;

  /// Starts a review; null when the PR cannot be reviewed (unknown repo).
  final VoidCallback? onReview;

  /// Shown first in the inspector, typically the merge status panel; in a
  /// narrow window, right under the header.
  final Widget? inspectorTop;

  /// Clock for relative times; injectable for tests.
  final DateTime Function() now;

  /// Opens a link; injectable for tests.
  final void Function(Uri uri) onOpen;

  const ReviewWorkspace({
    super.key,
    required this.pr,
    required this.reviews,
    this.onReview,
    this.inspectorTop,
    this.now = DateTime.now,
    this.onOpen = _launch,
  });

  /// From this width the inspector sits beside the content; below it, its
  /// parts go into the single column.
  static const double wideBreakpoint = 980;
  static const double inspectorWidth = 340;

  static void _launch(Uri uri) => launchUrl(uri);

  @override
  Widget build(BuildContext context) {
    final sorted = newestFirst(reviews);
    final at = now();
    final content = <Widget>[
      _Header(pr: pr, now: at),
      const SizedBox(height: 24),
      if (sorted.isEmpty)
        _NoReviewYet(onReview: onReview)
      else ...[
        _VerdictHero(pr: pr, review: sorted.first, now: at),
        const SizedBox(height: 24),
        FindingsSection(pr: pr, review: sorted.first, onOpen: onOpen),
        if (sorted.length > 1) ...[
          const SizedBox(height: 24),
          ReviewHistory(
            pr: pr,
            reviews: sorted.skip(1).toList(),
            now: at,
            onOpen: onOpen,
          ),
        ],
      ],
    ];
    final details = _DetailsCard(pr: pr, onOpen: onOpen);

    return LayoutBuilder(
      builder: (context, constraints) {
        if (constraints.maxWidth >= wideBreakpoint) {
          return Row(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Expanded(
                child: ListView(
                  key: const Key('workspace-content'),
                  padding: const EdgeInsets.fromLTRB(32, 28, 32, 36),
                  children: content,
                ),
              ),
              DecoratedBox(
                decoration: BoxDecoration(
                  color: resolveAppColor(context, AppColors.canvas),
                  border: Border(
                    left: BorderSide(
                      color: resolveAppColor(context, AppColors.border),
                    ),
                  ),
                ),
                child: SizedBox(
                  width: inspectorWidth,
                  child: ListView(
                    key: const Key('workspace-inspector'),
                    padding: const EdgeInsets.all(20),
                    children: [
                      if (inspectorTop != null) ...[
                        inspectorTop!,
                        const SizedBox(height: 16),
                      ],
                      details,
                    ],
                  ),
                ),
              ),
            ],
          );
        }
        // Narrow: the merge status goes right under the header, before the
        // review, as on the old screen: a red required check is the thing the
        // author has to act on. Details close the page.
        return ListView(
          key: const Key('workspace-content'),
          padding: const EdgeInsets.fromLTRB(20, 20, 20, 32),
          children: [
            content.first,
            if (inspectorTop != null) ...[
              const SizedBox(height: 20),
              inspectorTop!,
            ],
            ...content.skip(1),
            const SizedBox(height: 24),
            details,
          ],
        );
      },
    );
  }
}

/// Reviews newest first. The daemon already sends them so; the workspace does
/// not depend on it.
@visibleForTesting
List<Review> newestFirst(List<Review> reviews) =>
    [...reviews]..sort((a, b) => b.createdAt.compareTo(a.createdAt));

/// The verdict a review shows: GitHub's state, else the event the daemon
/// decided, else the one its severity implies (high requests changes), which is
/// the daemon's own fallback for reviews stored before the event was recorded.
@visibleForTesting
ReviewVerdict verdictOf(Review review) =>
    review.verdict ??
    (review.severity.toLowerCase() == 'high'
        ? ReviewVerdict.changesRequested
        : ReviewVerdict.approved);

final _repoSlug = RegExp(r'^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$');
final _commitSha = RegExp(r'^[0-9a-fA-F]{7,40}$');

/// The finding's line on GitHub at the reviewed commit, or null when a part is
/// missing or unsafe. The path comes from the agent's answer, so only a
/// relative path without dot segments on a github.com PR with a plain
/// owner/name and a hex commit becomes a link.
@visibleForTesting
Uri? findingUri(PR pr, Review review, Issue issue) {
  final prUri = Uri.tryParse(pr.url);
  if (prUri == null || prUri.scheme != 'https' || prUri.host != 'github.com') {
    return null;
  }
  if (!_repoSlug.hasMatch(pr.repo) || !_commitSha.hasMatch(review.headSha)) {
    return null;
  }
  final path = issue.file.split('/');
  if (issue.file.isEmpty ||
      path.any((s) => s.isEmpty || s == '.' || s == '..')) {
    return null;
  }
  final repo = pr.repo.split('/');
  return Uri(
    scheme: 'https',
    host: 'github.com',
    pathSegments: [repo[0], repo[1], 'blob', review.headSha, ...path],
    fragment: issue.line > 0 ? 'L${issue.line}' : null,
  );
}

/// "just now", "5 min ago", "3 h ago", "2 d ago", then the date.
@visibleForTesting
String relativeTime(DateTime at, DateTime now) {
  final d = now.difference(at);
  if (d.inMinutes < 1) return 'just now';
  if (d.inMinutes < 60) return '${d.inMinutes} min ago';
  if (d.inHours < 24) return '${d.inHours} h ago';
  if (d.inDays < 30) return '${d.inDays} d ago';
  final l = at.toLocal();
  String two(int n) => n.toString().padLeft(2, '0');
  return '${l.year}-${two(l.month)}-${two(l.day)}';
}

/// Initials for an avatar placeholder: "lmartin" → "LM", "octo-cat" → "OC".
@visibleForTesting
String initialsOf(String login) {
  final parts = login
      .split(RegExp(r'[-_.\s]+'))
      .where((p) => p.isNotEmpty)
      .toList();
  if (parts.isEmpty) return '?';
  if (parts.length == 1) {
    final p = parts.first;
    return p.substring(0, p.length < 2 ? p.length : 2).toUpperCase();
  }
  return (parts[0][0] + parts[1][0]).toUpperCase();
}

class _Header extends StatelessWidget {
  final PR pr;
  final DateTime now;

  const _Header({required this.pr, required this.now});

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final muted = resolveAppColor(context, AppColors.textMuted);
    final accent = resolveAppColor(context, AppColors.accent);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        AppText.mono(
          pr.repo.isEmpty ? '#${pr.number}' : '${pr.repo} · #${pr.number}',
          color: muted,
        ),
        const SizedBox(height: 6),
        Text(
          pr.title,
          style: theme.textTheme.headlineSmall?.copyWith(
            fontWeight: FontWeight.w600,
            height: 1.25,
          ),
        ),
        const SizedBox(height: 10),
        Wrap(
          spacing: 10,
          runSpacing: 6,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                ExcludeSemantics(
                  child: CircleAvatar(
                    radius: 10,
                    backgroundColor: accent.withValues(alpha: 0.14),
                    child: Text(
                      initialsOf(pr.author),
                      style: TextStyle(
                        fontSize: 9,
                        fontWeight: FontWeight.w700,
                        color: accent,
                      ),
                    ),
                  ),
                ),
                const SizedBox(width: 6),
                Text(pr.author, style: theme.textTheme.bodyMedium),
              ],
            ),
            Text(
              'updated ${relativeTime(pr.updatedAt, now)}',
              style: theme.textTheme.bodySmall?.copyWith(color: muted),
            ),
            StateBadge(state: pr.state),
          ],
        ),
      ],
    );
  }
}

class _NoReviewYet extends StatelessWidget {
  final VoidCallback? onReview;

  const _NoReviewYet({required this.onReview});

  @override
  Widget build(BuildContext context) {
    return AppSurface(
      padding: const EdgeInsets.symmetric(horizontal: 24, vertical: 32),
      child: Column(
        children: [
          Icon(
            Icons.rate_review_outlined,
            size: 32,
            color: resolveAppColor(context, AppColors.textMuted),
          ),
          const SizedBox(height: 12),
          const AppText.sectionTitle('No review yet'),
          const SizedBox(height: 6),
          const AppText.muted(
            'Heimdallm reviews this PR when your review is requested. You can '
            'also ask for one now.',
          ),
          const SizedBox(height: 16),
          FilledButton.icon(
            onPressed: onReview,
            icon: const Icon(Icons.play_arrow, size: 18),
            label: const Text('Review now'),
          ),
        ],
      ),
    );
  }
}

/// The verdict's label, icon and colour token.
({String label, IconData icon, ColorToken color}) _verdictLook(
  ReviewVerdict verdict,
) => switch (verdict) {
  ReviewVerdict.approved => (
    label: 'Approved',
    icon: Icons.check_circle_outline,
    color: AppColors.success,
  ),
  ReviewVerdict.changesRequested => (
    label: 'Changes requested',
    icon: Icons.error_outline,
    color: AppColors.danger,
  ),
  ReviewVerdict.commented => (
    label: 'Commented',
    icon: Icons.chat_bubble_outline,
    color: AppColors.info,
  ),
  ReviewVerdict.dismissed => (
    label: 'Dismissed',
    icon: Icons.remove_circle_outline,
    color: AppColors.textMuted,
  ),
};

class _VerdictHero extends StatelessWidget {
  final PR pr;
  final Review review;
  final DateTime now;

  const _VerdictHero({
    required this.pr,
    required this.review,
    required this.now,
  });

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final muted = resolveAppColor(context, AppColors.textMuted);
    final look = _verdictLook(verdictOf(review));
    final color = resolveAppColor(context, look.color);
    final counts = <String, int>{};
    for (final i in review.issues) {
      final s = i.severity.toLowerCase();
      counts[s] = (counts[s] ?? 0) + 1;
    }
    final agent = review.isPeer
        ? 'Covered by a peer instance'
        : 'Reviewed by ${review.cliUsed}';
    return AppSurface(
      key: const Key('verdict-hero'),
      elevation: AppSurfaceElevation.raised,
      radius: AppRadius.lg,
      padding: const EdgeInsets.fromLTRB(24, 20, 24, 20),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Wrap(
            spacing: 10,
            runSpacing: 8,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              Semantics(
                label: 'Verdict: ${look.label}',
                excludeSemantics: true,
                child: AppBadge(
                  label: look.label,
                  icon: Icon(look.icon),
                  foreground: color,
                  background: color.withValues(alpha: 0.12),
                  padding: const EdgeInsets.symmetric(
                    horizontal: 12,
                    vertical: 6,
                  ),
                  radius: 8,
                  fontSize: 14,
                  letterSpacing: 0,
                ),
              ),
              // The review's overall severity, which comment signals can
              // raise above its findings'. A peer row has none of its own.
              if (!review.isPeer) SeverityBadge(severity: review.severity),
              ReviewPublicationBadge(
                review: review,
                prUrl: pr.url,
                showVerdict: false,
              ),
              Text(
                '$agent · ${relativeTime(review.createdAt, now)}',
                style: theme.textTheme.bodySmall?.copyWith(color: muted),
              ),
            ],
          ),
          const SizedBox(height: 14),
          Text(
            review.summary,
            style: theme.textTheme.bodyLarge?.copyWith(height: 1.55),
          ),
          if (counts.isNotEmpty || review.hasTokenUsage) ...[
            const SizedBox(height: 14),
            Wrap(
              spacing: 8,
              runSpacing: 8,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                for (final s in const ['high', 'medium', 'low'])
                  if (counts[s] != null)
                    _CountPill(severity: s, count: counts[s]!),
                if (review.hasTokenUsage)
                  Text(
                    review.tokenUsageLabel,
                    style: theme.textTheme.labelSmall?.copyWith(color: muted),
                  ),
              ],
            ),
          ],
        ],
      ),
    );
  }
}

ColorToken _severityToken(String severity) => switch (severity.toLowerCase()) {
  'high' => AppColors.danger,
  'medium' => AppColors.warning,
  _ => AppColors.success,
};

class _CountPill extends StatelessWidget {
  final String severity;
  final int count;

  const _CountPill({required this.severity, required this.count});

  @override
  Widget build(BuildContext context) {
    final color = resolveAppColor(context, _severityToken(severity));
    return AppBadge(
      label: '$count $severity',
      foreground: color,
      background: color.withValues(alpha: 0.12),
      radius: 999,
      letterSpacing: 0,
    );
  }
}

/// The latest review's findings grouped by file, with a severity filter.
class FindingsSection extends StatefulWidget {
  final PR pr;
  final Review review;
  final void Function(Uri uri) onOpen;

  const FindingsSection({
    super.key,
    required this.pr,
    required this.review,
    required this.onOpen,
  });

  @override
  State<FindingsSection> createState() => _FindingsSectionState();
}

class _FindingsSectionState extends State<FindingsSection> {
  String _severity = 'all';
  final Set<String> _collapsed = {};

  @override
  Widget build(BuildContext context) {
    final issues = widget.review.issues;
    if (widget.review.isPeer) {
      // The peer instance published its own review; only a placeholder is
      // stored here, so its findings are on GitHub, not "none".
      return AppSurface(
        padding: const EdgeInsets.all(16),
        child: Row(
          children: [
            Icon(
              Icons.info_outline,
              size: 18,
              color: resolveAppColor(context, AppColors.info),
            ),
            const SizedBox(width: 10),
            const Expanded(
              child: AppText.muted(
                "The findings are in the peer instance's review on GitHub.",
              ),
            ),
          ],
        ),
      );
    }
    if (issues.isEmpty) {
      return AppSurface(
        padding: const EdgeInsets.all(16),
        child: Row(
          children: [
            Icon(
              Icons.check_circle_outline,
              size: 18,
              color: resolveAppColor(context, AppColors.success),
            ),
            const SizedBox(width: 10),
            const Expanded(child: AppText.muted('No findings in this review.')),
          ],
        ),
      );
    }
    int count(String s) =>
        issues.where((i) => i.severity.toLowerCase() == s).length;
    final shown = issues
        .where(
          (i) => _severity == 'all' || i.severity.toLowerCase() == _severity,
        )
        .toList();
    final byFile = <String, List<Issue>>{};
    for (final i in shown) {
      byFile.putIfAbsent(i.file, () => []).add(i);
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      spacing: 12,
      children: [
        Wrap(
          spacing: 12,
          runSpacing: 8,
          alignment: WrapAlignment.spaceBetween,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            const AppText.sectionTitle('Findings'),
            AppSegmentedFilter<String>(
              current: _severity,
              onChanged: (s) => setState(() => _severity = s),
              segments: [
                AppSegment(value: 'all', label: 'All', count: issues.length),
                AppSegment(value: 'high', label: 'High', count: count('high')),
                AppSegment(
                  value: 'medium',
                  label: 'Medium',
                  count: count('medium'),
                ),
                AppSegment(value: 'low', label: 'Low', count: count('low')),
              ],
            ),
          ],
        ),
        if (byFile.isEmpty)
          const AppSurface(
            padding: EdgeInsets.all(16),
            child: AppText.muted('No findings at this severity.'),
          ),
        for (final entry in byFile.entries)
          _FileGroup(
            path: entry.key,
            issues: entry.value,
            open: !_collapsed.contains(entry.key),
            onToggle: () => setState(() {
              if (!_collapsed.remove(entry.key)) _collapsed.add(entry.key);
            }),
            linkFor: (i) => findingUri(widget.pr, widget.review, i),
            onOpen: widget.onOpen,
          ),
      ],
    );
  }
}

class _FileGroup extends StatelessWidget {
  final String path;
  final List<Issue> issues;
  final bool open;
  final VoidCallback onToggle;
  final Uri? Function(Issue) linkFor;
  final void Function(Uri uri) onOpen;

  const _FileGroup({
    required this.path,
    required this.issues,
    required this.open,
    required this.onToggle,
    required this.linkFor,
    required this.onOpen,
  });

  @override
  Widget build(BuildContext context) {
    final muted = resolveAppColor(context, AppColors.textMuted);
    final border = resolveAppColor(context, AppColors.border);
    return AppSurface(
      elevation: AppSurfaceElevation.raised,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Material(
            color: resolveAppColor(context, AppColors.surface),
            child: InkWell(
              onTap: onToggle,
              child: Semantics(
                button: true,
                expanded: open,
                child: Padding(
                  padding: const EdgeInsets.symmetric(
                    horizontal: 14,
                    vertical: 10,
                  ),
                  child: Row(
                    children: [
                      AnimatedRotation(
                        turns: open ? 0.25 : 0,
                        duration: const Duration(milliseconds: 150),
                        child: Icon(
                          Icons.chevron_right,
                          size: 18,
                          color: muted,
                        ),
                      ),
                      const SizedBox(width: 6),
                      Icon(Icons.description_outlined, size: 16, color: muted),
                      const SizedBox(width: 8),
                      Expanded(
                        child: AppText.mono(
                          path.isEmpty ? '(no file)' : path,
                          maxLines: 1,
                        ),
                      ),
                      const SizedBox(width: 8),
                      AppText.label('${issues.length}', color: muted),
                    ],
                  ),
                ),
              ),
            ),
          ),
          if (open)
            for (final issue in issues)
              DecoratedBox(
                decoration: BoxDecoration(
                  border: Border(top: BorderSide(color: border)),
                ),
                child: _FindingRow(
                  issue: issue,
                  link: linkFor(issue),
                  onOpen: onOpen,
                ),
              ),
        ],
      ),
    );
  }
}

class _FindingRow extends StatelessWidget {
  final Issue issue;
  final Uri? link;
  final void Function(Uri uri) onOpen;

  /// Whether the row names its file: off under a file group's header, on in
  /// the history, where findings are not grouped.
  final bool showFile;

  const _FindingRow({
    required this.issue,
    required this.link,
    required this.onOpen,
    this.showFile = false,
  });

  @override
  Widget build(BuildContext context) {
    final muted = resolveAppColor(context, AppColors.textMuted);
    final where = issue.line > 0 ? '${issue.file}:${issue.line}' : issue.file;
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 12, 8, 12),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (!showFile)
            SizedBox(
              width: 52,
              child: Padding(
                padding: const EdgeInsets.only(top: 2),
                child: AppText.mono(
                  issue.line > 0 ? 'L${issue.line}' : '',
                  color: muted,
                ),
              ),
            ),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                if (showFile) ...[
                  AppText.mono(where, color: muted),
                  const SizedBox(height: 6),
                ],
                SeverityBadge(severity: issue.severity),
                const SizedBox(height: 6),
                SelectableText(
                  issue.description,
                  style: Theme.of(
                    context,
                  ).textTheme.bodyMedium?.copyWith(height: 1.5),
                ),
              ],
            ),
          ),
          if (link != null)
            IconButton(
              tooltip: 'Open this line on GitHub',
              iconSize: 17,
              visualDensity: VisualDensity.compact,
              icon: const Icon(Icons.open_in_new),
              onPressed: () => onOpen(link!),
            ),
          IconButton(
            tooltip: 'Copy finding',
            iconSize: 17,
            visualDensity: VisualDensity.compact,
            icon: const Icon(Icons.copy_outlined),
            onPressed: () async {
              await Clipboard.setData(
                ClipboardData(text: '$where: ${issue.description}'),
              );
              if (context.mounted) showToast(context, 'Finding copied');
            },
          ),
        ],
      ),
    );
  }
}

/// The reviews before the latest one, collapsed into a timeline. Each one
/// opens in place to its full summary, tokens and findings, so nothing an
/// earlier review said is out of reach.
class ReviewHistory extends StatefulWidget {
  final PR pr;
  final List<Review> reviews;
  final DateTime now;
  final void Function(Uri uri) onOpen;

  const ReviewHistory({
    super.key,
    required this.pr,
    required this.reviews,
    required this.now,
    required this.onOpen,
  });

  @override
  State<ReviewHistory> createState() => _ReviewHistoryState();
}

class _ReviewHistoryState extends State<ReviewHistory> {
  bool _open = false;
  final Set<int> _expanded = {};

  @override
  Widget build(BuildContext context) {
    final muted = resolveAppColor(context, AppColors.textMuted);
    final border = resolveAppColor(context, AppColors.border);
    final n = widget.reviews.length;
    return AppSurface(
      elevation: AppSurfaceElevation.raised,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          InkWell(
            onTap: () => setState(() => _open = !_open),
            child: Semantics(
              button: true,
              expanded: _open,
              child: Padding(
                padding: const EdgeInsets.symmetric(
                  horizontal: 14,
                  vertical: 12,
                ),
                child: Row(
                  children: [
                    AnimatedRotation(
                      turns: _open ? 0.25 : 0,
                      duration: const Duration(milliseconds: 150),
                      child: Icon(Icons.chevron_right, size: 18, color: muted),
                    ),
                    const SizedBox(width: 6),
                    Icon(Icons.history, size: 16, color: muted),
                    const SizedBox(width: 8),
                    const Expanded(
                      child: AppText.sectionTitle('Earlier reviews'),
                    ),
                    AppText.label(
                      n == 1 ? '1 review' : '$n reviews',
                      color: muted,
                    ),
                  ],
                ),
              ),
            ),
          ),
          if (_open)
            for (final r in widget.reviews)
              DecoratedBox(
                decoration: BoxDecoration(
                  border: Border(top: BorderSide(color: border)),
                ),
                child: _HistoryEntry(
                  pr: widget.pr,
                  review: r,
                  now: widget.now,
                  expanded: _expanded.contains(r.id),
                  onToggle: () => setState(() {
                    if (!_expanded.remove(r.id)) _expanded.add(r.id);
                  }),
                  onOpen: widget.onOpen,
                ),
              ),
        ],
      ),
    );
  }
}

/// One earlier review: its state and summary, opening to the full summary,
/// tokens and findings.
class _HistoryEntry extends StatelessWidget {
  final PR pr;
  final Review review;
  final DateTime now;
  final bool expanded;
  final VoidCallback onToggle;
  final void Function(Uri uri) onOpen;

  const _HistoryEntry({
    required this.pr,
    required this.review,
    required this.now,
    required this.expanded,
    required this.onToggle,
    required this.onOpen,
  });

  @override
  Widget build(BuildContext context) {
    final muted = resolveAppColor(context, AppColors.textMuted);
    final border = resolveAppColor(context, AppColors.border);
    final theme = Theme.of(context);
    final r = review;
    final findings = r.issues.length;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        InkWell(
          onTap: onToggle,
          child: Semantics(
            button: true,
            expanded: expanded,
            child: Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 12, 12),
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      spacing: 6,
                      children: [
                        Wrap(
                          spacing: 8,
                          runSpacing: 6,
                          crossAxisAlignment: WrapCrossAlignment.center,
                          children: [
                            ReviewPublicationBadge(review: r, prUrl: pr.url),
                            SeverityBadge(severity: r.severity),
                            Text(
                              '${r.isPeer ? 'Peer instance' : r.cliUsed} · '
                              '${relativeTime(r.createdAt, now)}'
                              '${findings == 0 ? '' : ' · $findings finding${findings == 1 ? '' : 's'}'}',
                              style: theme.textTheme.bodySmall?.copyWith(
                                color: muted,
                              ),
                            ),
                          ],
                        ),
                        Text(
                          r.summary,
                          maxLines: expanded ? null : 3,
                          overflow: expanded ? null : TextOverflow.ellipsis,
                          style: theme.textTheme.bodyMedium,
                        ),
                        if (expanded && r.hasTokenUsage)
                          Text(
                            r.tokenUsageLabel,
                            style: theme.textTheme.labelSmall?.copyWith(
                              color: muted,
                            ),
                          ),
                      ],
                    ),
                  ),
                  const SizedBox(width: 8),
                  Tooltip(
                    message: expanded ? 'Show less' : 'Show the whole review',
                    child: AnimatedRotation(
                      turns: expanded ? 0.5 : 0,
                      duration: const Duration(milliseconds: 150),
                      child: Icon(Icons.expand_more, size: 20, color: muted),
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
        if (expanded)
          for (final issue in r.issues)
            DecoratedBox(
              decoration: BoxDecoration(
                border: Border(top: BorderSide(color: border)),
              ),
              child: _FindingRow(
                issue: issue,
                link: findingUri(pr, r, issue),
                onOpen: onOpen,
                showFile: true,
              ),
            ),
      ],
    );
  }
}

class _DetailsCard extends StatelessWidget {
  final PR pr;
  final void Function(Uri uri) onOpen;

  const _DetailsCard({required this.pr, required this.onOpen});

  @override
  Widget build(BuildContext context) {
    final muted = resolveAppColor(context, AppColors.textMuted);
    final warning = resolveAppColor(context, AppColors.warning);
    final theme = Theme.of(context);
    final parsed = Uri.tryParse(pr.url);
    final Uri? prUri =
        parsed != null &&
            parsed.scheme == 'https' &&
            parsed.host == 'github.com'
        ? parsed
        : null;
    Widget row(String label, String value) => Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SizedBox(
            width: 84,
            child: Text(
              label,
              style: theme.textTheme.bodySmall?.copyWith(color: muted),
            ),
          ),
          Expanded(child: Text(value, style: theme.textTheme.bodyMedium)),
        ],
      ),
    );
    return AppSurface(
      elevation: AppSurfaceElevation.raised,
      padding: const EdgeInsets.fromLTRB(16, 14, 16, 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const AppText.sectionTitle('Details'),
          const SizedBox(height: 12),
          if (pr.repo.isEmpty)
            Container(
              margin: const EdgeInsets.only(bottom: 12),
              padding: const EdgeInsets.all(10),
              decoration: BoxDecoration(
                color: warning.withValues(alpha: 0.12),
                border: Border.all(color: warning.withValues(alpha: 0.4)),
                borderRadius: BorderRadius.circular(8),
              ),
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Icon(Icons.warning_amber, size: 16, color: warning),
                  const SizedBox(width: 8),
                  Expanded(
                    child: Text(
                      'Repo unknown. Re-discover repos in Settings to enable '
                      'auto-review.',
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: warning,
                      ),
                    ),
                  ),
                ],
              ),
            ),
          row('Repository', pr.repo.isEmpty ? '(unknown)' : pr.repo),
          row('Number', '#${pr.number}'),
          row('Author', pr.author),
          row('State', pr.state),
          // Exact, as the old screen showed it; the header has the relative.
          row('Updated', pr.updatedAt.toLocal().toString().substring(0, 16)),
          const SizedBox(height: 4),
          Align(
            alignment: Alignment.centerLeft,
            child: OutlinedButton.icon(
              icon: const Icon(Icons.open_in_new, size: 16),
              label: const Text('Open on GitHub'),
              onPressed: prUri == null ? null : () => onOpen(prUri),
            ),
          ),
          const SizedBox(height: 8),
        ],
      ),
    );
  }
}
