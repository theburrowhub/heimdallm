/// Models for Heimdallm's multi-instance control plane.
///
/// Named [DaemonInstance] rather than `Instance` on purpose: `instance` already
/// means something in Dart, and [PlatformServices.ensureSingleInstance] refers
/// to a single *app* window, which is an unrelated concept.
library;

/// Last observed condition of one instance, as reported by the hub's prober.
class InstanceState {
  final bool reachable;
  final String status;
  final String version;
  final String role;
  final double uptimeSeconds;
  final DateTime? lastSeenAt;
  final String lastError;
  final int consecutiveFailures;

  const InstanceState({
    this.reachable = false,
    this.status = '',
    this.version = '',
    this.role = '',
    this.uptimeSeconds = 0,
    this.lastSeenAt,
    this.lastError = '',
    this.consecutiveFailures = 0,
  });

  factory InstanceState.fromJson(Map<String, dynamic> json) {
    return InstanceState(
      reachable: json['reachable'] == true,
      status: (json['status'] as String?) ?? '',
      version: (json['version'] as String?) ?? '',
      role: (json['role'] as String?) ?? '',
      uptimeSeconds: (json['uptime_seconds'] as num?)?.toDouble() ?? 0,
      lastSeenAt: _parseTime(json['last_seen_at']),
      lastError: (json['last_error'] as String?) ?? '',
      consecutiveFailures: (json['consecutive_failures'] as num?)?.toInt() ?? 0,
    );
  }

  static DateTime? _parseTime(dynamic raw) {
    if (raw is! String || raw.isEmpty) return null;
    return DateTime.tryParse(raw);
  }
}

/// One registered instance: its configuration joined with observed health.
class DaemonInstance {
  final String id;
  final String name;
  final String baseUrl;
  final bool enabled;

  /// Whether this is the hub itself. The hub is the only instance the app may
  /// spawn, stop or restart, and it is served without going through the proxy.
  final bool isSelf;

  final List<String> labels;

  /// Why the hub cannot use this instance's token, if anything. Surfaced rather
  /// than hidden: an instance that silently stops working is far harder to
  /// diagnose than one that says what is wrong.
  final String tokenError;

  /// How many repos are explicitly routed here.
  final int assignedRepos;

  /// Whether this instance owns every repo no rule claims.
  final bool isFallback;

  /// Whether this instance takes part in round-robin.
  final bool inPool;

  final InstanceState? state;

  const DaemonInstance({
    required this.id,
    required this.name,
    required this.baseUrl,
    this.enabled = true,
    this.isSelf = false,
    this.labels = const [],
    this.tokenError = '',
    this.assignedRepos = 0,
    this.isFallback = false,
    this.inPool = false,
    this.state,
  });

  factory DaemonInstance.fromJson(Map<String, dynamic> json) {
    return DaemonInstance(
      id: (json['id'] as String?) ?? '',
      name: (json['name'] as String?) ?? '',
      baseUrl: (json['base_url'] as String?) ?? '',
      enabled: json['enabled'] != false,
      isSelf: json['self'] == true,
      labels:
          (json['labels'] as List<dynamic>?)
              ?.map((e) => e.toString())
              .toList() ??
          const [],
      tokenError: (json['token_error'] as String?) ?? '',
      assignedRepos: (json['assigned_repos'] as num?)?.toInt() ?? 0,
      isFallback: json['is_fallback'] == true,
      inPool: json['in_pool'] == true,
      state: json['state'] is Map<String, dynamic>
          ? InstanceState.fromJson(json['state'] as Map<String, dynamic>)
          : null,
    );
  }

  /// Label for the UI. Never empty, so a row always renders something.
  String get displayName => name.isNotEmpty ? name : id;

  /// Whether the app should try to read data from this instance. A disabled or
  /// tokenless instance is skipped by the aggregation rather than producing a
  /// failure row for every request.
  bool get usable => enabled && tokenError.isEmpty;

  /// Whether the hub currently believes this instance is answering. An instance
  /// that has never been probed counts as reachable: showing every row as down
  /// for the first probe interval after a hub restart would be misleading.
  bool get reachable => state?.reachable ?? true;
}

/// The whole registry plus the hub's own identity.
class ClusterRegistry {
  final String role;
  final String selfId;
  final String selfName;
  final List<DaemonInstance> instances;

  const ClusterRegistry({
    this.role = '',
    this.selfId = '',
    this.selfName = '',
    this.instances = const [],
  });

  static const ClusterRegistry empty = ClusterRegistry();

  factory ClusterRegistry.fromJson(Map<String, dynamic> json) {
    return ClusterRegistry(
      role: (json['role'] as String?) ?? '',
      selfId: (json['self_id'] as String?) ?? '',
      selfName: (json['self_name'] as String?) ?? '',
      instances:
          (json['instances'] as List<dynamic>?)
              ?.whereType<Map<String, dynamic>>()
              .map(DaemonInstance.fromJson)
              .toList() ??
          const [],
    );
  }

