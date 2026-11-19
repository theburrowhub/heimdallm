import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/platform/platform_services.dart';
import '../../core/platform/platform_services_provider.dart';

/// The title bar the app draws where the platform hides its own (#902).
///
/// On macOS the system title bar is hidden, so this strip takes its place in
/// the app bar's colour: title bar and app bar then read as one native toolbar
/// while the traffic lights stay native. Dragging the strip moves the window
/// and double-clicking it zooms, as a real title bar does. Everywhere else
/// (Linux, the web) the platform keeps its own title bar and [child] is
/// returned untouched.
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
    return Column(
      children: [
        GestureDetector(
          key: const Key('title-bar-strip'),
          behavior: HitTestBehavior.opaque,
          onPanStart: (_) => chrome.startWindowDrag(),
          onDoubleTap: chrome.toggleWindowZoom,
          child: ColoredBox(
            color:
                theme.appBarTheme.backgroundColor ?? theme.colorScheme.surface,
            child: SizedBox(height: height, width: double.infinity),
          ),
        ),
        Expanded(child: child),
      ],
    );
  }
}
