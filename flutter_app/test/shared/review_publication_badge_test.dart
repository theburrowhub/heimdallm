import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:mix/mix.dart';
import 'package:heimdallm/core/models/review.dart';
import 'package:heimdallm/shared/design_system/components/app_badge.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';
import 'package:heimdallm/shared/widgets/review_publication_badge.dart';

const _prUrl = 'https://github.com/acme/widgets/pull/7';

Review _review({
  int id = 0,
  String state = '',
  String event = '',
  String sha = '',
  String cli = 'claude',
}) => Review(
  id: 1,
  prId: 2,
  cliUsed: cli,
  summary: 'ok',
  issues: const [],
  severity: 'low',
  createdAt: DateTime.utc(2026, 11, 19),
  githubReviewId: id,
  githubReviewState: state,
  event: event,
  headSha: sha,
);

Widget _host(Widget child) => MaterialApp(
  builder: (context, child) =>
      HeimdallmTheme.scope(child: child ?? const SizedBox.shrink()),
  home: Scaffold(body: Center(child: child)),
);

AppBadge _badge(WidgetTester tester) =>
    tester.widget<AppBadge>(find.byType(AppBadge));

Color _token(WidgetTester tester, ColorToken token) =>
    token.resolve(tester.element(find.byType(AppBadge)));

