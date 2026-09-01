import 'dart:convert';

import '../instances/models.dart';
import 'api_client.dart';

/// The hub's control-plane endpoints.
///
/// Kept out of [ApiClient] itself because these calls only exist on a hub, and
/// because api_client.dart is already the app's largest file. Every method here
/// targets the hub — never a remote instance directly.
extension ClusterApi on ApiClient {
  /// Fetches the registry. Returns [ClusterRegistry.empty] when this daemon is
  /// not a hub, which is how the UI decides to stay hidden: a plain
  /// single-daemon install answers 404 and must not surface an error.
  Future<ClusterRegistry> fetchInstances() async {
    final resp = await getJson('/instances');
    if (resp == null) return ClusterRegistry.empty;
    return ClusterRegistry.fromJson(resp);
  }

  /// Registers a new instance. The hub probes it first, so a machine that is
  /// not answering is rejected rather than silently recorded.
  Future<String> registerInstance({
    required String baseUrl,
    String? id,
    String? name,
    String? token,
    String? tokenEnv,
    String? tokenFile,
    List<String> labels = const [],
    bool skipProbe = false,
  }) async {
    final body = <String, dynamic>{
      'base_url': baseUrl,
      if (id != null && id.isNotEmpty) 'id': id,
      if (name != null && name.isNotEmpty) 'name': name,
      if (token != null && token.isNotEmpty) 'token': token,
      if (tokenEnv != null && tokenEnv.isNotEmpty) 'token_env': tokenEnv,
      if (tokenFile != null && tokenFile.isNotEmpty) 'token_file': tokenFile,
      if (labels.isNotEmpty) 'labels': labels,
      if (skipProbe) 'skip_probe': true,
    };
    final resp = await sendJson('POST', '/instances', body: body);
    return (resp?['id'] as String?) ?? '';
  }

  /// Renames, enables/disables, relabels or rotates the token of an instance.
  Future<void> patchInstance(
    String id, {
    String? name,
    String? baseUrl,
    bool? enabled,
    String? token,
    String? tokenEnv,
    String? tokenFile,
    List<String>? labels,
  }) async {
    final body = <String, dynamic>{
      'name': ?name,
      'base_url': ?baseUrl,
      'enabled': ?enabled,
      'token': ?token,
      'token_env': ?tokenEnv,
      'token_file': ?tokenFile,
      'labels': ?labels,
    };
    await sendJson('PATCH', '/instances/${Uri.encodeComponent(id)}', body: body);
  }

  /// Deregisters an instance. The hub also drops every routing rule pointing at
  /// it, so the remaining config still loads.
  Future<void> deleteInstance(String id) async {
    await sendJson('DELETE', '/instances/${Uri.encodeComponent(id)}');
  }

  /// Forces an immediate health probe.
  Future<InstanceState> probeInstance(String id) async {
    final resp = await sendJson(
      'POST',
      '/instances/${Uri.encodeComponent(id)}/probe',
    );
    return resp == null
        ? const InstanceState()
        : InstanceState.fromJson(resp);
  }

  /// Fetches the org/repo routing rules.
  Future<RoutingRules> fetchRouting() async {
    final resp = await getJson('/cluster/routing');
    if (resp == null) return RoutingRules.empty;
    return RoutingRules.fromJson(resp);
  }

  /// Replaces routing rules. Omitted fields are left untouched; supplied maps
  /// replace the stored ones wholesale, which is what makes deleting a rule
  /// possible.
  Future<void> putRouting({
    String? mode,
    List<String>? roundRobinPool,
    List<String>? roundRobinOps,
    Map<String, String>? orgs,
    Map<String, String>? repos,
    String? defaultInstance,
  }) async {
    final body = <String, dynamic>{
      'mode': ?mode,
      'round_robin_pool': ?roundRobinPool,
      'round_robin_ops': ?roundRobinOps,
      'orgs': ?orgs,
      'repos': ?repos,
      'default_instance': ?defaultInstance,
    };
    await sendJson('PUT', '/cluster/routing', body: body);
  }

  /// Convenience wrapper for routing a single repo, used by the repo detail
  /// screen and the bulk action. Passing null clears the rule.
  Future<void> assignRepo(
    RoutingRules current,
    String repo,
    String? instanceId,
  ) async {
    final repos = Map<String, String>.from(current.repos);
    if (instanceId == null || instanceId.isEmpty) {
      repos.remove(repo);
    } else {
      repos[repo] = instanceId;
    }
    await putRouting(repos: repos);
  }

  /// The org-level equivalent of [assignRepo].
  Future<void> assignOrg(
    RoutingRules current,
    String org,
    String? instanceId,
  ) async {
    final orgs = Map<String, String>.from(current.orgs);
    if (instanceId == null || instanceId.isEmpty) {
      orgs.remove(org);
    } else {
      orgs[org] = instanceId;
    }
    await putRouting(orgs: orgs);
  }

  /// Compares the hub's shared config against every instance.
  Future<List<InstanceDrift>> fetchConfigDrift() async {
    final resp = await getJson('/cluster/drift');
    if (resp == null) return const [];
    return (resp['instances'] as List<dynamic>? ?? const [])
        .whereType<Map<String, dynamic>>()
        .map(InstanceDrift.fromJson)
        .toList();
  }

  /// Pushes shared config to the other instances.
  ///
  /// A partial failure answers 207 and is NOT an error: one machine rebooting
  /// must not hide the fact that the others were updated, and the caller needs
  /// the per-instance reasons either way.
  Future<PropagateReport> propagateConfig({
    List<String> targets = const [],
    Map<String, dynamic>? patch,
  }) async {
    final body = <String, dynamic>{
      if (targets.isNotEmpty) 'targets': targets,
      if (patch != null && patch.isNotEmpty) 'patch': patch,
    };
    final resp = await sendJson(
      'POST',
      '/cluster/propagate',
      body: body,
      acceptStatuses: const {200, 207},
    );
    return resp == null
        ? const PropagateReport()
        : PropagateReport.fromJson(resp);
  }

  /// Routes one operation to an instance, honouring the routing rules and
  /// round robin. [instance] forces a target, which is what the GUI's
  /// "run this here" action uses.
  Future<String> dispatch(
    String op, {
    int? prId,
    int? issueId,
    String? repo,
    int? number,
    String? headSha,
    String? prUrl,
    bool dryRun = false,
    String? instance,
  }) async {
    final body = <String, dynamic>{
      'pr_id': ?prId,
      'issue_id': ?issueId,
      'repo': ?repo,
      'number': ?number,
      'head_sha': ?headSha,
      'pr_url': ?prUrl,
      if (dryRun) 'dry_run': true,
      if (instance != null && instance.isNotEmpty) 'instance': instance,
    };
    final resp = await sendJson(
      'POST',
      '/cluster/dispatch/${Uri.encodeComponent(op)}',
      body: body,
      acceptStatuses: const {200, 202},
    );
    return (resp?['instance_id'] as String?) ?? '';
  }
}

/// Decodes a JSON object body, or null when there is nothing to decode.
Map<String, dynamic>? decodeJsonObject(String body) {
  if (body.isEmpty) return null;
  try {
    final decoded = jsonDecode(body);
    return decoded is Map<String, dynamic> ? decoded : null;
  } catch (_) {
    return null;
  }
}
