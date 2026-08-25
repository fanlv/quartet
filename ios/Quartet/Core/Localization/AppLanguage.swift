import Foundation

enum AppLanguage: String, CaseIterable, Identifiable, Sendable {
    case system
    case simplifiedChinese = "zh-Hans"
    case english = "en"

    var id: String { rawValue }

    var localizationKey: String {
        switch self {
        case .system: "跟随系统"
        case .simplifiedChinese: "简体中文"
        case .english: "English"
        }
    }

    /// Quartet currently ships Chinese and English. A system language outside
    /// those two intentionally resolves to English instead of depending on the
    /// device's localization fallback order.
    func resolvedLocale(systemLocale: Locale) -> Locale {
        switch self {
        case .simplifiedChinese:
            return Locale(identifier: "zh-Hans")
        case .english:
            return Locale(identifier: "en")
        case .system:
            let languageCode = systemLocale.language.languageCode?.identifier.lowercased()
            return Locale(identifier: languageCode == "zh" ? "zh-Hans" : "en")
        }
    }

    static var currentLocale: Locale {
#if DEBUG
        if ProcessInfo.processInfo.arguments.contains("--ui-testing-language-en") {
            return Locale(identifier: "en")
        }
        if ProcessInfo.processInfo.arguments.contains(where: { $0.hasPrefix("--ui-testing-") }) {
            return Locale(identifier: "zh-Hans")
        }
#endif
        let saved = UserDefaults.standard.string(forKey: "quartet.appLanguage")
            .flatMap(AppLanguage.init(rawValue:)) ?? .system
        let systemIdentifier = Locale.preferredLanguages.first ?? Locale.current.identifier
        return saved.resolvedLocale(systemLocale: Locale(identifier: systemIdentifier))
    }

    static func localized(_ key: String) -> String {
        localized(key, locale: currentLocale)
    }

    static func localized(_ key: String, locale: Locale) -> String {
        let languageCode = locale.language.languageCode?.identifier.lowercased()
        let resourceName = languageCode == "zh" ? "zh-Hans" : "en"
        guard let path = Bundle.main.path(forResource: resourceName, ofType: "lproj"),
              let localizedBundle = Bundle(path: path) else {
            return key
        }
        return localizedBundle.localizedString(forKey: key, value: key, table: nil)
    }

    static func localizedFormat(_ key: String, _ arguments: CVarArg...) -> String {
        String(format: localized(key), locale: currentLocale, arguments: arguments)
    }
}

extension String {
    var localizedForApp: String { AppLanguage.localized(self) }

    func localized(in locale: Locale) -> String {
        AppLanguage.localized(self, locale: locale)
    }
}
