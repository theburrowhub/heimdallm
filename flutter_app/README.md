# flutter_app

A new Flutter project.

## Getting Started

This project is a starting point for a Flutter application.

A few resources to get you started if this is your first Flutter project:

- [Learn Flutter](https://docs.flutter.dev/get-started/learn-flutter)
- [Write your first Flutter app](https://docs.flutter.dev/get-started/codelab)
- [Flutter learning resources](https://docs.flutter.dev/reference/learning-resources)

For help getting started with Flutter development, view the
[online documentation](https://docs.flutter.dev/), which offers tutorials,
samples, guidance on mobile development, and a full API reference.

## Testing

Run the Flutter checks from this directory:

```bash
flutter test
flutter analyze
```

Widget tests treat missed hit-test warnings as failures through
`test/flutter_test_config.dart`. A missed hit test can otherwise leave a test
green even though its gesture did not reach the widget a user would interact
with. Fix the finder, test layout, or widget visibility when this fails; do not
drive a controller directly merely to bypass the gesture path the test is meant
to verify.

### Coverage collector universe

Flutter's LCOV collector reports only production libraries reachable from a
test entrypoint. The coverage gate compares that report with every production
source under `lib/`, so a new source that no behavioral test imports causes a
missing-source failure.

Prefer importing new production code from a focused behavioral test. If a
library is intentionally not reachable from one, add a direct package import
and a minimal reference to `test/coverage_universe_test.dart`. Do not rely on a
transitive import: keeping each otherwise-unreferenced library explicit makes
the maintenance contract visible when dependencies are reorganized. The
universe test only makes the source visible to LCOV; normal coverage and diff
coverage requirements still apply.
