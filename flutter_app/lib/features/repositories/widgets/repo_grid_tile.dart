import 'package:flutter/material.dart';
import '../../../core/models/config_model.dart';
import 'feature_led.dart';
import 'feature_palette.dart';

class RepoGridTile extends StatelessWidget {
  final String repo;
  final RepoConfig config;
  final AppConfig appConfig;
  final bool selected;
  final VoidCallback onSelectionToggle;
  final VoidCallback onTap;

  const RepoGridTile({
    super.key,
    required this.repo,
    required this.config,
    required this.appConfig,
    required this.selected,
    required this.onSelectionToggle,
    required this.onTap,
  });

  @override
  Widget build(BuildContext context) {
    final parts = repo.split('/');
    final org = parts.length > 1 ? parts[0] : '';
    final name = parts.length > 1 ? parts.sublist(1).join('/') : repo;
    final hasDir = config.localDir != null && config.localDir!.isNotEmpty;
    final primary = Theme.of(context).colorScheme.primary;

    return InkWell(
      onTap: onTap,
      borderRadius: BorderRadius.circular(10),
      child: Container(
        padding: const EdgeInsets.fromLTRB(12, 12, 12, 10),
        decoration: BoxDecoration(
          color: selected ? primary.withOpacity(0.12) : const Color(0xFF22262E),
          border: Border.all(
            color: selected ? primary.withOpacity(0.55) : const Color(0xFF2E333B),
          ),
          borderRadius: BorderRadius.circular(10),
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(children: [
              GestureDetector(
                key: const Key('RepoGridTile_checkbox'),
                behavior: HitTestBehavior.opaque,
                onTap: onSelectionToggle,
                child: Container(
                  width: 15, height: 15,
                  decoration: BoxDecoration(
                    color: selected ? primary : Colors.transparent,
                    border: Border.all(
                      color: selected ? primary : const Color(0xFF6E7681),
                      width: 1.5,
                    ),
                    borderRadius: BorderRadius.circular(3),
                  ),
                  child: selected
                      ? const Icon(Icons.check, size: 10, color: Colors.white)
                      : null,
                ),
              ),
              const Spacer(),
              FeatureLed(
                feature: Feature.prReview,
                isOn: _isOn(Feature.prReview),
                sourceLine: _sourceLine(Feature.prReview),
                size: 9,
              ),
              const SizedBox(width: 4),
              FeatureLed(
                feature: Feature.issueTracking,
                isOn: _isOn(Feature.issueTracking),
                sourceLine: _sourceLine(Feature.issueTracking),
                size: 9,
              ),
              const SizedBox(width: 4),
              FeatureLed(
                feature: Feature.develop,
                isOn: _isOn(Feature.develop),
                sourceLine: _sourceLine(Feature.develop),
                size: 9,
              ),
            ]),
            const SizedBox(height: 10),
            Text(
              name,
              style: TextStyle(
                fontSize: 13,
                fontWeight: config.isMonitored ? FontWeight.w600 : FontWeight.w500,
                color: config.isMonitored ? null : Colors.grey.shade500,
              ),
              maxLines: 2, overflow: TextOverflow.ellipsis,
            ),
            const SizedBox(height: 2),
            Text(
              org,
              style: TextStyle(fontSize: 10.5, color: Colors.grey.shade500),
              maxLines: 1, overflow: TextOverflow.ellipsis,
            ),
            const Spacer(),
            Row(children: [
              Icon(
                hasDir ? Icons.folder : Icons.folder_off_outlined,
                size: 12, color: hasDir ? Colors.green.shade500 : Colors.grey.shade600,
              ),
              const SizedBox(width: 4),
              Flexible(
                child: Text(
                  hasDir ? config.localDir!.split('/').last : 'No local dir',
                  style: TextStyle(
                    fontSize: 10.5,
                    color: hasDir ? Colors.green.shade500 : Colors.grey.shade600,
                  ),
                  overflow: TextOverflow.ellipsis,
                ),
              ),
            ]),
          ],
        ),
      ),
    );
  }

  bool _isOn(Feature f) {
    final inGlobalList = appConfig.repositories.contains(repo);
    final globalIt = appConfig.issueTracking.enabled;
    final hasDir = config.localDir != null && config.localDir!.isNotEmpty;
    final status = switch (f) {
      Feature.prReview      => config.prLedStatus(inGlobalList),
      Feature.issueTracking => config.itLedStatus(globalIt),
      Feature.develop       => config.devLedStatus(globalIt, hasDir),
    };
    return status != 'off';
  }

  String _sourceLine(Feature f) {
    // Same logic as RepoListTile — duplicated here to keep the widget
    // self-contained. Kept intentionally: a shared helper would add an
    // awkward import dependency between siblings.
    final inGlobalList = appConfig.repositories.contains(repo);
    final globalIt = appConfig.issueTracking.enabled;
    final hasDir = config.localDir != null && config.localDir!.isNotEmpty;
    switch (f) {
      case Feature.prReview:
        if (config.prEnabled == true) return 'Source: repo-level (prEnabled = true)';
        if (config.prEnabled == false) return 'Source: disabled per-repo (prEnabled = false)';
        return inGlobalList
            ? 'Source: inherited from global monitored list'
            : 'Source: not in monitored list';
      case Feature.issueTracking:
        if (config.itEnabled == true) return 'Source: repo-level (itEnabled = true)';
        if (config.itEnabled == false) return 'Source: disabled per-repo (itEnabled = false)';
        if ((config.reviewOnlyLabels ?? const []).isNotEmpty) {
          return 'Source: implied by per-repo labels';
        }
        return globalIt
            ? 'Source: inherited from global issue tracking'
            : 'Source: globally disabled';
      case Feature.develop:
        if (config.devEnabled == true && hasDir) return 'Source: repo-level (devEnabled = true)';
        if (config.devEnabled == false) return 'Source: disabled per-repo (devEnabled = false)';
        if (!hasDir) return 'Reason: no local directory configured (Develop requires one)';
        if ((config.developLabels ?? const []).isNotEmpty) {
          return 'Source: implied by per-repo develop labels';
        }
        return globalIt
            ? 'Source: inherited from global issue tracking'
            : 'Source: globally disabled';
    }
  }
}
