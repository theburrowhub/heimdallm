import Cocoa
import FlutterMacOS

@main
@MainActor
class AppDelegate: FlutterAppDelegate {
  private let updateCoordinator = DaemonUpdateCoordinator()

  override func applicationDidFinishLaunching(_ notification: Notification) {
    // FlutterAppDelegate does not implement this selector: its lifecycle
    // registrar independently observes NSApplicationDidFinishLaunching.
    // Calling super here raises NSInvalidArgumentException at runtime.
    guard let controller = mainFlutterWindow?.contentViewController as? FlutterViewController else {
      assertionFailure("Flutter view controller is unavailable; updater channel was not attached")
      return
    }
    updateCoordinator.attach(to: controller)
  }

  override func applicationShouldTerminate(
    _ sender: NSApplication
  ) -> NSApplication.TerminateReply {
    updateCoordinator.applicationShouldTerminate()
  }

  override func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
    // Heimdallm is a tray app — keep the process alive when the window closes.
    // The user quits via "Quit" in the menu bar icon.
    return false
  }

  override func applicationSupportsSecureRestorableState(_ app: NSApplication) -> Bool {
    return true
  }
}
