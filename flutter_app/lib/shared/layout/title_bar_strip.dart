import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/platform/platform_services.dart';
import '../../core/platform/platform_services_provider.dart';

/// The title bar the app draws where the platform hides its own (#902).
///
/// On macOS the system title bar is hidden and the window content runs up to
/// the top edge, under the native traffic lights. This widget hands the app
/// that title bar band as a top inset (`MediaQuery.padding.top`), the way a
/// phone status bar is handed over: app bars grow into it in their own colour
/// and the sidebar runs up through it (#927), so title bar, toolbar and
/// sidebar read as one native window. A transparent strip over the band drags
/// the window, and double-clicking it does what the user's title bar setting
/// says, as a real title bar does. In native full screen the system hides its
/// title bar, so the inset collapses. Everywhere else (Linux, the web) the
/// platform keeps its own title bar and [child] is returned untouched.
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
    final media = MediaQuery.of(context);
    // The same tree in and out of full screen, so the app below keeps its
    // state; only the inset changes.
    final strip = ValueListenableBuilder<bool>(
      valueListenable: chrome.fullScreen,
      builder: (context, fullScreen, _) {
        final inset = fullScreen ? 0.0 : height;
        return Stack(
          fit: StackFit.expand,
          children: [
            MediaQuery(
              data: media.copyWith(
                padding: media.padding.copyWith(top: media.padding.top + inset),
                viewPadding: media.viewPadding.copyWith(
                  top: media.viewPadding.top + inset,
                ),
              ),
              child: child,
            ),
            Positioned(
              top: 0,
              left: 0,
              right: 0,
              height: inset,
              child: GestureDetector(
                key: const Key('title-bar-strip'),
                behavior: HitTestBehavior.opaque,
                onPanStart: (_) => chrome.startWindowDrag(),
                onDoubleTap: chrome.titleBarDoubleClick,
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
