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
        }
    }
}
