import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/instances/instances_providers.dart';
import '../dashboard/dashboard_providers.dart';

/// Identifies one PR record. Store ids are per-instance, so the id alone is
/// ambiguous once more than one daemon is registered; the empty instance id is
/// the daemon this app manages.
typedef PRRef = ({String instanceId, int prId});

final prDetailProvider =
    FutureProvider.family<Map<String, dynamic>, PRRef>((ref, key) async {
      ref.watch(sseStreamProvider);
      final api = ref.watch(apiClientForProvider(key.instanceId));
      return api.fetchPR(key.prId);
    });

class ReviewTriggerNotifier extends AsyncNotifier<void> {
  @override
  Future<void> build() async {}

  Future<void> trigger(int prId, {String instanceId = ''}) async {
    state = const AsyncLoading();
    state = await AsyncValue.guard(() async {
      // The trigger has to reach the instance that actually holds this PR, not
      // whichever one the dashboard happens to be scoped to.
      final api = ref.read(apiClientForProvider(instanceId));
      await api.triggerReview(prId);
    });
    // Invalidate after successful trigger
    if (!state.hasError) {
      ref.invalidate(prDetailProvider((instanceId: instanceId, prId: prId)));
      ref.invalidate(prsByInstanceProvider);
    }
  }
}

final reviewTriggerProvider =
    AsyncNotifierProvider.autoDispose<ReviewTriggerNotifier, void>(
      ReviewTriggerNotifier.new,
    );
