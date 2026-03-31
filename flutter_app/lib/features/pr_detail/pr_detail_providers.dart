import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../dashboard/dashboard_providers.dart';

final prDetailProvider = FutureProvider.family<Map<String, dynamic>, int>((ref, prId) async {
  ref.watch(sseStreamProvider);
  final api = ref.watch(apiClientProvider);
  return api.fetchPR(prId);
});

class ReviewTriggerNotifier extends AutoDisposeAsyncNotifier<void> {
  @override
  Future<void> build() async {}

  Future<void> trigger(int prId) async {
    state = const AsyncLoading();
    state = await AsyncValue.guard(() async {
      final api = ref.read(apiClientProvider);
      await api.triggerReview(prId);
    });
    // Invalidate after successful trigger
    if (!state.hasError) {
      ref.invalidate(prDetailProvider(prId));
      ref.invalidate(prsProvider);
    }
  }
}

final reviewTriggerProvider = AsyncNotifierProvider.autoDispose<ReviewTriggerNotifier, void>(
  ReviewTriggerNotifier.new,
);
