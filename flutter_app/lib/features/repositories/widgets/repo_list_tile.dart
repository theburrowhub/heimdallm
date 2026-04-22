import 'package:flutter/material.dart';
import '../../../core/models/config_model.dart';
import 'feature_led.dart';
import 'feature_palette.dart';
import 'led_source.dart';
import 'local_dir_resolution.dart';

/// One row in the repos list. Stateless; all state flows via parameters
/// and callbacks.
class RepoListTile extends StatelessWidget {
  final String repo;
  final RepoConfig config;
  final AppConfig appConfig;
  final bool selected;
  final bool showNew;
  final VoidCallback onSelectionToggle;
  final VoidCallback onTap;

  const RepoListTile({
    super.key,
    required this.repo,
    required this.config,
    required this.appConfig,
    required this.selected,
    required this.showNew,
    required this.onSelectionToggle,
    required this.onTap,
  });

  @override
  Widget build(BuildContext context) {
    final localDir = LocalDirResolution.resolve(
      repo: repo, config: config, appConfig: appConfig,
    );
    final theme = Theme.of(context);
    final selectedBg = theme.colorScheme.primary.withValues(alpha:0.12);

    return Card(
      color: selected ? selectedBg : null,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(10),
        side: selected
            ? BorderSide(color: theme.colorScheme.primary.withValues(alpha:0.55))
            : BorderSide.none,
      ),
      margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
      child: InkWell(
        borderRadius: BorderRadius.circular(10),
        onTap: onTap,
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
          child: Row(
            children: [
              GestureDetector(
                key: const Key('RepoListTile_checkbox'),
                onTap: onSelectionToggle,
                behavior: HitTestBehavior.opaque,
                child: Padding(
                  padding: const EdgeInsets.only(right: 10),
                  child: _CheckboxIcon(selected: selected),
                ),
              ),
              Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  FeatureLed(
                    feature: Feature.prReview,
                    isOn: featureIsOn(
                      feature: Feature.prReview,
                      repo: repo,
                      config: config,
                      appConfig: appConfig,
                    ),
                    sourceLine: featureSourceLine(
                      feature: Feature.prReview,
                      repo: repo,
                      config: config,
                      appConfig: appConfig,
                    ),
                  ),
                  const SizedBox(height: 3),
                  FeatureLed(
                    feature: Feature.issueTracking,
                    isOn: featureIsOn(
                      feature: Feature.issueTracking,
                      repo: repo,
                      config: config,
                      appConfig: appConfig,
                    ),
                    sourceLine: featureSourceLine(
                      feature: Feature.issueTracking,
                      repo: repo,
                      config: config,
                      appConfig: appConfig,
                    ),
                  ),
                  const SizedBox(height: 3),
                  FeatureLed(
                    feature: Feature.develop,
                    isOn: featureIsOn(
                      feature: Feature.develop,
                      repo: repo,
                      config: config,
                      appConfig: appConfig,
                    ),
                    sourceLine: featureSourceLine(
                      feature: Feature.develop,
                      repo: repo,
                      config: config,
                      appConfig: appConfig,
                    ),
                  ),
                ],
              ),
              const SizedBox(width: 10),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Row(children: [
                      Flexible(
                        child: Text(
                          repo,
                          style: TextStyle(
                            fontWeight: config.isMonitored
                                ? FontWeight.w600
                                : FontWeight.normal,
                            color: config.isMonitored ? null : Colors.grey,
                          ),
                          overflow: TextOverflow.ellipsis,
                        ),
                      ),
                      if (showNew) ...[
                        const SizedBox(width: 6),
                        const _NewBadge(),
                      ],
                    ]),
                    const SizedBox(height: 2),
                    LocalDirBadge(resolution: localDir, fontSize: 11, iconSize: 13),
                  ],
                ),
              ),
              Icon(Icons.chevron_right, size: 18, color: Colors.grey.shade600),
            ],
          ),
        ),
      ),
    );
  }

}

class _CheckboxIcon extends StatelessWidget {
  final bool selected;
  const _CheckboxIcon({required this.selected});

  @override
  Widget build(BuildContext context) {
    final primary = Theme.of(context).colorScheme.primary;
    return Container(
      width: 16, height: 16,
      decoration: BoxDecoration(
        color: selected ? primary : Colors.transparent,
        border: Border.all(
          color: selected ? primary : const Color(0xFF6E7681),
          width: 1.5,
        ),
        borderRadius: BorderRadius.circular(3),
      ),
      child: selected
          ? const Icon(Icons.check, size: 12, color: Colors.white)
          : null,
    );
  }
}

class _NewBadge extends StatelessWidget {
  const _NewBadge();
  @override
  Widget build(BuildContext context) {
    final primary = Theme.of(context).colorScheme.primary;
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 1),
      decoration: BoxDecoration(
        color: primary.withValues(alpha:0.22),
        borderRadius: BorderRadius.circular(8),
      ),
      child: Text(
        'NEW',
        style: TextStyle(
          color: primary,
          fontSize: 10,
          fontWeight: FontWeight.w700,
          letterSpacing: 0.4,
        ),
      ),
    );
  }
}
