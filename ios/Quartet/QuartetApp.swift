import SwiftUI
import UIKit
import UserNotifications

final class QuartetAppDelegate: NSObject, UIApplicationDelegate, @preconcurrency UNUserNotificationCenterDelegate {
    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        return true
    }

    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        [.banner, .sound]
    }
}

@main
struct QuartetApp: App {
    @UIApplicationDelegateAdaptor(QuartetAppDelegate.self) private var appDelegate
    @Environment(\.locale) private var systemLocale
    @StateObject private var model = AppModel()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environmentObject(model)
                .environment(\.locale, model.appLanguage.resolvedLocale(systemLocale: systemLocale))
                // Keep the fallback outside RootView so sheets, popovers and every future page
                // inherit the bundled typeface even when they do not set a more specific role.
                .font(.quartet(.regular))
        }
    }
}
