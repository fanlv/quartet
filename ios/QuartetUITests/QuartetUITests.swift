import XCTest

@MainActor
final class QuartetUITests: XCTestCase {
    private let app = XCUIApplication()

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    func testOnboardingValidatesAddressAndShowsCompleteError() {
        app.launchArguments = ["--ui-testing-onboarding"]
        app.launch()

        let server = app.textFields["connection-server"]
        XCTAssertTrue(server.waitForExistence(timeout: 5))
        server.tap()
        server.clearAndTypeText("not-a-url")
        app.secureTextFields["connection-token"].tap()
        app.secureTextFields["connection-token"].typeText("test-token")
        app.buttons["connection-submit"].tap()

        XCTAssertTrue(app.navigationBars["服务地址无效"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "当前值：not-a-url")).firstMatch.exists)
        XCTAssertTrue(app.buttons["复制"].exists)
        app.buttons["关闭"].tap()
        XCTAssertTrue(server.waitForExistence(timeout: 2))
    }

    func testDashboardNavigationAndWorkspaceFilter() {
        launchDashboard()

        XCTAssertTrue(app.buttons["new-conversation-button"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["dashboard-new-conversation"].exists)
        XCTAssertFalse(app.searchFields.firstMatch.exists)
        XCTAssertTrue(app.staticTexts["优化 iOS 交互体验"].exists)
        XCTAssertTrue(app.staticTexts["发布前检查流水线"].exists)
        XCTAssertFalse(app.staticTexts["组件回归检查"].exists)
        XCTAssertFalse(app.staticTexts["连接正常"].exists)
        XCTAssertTrue(app.buttons["workspace-selector"].exists)

        app.buttons["connection-status-button"].tap()
        XCTAssertTrue(app.navigationBars["连接状态"].waitForExistence(timeout: 2))
        XCTAssertTrue(app.staticTexts["连接正常"].exists)
        app.buttons["connection-status-close"].tap()

        app.buttons["job-filter-menu"].tap()
        let hideScheduledJobs = app.buttons["hide-scheduled-jobs-toggle"]
        XCTAssertTrue(hideScheduledJobs.exists)
        hideScheduledJobs.tap()
        XCTAssertTrue(app.staticTexts["组件回归检查"].waitForExistence(timeout: 2))

        app.buttons["workspace-selector"].tap()
        app.buttons["workspace-filter-ws-lab"].tap()
        XCTAssertTrue(app.staticTexts["组件回归检查"].waitForExistence(timeout: 2))
        XCTAssertFalse(app.staticTexts["优化 iOS 交互体验"].exists)

        app.buttons["workspace-selector"].tap()
        app.buttons["workspace-filter-all"].tap()
        XCTAssertTrue(app.staticTexts["发布前检查流水线"].waitForExistence(timeout: 2))
        XCTAssertTrue(app.staticTexts["优化 iOS 交互体验"].exists)
    }

    func testConversationAndSettingsFlows() {
        launchDashboard()

        app.buttons["job-job-chat-running"].tap()
        XCTAssertTrue(app.navigationBars["优化 iOS 交互体验"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.textFields["chat-composer"].exists)
        XCTAssertTrue(app.staticTexts["已完成第一轮检查。运行状态和操作反馈都已同步。"].exists)
        app.navigationBars.buttons.element(boundBy: 0).tap()

        app.tabBars.buttons["设置"].tap()
        XCTAssertTrue(app.navigationBars["设置"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.staticTexts["https://quartet.example.test/"].exists)
        app.buttons["settings-edit-connection"].tap()
        XCTAssertTrue(app.buttons["connection-submit"].waitForExistence(timeout: 3))
    }

    func testCreatesANewConversationFromPrimaryAction() {
        launchDashboard()

        app.buttons["new-conversation-button"].tap()
        XCTAssertTrue(app.navigationBars["新对话"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Quartet Studio"].exists)
        XCTAssertTrue(app.staticTexts["TraeCode"].exists)

        let message = app.textViews["new-conversation-message"]
        XCTAssertTrue(message.waitForExistence(timeout: 2))
        message.tap()
        message.typeText("验证新建对话主路径")
        app.buttons["new-conversation-create"].tap()

        XCTAssertTrue(app.navigationBars["新对话"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["验证新建对话主路径"].exists)
        XCTAssertTrue(app.textFields["chat-composer"].exists)
    }

    func testGraphRunShowsProgressAndHumanAction() {
        launchDashboard()

        app.buttons["job-job-graph-waiting"].tap()
        XCTAssertTrue(app.navigationBars["发布前检查流水线"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["等待人工讨论"].exists)
        XCTAssertTrue(app.staticTexts["2/4"].exists)
        XCTAssertTrue(app.staticTexts["人工确认发布"].exists)
    }

    private func launchDashboard() {
        app.launchArguments = ["--ui-testing-dashboard"]
        app.launch()
        XCTAssertTrue(app.tabBars.buttons["运行台"].waitForExistence(timeout: 5))
    }
}

private extension XCUIElement {
    func clearAndTypeText(_ text: String) {
        tap()
        press(forDuration: 0.8)
        let application = XCUIApplication()
        if application.menuItems["全选"].waitForExistence(timeout: 1) {
            application.menuItems["全选"].tap()
        } else if application.menuItems["Select All"].waitForExistence(timeout: 1) {
            application.menuItems["Select All"].tap()
        }
        typeText(text)
    }
}