  /// Whether the multi-instance UI should appear at all. A single instance is
  /// indistinguishable from a plain single-daemon install, so the extra
  /// navigation and badges stay hidden.
  bool get isMultiInstance => instances.length > 1;

  /// Instances worth reading data from, in registry order.
  List<DaemonInstance> get usable =>
      instances.where((i) => i.usable).toList(growable: false);

  DaemonInstance? byId(String id) {
    for (final instance in instances) {
      if (instance.id == id) return instance;
    }
    return null;
  }

  int get reachableCount => usable.where((i) => i.reachable).length;
}

/// Routing modes. Mirrors the daemon's [cluster.routing].mode.
class RoutingMode {
  /// Repos are partitioned; each daemon polls and acts on what it owns.
  static const assignment = 'assignment';

  /// Additionally spreads explicitly triggered operations across the pool.
  static const dispatch = 'dispatch';

  static const all = [assignment, dispatch];
}

/// Operations that can be round-robined in dispatch mode.
class RoutingOp {
  static const review = 'review';
  static const merge = 'merge';
  static const issue = 'issue';

  static const all = [review, merge, issue];
}

/// Cluster roles. Mirrors the daemon's config.Role* constants
/// (daemon/internal/config/cluster.go).
class ClusterRole {
  /// A plain single-daemon install: no control plane, no /instances routes.
  static const standalone = 'standalone';

  /// Manages other Heimdallm daemons: registers them, routes work to them,
  /// can push configuration to them.
  static const hub = 'hub';

  /// Managed by a hub. Not distinguished from standalone in this GUI today.
  static const worker = 'worker';

  static const all = [standalone, hub, worker];
}

/// The daemon's verbatim refusal, from hubOnly (daemon/internal/server/
/// cluster.go), for any /instances or /cluster/* call made against a daemon
/// that is not currently wired as a hub.
///
/// This is the only signal available to tell this specific failure apart from
/// any other: [ApiException] carries just a `String message`, no status code
/// and no error type. Kept as one named sentinel + helper so there is exactly
/// one place to fix if the daemon's wording ever changes.
const kNotAClusterHubError = 'this daemon is not a cluster hub';

/// Whether [message] is the daemon's "not a cluster hub" refusal.
///
/// Substring + case-insensitive rather than exact match: robust to the
/// message being wrapped or re-cased by an intermediate layer without
/// silently regressing back to showing the raw string to the user.
bool isNotAClusterHubError(String? message) =>
    message != null &&
    message.toLowerCase().contains(kNotAClusterHubError);

/// The org/repo to instance rules.
class RoutingRules {
  final String mode;
  final List<String> roundRobinPool;
  final List<String> roundRobinOps;
  final Map<String, String> orgs;
  final Map<String, String> repos;
  final String defaultInstance;

  /// The pool after dropping unknown and disabled instances — what round robin
  /// will actually rotate through.
  final List<String> resolvedPool;

  /// Whether routing is in play. False means every daemon owns every repo.
  final bool enabled;

  const RoutingRules({
    this.mode = RoutingMode.assignment,
    this.roundRobinPool = const [],
    this.roundRobinOps = const [],
    this.orgs = const {},
    this.repos = const {},
    this.defaultInstance = '',
    this.resolvedPool = const [],
    this.enabled = false,
  });

  static const RoutingRules empty = RoutingRules();

  factory RoutingRules.fromJson(Map<String, dynamic> json) {
    return RoutingRules(
      mode: (json['mode'] as String?) ?? RoutingMode.assignment,
      roundRobinPool: _stringList(json['round_robin_pool']),
      roundRobinOps: _stringList(json['round_robin_ops']),
      orgs: _stringMap(json['orgs']),
      repos: _stringMap(json['repos']),
      defaultInstance: (json['default_instance'] as String?) ?? '',
      resolvedPool: _stringList(json['resolved_pool']),
      enabled: json['enabled'] == true,
    );
  }

  /// Resolves the owner of a repo the way the daemon does: an explicit repo
  /// rule, then the org rule, then the fallback. Kept in sync with
  /// instances.Router.OwnerFor so the UI shows what will actually happen.
  String ownerFor(String repo) {
    final direct = repos[repo];
    if (direct != null && direct.isNotEmpty) return direct;
    final slash = repo.indexOf('/');
    if (slash > 0) {
      final org = repo.substring(0, slash);
      for (final entry in orgs.entries) {
        if (entry.key.toLowerCase() == org.toLowerCase() &&
            entry.value.isNotEmpty) {
          return entry.value;
        }
      }
    }
    return defaultInstance;
  }

