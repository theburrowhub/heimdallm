import 'package:flutter/material.dart';
import '../../../core/models/config_model.dart';

/// Resolution of the "effective local_dir" for a single repo, consumed by
/// both RepoGridTile and RepoListTile so the grid/list views stay visually
/// consistent.
///
/// The two tiles were drifting: the grid variant landed first, the list
/// variant copy-pasted five lines of precedence logic, and any future
/// change to either (e.g. adding a third source — a symlink resolver,
/// workspace-wide default, whatever) would have to happen in sync by
/// hand. Extracting here makes that impossible to get wrong.
class LocalDirResolution {
  /// Explicit per-repo `local_dir` (empty → null). Wins over detection;
  /// when set, the UI paints it green.
  final String? configured;

  /// Daemon-detected path (from `localDirsDetected` on AppConfig). Only
  /// meaningful when `configured` is null — when both exist, `effective`
  /// resolves to `configured`. When detection is the only source, the UI
  /// paints it blue ("Auto: <name>").
  final String? detected;

  const LocalDirResolution({this.configured, this.detected});

  /// The path the daemon will hand to the agent as CWD — configured wins,
  /// then detected, then null ("diff-only review, no full-repo context").
  String? get effective => configured ?? detected;

  /// Convenience: either source produced a usable path. Tiles branch their
  /// icon + text off this.
  bool get hasDir => (effective ?? '').isNotEmpty;

  /// True when the UI should label the badge "Auto: <name>" in blue —
  /// i.e. no explicit configuration, only the bind-mount fallback kicked in.
  bool get isAutoDetected => configured == null && (detected ?? '').isNotEmpty;

  /// Factory for tile build methods. Normalises an empty explicit
  /// `config.localDir` to null so downstream code only branches on `== null`.
  factory LocalDirResolution.resolve({
    required String repo,
    required RepoConfig config,
    required AppConfig appConfig,
  }) {
    final configured =
        (config.localDir ?? '').isNotEmpty ? config.localDir : null;
    return LocalDirResolution(
      configured: configured,
      detected: appConfig.localDirsDetected[repo],
    );
  }
}

/// Shared folder-icon + label rendered by both RepoGridTile and
/// RepoListTile. Font and icon sizes are parameterised so the list view
/// (comfortable density) and the grid view (tighter, more compact) each
/// get their own, but the three colour branches — green (configured),
/// blue (auto-detected), grey (no dir) — and the label format stay
/// identical across both surfaces.
class LocalDirBadge extends StatelessWidget {
  final LocalDirResolution resolution;
  final double fontSize;
  final double iconSize;
  const LocalDirBadge({
    super.key,
    required this.resolution,
    required this.fontSize,
    required this.iconSize,
  });

  @override
  Widget build(BuildContext context) {
    // `hasDir` guarantees `effective` is a non-empty String, so `!` is
    // safe inside the branch that actually reads it. Keeping the bang
    // local (not spread through callers) makes the narrowing explicit
    // for linters that don't promote through getters.
    final color = resolution.hasDir
        ? (resolution.isAutoDetected ? Colors.blue.shade400 : Colors.green.shade500)
        : Colors.grey.shade600;
    final String label;
    if (!resolution.hasDir) {
      label = 'No local dir';
    } else {
      final dirName = resolution.effective!.split('/').last;
      label = resolution.isAutoDetected ? 'Auto: $dirName' : dirName;
    }
    return Row(children: [
      Icon(
        resolution.hasDir ? Icons.folder : Icons.folder_off_outlined,
        size: iconSize,
        color: color,
      ),
      const SizedBox(width: 4),
      Flexible(
        child: Text(
          label,
          style: TextStyle(fontSize: fontSize, color: color),
          overflow: TextOverflow.ellipsis,
        ),
      ),
    ]);
  }
}
