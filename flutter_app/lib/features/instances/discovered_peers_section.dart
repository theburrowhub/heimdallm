import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/api_client.dart';
import '../../core/api/cluster_api.dart';
import '../../core/instances/instances_providers.dart';
import '../../core/instances/models.dart';
import 'instance_dialog.dart';

/// Heimdallm daemons the hub can see on the local network but nobody has
/// registered.
///
/// Deliberately a proposal and nothing more. mDNS is unauthenticated, so
/// anything on the LAN can advertise itself as a daemon; the hub has already
/// checked that each of these actually answers and says who it is, but
/// registering one stays a deliberate act with a token the operator supplies
/// out of band.
///
/// Rendered above the registered list rather than below it, because the case
/// that matters most is a fresh hub with nothing registered and a machine
/// waiting to be adopted.
class DiscoveredPeersSection extends ConsumerWidget {
  const DiscoveredPeersSection({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    // Confirmed hub, not merely "not known to be a standalone". A non-hub
    // answers 404 on the control plane, which maps to an empty, disabled
    // listing — indistinguishable from a hub with discovery switched off. So
    // an unreachable daemon would otherwise be invited to turn on a feature it
    // may have no route to.
    final isHub = ref.watch(localIsHubProvider) == true;

    final peersAsync = ref.watch(discoveredPeersProvider);
    return peersAsync.when(
      // Silent while loading and on error: this is a secondary surface, and a
      // spinner or a red box above the registry would give a convenience more
      // visual weight than the fleet it sits on top of. The error still
      // reaches anyone who presses Scan, which is when someone is actually
      // waiting on an answer.
      loading: () => const SizedBox.shrink(),
      error: (_, _) => const SizedBox.shrink(),
      data: (found) {
        if (!found.enabled) {
          return isHub ? const _DiscoveryOffCard() : const SizedBox.shrink();
        }
        final peers = found.unregistered;
        if (peers.isEmpty) return const SizedBox.shrink();
        return _FoundList(peers: peers);
      },
    );
  }
}

class _FoundList extends ConsumerWidget {
  const _FoundList({required this.peers});

  final List<DiscoveredPeer> peers;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final scheme = Theme.of(context).colorScheme;
    return Card(
      color: scheme.surfaceContainerHighest,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(12, 10, 12, 12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(Icons.wifi_find_outlined, size: 18, color: scheme.primary),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    peers.length == 1
                        ? '1 daemon found on this network, not registered'
                        : '${peers.length} daemons found on this network, '
                              'not registered',
                    style: Theme.of(context).textTheme.titleSmall,
                  ),
                ),
                const _ScanButton(),
              ],
            ),
            const SizedBox(height: 4),
            Text(
              'Registering one still needs its API token, which does not travel '
              'over the network. Anything on this LAN can advertise itself, so '
              'only adopt machines you recognise.',
              style: TextStyle(fontSize: 11, color: scheme.onSurfaceVariant),
            ),
            const SizedBox(height: 8),
            for (final peer in peers) _PeerRow(peer: peer),
          ],
        ),
      ),
    );
  }
}

class _PeerRow extends ConsumerWidget {
  const _PeerRow({required this.peer});

  final DiscoveredPeer peer;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final scheme = Theme.of(context).colorScheme;
    return Padding(
      padding: const EdgeInsets.only(top: 6),
      child: Row(
        children: [
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  peer.displayName,
                  style: const TextStyle(fontWeight: FontWeight.w500),
                ),
                Text(
                  [
                    peer.baseUrl,
                    if (peer.role.isNotEmpty) peer.role,
                    if (peer.version.isNotEmpty) 'v${peer.version}',
                  ].join(' · '),
                  style: TextStyle(
                    fontSize: 11,
                    color: scheme.onSurfaceVariant,
                  ),
                ),
              ],
            ),
          ),
          const SizedBox(width: 8),
          FilledButton.tonal(
            onPressed: () => showInstanceDialog(context, ref, discovered: peer),
            child: const Text('Register'),
          ),
        ],
      ),
    );
  }
}

/// Shown when the hub is a hub but `cluster.discovery` is off, so the section
/// is not silently absent. Without this the feature is invisible until someone
/// finds the key in config.toml.
class _DiscoveryOffCard extends ConsumerStatefulWidget {
  const _DiscoveryOffCard();

  @override
  ConsumerState<_DiscoveryOffCard> createState() => _DiscoveryOffCardState();
}

class _DiscoveryOffCardState extends ConsumerState<_DiscoveryOffCard> {
  bool _saving = false;
  String? _error;

