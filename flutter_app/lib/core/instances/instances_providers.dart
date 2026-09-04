import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../api/api_client.dart';
import '../api/cluster_api.dart';
import '../api/daemon_endpoint.dart';
import '../api/sse_client.dart';
import '../platform/platform_services_provider.dart';
import 'models.dart';

/// The daemon this app manages: the local process on desktop, the single
/// upstream Nginx proxies to on web. Also the hub, when clustering is on.
final localEndpointProvider = Provider<DaemonEndpoint>((ref) {
  return DaemonEndpoint.local(ref.watch(platformServicesProvider));
});

/// The registry, fetched from the hub.
///
/// Returns [ClusterRegistry.empty] on a plain single-daemon install: the
/// control-plane routes answer 404 there, which [ClusterApi.fetchInstances]
/// maps to empty rather than an error, so the whole instances UI stays hidden
/// without any caller having to know why.
final daemonInstancesProvider = FutureProvider<ClusterRegistry>((ref) async {
  final api = ref.watch(hubApiClientProvider);
  try {
    return await api.fetchInstances();
  } catch (_) {
    // A hub that is starting, or briefly unreachable, must degrade to
    // single-daemon behaviour rather than break the dashboard.
    return ClusterRegistry.empty;
  }
});

/// The hub's client. Separate from `apiClientProvider` (which follows the
/// selected instance) because control-plane calls always go to the hub.
final hubApiClientProvider = Provider<ApiClient>((ref) {
  return ApiClient(endpoint: ref.watch(localEndpointProvider));
});

/// The local daemon's EFFECTIVE cluster role, straight from the
/// unauthenticated `/health`.
///
/// `null` means "could not determine" (daemon unreachable, or not a
/// Heimdallm responder); `''` means reachable and not clustered at all.
/// Deliberately NOT derived from [daemonInstancesProvider]: that maps every
/// failure — including "the daemon is simply down" — to
/// [ClusterRegistry.empty], so it cannot tell a standalone daemon from a dead
/// one.
///
/// This reports what the RUNNING process is wired as, not what config.toml
/// says: the daemon only calls SetClusterIdentity from wireCluster at
/// startup, so a saved-but-not-yet-restarted role change is visible here as
/// (config says hub) != (health says standalone) — which is exactly the
/// "restart required" signal the GUI needs.
final localClusterRoleProvider = FutureProvider<String?>((ref) async {
  final raw = await ref.watch(hubApiClientProvider).fetchHealth();
  if (raw == null) return null;
  return (raw['role'] as String?) ?? '';
});

/// True only when the running local daemon is confirmed to be a hub right
/// now. Unknown (daemon unreachable) is treated as "not confirmed" rather
/// than "not a hub", so no call-to-action is shown to someone whose daemon is
/// simply offline.
final localIsHubProvider = Provider<bool?>((ref) {
  final role = ref.watch(localClusterRoleProvider).value;
  if (role == null) return null;
  return role.toLowerCase() == ClusterRole.hub;
});

/// Endpoint for one instance, routed through the hub's proxy.
final instanceEndpointProvider = Provider.family<DaemonEndpoint, String>((
  ref,
  instanceId,
) {
  final hub = ref.watch(localEndpointProvider);
  if (instanceId.isEmpty) return hub;

  final registry = ref.watch(daemonInstancesProvider).value;
  final instance = registry?.byId(instanceId);
  // The hub serves its own data directly; proxying to itself would add a
  // network hop for no benefit.
  if (instance != null && instance.isSelf) return hub;

  return DaemonEndpoint.viaHub(
    hub: hub,
    instanceId: instanceId,
    name: instance?.displayName ?? instanceId,
  );
});

/// An [ApiClient] for one instance.
final apiClientForProvider = Provider.family<ApiClient, String>((
  ref,
  instanceId,
) {
  return ApiClient(endpoint: ref.watch(instanceEndpointProvider(instanceId)));
});

/// The instance the UI is scoped to, or null for "all instances".
///
/// Persisted so a chosen scope survives a restart, which matters when an
/// operator is working on one machine's queue.
class ActiveInstanceNotifier extends Notifier<String?> {
  static const prefsKey = 'active_instance';

  @override
  String? build() {
    unawaited(_restore());
    return null;
  }

  Future<void> _restore() async {
    try {
      final prefs = await SharedPreferences.getInstance();
      final saved = prefs.getString(prefsKey);
      if (saved != null && saved.isNotEmpty) state = saved;
    } catch (e) {
      // Preferences are a convenience; failing to read them must not stop the
      // dashboard from rendering.
      debugPrint('active instance: could not restore: $e');
    }
  }