  /// Whether the owner of [repo] comes from an explicit rule rather than being
  /// inherited from the fallback. The UI shows inherited owners differently so
  /// an operator can tell what they actually configured.
  bool hasExplicitRule(String repo) {
    if (repos.containsKey(repo)) return true;
    final slash = repo.indexOf('/');
    if (slash <= 0) return false;
    final org = repo.substring(0, slash).toLowerCase();
    return orgs.keys.any((k) => k.toLowerCase() == org);
  }

  RoutingRules copyWith({
    String? mode,
    List<String>? roundRobinPool,
    List<String>? roundRobinOps,
    Map<String, String>? orgs,
    Map<String, String>? repos,
    String? defaultInstance,
  }) {
    return RoutingRules(
      mode: mode ?? this.mode,
      roundRobinPool: roundRobinPool ?? this.roundRobinPool,
      roundRobinOps: roundRobinOps ?? this.roundRobinOps,
      orgs: orgs ?? this.orgs,
      repos: repos ?? this.repos,
      defaultInstance: defaultInstance ?? this.defaultInstance,
      resolvedPool: resolvedPool,
      enabled: enabled,
    );
  }

  static List<String> _stringList(dynamic raw) {
    if (raw is! List) return const [];
    return raw.map((e) => e.toString()).toList(growable: false);
  }

  static Map<String, String> _stringMap(dynamic raw) {
    if (raw is! Map) return const {};
    return {
      for (final entry in raw.entries) entry.key.toString(): '${entry.value}',
    };
  }
}

/// The outcome of pushing config to one instance.
class PropagateResult {
  final String instanceId;
  final String name;
  final bool ok;
  final bool skipped;
  final String error;
  final List<String> appliedKeys;

  const PropagateResult({
    required this.instanceId,
    this.name = '',
    this.ok = false,
    this.skipped = false,
    this.error = '',
    this.appliedKeys = const [],
  });

  factory PropagateResult.fromJson(Map<String, dynamic> json) {
    return PropagateResult(
      instanceId: (json['instance_id'] as String?) ?? '',
      name: (json['name'] as String?) ?? '',
      ok: json['ok'] == true,
      skipped: json['skipped'] == true,
      error: (json['error'] as String?) ?? '',
      appliedKeys: RoutingRules._stringList(json['applied_keys']),
    );
  }

  String get displayName => name.isNotEmpty ? name : instanceId;
}

/// A whole propagation run.
class PropagateReport {
  final List<PropagateResult> results;

  /// Machine-specific keys the hub refused to send. Shown so an operator can
  /// see that a port or a token was deliberately not propagated, rather than
  /// wondering why it did not take effect.
  final List<String> skippedLocal;
  final int failures;

  const PropagateReport({
    this.results = const [],
    this.skippedLocal = const [],
    this.failures = 0,
  });

  factory PropagateReport.fromJson(Map<String, dynamic> json) {
    return PropagateReport(
      results:
          (json['results'] as List<dynamic>?)
              ?.whereType<Map<String, dynamic>>()
              .map(PropagateResult.fromJson)
              .toList() ??
          const [],
      skippedLocal: RoutingRules._stringList(json['skipped_local']),
      failures: (json['failures'] as num?)?.toInt() ?? 0,
    );
  }

  bool get allOk => failures == 0;
}

/// One config key that differs between the hub and an instance.
class ConfigDrift {
  final String key;
  final Object? hubValue;
  final Object? remoteValue;

  /// The key is absent on the instance entirely.
  final bool missing;

  const ConfigDrift({
    required this.key,
    this.hubValue,
    this.remoteValue,
    this.missing = false,
  });

  factory ConfigDrift.fromJson(Map<String, dynamic> json) {
    return ConfigDrift(
      key: (json['key'] as String?) ?? '',
      hubValue: json['hub_value'],
      remoteValue: json['remote_value'],
      missing: json['missing'] == true,
    );
  }
}

/// Drift found on one instance, or why it could not be inspected.
class InstanceDrift {
  final String instanceId;
  final String name;
  final bool ok;
  final bool skipped;
  final String error;
  final List<ConfigDrift> drifts;

  const InstanceDrift({
    required this.instanceId,
    this.name = '',
    this.ok = false,
    this.skipped = false,
    this.error = '',
    this.drifts = const [],
  });

  factory InstanceDrift.fromJson(Map<String, dynamic> json) {
    return InstanceDrift(
      instanceId: (json['instance_id'] as String?) ?? '',
      name: (json['name'] as String?) ?? '',
      ok: json['ok'] == true,
      skipped: json['skipped'] == true,
      error: (json['error'] as String?) ?? '',
      drifts:
          (json['drifts'] as List<dynamic>?)
              ?.whereType<Map<String, dynamic>>()
              .map(ConfigDrift.fromJson)
              .toList() ??
          const [],
    );
  }

