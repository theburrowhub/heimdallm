import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/models/agent_schema.dart';
import '../../core/models/config_model.dart' show reviewAgentIds;
import '../dashboard/dashboard_providers.dart';

/// Every agent's options schema (GET /agent-schemas). The settings forms are
/// rendered from it; an older daemon without the endpoint leaves the forms
/// with the shared settings only.
final agentSchemasProvider = FutureProvider.autoDispose<AgentSchemaCatalog>((
  ref,
) {
  return ref.watch(apiClientProvider).fetchAgentSchemas();
});

/// Every agent a review can be assigned to: the built-in agents, then the
/// custom agents declared in config.toml.
final reviewAgentIdsProvider = Provider.autoDispose<List<String>>((ref) {
  final custom =
      ref.watch(agentSchemasProvider).value?.custom.map((s) => s.name) ??
      const <String>[];
  return [
    ...reviewAgentIds,
    for (final name in custom)
      if (!reviewAgentIds.contains(name)) name,
  ];
});
