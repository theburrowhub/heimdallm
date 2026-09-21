import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/state/view_mode_preferences.dart';
import 'package:heimdallm/shared/design_system/components/app_view_toggle.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
  });

  test('defaults to list before preferences load', () {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    expect(container.read(viewModeProvider('merge_view')), AppViewMode.list);
  });

  test('each prefsKey is loaded and persisted independently', () async {
    SharedPreferences.setMockInitialValues({'merge_view': 'grid'});
    final container = ProviderContainer();
    addTearDown(container.dispose);

    container.read(viewModeProvider('merge_view'));
    container.read(viewModeProvider('instances_view'));
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);

    expect(container.read(viewModeProvider('merge_view')), AppViewMode.grid);
    expect(
      container.read(viewModeProvider('instances_view')),
      AppViewMode.list,
    );

    container
        .read(viewModeProvider('instances_view').notifier)
        .set(AppViewMode.grid);
    await Future<void>.delayed(Duration.zero);

    final prefs = await SharedPreferences.getInstance();
    expect(prefs.getString('instances_view'), 'grid');
    // Unchanged by instances_view's set(): each prefsKey's family notifier
    // must not clobber another key's stored value.
    expect(prefs.getString('merge_view'), 'grid');
  });
}
