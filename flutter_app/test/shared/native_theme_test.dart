import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

// Native desktop chrome (#902), in both themes.
void main() {
  for (final (name, theme) in [
    ('light', HeimdallmTheme.light()),
    ('dark', HeimdallmTheme.dark()),
  ]) {
    test('$name: no ink ripples', () {
      expect(theme.splashFactory, NoSplash.splashFactory);
    });

    test('$name: a flat toolbar on the surface colour', () {
      final bar = theme.appBarTheme;
      expect(bar.backgroundColor, theme.colorScheme.surface);
      expect(bar.scrolledUnderElevation, 0);
      expect(bar.surfaceTintColor, Colors.transparent);
      expect(bar.toolbarHeight, kNativeToolbarHeight);
      expect(bar.centerTitle, isFalse);
      expect(bar.titleTextStyle?.fontWeight, FontWeight.w600);
    });

    test('$name: thin scrollbars and patient tooltips', () {
      expect(theme.scrollbarTheme.thickness?.resolve({}), 6);
      expect(
        theme.tooltipTheme.waitDuration,
        const Duration(milliseconds: 500),
      );
    });
  }
}
