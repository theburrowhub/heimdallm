import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';

// Flutter only adds libraries reachable from a test entrypoint to its LCOV
// report. Keep a direct import and a minimal reference here for each production
// library that is not already imported by a behavioral test. This is only a
// collector entrypoint; it does not replace focused tests for that library.
// See the maintenance notes in ../README.md.
void main() {
  test('keeps otherwise-unreferenced production libraries in coverage', () {
    // The route helpers live beside the instance providers and are otherwise
    // only reached from widget callbacks, which coverage does not walk into.
    expect(prDetailRoute(1, ''), '/prs/1');
    expect(prDetailRoute(2, 'srv-a'), contains('instance=srv-a'));
  });

  test('design-system components barrel re-exports its widgets', () {
    expect(designSystemComponentTypes(), [
      AppBadge,
      AppButton,
      AppFieldGrid,
      AppFilterChip,
      AppGridCard,
      AppIconButton,
      AppListRow,
      AppMultiSelectChip,
      AppPageBody,
      AppSearchField,
      AppSegmentedFilter,
      AppSurface,
      AppText,
      AppToolbar,
      AppViewToggle,
    ]);
  });
}
