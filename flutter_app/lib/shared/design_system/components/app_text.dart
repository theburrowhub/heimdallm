import 'package:flutter/widgets.dart';
import 'package:mix/mix.dart';

import '../tokens.dart';

/// Semantic text roles for the design system.
///
/// Prefer this over a bare Material `Text` in redesigned surfaces so every
/// screen shares the same size/weight/color scale. Falls back gracefully
/// when used without a `MixScope` ancestor is *not* supported — components
/// always render under `HeimdallmTheme.scope` (wired in `main.dart`).
enum AppTextRole { pageTitle, sectionTitle, body, bodyMuted, label, mono }

class AppText extends StatelessWidget {
  final String text;
  final AppTextRole role;
  final Color? color;
  final TextAlign? textAlign;
  final int? maxLines;
  final TextOverflow? overflow;

  const AppText(
    this.text, {
    super.key,
    this.role = AppTextRole.body,
    this.color,
    this.textAlign,
    this.maxLines,
    this.overflow,
  });

  const AppText.pageTitle(this.text, {super.key, this.color})
    : role = AppTextRole.pageTitle,
      textAlign = null,
      maxLines = null,
      overflow = null;

  const AppText.sectionTitle(this.text, {super.key, this.color})
    : role = AppTextRole.sectionTitle,
      textAlign = null,
      maxLines = null,
      overflow = null;

  const AppText.muted(
    this.text, {
    super.key,
    this.textAlign,
    this.maxLines,
    this.overflow,
  }) : role = AppTextRole.bodyMuted,
       color = null;

  const AppText.label(this.text, {super.key, this.color})
    : role = AppTextRole.label,
      textAlign = null,
      maxLines = null,
      overflow = null;

  const AppText.mono(this.text, {super.key, this.color, this.maxLines})
    : role = AppTextRole.mono,
      textAlign = null,
      overflow = null;

  TextStyleToken get _token => switch (role) {
    AppTextRole.pageTitle => AppTextStyles.pageTitle,
    AppTextRole.sectionTitle => AppTextStyles.sectionTitle,
    AppTextRole.body => AppTextStyles.body,
    AppTextRole.bodyMuted => AppTextStyles.bodyMuted,
    AppTextRole.label => AppTextStyles.label,
    AppTextRole.mono => AppTextStyles.mono,
  };

  @override
  Widget build(BuildContext context) {
    var style = TextStyler().style(_token.mix());
    if (color != null) style = style.color(color!);
    if (textAlign != null) style = style.textAlign(textAlign!);
    if (maxLines != null) style = style.maxLines(maxLines!);
    if (overflow != null) style = style.overflow(overflow!);

    return StyledText(text, style: style);
  }
}
