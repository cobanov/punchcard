import AppKit

// LSUIElement in Info.plist keeps this out of the Dock and the app switcher;
// a menu bar app that also takes a Dock slot is two apps' worth of presence for
// one app's worth of use.
let application = NSApplication.shared
let delegate = AppDelegate()
application.delegate = delegate
application.run()
