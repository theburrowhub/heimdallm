import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
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
      error: (e, _) => Center(child: Text('Could not load config: $e')),
      data: (config) {
        final orgs = config.knownOrganizations;
        if (orgs.isEmpty) {
          return const Center(
            child: Padding(
              padding: EdgeInsets.all(24),
              child: Text(
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
            return Card(
              margin: EdgeInsets.zero,
              child: ListTile(
                leading: const Icon(Icons.business_outlined),
                title: Text(org),
                subtitle: Text(
                  overridden
                      ? 'Custom overrides on global defaults'
                      : 'Inherits global defaults',
                  style: const TextStyle(fontSize: 12),
                ),
                trailing: const Icon(Icons.chevron_right),
                onTap: () => context.push('/orgs/${Uri.encodeComponent(org)}'),
              ),
            );
          },
        );
      },
    );
  }
}
