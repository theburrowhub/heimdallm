import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/issue.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/models/review.dart';
import 'package:heimdallm/features/pr_detail/review_workspace.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

final _now = DateTime.utc(2026, 11, 19, 12);

PR _pr({
  String url = 'https://github.com/acme/payments-api/pull/482',
  String repo = 'acme/payments-api',
}) => PR(
  id: 1,
  githubId: 101,
  repo: repo,
  number: 482,
  title: 'Retry idempotent webhook deliveries',
  author: 'lmartin',
  url: url,
  state: 'open',
  updatedAt: _now.subtract(const Duration(minutes: 4)),
);

const _findings = [
  Issue(
    file: 'internal/webhooks/dispatcher.go',
    line: 142,
    description: 'A delivery can run twice.',
    severity: 'high',
  ),
  Issue(
    file: 'internal/webhooks/dispatcher.go',
    line: 188,
    description: 'A 409 is retried as transient.',
    severity: 'medium',
  ),
  Issue(
    file: 'internal/webhooks/backoff.go',
    line: 37,
    description: 'The backoff has no ceiling.',
    severity: 'medium',
  ),
];

Review _review({
  int id = 1,
  List<Issue> issues = _findings,
  String severity = 'high',
  int githubId = 4242,
  String state = 'CHANGES_REQUESTED',
  String event = '',
  String sha = 'c0ffee1234567890',
  Duration age = const Duration(minutes: 2),
  String summary = 'Two paths can deliver the same webhook twice.',
  int inputTokens = 0,
}) => Review(
  id: id,
  prId: 1,
  cliUsed: 'claude',
  summary: summary,
  issues: issues,
  severity: severity,
  createdAt: _now.subtract(age),
  githubReviewId: githubId,
  githubReviewState: state,
  event: event,
  headSha: sha,
  inputTokens: inputTokens,
  outputTokens: inputTokens == 0 ? 0 : 300,
);

Widget _host(Widget child, {Size size = const Size(1400, 1000)}) => MediaQuery(
  data: MediaQueryData(size: size),
  child: MaterialApp(
    theme: HeimdallmTheme.light(),
    builder: (context, c) => HeimdallmTheme.scope(child: c ?? const SizedBox()),
    home: Scaffold(body: child),
  ),
);

Future<void> _pumpAt(
  WidgetTester tester,
  Widget w, {
  Size size = const Size(1400, 1000),
}) async {
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(_host(w, size: size));
  await tester.pumpAndSettle();
}

