import 'package:flutter/material.dart';
import 'package:mix/mix.dart';

import '../../../core/models/config_model.dart';
import '../../../shared/design_system/components/components.dart';
import '../../../shared/design_system/tokens.dart';
import 'feature_led.dart';
import 'feature_palette.dart';
import 'led_source.dart';
import 'local_dir_resolution.dart';

class RepoGridTile extends StatelessWidget {
  final String repo;
  final RepoConfig config;
  final AppConfig appConfig;
  final bool selected;
  final bool showNew;
  final VoidCallback onSelectionToggle;
  final VoidCallback onTap;

  const RepoGridTile({
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
    final parts = repo.split('/');
    final org = parts.length > 1 ? parts[0] : '';
    final name = parts.length > 1 ? parts.sublist(1).join('/') : repo;
    final localDir = LocalDirResolution.resolve(
      repo: repo,
      config: config,
      appConfig: appConfig,
    );
    final primary = Theme.of(context).colorScheme.primary;

    return Box(
      style: BoxStyler()
          .color(
            selected ? primary.withValues(alpha: 0.12) : AppColors.surface(),
          )
          .borderAll(
            color: selected
                ? primary.withValues(alpha: 0.55)
                : AppColors.border(),
            width: 1,
          )
          .borderRadiusAll(AppRadius.lg())
          .clipBehavior(Clip.antiAlias),
      child: Material(
        color: Colors.transparent,
        child: InkWell(
          onTap: onTap,
          borderRadius: BorderRadius.circular(12),
          child: Padding(
            padding: const EdgeInsets.fromLTRB(12, 12, 12, 10),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    GestureDetector(
                      key: const Key('RepoGridTile_checkbox'),
                      behavior: HitTestBehavior.opaque,
                      onTap: onSelectionToggle,
                      child: _GridCheckbox(selected: selected),
                    ),
                    const Spacer(),
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
                      size: 9,
                    ),
                    const SizedBox(width: 4),
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
                      size: 9,
                    ),
                    const SizedBox(width: 4),
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
                      size: 9,
                    ),
                    const SizedBox(width: 4),
                    FeatureLed(
                      feature: Feature.mergeTracking,
                      isOn: featureIsOn(
                        feature: Feature.mergeTracking,
                        repo: repo,
                        config: config,
                        appConfig: appConfig,
                      ),
                      sourceLine: featureSourceLine(
                        feature: Feature.mergeTracking,
                        repo: repo,
                        config: config,
                        appConfig: appConfig,
                      ),
                      size: 9,
                    ),
                  ],
                ),
                const SizedBox(height: 10),
                Row(
                  children: [
                    Flexible(
                      child: StyledText(
                        name,
                        style: TextStyler()
                            .style(AppTextStyles.body.mix())
                            .fontSize(13)
                            .fontWeight(
                              config.isMonitored
                                  ? FontWeight.w600
                                  : FontWeight.w500,
                            )
                            .color(
                              config.isMonitored
                                  ? AppColors.text()
                                  : AppColors.textMuted(),
                            )
                            .maxLines(2)
                            .overflow(TextOverflow.ellipsis),
                      ),
                    ),
                    if (showNew) ...[
                      const SizedBox(width: 4),
                      const _NewBadge(),
                    ],
                  ],
                ),
                const SizedBox(height: 2),
                AppText.muted(
                  org,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                ),
                const Spacer(),
                LocalDirBadge(
                  resolution: localDir,
                  fontSize: 10.5,
                  iconSize: 12,
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

class _NewBadge extends StatelessWidget {
  const _NewBadge();
  @override
  Widget build(BuildContext context) {
    final primary = Theme.of(context).colorScheme.primary;
    return AppBadge(
      label: 'NEW',
      foreground: primary,
      background: primary.withValues(alpha: 0.18),
      padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 1),
      radius: 8,
      fontSize: 10,
      fontWeight: FontWeight.w700,
      letterSpacing: 0.4,
    );
  }
}

class _GridCheckbox extends StatelessWidget {
  final bool selected;

  const _GridCheckbox({required this.selected});

  @override
  Widget build(BuildContext context) {
    final primary = Theme.of(context).colorScheme.primary;
    return Box(
      style: BoxStyler()
          .width(15)
          .height(15)
          .color(selected ? primary : Colors.transparent)
          .borderAll(
            color: selected ? primary : AppColors.textMuted(),
            width: 1.5,
          )
          .borderRadiusAll(Radius.circular(3)),
      child: selected
          ? const Icon(Icons.check, size: 10, color: Colors.white)
          : null,
    );
  }
}
