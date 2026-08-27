import SwiftUI

@main
struct QuartetApp: App {
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