void main() {
  group('helpers', () {
    test('newestFirst sorts by creation time, newest first', () {
      final old = _review(id: 1, age: const Duration(days: 2));
      final mid = _review(id: 2, age: const Duration(days: 1));
      final latest = _review(id: 3);
      expect(newestFirst([old, latest, mid]).map((r) => r.id), [3, 2, 1]);
    });

    test('verdictOf prefers GitHub, then the event, then the severity', () {
      expect(verdictOf(_review(state: 'APPROVED')), ReviewVerdict.approved);
      expect(
        verdictOf(_review(state: '', event: 'COMMENT')),
        ReviewVerdict.commented,
      );
      expect(
        verdictOf(_review(state: '', severity: 'high')),
        ReviewVerdict.changesRequested,
      );
      expect(
        verdictOf(_review(state: '', severity: 'medium')),
        ReviewVerdict.approved,
      );
    });

    test('findingUri links the line at the reviewed commit', () {
      expect(
        findingUri(_pr(), _review(), _findings.first).toString(),
        'https://github.com/acme/payments-api/blob/c0ffee1234567890/internal/webhooks/dispatcher.go#L142',
      );
      const noLine = Issue(
        file: 'a.go',
        line: 0,
        description: 'd',
        severity: 'low',
      );
      expect(findingUri(_pr(), _review(), noLine)!.hasFragment, isFalse);
    });

    test('findingUri refuses anything it cannot trust', () {
      Issue at(String file) =>
          Issue(file: file, line: 1, description: 'd', severity: 'low');
      final good = at('a.go');
      expect(
        findingUri(
          _pr(url: 'http://github.com/acme/payments-api/pull/482'),
          _review(),
          good,
        ),
        isNull,
      );
      expect(
        findingUri(
          _pr(url: 'https://evil.example/acme/payments-api/pull/482'),
          _review(),
          good,
        ),
        isNull,
      );
      expect(findingUri(_pr(repo: 'acme/pay ments'), _review(), good), isNull);
      expect(findingUri(_pr(repo: ''), _review(), good), isNull);
      expect(findingUri(_pr(), _review(sha: ''), good), isNull);
      expect(findingUri(_pr(), _review(sha: 'main'), good), isNull);
      for (final bad in [
        '',
        '/etc/passwd',
        '../../settings',
        'a/./b.go',
        'a//b.go',
      ]) {
        expect(findingUri(_pr(), _review(), at(bad)), isNull, reason: bad);
      }
    });

    test('relativeTime reads like a person would say it', () {
      expect(relativeTime(_now, _now), 'just now');
      expect(
        relativeTime(_now.subtract(const Duration(minutes: 5)), _now),
        '5 min ago',
      );
      expect(
        relativeTime(_now.subtract(const Duration(hours: 3)), _now),
        '3 h ago',
      );
      expect(
        relativeTime(_now.subtract(const Duration(days: 2)), _now),
        '2 d ago',
      );
      expect(relativeTime(DateTime(2026, 1, 9, 12), _now), '2026-01-09');
    });

    test('initialsOf', () {
      expect(initialsOf('lmartin'), 'LM');
      expect(initialsOf('octo-cat'), 'OC');
      expect(initialsOf('x'), 'X');
      expect(initialsOf(''), '?');
    });
  });

  group('layout', () {
    testWidgets('a wide window puts the inspector beside the content', (
      tester,
    ) async {
      await _pumpAt(
        tester,
        ReviewWorkspace(pr: _pr(), reviews: [_review()], now: () => _now),
      );
      final content = tester.getRect(
        find.byKey(const Key('workspace-content')),
      );
      final inspector = tester.getRect(
        find.byKey(const Key('workspace-inspector')),
      );
      expect(inspector.left, greaterThanOrEqualTo(content.right));
      expect(inspector.width, ReviewWorkspace.inspectorWidth);
      expect(tester.takeException(), isNull);
    });

    testWidgets('a narrow window stacks the inspector under the content', (
      tester,
    ) async {
      await _pumpAt(
        tester,
        ReviewWorkspace(pr: _pr(), reviews: [_review()], now: () => _now),
        size: const Size(700, 1000),
      );
      expect(find.byKey(const Key('workspace-inspector')), findsNothing);
      await tester.ensureVisible(find.text('Details'));
      expect(find.text('Open on GitHub'), findsOneWidget);
      expect(tester.takeException(), isNull);
    });
  });

  testWidgets('the latest review leads as a verdict', (tester) async {
    await _pumpAt(
      tester,
      ReviewWorkspace(
        pr: _pr(),
        reviews: [_review(inputTokens: 12000)],
        now: () => _now,
      ),
    );
    final hero = find.byKey(const Key('verdict-hero'));
    expect(
      find.descendant(of: hero, matching: find.text('Changes requested')),
      findsOneWidget,
    );
    expect(
      find.descendant(of: hero, matching: find.text('Posted · c0ffee1')),
      findsOneWidget,
    );
    expect(
      find.descendant(
        of: hero,
        matching: find.text('Reviewed by claude · 2 min ago'),
      ),
      findsOneWidget,
    );
    expect(
      find.descendant(
        of: hero,
        matching: find.text('Two paths can deliver the same webhook twice.'),
      ),
      findsOneWidget,
    );
    expect(
      find.descendant(of: hero, matching: find.text('1 high')),
      findsOneWidget,
    );
    expect(
      find.descendant(of: hero, matching: find.text('2 medium')),
      findsOneWidget,
    );
    expect(
      find.descendant(of: hero, matching: find.textContaining('12.0k in')),
      findsOneWidget,
    );
  });

  testWidgets('findings are grouped by file and filtered by severity', (
    tester,
  ) async {
    await _pumpAt(
      tester,
      ReviewWorkspace(pr: _pr(), reviews: [_review()], now: () => _now),
    );
    expect(find.text('internal/webhooks/dispatcher.go'), findsOneWidget);
    expect(find.text('internal/webhooks/backoff.go'), findsOneWidget);
    expect(find.text('A delivery can run twice.'), findsOneWidget);

    await tester.tap(find.text('High'));
    await tester.pumpAndSettle();
    expect(find.text('A delivery can run twice.'), findsOneWidget);
    expect(find.text('The backoff has no ceiling.'), findsNothing);
    expect(find.text('internal/webhooks/backoff.go'), findsNothing);

    await tester.tap(find.text('Low'));
    await tester.pumpAndSettle();
    expect(find.text('No findings at this severity.'), findsOneWidget);
  });

  testWidgets('a file group folds away', (tester) async {
    await _pumpAt(
      tester,
      ReviewWorkspace(pr: _pr(), reviews: [_review()], now: () => _now),
    );
    await tester.tap(find.text('internal/webhooks/dispatcher.go'));
    await tester.pumpAndSettle();
    expect(find.text('A delivery can run twice.'), findsNothing);
    expect(find.text('The backoff has no ceiling.'), findsOneWidget);
    await tester.tap(find.text('internal/webhooks/dispatcher.go'));
    await tester.pumpAndSettle();
    expect(find.text('A delivery can run twice.'), findsOneWidget);
  });

  testWidgets('a finding opens its line on GitHub and copies itself', (
    tester,
  ) async {
    final opened = <Uri>[];
    String? copied;
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
      SystemChannels.platform,
      (call) async {
        if (call.method == 'Clipboard.setData') {
          copied = (call.arguments as Map)['text'] as String;
        }
        return null;
      },
    );
    addTearDown(
      () => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        null,
      ),
    );

    await _pumpAt(
      tester,
      ReviewWorkspace(
        pr: _pr(),
        reviews: [_review()],
        now: () => _now,
        onOpen: opened.add,
      ),
    );
    await tester.tap(find.byTooltip('Open this line on GitHub').first);
    expect(
      opened.single.toString(),
      endsWith('/blob/c0ffee1234567890/internal/webhooks/dispatcher.go#L142'),
    );

    await tester.tap(find.byTooltip('Copy finding').first);
    await tester.pump();
    expect(
      copied,
      'internal/webhooks/dispatcher.go:142: A delivery can run twice.',
    );
    expect(find.text('Finding copied'), findsOneWidget);
    await tester.pumpAndSettle(const Duration(seconds: 5));
  });

  testWidgets('without a reviewed commit a finding has no GitHub link', (
    tester,
  ) async {
    await _pumpAt(
      tester,
      ReviewWorkspace(
        pr: _pr(),
        reviews: [_review(sha: '')],
        now: () => _now,
      ),
    );
    expect(find.byTooltip('Open this line on GitHub'), findsNothing);
    expect(find.byTooltip('Copy finding'), findsNWidgets(3));
  });

  testWidgets('a clean review says it found nothing', (tester) async {
    await _pumpAt(
      tester,
      ReviewWorkspace(
        pr: _pr(),
        reviews: [
          _review(issues: const [], severity: 'low', state: 'APPROVED'),
        ],
        now: () => _now,
      ),
    );
    expect(find.text('No findings in this review.'), findsOneWidget);
    expect(find.text('Approved'), findsOneWidget);
  });

  testWidgets('no review yet offers to review now', (tester) async {
    var asked = 0;
    await _pumpAt(
      tester,
      ReviewWorkspace(
        pr: _pr(),
        reviews: const [],
        now: () => _now,
        onReview: () => asked++,
      ),
    );
    expect(find.text('No review yet'), findsOneWidget);
    await tester.tap(find.text('Review now'));
    expect(asked, 1);
  });

  testWidgets('earlier reviews fold into a history', (tester) async {
    final earlier = _review(
      id: 2,
      githubId: -2,
      state: '',
      age: const Duration(days: 2),
      summary: 'An older look at the retry loop.',
    );
    await _pumpAt(
      tester,
      ReviewWorkspace(
        pr: _pr(),
        reviews: [earlier, _review()],
        now: () => _now,
      ),
    );
    expect(find.text('Earlier reviews'), findsOneWidget);
    expect(find.text('1 review'), findsOneWidget);
    expect(find.text('An older look at the retry loop.'), findsNothing);

    await tester.ensureVisible(find.text('Earlier reviews'));
    await tester.tap(find.text('Earlier reviews'));
    await tester.pumpAndSettle();
    expect(find.text('An older look at the retry loop.'), findsOneWidget);
    expect(find.text('Not posted: superseded'), findsOneWidget);
    expect(
      find.textContaining('claude · 2 d ago · 3 findings'),
      findsOneWidget,
    );
  });

  testWidgets('the details card opens the PR and warns about an unknown repo', (
    tester,
  ) async {
    final opened = <Uri>[];
    await _pumpAt(
      tester,
      ReviewWorkspace(
        pr: _pr(repo: ''),
        reviews: const [],
        now: () => _now,
        onOpen: opened.add,
      ),
    );
    expect(find.textContaining('Repo unknown.'), findsOneWidget);
    expect(find.text('(unknown)'), findsOneWidget);
    await tester.tap(find.text('Open on GitHub'));
    expect(
      opened.single.toString(),
      'https://github.com/acme/payments-api/pull/482',
    );
  });

  testWidgets('a PR URL off github.com disables Open on GitHub', (
    tester,
  ) async {
    await _pumpAt(
      tester,
      ReviewWorkspace(
        pr: _pr(url: 'https://evil.example/x'),
        reviews: const [],
        now: () => _now,
      ),
    );
    final button = tester.widget<OutlinedButton>(
      find.ancestor(
        of: find.text('Open on GitHub'),
        matching: find.byWidgetPredicate((w) => w is OutlinedButton),
      ),
    );
    expect(button.onPressed, isNull);
  });

  testWidgets('dark mode renders without layout errors', (tester) async {
    tester.view.physicalSize = const Size(1400, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      MaterialApp(
        theme: HeimdallmTheme.light(),
        darkTheme: HeimdallmTheme.dark(),
        themeMode: ThemeMode.dark,
        builder: (context, c) =>
            HeimdallmTheme.scope(child: c ?? const SizedBox()),
        home: Scaffold(
          body: ReviewWorkspace(
            pr: _pr(),
            reviews: [_review()],
            now: () => _now,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(tester.takeException(), isNull);
    expect(find.byKey(const Key('verdict-hero')), findsOneWidget);
  });
}
