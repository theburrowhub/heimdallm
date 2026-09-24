# Heimdallm Flutter UI

Heimdallm's desktop/web GUI is built with Flutter, Riverpod, GoRouter, and a custom design system on top of Mix. The app talks to the Go daemon over HTTP/SSE and ships in three shapes: macOS desktop, Linux desktop, and Flutter Web.

## Design system

The design system lives in `lib/shared/design_system/`:

- `tokens.dart` — semantic tokens for `AppColors`, `AppSpace`, `AppRadius`, `AppTextStyles`, and shared layout breakpoints in `AppBreakpoints`
- `theme.dart` — `HeimdallmTheme.light()`, `HeimdallmTheme.dark()`, and `HeimdallmTheme.scope(...)`, which bridge Material `ThemeData` into a `MixScope`
- `components/` — reusable themed widgets, split into two layers:
  - primitives: `AppSurface`, `AppButton`, `AppText`, `AppBadge`, `AppIconButton`
  - composition components, extracted so every screen's toolbar/list/grid share one look instead of each rebuilding its own: `AppToolbar` (the filter/action bar — leading chips + filters + a right-pinned trailing zone, no decorated container, matching Activity's reference style), `AppFilterChip` (a selectable pill, with an optional accent `ColorToken` and count badge), `AppSearchField` (owns its controller/focus and resyncs safely on external resets), `AppSegmentedFilter`/`AppSegment` (a bordered, connected multi-option control with per-segment counts), `AppMultiSelectChip`/`showAppMultiSelect` (the one shared multi-select dialog), `AppViewToggle`/`AppViewMode` (the list/grid switch), `AppListRow` (a list row — accent bar, leading badges, title/subtitle, and a *tight*-`Flexible` trailing zone that always reaches the row's right edge — see the "trailing alignment" note below), `AppGridCard`/`AppGridDelegate` (the matching mosaic tile and shared grid metrics), `AppPageBody` (the full-width `Column[toolbar, header, Expanded(child)]` wrapper — no screen should wrap its body in a `ConstrainedBox`), and `AppFieldGrid` (lays a form's short fields into as many columns as fit, instead of stretching them to the full window width)
- `color_resolver.dart` — helper for resolving color tokens safely in tests or minimal hosts that may not have a full `MixScope`

### Token inventory

Authoritative token definitions are in `lib/shared/design_system/tokens.dart`:

- **Colors (`AppColors`)**
  - surfaces: `canvas`, `surface`, `surfaceRaised`, `border`
  - text: `text`, `textMuted`, `onAccent`
  - brand/interaction: `accent`, `accentMuted`, `focus`
  - status: `success`, `warning`, `danger`, `info`
  - feature palette roles: `featurePrReview`, `featureMergeTracking`, `featureMixed`, `featureOffFill`, `featureOffOutline`
- **Spacing (`AppSpace`)**: `xs`, `sm`, `md`, `lg`, `xl`, `xxl`
- **Radii (`AppRadius`)**: `sm`, `md`, `lg`, `pill` (999 — fully rounded chips/pills)
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

### Important gotcha: pinning `AppListRow`/toolbar trailing content to the right edge

A `Row` with an `Expanded` title and a `Flexible` trailing zone sharing the same flex looks reasonable but leaves a dead gap: a *loose* `Flexible` (the default `fit`) shrinks to its content instead of claiming its share of the row, so `WrapAlignment.end` inside it never reaches the row's actual right edge. `AppListRow` and `AppToolbar` avoid this with `Flexible(fit: FlexFit.tight, child: Align(alignment: Alignment.centerRight, child: Wrap(...)))` — the tight fit forces the trailing slot to claim its full share of the row, and `Align` pins the (still-reflowing) `Wrap` against that slot's own right edge. Reach for this pattern instead of a loose `Flexible` any time trailing content (badges, buttons, an icon button) needs to sit flush against a row's edge while still being allowed to wrap onto a second line at narrow widths.

## Navigation

The app now uses `StatefulShellRoute.indexedStack` in `lib/shared/router.dart` with a responsive shell from `lib/shared/layout/app_shell.dart`. The shell preserves branch state while adapting navigation chrome by width and by the user's own sidebar preference:

- `< 768px` (`AppBreakpoints.compact`): drawer navigation, regardless of preference — there's no room for a rail
- `>= 768px`: a `sidebarModeProvider` (`lib/core/state/sidebar_preferences.dart`) preference of `hidden` / `icons` / `extended`, persisted like `appearanceProvider`. Its default, `AppSidebarMode.auto`, keeps the shell's original width-derived behavior (extended at/above `AppBreakpoints.medium`, icons-only below it) until the user cycles the `Key('sidebar-toggle')` button in the app bar, at which point an explicit choice always wins over width. `effectiveSidebarMode(preference, width)` and `nextSidebarMode(effective)` are the two pure functions this resolves through — cycling always starts from what's currently on screen, not from the raw stored preference.

Global chrome is shared across every destination: the sidebar toggle, instance selector, update banner, circuit-breaker banner, connection banner, instance-failure banner, daemon start/stop control, Server shortcut, Settings shortcut, and refresh action.

### List/grid view-mode preferences

Activity and Repositories each persist their view-mode choice inside their own pre-existing state (`ActivityFilters.viewMode`, `repos_screen.dart`'s `repos_view` key) and keep doing so. Every other screen that offers an `AppViewToggle` (Merge, Instances, Prompts, CLI Agents) uses the shared `viewModeProvider(prefsKey)` family in `lib/core/state/view_mode_preferences.dart` instead — one `ViewModeNotifier` per screen-specific `prefsKey`, so each screen remembers its own choice independently.

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
| `/repos/:name` | Repository detail | `:name` is URI-decoded |
| `/orgs/:name` | Organization detail | `:name` is URI-decoded |
| `/config` | Settings | Stays reachable outside the shell for first-run/bootstrap flows |
| `/server` | Server screen | Preserves `?tab=` with `status` default |
| `/logs` | Logs alias | Redirects to `/server?tab=logs` |

Preserved deep-link/query-param contracts from the pre-migration UI:

- `?instance=` remains the instance disambiguator for PR detail routes
- `/server?tab=status|events|logs` remains the server/logs deep-link contract
- `/logs` still redirects to `/server?tab=logs`
- `/agents` still works as a compatibility alias for `/prompts`

## Layout width

Every screen uses the full available width — no screen wraps its body in a `ConstrainedBox` to cap it. `AppPageBody` is the one place that rule lives (`Column[toolbar, header, Expanded(child)]`, unconstrained). Where a form has several short fields (a poll interval, a retention count, a timeout) that would otherwise stretch to the full window width, lay them out with `AppFieldGrid` instead, which puts as many equal-width columns as fit `minFieldWidth` (capped at 3) rather than one full-width field per row.

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

- `lib/features/repositories/widgets/feature_palette.dart` keeps concrete feature colors because the LEDs, switches, section accents, and bulk-action chips are a stable capability legend (`PR Review`, `Merge Tracking`), not generic theme roles.
- `lib/features/merge_tracking/widgets/check_visuals.dart` stays partly concrete for merge-specific warning semantics, and related merge-tracking widgets keep a few fixed domain hues (for example warning-banner yellows plus phase colors such as merged purple, auto-merge teal, and neutral grey tracking states) so those states remain instantly recognizable.
