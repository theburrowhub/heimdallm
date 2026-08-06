import 'dart:async';

import 'package:flutter_test/flutter_test.dart';

Future<void> testExecutable(FutureOr<void> Function() testMain) async {
  // A missed hit test means the gesture did not reach the widget identified by
  // the test. Fail instead of allowing a warning-only tap to give false
  // confidence that the user interaction was exercised. See ../README.md.
  WidgetController.hitTestWarningShouldBeFatal = true;
  await testMain();
}
