# Heimdallm Flutter UI

Heimdallm's desktop/web GUI is built with Flutter, Riverpod, GoRouter, and a custom design system on top of Mix. The app talks to the Go daemon over HTTP/SSE and ships in three shapes: macOS desktop, Linux desktop, and Flutter Web.

## Design system

The design system lives in `lib/shared/design_system/`:

- `tokens.dart` — semantic tokens for `AppColors`, `AppSpace`, `AppRadius`, `AppTextStyles`, and shared layout breakpoints in `AppBreakpoints`
- `theme.dart` — `HeimdallmTheme.light()`, `HeimdallmTheme.dark()`, and `HeimdallmTheme.scope(...)`, which bridge Material `ThemeData` into a `MixScope`
- `components/` — reusable themed primitives such as `AppSurface`, `AppButton`, `AppText`, and `AppBadge`
- `color_resolver.dart` — helper for resolving color tokens safely in tests or minimal hosts that may not have a full `MixScope`

### Token inventory

Authoritative token definitions are in `lib/shared/design_system/tokens.dart`:

- **Colors (`AppColors`)**
  - surfaces: `canvas`, `surface`, `surfaceRaised`, `border`
  - text: `text`, `textMuted`, `onAccent`
  - brand/interaction: `accent`, `accentMuted`, `focus`
  - status: `success`, `warning`, `danger`, `info`
  - feature palette roles: `featurePrReview`, `featureIssueTracking`, `featureDevelop`, `featureMergeTracking`, `featureMixed`, `featureOffFill`, `featureOffOutline`
- **Spacing (`AppSpace`)**: `xs`, `sm`, `md`, `lg`, `xl`, `xxl`
- **Radii (`AppRadius`)**: `sm`, `md`, `lg`
- **Typography (`AppTextStyles`)**: `pageTitle`, `sectionTitle`, `body`, `bodyMuted`, `label`, `mono`
- **Breakpoints (`AppBreakpoints`)**: `compact = 768`, `medium = 1200`

Use components and tokens together rather than styling directly with raw Material colors/widgets:

```dart
import 'package:flutter/material.dart';

import 'shared/design_system/components/components.dart';

AppSurface(
  padding: const EdgeInsets.all(16),
  child: Column(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      const AppText.sectionTitle('Daemon settings'),
      const SizedBox(height: 8),
      const AppText.muted('Changes are styled through the shared token set.'),
      const SizedBox(height: 12),
      AppButton.secondary(
        label: 'Restart daemon',
        leading: const Icon(Icons.refresh),
        onPressed: () {},
      ),
    ],
  ),
)
```

### Themes

`HeimdallmTheme.light()` and `HeimdallmTheme.dark()` both derive a Material 3 `ColorScheme` from the shared brand seed (`0xFF0969DA`). `main.dart` applies those as `theme`/`darkTheme`, uses `appearanceProvider` for `ThemeMode.light` / `dark` / `system`, and then wraps the app in `HeimdallmTheme.scope(...)` so Mix tokens resolve from the active Material theme.

### Important gotcha: `ListTile` widgets inside `AppSurface`

`AppSurface` renders a styled Mix `Box`/`DecoratedBox`, not a `Material`. Flutter's `ListTile` family (`SwitchListTile`, `CheckboxListTile`, `RadioListTile`, `ExpansionTile`) needs a nearest `Material` ancestor for ink/background painting. When one of those widgets sits inside an `AppSurface`, wrap it in `Material(type: MaterialType.transparency)`:

```dart
AppSurface(
  padding: const EdgeInsets.all(16),
  child: Material(
    type: MaterialType.transparency,
    child: SwitchListTile(
      value: enabled,
      onChanged: onChanged,
      title: const Text('Enable feature'),
    ),
  ),
)
```

This is not optional. Missing the wrapper triggers Flutter's “ListTile background color or ink splashes may be invisible” assertion. It was caught in Linux verification (`make verify-linux`) even though it did not reproduce reliably in macOS `flutter test`.