  String get displayName => name.isNotEmpty ? name : instanceId;
  bool get inSync => ok && drifts.isEmpty;
}

/// What a discovered peer is being offered as.
///
/// These describe the proposal, not something the hub has done. Discovery never
/// registers anything and never rewrites an address.
class PeerStatus {
  /// A verified daemon nobody has registered yet.
  static const newPeer = 'new';

  /// Already in the registry at the same address. Shown so the list reads as
  /// "here is the network" rather than implying everything on it needs action.
  static const registered = 'registered';

  /// A registered instance answering somewhere its `base_url` no longer points.
  /// Its peers will be taking over the repositories it is still reviewing, so
  /// this is the one worth interrupting the operator for.
  static const addressChanged = 'address_changed';
}

/// One Heimdallm daemon the hub found on the local network over mDNS.
///
/// Everything here has already been verified by the daemon: the hub reached the
/// peer over HTTP and let it identify itself, so the id and name are the
/// instance's own claims about itself rather than whatever was advertised.
class DiscoveredPeer {
  final String instanceId;
  final String name;
  final String role;
  final String version;

  /// Where to reach it, built from its mDNS hostname rather than an IP — the
  /// point of discovery is an address that survives the next DHCP lease.
  final String baseUrl;
  final String hostname;

  /// The addresses it answered from. Diagnostics only; [baseUrl] is what gets
  /// registered.
  final List<String> addresses;

  /// One of the [PeerStatus] values.
  final String status;

  /// The registry entry this peer matches, when it matches one.
  final String registeredId;
  final String registeredBaseUrl;

  final DateTime? seenAt;

  const DiscoveredPeer({
    required this.instanceId,
    required this.baseUrl,
    this.name = '',
    this.role = '',
    this.version = '',
    this.hostname = '',
    this.addresses = const [],
    this.status = PeerStatus.newPeer,
    this.registeredId = '',
    this.registeredBaseUrl = '',
    this.seenAt,
  });

  factory DiscoveredPeer.fromJson(Map<String, dynamic> json) {
    return DiscoveredPeer(
      instanceId: (json['instance_id'] as String?) ?? '',
      baseUrl: (json['base_url'] as String?) ?? '',
      name: (json['name'] as String?) ?? '',
      role: (json['role'] as String?) ?? '',
      version: (json['version'] as String?) ?? '',
      hostname: (json['hostname'] as String?) ?? '',
      addresses:
          (json['addresses'] as List<dynamic>?)
              ?.map((e) => e.toString())
              .toList() ??
          const [],
      status: (json['status'] as String?) ?? PeerStatus.newPeer,
      registeredId: (json['registered_id'] as String?) ?? '',
      registeredBaseUrl: (json['registered_base_url'] as String?) ?? '',
      seenAt: InstanceState._parseTime(json['seen_at']),
    );
  }

  /// Label for the UI. Never empty, so a row always renders something.
  String get displayName => name.isNotEmpty ? name : instanceId;

  /// Whether this peer is something the operator can act on: register it, or
  /// repair the address of the entry it already has.
  bool get isActionable => status != PeerStatus.registered;
}

/// The hub's view of the local network.
class DiscoveredPeers {
  /// Whether `cluster.discovery` is on. Carried separately from the list
  /// because "switched off" and "found nothing" look identical when empty and
  /// need completely different copy.
  final bool enabled;
  final DateTime? lastScan;
  final List<DiscoveredPeer> peers;

  const DiscoveredPeers({
    this.enabled = false,
    this.lastScan,
    this.peers = const [],
  });

  static const DiscoveredPeers empty = DiscoveredPeers();

  factory DiscoveredPeers.fromJson(Map<String, dynamic> json) {
    return DiscoveredPeers(
      enabled: json['enabled'] == true,
      lastScan: InstanceState._parseTime(json['last_scan']),
      peers:
          (json['peers'] as List<dynamic>?)
              ?.whereType<Map<String, dynamic>>()
              .map(DiscoveredPeer.fromJson)
              .toList() ??
          const [],
    );
  }

  /// Peers the operator has not registered yet.
  List<DiscoveredPeer> get unregistered =>
      peers.where((p) => p.status == PeerStatus.newPeer).toList();

  /// Registered instances answering at an address the registry does not have.
  List<DiscoveredPeer> get moved =>
      peers.where((p) => p.status == PeerStatus.addressChanged).toList();

  /// The moved entry for a registered instance, if the network says it has one.
  DiscoveredPeer? movedFor(String instanceId) {
    for (final peer in moved) {
      if (peer.registeredId == instanceId) return peer;
    }
    return null;
  }
}
