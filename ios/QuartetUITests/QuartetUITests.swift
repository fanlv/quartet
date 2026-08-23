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
        app.textFields["connection-username"].tap()
        app.textFields["connection-username"].typeText("admin")
        app.secureTextFields["connection-password"].tap()
        app.secureTextFields["connection-password"].typeText("test-password")
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

    func testUsageStatsTabShowsDashboard() {
        launchDashboard()

        app.tabBars.buttons["统计"].tap()
        XCTAssertTrue(app.navigationBars["使用统计"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.otherElements["stats-kpis"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.staticTexts["总耗时"].exists)
        XCTAssertTrue(app.staticTexts["总轮次"].exists)
        XCTAssertTrue(app.staticTexts["账号"].exists)
        XCTAssertTrue(app.otherElements["stats-trend"].exists)
        XCTAssertTrue(app.staticTexts["按工作区"].exists)
    }

    func testCreatesANewConversationFromPrimaryAction() {
        launchDashboard()

        app.buttons["new-conversation-button"].tap()
        XCTAssertTrue(app.navigationBars["新任务"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Quartet Studio"].exists)
        XCTAssertTrue(app.staticTexts["TraeCode"].exists)

        let message = app.textViews["new-conversation-message"]
        XCTAssertTrue(message.waitForExistence(timeout: 2))
        message.tap()
        message.typeText("验证新建对话主路径")
        app.buttons["new-conversation-create"].tap()

        XCTAssertTrue(app.navigationBars["新任务"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["验证新建对话主路径"].exists)
        XCTAssertTrue(app.textFields["chat-composer"].exists)

        app.navigationBars.buttons.element(boundBy: 0).tap()
        app.buttons["new-conversation-button"].tap()
        XCTAssertTrue(app.navigationBars["新任务"].waitForExistence(timeout: 5))
        app.buttons["new-task-message-history"].tap()
        let historyItem = app.buttons["验证新建对话主路径"]
        XCTAssertTrue(historyItem.waitForExistence(timeout: 2))
        historyItem.tap()
        XCTAssertTrue(app.staticTexts["9 字"].waitForExistence(timeout: 2))
        let recalledMessage = app.textViews["new-conversation-message"]
        XCTAssertTrue(recalledMessage.waitForExistence(timeout: 2))
        recalledMessage.tap()
        recalledMessage.typeText("，补充范围")
        XCTAssertTrue(app.staticTexts["14 字"].waitForExistence(timeout: 2))
    }

    func testGraphRunShowsProgressAndHumanAction() {
        launchDashboard()

        app.buttons["job-job-graph-waiting"].tap()
        XCTAssertTrue(app.navigationBars["发布前检查流水线"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["等待人工讨论"].exists)
        XCTAssertTrue(app.staticTexts["2/4"].exists)
        XCTAssertTrue(app.staticTexts["人工确认发布"].exists)
    }

    func testLiveBackendNewConversationStreamsAssistantMessage() throws {
        guard ProcessInfo.processInfo.environment["QUARTET_LIVE_E2E"] == "1" else {
            throw XCTSkip("Set QUARTET_LIVE_E2E=1 to run against the configured real backend.")
        }

        app.launch()
        XCTAssertTrue(app.tabBars.buttons["最近任务"].waitForExistence(timeout: 30))
        app.buttons["new-conversation-button"].tap()
        XCTAssertTrue(app.navigationBars["新任务"].waitForExistence(timeout: 15))

        let message = app.textViews["new-conversation-message"]
        XCTAssertTrue(message.waitForExistence(timeout: 30))
        let prompt = "请从 1 数到 100，每个数字单独一行，最后输出 IOS_E2E_STREAM_OK。不要使用工具。"
        message.tap()
        message.typeText(prompt)
        app.buttons["new-conversation-create"].tap()

        XCTAssertTrue(app.textFields["chat-composer"].waitForExistence(timeout: 45))
        XCTAssertTrue(app.staticTexts[prompt].waitForExistence(timeout: 15))
        XCTAssertTrue(app.staticTexts["ASSISTANT"].firstMatch.waitForExistence(timeout: 45))
        XCTAssertTrue(
            app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "IOS_E2E_STREAM_OK")).firstMatch
                .waitForExistence(timeout: 180)
        )
    }

    private func launchDashboard() {
        app.launchArguments = ["--ui-testing-dashboard"]
        app.launch()
        XCTAssertTrue(app.buttons["new-conversation-button"].waitForExistence(timeout: 5))
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