## Navigation

The app now uses `StatefulShellRoute.indexedStack` in `lib/shared/router.dart` with a responsive shell from `lib/shared/layout/app_shell.dart`. The shell preserves branch state while adapting navigation chrome by width:

- `< 768px` (`AppBreakpoints.compact`): drawer navigation
- `768px .. < 1200px`: collapsed `NavigationRail`
- `>= 1200px`: extended `NavigationRail`

Global chrome is shared across every destination: instance selector, update banner, circuit-breaker banner, connection banner, instance-failure banner, daemon start/stop control, Server shortcut, Settings shortcut, and refresh action.

### Routes and aliases

| Path | Purpose | Notes |
|---|---|---|
| `/` | Activity home | Root shell branch; builds `DashboardScreen` as the Activity destination |
| `/activity` | Activity log | Separate shell branch |
| `/merge` | Merge tracking | Separate shell branch |
| `/repos` | Repository list | Separate shell branch |
| `/orgs` | Organization list | Separate shell branch |
| `/prompts` | Prompt profiles | Canonical route for `AgentsScreen` |
| `/agents` | Prompt profiles alias | Redirects to `/prompts` for backwards compatibility |
| `/cli-agents` | CLI/model agent settings | Separate shell branch |
| `/stats` | Statistics | Separate shell branch |
| `/instances` | Instances | Separate shell branch |
| `/instances/routing` | Routing rules | Nested under Instances |
| `/prs/:id` | PR detail | Preserve `?instance=` query param; empty/missing means local daemon |
| `/issues/:id` | Issue detail | Preserve `?instance=` query param; empty/missing means local daemon |
| `/repos/:name` | Repository detail | `:name` is URI-decoded |
| `/orgs/:name` | Organization detail | `:name` is URI-decoded |
| `/config` | Settings | Stays reachable outside the shell for first-run/bootstrap flows |
| `/server` | Server screen | Preserves `?tab=` with `status` default |
| `/logs` | Logs alias | Redirects to `/server?tab=logs` |

Preserved deep-link/query-param contracts from the pre-migration UI:

- `?instance=` remains the instance disambiguator for PR and issue detail routes
- `/server?tab=status|events|logs` remains the server/logs deep-link contract
- `/logs` still redirects to `/server?tab=logs`
- `/agents` still works as a compatibility alias for `/prompts`

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

For design-system widget tests, prefer the real app host instead of mocking Mix away: wrap the widget in `MaterialApp` or `MaterialApp.router`, apply `HeimdallmTheme.light()`/`dark()`, and set `builder: (context, child) => HeimdallmTheme.scope(child: child ?? const SizedBox.shrink())`. Add `ProviderScope` overrides and `FakePlatformServices` when the widget depends on Riverpod/platform APIs; `test/widget_test.dart`, `test/shared/router_instance_test.dart`, and screen tests such as `test/features/activity/activity_screen_test.dart` show the pattern.

For any change that touches layout, `AppSurface`, or `ListTile`-family widgets nested inside custom surfaces, run `make verify-linux` from the repo root in addition to local Flutter tests. Some Material-ancestor/ink-painting failures are Linux-only and may stay green under macOS `flutter test`.

## Justified concrete-color exceptions

- `lib/features/repositories/widgets/feature_palette.dart` keeps concrete feature colors because the LEDs, switches, section accents, and bulk-action chips are a stable capability legend (`PR Review`, `Issue Tracking`, `Develop`, `Merge Tracking`), not generic theme roles.
- `lib/features/merge_tracking/widgets/check_visuals.dart` stays partly concrete for merge-specific warning semantics, and related merge-tracking widgets keep a few fixed domain hues (for example warning-banner yellows plus phase colors such as merged purple, auto-merge teal, and neutral grey tracking states) so those states remain instantly recognizable.
