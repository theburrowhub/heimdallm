import 'dart:io';
import 'package:tray_manager/tray_manager.dart';
import 'package:url_launcher/url_launcher.dart';
import 'package:window_manager/window_manager.dart';
import '../api/api_client.dart';
import '../models/config_model.dart';
import '../models/pr.dart';
import '../../main.dart' show appRouter;

/// Convenience accessor for rebuilding the tray from providers.
class TrayMenuRef {
  static Future<void> rebuild({
    required List<PR> prs,
    required String me,
    required AppConfig config,
  }) => TrayMenu.instance.rebuild(prs: prs, me: me, config: config);
}

/// Manages the system tray context menu.
///
/// Call [rebuild] whenever the PR list or config changes.
/// The menu handles navigation, GitHub links, and review triggers internally.
class TrayMenu with TrayListener {
  static final TrayMenu _instance = TrayMenu._();
  static TrayMenu get instance => _instance;
  TrayMenu._();

  final ApiClient _api = ApiClient();

  // Latest known PRs — used by menu item click handlers
  List<PR> _prs = [];

  void init() {
    trayManager.addListener(this);
  }

  /// Rebuilds the tray context menu with current data.
  Future<void> rebuild({
    required List<PR> prs,
    required String me,
    required AppConfig config,
  }) async {
    _prs = prs;

    final myPRs = prs
        .where((p) => p.author.toLowerCase() == me.toLowerCase())
        .toList();
    final myReviews = prs
        .where((p) => p.author.toLowerCase() != me.toLowerCase())
        .toList();
    final monitoredRepos = config.repoConfigs.entries
        .where((e) => e.value.monitored)
        .map((e) => e.key)
        .toList()
      ..sort();
    final disabledRepos = config.repoConfigs.entries
        .where((e) => !e.value.monitored)
        .map((e) => e.key)
        .toList()
      ..sort();

    final items = <MenuItem>[
      // ── My PRs ───────────────────────────────────────────────────────
      _header('My PRs  (${myPRs.length})'),
      if (myPRs.isEmpty)
        _disabled('  No open PRs'),
      ...myPRs.take(6).map(_prItem),

      MenuItem.separator(),

      // ── My Reviews ───────────────────────────────────────────────────
      _header('My Reviews  (${myReviews.length})'),
      if (myReviews.isEmpty)
        _disabled('  No pending reviews'),
      ...myReviews.take(6).map(_prItem),

      MenuItem.separator(),

      // ── Repositories ─────────────────────────────────────────────────
      _header('Repositories'),
      ...monitoredRepos.take(5).map((r) => MenuItem(
            key: 'repo_$r',
            label: '✓  $r',
          )),
      ...disabledRepos.take(3).map((r) => MenuItem(
            key: 'repo_dis_$r',
            label: '○  $r',
            disabled: true,
          )),
      if (config.repoConfigs.isEmpty) _disabled('  Not configured'),

      MenuItem.separator(),

      // ── App ──────────────────────────────────────────────────────────
      MenuItem(key: 'open', label: 'Open Heimdallr'),
      MenuItem(key: 'settings', label: 'Settings'),

      MenuItem.separator(),

      MenuItem(key: 'quit', label: 'Quit'),
    ];

    await trayManager.setContextMenu(Menu(items: items));
  }

  MenuItem _prItem(PR pr) {
    final reviewed = pr.latestReview != null;
    final statusIcon = reviewed
        ? _severityIcon(pr.latestReview!.severity)
        : '○';
    final label = '$statusIcon  #${pr.number}  ${pr.repo}';

    return MenuItem(
      key: 'pr_${pr.id}',
      label: label,
      toolTip: pr.title,
      submenu: Menu(items: [
        MenuItem(
          key: 'pr_title_${pr.id}',
          label: pr.title,
          disabled: true,
        ),
        MenuItem.separator(),
        MenuItem(key: 'view_${pr.id}', label: 'View on GitHub'),
        MenuItem(key: 'open_pr_${pr.id}', label: 'Open in Heimdallr'),
        MenuItem.separator(),
        MenuItem(
          key: 'review_${pr.id}',
          label: reviewed ? 'Re-review' : 'Review Now',
        ),
      ]),
    );
  }

  MenuItem _header(String label) => MenuItem(
        key: '_h_$label',
        label: label,
        disabled: true,
      );

  MenuItem _disabled(String label) => MenuItem(
        key: '_d_$label',
        label: label,
        disabled: true,
      );

  String _severityIcon(String severity) {
    switch (severity.toLowerCase()) {
      case 'high':   return '🔴';
      case 'medium': return '🟡';
      default:       return '🟢';
    }
  }

  // ── Click handlers ─────────────────────────────────────────────────────

  @override
  void onTrayIconMouseDown() {
    trayManager.popUpContextMenu();
  }

  @override
  void onTrayMenuItemClick(MenuItem menuItem) {
    final key = menuItem.key ?? '';

    // Quit
    if (key == 'quit') { exit(0); }

    // Open main window
    if (key == 'open') {
      _showApp();
      return;
    }

    // Settings
    if (key == 'settings') {
      _showApp();
      Future.delayed(const Duration(milliseconds: 200), () {
        appRouter.go('/config');
      });
      return;
    }

    // View on GitHub — launch URL
    if (key.startsWith('view_')) {
      final prId = int.tryParse(key.substring(5));
      if (prId != null) {
        final pr = _prs.where((p) => p.id == prId).firstOrNull;
        if (pr != null) launchUrl(Uri.parse(pr.url));
      }
      return;
    }

    // Open PR detail in app
    if (key.startsWith('open_pr_')) {
      final prId = int.tryParse(key.substring(8));
      if (prId != null) {
        _showApp();
        Future.delayed(const Duration(milliseconds: 200), () {
          appRouter.push('/prs/$prId');
        });
      }
      return;
    }

    // Trigger review
    if (key.startsWith('review_')) {
      final prId = int.tryParse(key.substring(7));
      if (prId != null) {
        _api.triggerReview(prId).catchError((_) => null);
      }
      return;
    }
  }

  void _showApp() {
    windowManager.show();
    windowManager.focus();
  }
}
