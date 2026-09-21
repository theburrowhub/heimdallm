import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../shared/design_system/components/components.dart';
import '../config/config_providers.dart';

/// First-class Organizations list (top-level tab). Each entry opens the
/// organization's config (OrgDetailScreen), where every option overrides the
/// global default and is in turn overridable per-repo.
class OrgsScreen extends ConsumerWidget {
  const OrgsScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final configAsync = ref.watch(configNotifierProvider);
    return configAsync.when(
      loading: () => const Center(child: CircularProgressIndicator()),
      error: (e, _) => Center(
        child: AppText(
          'Could not load config: $e',
          textAlign: TextAlign.center,
        ),
      ),
      data: (config) {
        final orgs = config.knownOrganizations;
        if (orgs.isEmpty) {
          return const Center(
            child: Padding(
              padding: EdgeInsets.all(24),
              child: AppText(
                'No organizations yet — they appear automatically from your '
                'monitored repositories.',
                textAlign: TextAlign.center,
              ),
            ),
          );
        }
        return ListView.separated(
          padding: const EdgeInsets.all(12),
          itemCount: orgs.length,
          separatorBuilder: (_, _) => const SizedBox(height: 4),
          itemBuilder: (context, i) {
            final org = orgs[i];
            final overridden = config.orgConfigs[org]?.hasOverride ?? false;
            return AppSurface(
              child: Material(
                type: MaterialType.transparency,
                child: ListTile(
                  leading: const Icon(Icons.business_outlined),
                  title: AppText.sectionTitle(org),
                  subtitle: AppText.muted(
                    overridden
                        ? 'Custom overrides on global defaults'
                        : 'Inherits global defaults',
                  ),
                  trailing: Icon(
                    Icons.chevron_right,
                    color: Theme.of(context).colorScheme.onSurfaceVariant,
                  ),
                  onTap: () =>
                      context.push('/orgs/${Uri.encodeComponent(org)}'),
                ),
              ),
            );
          },
        );
      },
    );
  }
}
