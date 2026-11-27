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
    final strip = ValueListenableBuilder<bool>(
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
    return chrome.translucentSidebar
        ? _WindowBrightnessSync(chrome: chrome, child: strip)
        : strip;
  }
}

/// Keeps a translucent window's light or dark look in step with the app's
/// theme (#927). The sidebar material and the traffic lights follow the
/// window's appearance, which is the system's unless the app sets it, so an
/// app in dark mode on a light system would otherwise draw light text on
/// light glass.
class _WindowBrightnessSync extends StatefulWidget {
  final WindowChromePlatformCapability chrome;
  final Widget child;

  const _WindowBrightnessSync({required this.chrome, required this.child});

  @override
  State<_WindowBrightnessSync> createState() => _WindowBrightnessSyncState();
}

class _WindowBrightnessSyncState extends State<_WindowBrightnessSync> {
  Brightness? _sent;

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    final brightness = Theme.of(context).brightness;
    if (brightness == _sent) return;
    _sent = brightness;
    widget.chrome.setWindowBrightness(brightness);
  }

  @override
  Widget build(BuildContext context) => widget.child;
}
