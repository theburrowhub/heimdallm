import 'dart:io';
import 'package:tray_manager/tray_manager.dart';
import 'package:url_launcher/url_launcher.dart';
import 'package:window_manager/window_manager.dart';
import '../api/api_client.dart';
import '../models/pr.dart';
import '../../main.dart' show appRouter;

/// Convenience accessor for rebuilding the tray from providers.
class TrayMenuRef {
  static Future<void> rebuild({
    required List<PR> prs,
    required String me,
  }) => TrayMenu.instance.rebuild(prs: prs, me: me);
}

/// Manages the system tray context menu.
class TrayMenu with TrayListener {
  static final TrayMenu _instance = TrayMenu._();
  static TrayMenu get instance => _instance;
  TrayMenu._();

  ApiClient? _api;
  List<PR> _prs = [];

  /// Initialises the tray listener.
  /// [apiClient] must be the shared instance from the app's provider so that
  /// token cache invalidations (clearTokenCache) propagate correctly.
  void init({required ApiClient apiClient}) {
    _api = apiClient;
    trayManager.addListener(this);
  }

  /// Rebuilds the tray context menu with current data.
  Future<void> rebuild({
    required List<PR> prs,
    required String me,
  }) async {
    _prs = prs;

    // Pending = reviews where I'm the reviewer and there's no review yet
    final pending = prs
        .where((p) =>
            p.repo.isNotEmpty &&
            p.author.toLowerCase() != me.toLowerCase() &&
            p.latestReview == null)
        .toList();

    // Reviewed today (any review created today)
    final now = DateTime.now().toLocal();
    final reviewedToday = prs.where((p) {
      if (p.latestReview == null) return false;
      final d = p.latestReview!.createdAt.toLocal();
      return d.year == now.year && d.month == now.month && d.day == now.day;
    }).length;

    // Update tray icon urgency
    await _updateIcon(pending);

    final items = <MenuItem>[];

    // ── Summary ─────────────────────────────────────────────────────────
    if (pending.isEmpty) {
      items.add(_info('✓  No pending reviews'));
    } else {
      items.add(_info(
          '⏳  ${pending.length} pending review${pending.length == 1 ? '' : 's'}'));
    }
    if (reviewedToday > 0) {
      items.add(_info('✓  $reviewedToday reviewed today'));
    }

    items.add(MenuItem.separator());

    // ── Pending reviews ──────────────────────────────────────────────────
    if (pending.isNotEmpty) {
      const maxShown = 7;
      for (final pr in pending.take(maxShown)) {
        items.add(_pendingItem(pr));
      }
      if (pending.length > maxShown) {
        items.add(MenuItem(
          key: 'open',
          label: '   + ${pending.length - maxShown} more…',
        ));
      }
      items.add(MenuItem.separator());
    }

    // ── App controls ────────────────────────────────────────────────────
    items.add(MenuItem(key: 'open', label: 'Open Heimdallm'));
    items.add(MenuItem.separator());
    items.add(MenuItem(key: 'quit', label: 'Quit'));

    await trayManager.setContextMenu(Menu(items: items));
  }

  // ── Item builders ──────────────────────────────────────────────────────

  MenuItem _pendingItem(PR pr) {
    final short = _shortRepo(pr.repo);
    return MenuItem(
      key: 'pr_${pr.id}',
      label: '○   #${pr.number}  $short',
      toolTip: pr.title,
      submenu: Menu(items: [
        MenuItem(key: 'pr_title_${pr.id}', label: pr.title, disabled: true),
        _info(pr.repo),
        MenuItem.separator(),
        MenuItem(key: 'open_pr_${pr.id}', label: 'Open in Heimdallm'),
        MenuItem(key: 'view_${pr.id}',    label: 'View on GitHub'),
        MenuItem.separator(),
        MenuItem(key: 'review_${pr.id}',  label: 'Review Now'),
      ]),
    );
  }

  MenuItem _info(String label) =>
      MenuItem(key: '_i_${label.hashCode}', label: label, disabled: true);

  // ── Tray icon ─────────────────────────────────────────────────────────

  Future<void> _updateIcon(List<PR> pending) async {
    // TODO: swap icons when urgency assets are available.
    // For now a single icon is used; the pending count in the menu conveys urgency.
    try {
      await trayManager.setIcon(
        Platform.isLinux ? 'assets/tray_icon@2x.png' : 'assets/tray_icon.png',
      );
    } catch (_) {}
  }

  // ── Helpers ───────────────────────────────────────────────────────────

  /// Returns just the repo name without the org prefix.
  /// `freepik-company/ai-bumblebee-proxy` → `bumblebee-proxy`
  String _shortRepo(String repo) {
    final idx = repo.lastIndexOf('/');
    return idx >= 0 ? repo.substring(idx + 1) : repo;
  }

  // ── Click handlers ────────────────────────────────────────────────────

  @override
  void onTrayIconMouseDown() {
    trayManager.popUpContextMenu();
  }

  @override
  void onTrayMenuItemClick(MenuItem menuItem) {
    final key = menuItem.key ?? '';

    if (key == 'quit') { exit(0); }

    if (key == 'open') { _showApp(); return; }

    if (key.startsWith('view_')) {
      final prId = int.tryParse(key.substring(5));
      if (prId != null) {
        final pr = _prs.where((p) => p.id == prId).firstOrNull;
        if (pr != null) _launchGitHubUrl(pr.url);
      }
      return;
    }

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

    if (key.startsWith('review_')) {
      final prId = int.tryParse(key.substring(7));
      if (prId != null) {
        _api?.triggerReview(prId).catchError((_) => null);
      }
      return;
    }
  }

  void _showApp() {
    windowManager.show();
    windowManager.focus();
  }

  /// Launches [url] only if it is a valid https://github.com URL.
  /// Silently ignores invalid or non-GitHub URLs to prevent handler hijacking.
  void _launchGitHubUrl(String url) {
    final uri = Uri.tryParse(url);
    if (uri != null && uri.scheme == 'https' && uri.host == 'github.com') {
      launchUrl(uri);
    }
  }
}
