import Cocoa
import FlutterMacOS

class MainFlutterWindow: NSWindow {
  override func awakeFromNib() {
    let flutterViewController = FlutterViewController()
    // The window's content is the sidebar material (#927). Flutter paints over
    // it with a clear background, so wherever the app draws nothing (the
    // sidebar) the desktop shows through blurred, as in Finder or Mail.
    flutterViewController.backgroundColor = .clear
    let material = NSVisualEffectView(frame: self.contentView?.bounds ?? .zero)
    material.material = .sidebar
    material.blendingMode = .behindWindow
    material.state = .followsWindowActiveState
    let container = NSViewController()
    container.view = material
    container.addChild(flutterViewController)
    flutterViewController.view.frame = material.bounds
    flutterViewController.view.autoresizingMask = [.width, .height]
    material.addSubview(flutterViewController.view)

    let windowFrame = self.frame
    self.contentViewController = container
    self.setFrame(windowFrame, display: true)

    RegisterGeneratedPlugins(registry: flutterViewController)

    super.awakeFromNib()
  }
}