  Future<void> select(String? instanceId) async {
    state = instanceId;
    try {
      final prefs = await SharedPreferences.getInstance();
      if (instanceId == null || instanceId.isEmpty) {
        await prefs.remove(prefsKey);
      } else {
        await prefs.setString(prefsKey, instanceId);
      }
    } catch (e) {
      debugPrint('active instance: could not persist: $e');
    }
  }
}

final activeInstanceProvider =
    NotifierProvider<ActiveInstanceNotifier, String?>(
      ActiveInstanceNotifier.new,
    );

/// The instances the UI should currently read from.
///
/// With no cluster configured this is a single unnamed entry representing the
/// local daemon, so every aggregating provider has exactly one source and
/// behaves identically to the pre-instances app.
final targetInstancesProvider = Provider<List<DaemonInstance>>((ref) {
  final registry =
      ref.watch(daemonInstancesProvider).value ?? ClusterRegistry.empty;
  if (!registry.isMultiInstance) {
    return const [DaemonInstance(id: '', name: '', baseUrl: '')];
  }

  final active = ref.watch(activeInstanceProvider);
  final usable = registry.usable;
  if (active == null || active.isEmpty) return usable;

  final selected = usable.where((i) => i.id == active).toList();
  // A selection pointing at an instance that has since been removed or
  // disabled falls back to everything rather than showing an empty dashboard.
  return selected.isEmpty ? usable : selected;
});

/// The routing rules, or [RoutingRules.empty] when not clustered.
final routingRulesProvider = FutureProvider<RoutingRules>((ref) async {
  final api = ref.watch(hubApiClientProvider);
  try {
    return await api.fetchRouting();
  } catch (_) {
    return RoutingRules.empty;
  }
});

/// Config differences between the hub and each instance.
final configDriftProvider = FutureProvider<List<InstanceDrift>>((ref) async {
  final api = ref.watch(hubApiClientProvider);
  return api.fetchConfigDrift();
});

/// Daemons the hub can see on the local network over mDNS.
///
/// Errors surface rather than degrading to empty, unlike
/// [daemonInstancesProvider]. The registry has to keep working when the hub
/// hiccups, because the whole dashboard hangs off it; this list does not, and
/// a scan that quietly failed would read as "there is nothing on your network"
/// — which is exactly the wrong thing to tell someone who is looking for a
/// machine they know is switched on.
final discoveredPeersProvider = FutureProvider<DiscoveredPeers>((ref) async {
  final api = ref.watch(hubApiClientProvider);
  return api.fetchDiscoveredPeers();
});

/// SSE clients, one per target instance.
final instanceSseClientsProvider = Provider<Map<String, SseClient>>((ref) {
  final targets = ref.watch(targetInstancesProvider);
  final clients = <String, SseClient>{
    for (final instance in targets)
      instance.id: SseClient(
        endpoint: ref.watch(instanceEndpointProvider(instance.id)),
      ),
  };
  ref.onDispose(() {
    for (final client in clients.values) {
      client.disconnect();
    }
  });
  return clients;
});

/// One merged event stream across every target instance, with each event
/// tagged by its origin.
///
/// Deliberately keeps the `Stream<SseEvent>` shape of the original
/// single-daemon provider: the dozen-plus existing listeners keep working
/// untouched, and only the code that cares about provenance reads
/// [SseEvent.instanceId].
Stream<SseEvent> mergeInstanceEvents(Map<String, SseClient> clients) {
  if (clients.isEmpty) return const Stream<SseEvent>.empty();
  if (clients.length == 1) {
    final entry = clients.entries.first;
    return entry.value.connect().map((e) => e.withInstance(entry.key));
  }

  final controller = StreamController<SseEvent>.broadcast();
  final subscriptions = <StreamSubscription<SseEvent>>[];
  for (final entry in clients.entries) {
    subscriptions.add(
      entry.value.connect().listen(
        (event) => controller.add(event.withInstance(entry.key)),
        // One instance failing must not tear down the merged stream: the other
        // instances keep streaming and the dashboard degrades instead of
        // going blank.
        onError: (Object error) =>
            debugPrint('sse: instance ${entry.key} errored: $error'),
        cancelOnError: false,
      ),
    );
  }
  controller.onCancel = () async {
    for (final sub in subscriptions) {
      await sub.cancel();
    }
  };
  return controller.stream;
}

/// Route to a PR detail screen, carrying the instance so the record is
/// unambiguous. Store ids are per-instance, so `/prs/42` alone can mean two
/// different pull requests once more than one daemon is registered.
String prDetailRoute(int prId, String instanceId) =>
    instanceId.isEmpty
    ? '/prs/$prId'
    : '/prs/$prId?instance=${Uri.encodeQueryComponent(instanceId)}';

/// The issue-side equivalent of [prDetailRoute].
String issueDetailRoute(int issueId, String instanceId) =>
    instanceId.isEmpty
    ? '/issues/$issueId'
    : '/issues/$issueId?instance=${Uri.encodeQueryComponent(instanceId)}';