void main() {
  testWidgets('a posted review shows its verdict and commit and opens itself', (
    tester,
  ) async {
    Uri? opened;
    await tester.pumpWidget(
      _host(
        ReviewPublicationBadge(
          review: _review(id: 4242, state: 'APPROVED', sha: '0123456789abcdef'),
          prUrl: _prUrl,
          onOpen: (uri) async {
            opened = uri;
            return true;
          },
        ),
      ),
    );
    expect(find.text('Approved · 0123456'), findsOneWidget);
    expect(_badge(tester).foreground, _token(tester, AppColors.success));

    await tester.tap(find.byType(AppBadge));
    expect(
      opened,
      Uri.parse(
        'https://github.com/acme/widgets/pull/7#pullrequestreview-4242',
      ),
    );
  });

  testWidgets('each verdict has its own label and colour', (tester) async {
    final cases = {
      'CHANGES_REQUESTED': ('Changes requested', AppColors.danger),
      'COMMENTED': ('Commented', AppColors.info),
      'DISMISSED': ('Dismissed', AppColors.textMuted),
      '': ('Posted', AppColors.info),
    };
    for (final entry in cases.entries) {
      await tester.pumpWidget(
        _host(
          ReviewPublicationBadge(
            review: _review(id: 1, state: entry.key),
            prUrl: _prUrl,
          ),
        ),
      );
      expect(find.text(entry.value.$1), findsOneWidget, reason: entry.key);
      expect(
        _badge(tester).foreground,
        _token(tester, entry.value.$2),
        reason: entry.key,
      );
    }
  });

  testWidgets('reviews that were not posted say why and link nowhere', (
    tester,
  ) async {
    final cases = {
      0: ('Not posted yet', AppColors.warning),
      -1: ('Not posted', AppColors.textMuted),
      -2: ('Not posted: superseded', AppColors.textMuted),
    };
    for (final entry in cases.entries) {
      Uri? opened;
      final badge = ReviewPublicationBadge(
        review: _review(id: entry.key, event: 'APPROVE', sha: 'abcdef1234'),
        prUrl: _prUrl,
        onOpen: (uri) async {
          opened = uri;
          return true;
        },
      );
      await tester.pumpWidget(_host(badge));
      expect(find.text(entry.value.$1), findsOneWidget, reason: '${entry.key}');
      expect(
        _badge(tester).foreground,
        _token(tester, entry.value.$2),
        reason: '${entry.key}',
      );
      expect(badge.reviewUri, isNull);
      await tester.tap(find.byType(AppBadge));
      expect(opened, isNull);
    }
  });

  testWidgets('a peer instance review is labelled as such', (tester) async {
    await tester.pumpWidget(
      _host(
        ReviewPublicationBadge(
          review: _review(
            id: 9,
            state: 'APPROVED',
            sha: 'feedbeef00',
            cli: 'peer',
          ),
          prUrl: _prUrl,
        ),
      ),
    );
    expect(find.text('Peer · Approved · feedbee'), findsOneWidget);
  });

  test('only an https github.com PR URL becomes a link', () {
    final posted = _review(id: 5, state: 'APPROVED');
    for (final url in [
      'http://github.com/acme/widgets/pull/7',
      'https://evil.example/acme/widgets/pull/7',
      'javascript:alert(1)',
      '',
    ]) {
      expect(
        ReviewPublicationBadge(review: posted, prUrl: url).reviewUri,
        isNull,
        reason: url,
      );
    }
  });

  testWidgets('screen readers hear the label and the explanation', (
    tester,
  ) async {
    final handle = tester.ensureSemantics();
    await tester.pumpWidget(
      _host(ReviewPublicationBadge(review: _review(id: -1), prUrl: _prUrl)),
    );
    expect(
      find.bySemanticsLabel(RegExp(r'^Not posted\. Heimdallm will not retry')),
      findsOneWidget,
    );
    handle.dispose();
  });

  // Keyboard users reach the link with Tab and open it with Enter.
  testWidgets('a posted review opens from the keyboard', (tester) async {
    Uri? opened;
    await tester.pumpWidget(
      _host(
        ReviewPublicationBadge(
          review: _review(id: 4242, state: 'APPROVED'),
          prUrl: _prUrl,
          onOpen: (uri) async {
            opened = uri;
            return true;
          },
        ),
      ),
    );
    await tester.sendKeyEvent(LogicalKeyboardKey.tab);
    await tester.pump();
    await tester.sendKeyEvent(LogicalKeyboardKey.enter);
    await tester.pump();
    expect(opened?.fragment, 'pullrequestreview-4242');
  });

  // The badge's semantics node is a link that a screen reader can activate.
  testWidgets('a screen reader can open a posted review', (tester) async {
    final handle = tester.ensureSemantics();
    Uri? opened;
    await tester.pumpWidget(
      _host(
        ReviewPublicationBadge(
          review: _review(id: 4242, state: 'APPROVED'),
          prUrl: _prUrl,
          onOpen: (uri) async {
            opened = uri;
            return true;
          },
        ),
      ),
    );
    final badge = find.byType(ReviewPublicationBadge);
    expect(
      tester.getSemantics(badge),
      isSemantics(isLink: true, hasTapAction: true),
    );
    tester.semantics.tap(find.semantics.byLabel(RegExp('^Approved')));
    await tester.pump();
    expect(opened?.fragment, 'pullrequestreview-4242');
    handle.dispose();
  });

  // When the link cannot open (no browser, or the launch is refused), the
  // user is told rather than nothing happening.
  testWidgets('a link that cannot open says so', (tester) async {
    for (final open in <Future<bool> Function(Uri)>[
      (_) async => false,
      (_) async => throw PlatformException(code: 'ACTIVITY_NOT_FOUND'),
    ]) {
      await tester.pumpWidget(
        _host(
          ReviewPublicationBadge(
            review: _review(id: 4242, state: 'APPROVED'),
            prUrl: _prUrl,
            onOpen: open,
          ),
        ),
      );
      await tester.tap(find.byType(AppBadge));
      await tester.pump();
      expect(find.text('Could not open the review on GitHub.'), findsOneWidget);
      await tester.pump(const Duration(seconds: 4)); // let the toast expire
      await tester.pumpAndSettle();
    }
  });

  // Beside a verdict already on screen, the badge says only that it posted.
  testWidgets('without the verdict a posted review reads Posted', (
    tester,
  ) async {
    for (final (review, label) in [
      (
        _review(id: 7, state: 'APPROVED', sha: 'c0ffee1234'),
        'Posted · c0ffee1',
      ),
      (
        _review(id: 8, state: 'APPROVED', sha: 'feedbeef00', cli: 'peer'),
        'Posted by a peer · feedbee',
      ),
    ]) {
      await tester.pumpWidget(
        _host(
          ReviewPublicationBadge(
            review: review,
            prUrl: _prUrl,
            showVerdict: false,
          ),
        ),
      );
      expect(find.text(label), findsOneWidget);
      expect(_badge(tester).foreground, _token(tester, AppColors.info));
    }
    // Reviews that were not posted still say why.
    await tester.pumpWidget(
      _host(
        ReviewPublicationBadge(
          review: _review(id: 0),
          prUrl: _prUrl,
          showVerdict: false,
        ),
      ),
    );
    expect(find.text('Not posted yet'), findsOneWidget);
  });
}
