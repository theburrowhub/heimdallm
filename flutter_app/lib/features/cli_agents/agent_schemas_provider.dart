import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/models/agent_schema.dart';
import '../../core/models/config_model.dart'
    show CLIAgentConfig, reviewAgentIds;
import '../config/config_providers.dart';
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
/// custom agents declared in config.toml. The custom agents come from both
/// the schemas and the config itself, so one that is already selected stays
/// in the list while the schemas load, or if they cannot be loaded.
final reviewAgentIdsProvider = Provider.autoDispose<List<String>>((ref) {
  final fromSchemas =
      ref.watch(agentSchemasProvider).value?.custom.map((s) => s.name) ??
      const <String>[];
  final fromConfig = [
    for (final e
        in (ref.watch(configNotifierProvider).value?.agentConfigs ??
                const <String, CLIAgentConfig>{})
            .entries)
      if (e.value.isCustom) e.key,
  ]..sort();
  final out = [...reviewAgentIds];
  for (final name in [...fromSchemas, ...fromConfig]) {
    if (!out.contains(name)) out.add(name);
  }
  return out;
});
