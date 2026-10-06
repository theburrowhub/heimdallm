/// How the app presents an OS notification.
enum NotificationMode {
  /// Banner plus the platform's notification sound (the historic behaviour).
  sound,

  /// Banner only, no sound.
  silent,

  /// Nothing: neither banner nor sound.
  off,
}

/// Which family a notification belongs to, so each one can have its own
/// [NotificationMode].
enum NotificationCategory {
  /// Review lifecycle (started / complete / failed) and My PRs alerts,
  /// including the daily digest.
  activity,

  /// "Heimdallm update available".
  update,
}

String encodeNotificationMode(NotificationMode mode) => switch (mode) {
  NotificationMode.sound => 'sound',
  NotificationMode.silent => 'silent',
  NotificationMode.off => 'off',
};

/// Unknown or missing values fall back to [NotificationMode.sound] so an
/// install without the preference keeps notifying exactly as before.
NotificationMode decodeNotificationMode(String? value) => switch (value) {
  'silent' => NotificationMode.silent,
  'off' => NotificationMode.off,
  _ => NotificationMode.sound,
};

/// Per-category notification modes chosen by the user on this device.
class NotificationPreferences {
  const NotificationPreferences({
    this.activity = NotificationMode.sound,
    this.update = NotificationMode.sound,
  });

  final NotificationMode activity;
  final NotificationMode update;

  NotificationMode modeFor(NotificationCategory category) => switch (category) {
    NotificationCategory.activity => activity,
    NotificationCategory.update => update,
  };

  NotificationPreferences copyWith({
    NotificationMode? activity,
    NotificationMode? update,
  }) => NotificationPreferences(
    activity: activity ?? this.activity,
    update: update ?? this.update,
  );

  @override
  bool operator ==(Object other) =>
      other is NotificationPreferences &&
      other.activity == activity &&
      other.update == update;

  @override
  int get hashCode => Object.hash(activity, update);

  @override
  String toString() =>
      'NotificationPreferences(activity: $activity, update: $update)';
}
