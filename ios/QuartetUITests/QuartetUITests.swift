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

        XCTAssertTrue(app.buttons["main-tab-0"].exists)
        app.buttons["job-job-chat-running"].tap()
        XCTAssertTrue(app.navigationBars["优化 iOS 交互体验"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.descendants(matching: .any)["main-tab-bar"].exists)
        XCTAssertTrue(app.textFields["chat-composer"].exists)
        XCTAssertTrue(app.staticTexts["已完成第一轮检查。运行状态和操作反馈都已同步。"].exists)
        XCTAssertTrue(app.staticTexts["TraeCode"].exists)
        XCTAssertFalse(app.staticTexts["ASSISTANT"].exists)
        XCTAssertTrue(app.staticTexts["AI 正在思考..."].exists)
        XCTAssertFalse(app.staticTexts["当前轮次运行中，新消息会保存到服务端队列并按顺序发送。"].exists)
        XCTAssertTrue(app.buttons["chat-message-history"].exists)
        XCTAssertFalse(app.staticTexts["历史会话"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["workspace-footer"].exists)
        XCTAssertTrue(app.staticTexts["Workspace(Quartet Studio) :"].exists)
        XCTAssertTrue(app.staticTexts["/workspace/quartet"].exists)
        XCTAssertTrue(app.staticTexts["main"].exists)

        app.buttons["chat-model-selector"].tap()
        XCTAssertTrue(app.buttons["GPT-5.4"].waitForExistence(timeout: 2))
        app.buttons["GPT-5.4"].tap()
        XCTAssertTrue(app.staticTexts["GPT-5.4"].waitForExistence(timeout: 2))

        app.buttons["chat-thought-level-selector"].tap()
        XCTAssertTrue(app.buttons["深入"].waitForExistence(timeout: 2))
        app.buttons["深入"].tap()
        XCTAssertTrue(app.staticTexts["深入"].waitForExistence(timeout: 2))

        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.buttons["main-tab-0"].waitForExistence(timeout: 2))

        app.buttons["main-tab-2"].tap()
        XCTAssertTrue(app.navigationBars["设置"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.staticTexts["https://quartet.example.test/"].exists)
        app.buttons["settings-edit-connection"].tap()
        XCTAssertTrue(app.buttons["connection-submit"].waitForExistence(timeout: 3))
    }

    func testChatAttachmentMenuAnchorsAboveComposerButton() {
        launchDashboard()

        app.buttons["job-job-chat-running"].tap()
        XCTAssertTrue(app.navigationBars["优化 iOS 交互体验"].waitForExistence(timeout: 5))

        let attachmentMenu = app.buttons["chat-attachment-menu"]
        XCTAssertTrue(attachmentMenu.exists)
        let attachmentAnchorFrame = attachmentMenu.frame
        attachmentMenu.tap()

        let cameraAction = app.buttons["相机"]
        let fileAction = app.buttons["文件"]
        XCTAssertTrue(cameraAction.waitForExistence(timeout: 2))
        XCTAssertTrue(fileAction.exists)
        XCTAssertLessThan(fileAction.frame.maxY, attachmentAnchorFrame.minY)
        XCTAssertLessThan(fileAction.frame.minX, attachmentAnchorFrame.midX)
        XCTAssertGreaterThan(fileAction.frame.maxX, attachmentAnchorFrame.midX)

        app.coordinate(withNormalizedOffset: CGVector(dx: 0.08, dy: 0.08)).tap()
        XCTAssertFalse(fileAction.waitForExistence(timeout: 1))
    }

    func testUsageStatsTabShowsDashboard() {
        launchDashboard()

        app.buttons["main-tab-1"].tap()
        XCTAssertTrue(app.navigationBars["使用统计"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.otherElements["stats-kpis"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.staticTexts["总耗时"].exists)
        XCTAssertTrue(app.staticTexts["总轮次"].exists)
        XCTAssertTrue(app.staticTexts["账号"].exists)
        XCTAssertTrue(app.otherElements["stats-trend"].exists)
        XCTAssertTrue(app.staticTexts["按工作区"].exists)
    }

    func testRecentJobsScrollBehindTransparentTabBar() {
        app.launchArguments = ["--ui-testing-transparent-tabbar"]
        app.launch()

        let recentTab = app.buttons["main-tab-0"]
        let targetJob = app.buttons["job-job-tabbar-10"]
        XCTAssertTrue(recentTab.waitForExistence(timeout: 5))

        let scrollView = app.scrollViews.firstMatch
        XCTAssertTrue(scrollView.exists)
        for _ in 0..<8 where !targetJob.isHittable {
            scrollView.swipeUp()
        }
        XCTAssertTrue(targetJob.waitForExistence(timeout: 2))

        let start = scrollView.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.72))
        let end = scrollView.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.86))
        start.press(forDuration: 0.05, thenDragTo: end)

        XCTAssertGreaterThan(
            targetJob.frame.maxY,
            recentTab.frame.minY,
            "任务行应该能够滚动到透明 TabBar 后方"
        )
        let attachment = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        attachment.name = "最近任务从透明 TabBar 后方划过"
        attachment.lifetime = .keepAlways
        add(attachment)
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

    func testNewConversationAppliesProjectAndGlobalPresets() {
        launchDashboard()
        app.buttons["new-conversation-button"].tap()
        XCTAssertTrue(app.navigationBars["新任务"].waitForExistence(timeout: 5))

        app.buttons["new-task-message-history"].tap()
        XCTAssertTrue(app.navigationBars["预置消息与历史"].waitForExistence(timeout: 2))
        XCTAssertTrue(app.staticTexts["当前项目"].exists)
        XCTAssertTrue(app.staticTexts["全部项目"].exists)
        app.buttons["检查当前改动"].tap()

        let message = app.textViews["new-conversation-message"]
        XCTAssertTrue(message.waitForExistence(timeout: 2))
        XCTAssertEqual(message.value as? String, "请检查当前工作区的改动并给出风险清单。")

        app.buttons["new-task-message-history"].tap()
        app.buttons["总结进展"].tap()
        XCTAssertTrue(app.buttons["追加"].waitForExistence(timeout: 2))
        app.buttons["追加"].tap()
        XCTAssertEqual(
            app.textViews["new-conversation-message"].value as? String,
            "请检查当前工作区的改动并给出风险清单。\n\n请总结当前进展、遗留问题和下一步建议。"
        )

        app.buttons["new-task-message-history"].tap()
        XCTAssertTrue(app.navigationBars["预置消息与历史"].waitForExistence(timeout: 2))
        let replacementPreset = app.buttons["总结进展"]
        XCTAssertTrue(replacementPreset.waitForExistence(timeout: 2))
        replacementPreset.tap()
        XCTAssertTrue(app.buttons["替换"].waitForExistence(timeout: 2))
        app.buttons["替换"].tap()
        XCTAssertEqual(
            app.textViews["new-conversation-message"].value as? String,
            "请总结当前进展、遗留问题和下一步建议。"
        )
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
        XCTAssertTrue(app.buttons["main-tab-0"].waitForExistence(timeout: 30))
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
        XCTAssertTrue(
            app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "IOS_E2E_STREAM_OK")).firstMatch
                .waitForExistence(timeout: 180)
        )
    }

    func testLiveBackendOpensExistingChatWithoutError() throws {
        guard ProcessInfo.processInfo.environment["QUARTET_LIVE_E2E"] == "1" else {
            throw XCTSkip("Set QUARTET_LIVE_E2E=1 to run against the configured real backend.")
        }

        app.launch()
        guard app.buttons["main-tab-0"].waitForExistence(timeout: 30) else {
            if app.buttons["connection-submit"].exists {
                throw XCTSkip("The device has no reusable Quartet login session.")
            }
            XCTFail("The live Quartet dashboard did not become available.")
            return
        }

        let jobButtons = app.buttons.matching(
            NSPredicate(format: "identifier BEGINSWITH 'job-' AND NOT identifier BEGINSWITH 'job-more-'")
        )
        XCTAssertTrue(jobButtons.firstMatch.waitForExistence(timeout: 30))

        var openedChat = false
        for index in 0..<jobButtons.count {
            jobButtons.element(boundBy: index).tap()
            if app.textFields["chat-composer"].waitForExistence(timeout: 8) {
                openedChat = true
                break
            }
            if app.buttons["复制"].exists && app.buttons["关闭"].exists {
                XCTFail("Opening an existing Job presented an error detail sheet.")
                return
            }
            app.navigationBars.buttons.element(boundBy: 0).tap()
            XCTAssertTrue(app.buttons["main-tab-0"].waitForExistence(timeout: 5))
        }

        XCTAssertTrue(openedChat, "No interactive chat was available in the live Job list.")
        XCTAssertFalse(app.buttons["复制"].exists && app.buttons["关闭"].exists)
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
