import 'dart:async';
import 'dart:ui' show VoidCallback;
import 'package:flutter/painting.dart' show Size;
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/platform/platform_services.dart';

/// In-memory fake used by every non-platform-specific test. Every method
/// records its calls so assertions stay tight — no unexpected interactions
/// should slip past a test.
class FakePlatformServices
    implements
        PlatformServices,
        AppUpdatePlatformCapability,
        DuplicateInstancePlatformCapability {
  FakePlatformServices({
    this.apiBaseUrl = 'http://127.0.0.1:7842',
    this.token,
    Map<String, String>? env,
    this.configExistsValue = true,
    this.githubToken,
    this.daemonBinaryPath,
    this.appUpdateSupport = AppUpdateSupport.unavailable,
    String? appUpdateUnavailableReason,
    this.appVersion = const AppVersionInfo(version: '0.8.4'),
    this.pendingUpdateVersion,
    this.pendingUpdateError,
    this.completeUpdateError,
    AppUpdateStatus? appUpdateStatus,
  }) : _env = env ?? const {},
       _appUpdateUnavailableReason =
           appUpdateUnavailableReason ??
           (appUpdateSupport == AppUpdateSupport.native
               ? null
               : 'This test build cannot update itself.'),
       _appUpdateStatus = appUpdateStatus ?? const AppUpdateStatus.idle();

  @override
  final String apiBaseUrl;
  String? token;
  final Map<String, String> _env;
  bool configExistsValue;
  String? githubToken;
  String? daemonBinaryPath;
  @override
  final AppUpdateSupport appUpdateSupport;
  final String? _appUpdateUnavailableReason;
  @override
  String? get appUpdateUnavailableReason => _appUpdateUnavailableReason;
  AppVersionInfo appVersion;
  String? pendingUpdateVersion;
  Object? pendingUpdateError;
  Object? completeUpdateError;
  AppUpdateStatus _appUpdateStatus;
  final StreamController<AppUpdateStatus> _appUpdateEvents =
      StreamController<AppUpdateStatus>.broadcast();

  // Records of calls for assertions.
  int loadApiTokenCalls = 0;
  int clearApiTokenCacheCalls = 0;
  int ensureSingleInstanceCalls = 0;
  VoidCallback? activationListener;
  int setupWindowCalls = 0;
  int setupTrayCalls = 0;
  int setupNotifierCalls = 0;
  final List<({String title, String body})> notifications = [];
  int showAndFocusCalls = 0;
  int hideCalls = 0;
  int quitCalls = 0;
  int quitDuplicateInstanceCalls = 0;
  int setupAppUpdaterCalls = 0;
  int loadAppVersionCalls = 0;
  int checkForAppUpdatesCalls = 0;
  int installAppUpdateCalls = 0;
  int completeAppUpdateCalls = 0;
  int finalizeAppUpdateCalls = 0;
  final List<AppConfig> writtenConfigs = [];
  final List<String> spawnedDaemons = [];
  void Function(String location)? trayNavigationHandler;

  @override
  Future<String?> loadApiToken() async {
    loadApiTokenCalls++;
    return token;
  }

  @override
  void clearApiTokenCache() => clearApiTokenCacheCalls++;

  @override
  String? readEnv(String name) => _env[name];

  @override
  Future<bool> ensureSingleInstance() async {
    ensureSingleInstanceCalls++;
    return true;
  }

  @override
  void listenForActivationSignal(VoidCallback onActivate) {
    activationListener = onActivate;
  }

  @override
  Future<void> setupWindow({
    required String title,
    required Size size,
    required Size minimumSize,
  }) async {
    setupWindowCalls++;
  }

  @override
  Future<void> setupTray({required ApiClient apiClient}) async {
    setupTrayCalls++;
  }

  @override
  void setTrayNavigationHandler(void Function(String location) handler) {
    trayNavigationHandler = handler;
  }

  @override
  Future<void> setupNotifier({required String appName}) async {
    setupNotifierCalls++;
  }

  @override
  void showNotification({
    required String title,
    required String body,
    VoidCallback? onClick,
  }) {
    notifications.add((title: title, body: body));
  }

  @override
  Future<void> setPreventWindowClose(bool enable) async {}

  @override
  Future<void> showAndFocusWindow() async => showAndFocusCalls++;

  @override
  Future<void> hideWindow() async => hideCalls++;

  @override
  void quitApp() {
    quitCalls++;
  }

  @override
  void quitDuplicateInstance() {
    quitDuplicateInstanceCalls++;
  }

  @override
  Future<void> setupAppUpdater() async => setupAppUpdaterCalls++;

  @override
  Future<AppVersionInfo> loadAppVersion() async {
    loadAppVersionCalls++;
    return appVersion;
  }

  @override
  AppUpdateStatus get appUpdateStatus => _appUpdateStatus;

  @override
  Stream<AppUpdateStatus> get appUpdateEvents => _appUpdateEvents.stream;

  void emitAppUpdateStatus(AppUpdateStatus status) {
    _appUpdateStatus = status;
    _appUpdateEvents.add(status);
  }

  @override
  Future<void> checkForAppUpdates() async => checkForAppUpdatesCalls++;

  @override
  Future<void> installAppUpdate() async => installAppUpdateCalls++;

  @override
  Future<String?> pendingAppUpdateVersion() async {
    final error = pendingUpdateError;
    if (error != null) throw error;
    return pendingUpdateVersion;
  }

  @override
  Future<void> completeAppUpdate() async {
    completeAppUpdateCalls++;
    final error = completeUpdateError;
    if (error != null) throw error;
    pendingUpdateVersion = null;
  }

  @override
  Future<void> finalizeAppUpdate() async {
    finalizeAppUpdateCalls++;
  }

  @override
  Future<String?> detectGitHubToken() async => githubToken;

  @override
  Future<String?> getStoredGitHubToken() async => githubToken;

  @override
  Future<void> storeGitHubToken(String newToken) async {
    githubToken = newToken;
  }

  @override
  Future<void> writeDaemonConfig(AppConfig config) async {
    writtenConfigs.add(config);
  }

  @override
  Future<bool> daemonConfigExists() async => configExistsValue;

  @override
  String? defaultDaemonBinaryPath() => daemonBinaryPath;

  @override
  Future<void> spawnDaemon(String binaryPath) async {
    spawnedDaemons.add(binaryPath);
  }

  final List<({List<PR> prs, String me})> trayRebuilds = [];

  @override
  Future<void> rebuildTrayMenu({
    required List<PR> prs,
    required String me,
  }) async {
    trayRebuilds.add((prs: prs, me: me));
  }

  List<String> discoveredRepos = const [];

  @override
  Future<List<String>> discoverReposFromPRs(String token) async =>
      discoveredRepos;
}
