import 'dart:convert';
import 'dart:io';

import 'package:heimdallm/core/models/agent_schema.dart';

/// The daemon's built-in agent schemas, read from the files the daemon embeds
/// (daemon/internal/agentschema/builtin), so the app's tests always render the
/// forms the daemon actually serves.
List<AgentSchema> builtinAgentSchemas() {
  final dir = Directory('../daemon/internal/agentschema/builtin');
  final files = dir.listSync().whereType<File>().toList()
    ..sort((a, b) => a.path.compareTo(b.path));
  return [
    for (final f in files)
      if (f.path.endsWith('.json'))
        AgentSchema.fromJson({
          ...jsonDecode(f.readAsStringSync()) as Map<String, dynamic>,
          'builtin': true,
        }),
  ];
}

/// GET /agent-schemas as the daemon answers it, plus [custom] agents.
AgentSchemaCatalog agentSchemaCatalog({
  List<AgentSchema> custom = const [],
  bool customAgentsApiEnabled = false,
}) => AgentSchemaCatalog(
  schemas: [...builtinAgentSchemas(), ...custom],
  customAgentsApiEnabled: customAgentsApiEnabled,
);
