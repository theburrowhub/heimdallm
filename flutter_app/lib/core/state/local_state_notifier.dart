import 'package:flutter_riverpod/flutter_riverpod.dart';

/// Drop-in replacement for Riverpod 2's `StateProvider`. Exposes the same
/// `.set(T)` / `.update((T)=>T)` ergonomics so existing call sites keep their
/// shape; the only consumer-side change is `notifier.state = v` → `.set(v)`.
class LocalStateNotifier<T> extends Notifier<T> {
  LocalStateNotifier(this._initial);
  final T _initial;

  @override
  T build() => _initial;

  void set(T value) => state = value;
  void update(T Function(T) updater) => state = updater(state);
}
