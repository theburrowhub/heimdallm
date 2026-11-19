import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/platform/platform_services.dart';
import '../../core/platform/platform_services_provider.dart';

/// The title bar the app draws where the platform hides its own (#902).
///
/// On macOS the system title bar is hidden, so this strip takes its place in
/// the app bar's colour: title bar and app bar then read as one native toolbar
/// while the traffic lights stay native. Dragging the strip moves the window
/// and double-clicking it does what the user's title bar setting says, as a
/// real title bar does. In native full screen the system hides its title bar,
/// so the strip collapses. Everywhere else (Linux, the web) the platform keeps
/// its own title bar and [child] is returned untouched.
class TitleBarStrip extends ConsumerWidget {
  final Widget child;

  const TitleBarStrip({super.key, required this.child});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final services = ref.watch(platformServicesProvider);
    if (services is! WindowChromePlatformCapability) return child;
    final chrome = services as WindowChromePlatformCapability;
    final height = chrome.titleBarHeight;
    if (height <= 0) return child;
    final theme = Theme.of(context);
    final media = MediaQuery.of(context);
    // The same tree in and out of full screen, so the app below keeps its
    // state; only the strip's height changes.
    return ValueListenableBuilder<bool>(
      valueListenable: chrome.fullScreen,
      builder: (context, fullScreen, _) {
        final strip = fullScreen ? 0.0 : height;
        return Column(
          children: [
            GestureDetector(
              key: const Key('title-bar-strip'),
              behavior: HitTestBehavior.opaque,
              onPanStart: (_) => chrome.startWindowDrag(),
              onDoubleTap: chrome.titleBarDoubleClick,
              child: ColoredBox(
                color:
                    theme.appBarTheme.backgroundColor ??
                    theme.colorScheme.surface,
                child: SizedBox(height: strip, width: double.infinity),
              ),
            ),
            Expanded(
              // The app gets the window minus the strip; sizes it reads from
              // MediaQuery (dialogs, sheets) must say so.
              child: MediaQuery(
                data: media.copyWith(
                  size: Size(media.size.width, media.size.height - strip),
                ),
                child: child,
              ),
            ),
          ],
        );
      },
    );
  }
}
