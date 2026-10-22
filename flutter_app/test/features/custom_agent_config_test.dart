import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/agent_schema.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/cli_agents/agent_schemas_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart'
    show computeGlobalDiffForTest;
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:mocktail/mocktail.dart';

import '../support/agent_schemas.dart';

class _MockApiClient extends Mock implements ApiClient {}

void main() {
  test('a custom agent parses its binary, options and timeout', () {
    final c = CLIAgentConfig.fromJson({
      'model': 'gpt-4o',
      'binary': 'aider',
      'options': {'auto_commits': false, 'map_tokens': 1024},
      'execution_timeout': '5m',
    });
    expect(c.isCustom, isTrue);
    expect(c.options, {'auto_commits': false, 'map_tokens': 1024});
    expect(c.executionTimeout, '5m');
    expect(c.hasConfig, isTrue);
    expect(const CLIAgentConfig().isCustom, isFalse);
    final copy = c.copyWith(options: {'x': 1}, executionTimeout: '');
    expect(copy.binary, 'aider');
    expect(copy.options, {'x': 1});
  });

  test('custom options are patched as flat keys, never the binary', () {
    const old = AppConfig(
      agentConfigs: {
        'aider': CLIAgentConfig(
          binary: 'aider',
          options: {'auto_commits': true, 'edit_format': 'diff'},
        ),
      },
    );
    final updated = old.copyWith(
      agentConfigs: {
        'aider': old.agentConfigs['aider']!.copyWith(
          options: {'auto_commits': false},
          executionTimeout: '5m',
        ),
      },
    );
    final diff = computeGlobalDiffForTest(old, updated);
    final agent = diff['ai']['agents']['aider'] as Map<String, dynamic>;
    // edit_format went back to its default: there is nothing to send (the
    // API refuses null), so it simply is not patched.
    expect(agent, {'auto_commits': false, 'execution_timeout': '5m'});
  });

  test('review agents include custom agents after the built-in ones', () async {
    final api = _MockApiClient();
    when(() => api.fetchAgentSchemas()).thenAnswer(
      (_) async => agentSchemaCatalog(
        custom: const [
          AgentSchema(name: 'aider', binary: 'aider'),
          // A custom schema may not reuse a built-in name; never list twice.
          AgentSchema(name: 'claude'),
        ],
      ),
    );
    final container = ProviderContainer(
      overrides: [apiClientProvider.overrideWithValue(api)],
    );
    addTearDown(container.dispose);
    final sub = container.listen(reviewAgentIdsProvider, (_, _) {});
    addTearDown(sub.close);
    expect(sub.read(), reviewAgentIds);
    await container.read(agentSchemasProvider.future);
    expect(sub.read(), [...reviewAgentIds, 'aider']);
  });
}