  Future<void> _enable() async {
    setState(() {
      _saving = true;
      _error = null;
    });
    try {
      await ref
          .read(hubApiClientProvider)
          .patchConfig({
            'cluster': {'discovery': 'mdns'},
          });
      ref.invalidate(discoveredPeersProvider);
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    } catch (e) {
      if (mounted) setState(() => _error = '$e');
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Card(
      color: scheme.surfaceContainerHighest,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(12, 10, 12, 12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(
                  Icons.wifi_find_outlined,
                  size: 18,
                  color: scheme.onSurfaceVariant,
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    'Find instances on this network',
                    style: Theme.of(context).textTheme.titleSmall,
                  ),
                ),
                FilledButton.tonal(
                  onPressed: _saving ? null : _enable,
                  child: Text(_saving ? 'Enabling…' : 'Turn on'),
                ),
              ],
            ),
            const SizedBox(height: 4),
            Text(
              'Daemons can announce themselves over mDNS so this hub can offer '
              'them for registration, and so their addresses follow a DHCP '
              'change instead of going stale. Off by default: announcing a '
              'service on a shared network should be a choice. Only works '
              'within one subnet, and not across a container bridge.',
              style: TextStyle(fontSize: 11, color: scheme.onSurfaceVariant),
            ),
            if (_error != null) ...[
              const SizedBox(height: 8),
              Text(
                _error!,
                style: TextStyle(fontSize: 11, color: scheme.error),
              ),
            ],
          ],
        ),
      ),
    );
  }
}

class _ScanButton extends ConsumerStatefulWidget {
  const _ScanButton();

  @override
  ConsumerState<_ScanButton> createState() => _ScanButtonState();
}

class _ScanButtonState extends ConsumerState<_ScanButton> {
  bool _scanning = false;

  Future<void> _scan() async {
    setState(() => _scanning = true);
    try {
      await ref.read(hubApiClientProvider).scanForPeers();
      ref.invalidate(discoveredPeersProvider);
    } on ApiException catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(SnackBar(content: Text('Scan failed: ${e.message}')));
      }
    } finally {
      if (mounted) setState(() => _scanning = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return IconButton(
      tooltip: 'Scan the network now',
      icon: _scanning
          ? const SizedBox(
              width: 16,
              height: 16,
              child: CircularProgressIndicator(strokeWidth: 2),
            )
          : const Icon(Icons.refresh, size: 18),
      onPressed: _scanning ? null : _scan,
    );
  }
}

/// Offers to repair a registered instance whose address has moved.
///
/// This is the #765 failure in the making: the hub has the instance down at an
/// address it no longer answers on, so its peers are taking over repositories
/// it is still reviewing. The network says where it actually is — but the fix
/// is still one click, never an automatic rewrite, because a rogue advertiser
/// must not be able to redirect the hub on its own.
class AddressChangedBanner extends ConsumerStatefulWidget {
  const AddressChangedBanner({required this.instanceId, super.key});

  final String instanceId;

  @override
  ConsumerState<AddressChangedBanner> createState() =>
      _AddressChangedBannerState();
}

class _AddressChangedBannerState extends ConsumerState<AddressChangedBanner> {
  bool _saving = false;
  String? _error;

  Future<void> _apply(String baseUrl) async {
    setState(() {
      _saving = true;
      _error = null;
    });
    try {
      await ref
          .read(hubApiClientProvider)
          .patchInstance(widget.instanceId, baseUrl: baseUrl);
      ref.invalidate(daemonInstancesProvider);
      ref.invalidate(discoveredPeersProvider);
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    } catch (e) {
      if (mounted) setState(() => _error = '$e');
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final peer = ref
        .watch(discoveredPeersProvider)
        .maybeWhen(
          data: (found) => found.movedFor(widget.instanceId),
          orElse: () => null,
        );
    if (peer == null) return const SizedBox.shrink();

    final scheme = Theme.of(context).colorScheme;
    return Padding(
      padding: const EdgeInsets.only(top: 8),
      child: Container(
        padding: const EdgeInsets.all(8),
        decoration: BoxDecoration(
          color: scheme.tertiaryContainer.withValues(alpha: 0.4),
          borderRadius: BorderRadius.circular(6),
        ),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(Icons.swap_horiz, size: 16, color: scheme.tertiary),
                const SizedBox(width: 6),
                Expanded(
                  child: Text(
                    'Answering at ${peer.baseUrl}',
                    style: const TextStyle(
                      fontSize: 12,
                      fontWeight: FontWeight.w500,
                    ),
                  ),
                ),
                TextButton(
                  onPressed: _saving ? null : () => _apply(peer.baseUrl),
                  child: Text(_saving ? 'Updating…' : 'Update address'),
                ),
              ],
            ),
            Text(
              'This instance is registered at ${peer.registeredBaseUrl}, which '
              'it no longer answers on. Until the address is corrected the '
              'other instances will take over its repositories while it keeps '
              'reviewing them.',
              style: TextStyle(fontSize: 11, color: scheme.onSurfaceVariant),
            ),
            if (_error != null)
              Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Text(
                  _error!,
                  style: TextStyle(fontSize: 11, color: scheme.error),
                ),
              ),
          ],
        ),
      ),
    );
  }
}
