import XCTest
import UIKit

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

        app.buttons["connection-status-button"].tap()
        XCTAssertTrue(app.navigationBars["连接状态"].waitForExistence(timeout: 2))
        XCTAssertTrue(app.staticTexts["连接正常"].exists)
    }

    func testDashboardCompactJobRowActions() {
        launchDashboard()

        XCTAssertFalse(app.buttons["job-more-job-chat-running"].exists)
        XCTAssertTrue(app.buttons["job-time-job-chat-running"].exists)
        app.buttons["job-time-job-chat-running"].tap()
        XCTAssertTrue(app.buttons["job-action-pin"].waitForExistence(timeout: 2))
        XCTAssertTrue(app.buttons["job-action-rename"].exists)
        XCTAssertTrue(app.buttons["job-action-delete"].exists)
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
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.buttons["main-tab-0"].waitForExistence(timeout: 2))

        app.buttons["main-tab-3"].tap()
        XCTAssertTrue(app.navigationBars["设置"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.staticTexts["https://quartet.example.test/"].exists)
        app.buttons["settings-edit-connection"].tap()
        XCTAssertTrue(app.buttons["connection-submit"].waitForExistence(timeout: 3))
    }

    func testAgentManagementMoreMenuAndSingleUpgrade() {
        launchDashboard()

        app.buttons["main-tab-3"].tap()
        XCTAssertTrue(app.navigationBars["设置"].waitForExistence(timeout: 3))

        XCTAssertTrue(app.staticTexts["Agent 管理"].exists)
        XCTAssertTrue(app.buttons["settings-agent-catalog"].exists)
        XCTAssertTrue(app.buttons["settings-agent-environment"].exists)
        XCTAssertTrue(app.buttons["settings-agent-defaults"].exists)
        XCTAssertTrue(app.buttons["settings-agent-roles"].exists)

        app.buttons["settings-agent-catalog"].tap()
        XCTAssertTrue(app.navigationBars["目录"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["agent-catalog-upgrade-all"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["2 个可更新"].exists)
        XCTAssertTrue(app.buttons["agent-catalog-upgrade-trae"].exists)

        let more = app.buttons["agent-catalog-more-trae"]
        XCTAssertTrue(more.waitForExistence(timeout: 3))
        more.tap()
        let upgradeAction = app.buttons["agent-catalog-action-upgrade"]
        XCTAssertTrue(upgradeAction.waitForExistence(timeout: 3))
        upgradeAction.tap()
        XCTAssertTrue(app.navigationBars["升级这个 Agent？"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["agent-catalog-confirm"].exists)
        app.buttons["agent-catalog-confirm"].tap()
        XCTAssertTrue(app.staticTexts.matching(
            NSPredicate(format: "label CONTAINS %@", "升级成功")
        ).firstMatch.waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["agent-catalog-upgrade-all"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["1 个可更新"].exists)
    }

    func testAgentManagementSettingsDestinationsAndKeyboard() {
        launchDashboard()
        app.buttons["main-tab-3"].tap()
        XCTAssertTrue(app.navigationBars["设置"].waitForExistence(timeout: 3))
        let mainTabBar = app.descendants(matching: .any)["main-tab-bar"]
        XCTAssertTrue(mainTabBar.exists)
        XCTAssertEqual(mainTabBar.frame.maxY, app.frame.maxY, accuracy: 1)

        openAgentSettings("settings-agent-environment", title: "环境变量")
        let valueField = app.textFields["agent-env-value-field"].firstMatch
        XCTAssertTrue(valueField.waitForExistence(timeout: 5))
        valueField.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 3))
        valueField.typeText("/e2e")
        XCTAssertTrue((valueField.value as? String)?.contains("/e2e") == true)
        XCTAssertFalse(mainTabBar.exists)

        // The editor content is horizontally inset, so this point is blank canvas rather than a control.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.02, dy: 0.5)).tap()
        XCTAssertTrue(
            app.keyboards.firstMatch.waitForNonExistence(timeout: 3),
            "点击输入框外空白后键盘应消失"
        )
        XCTAssertTrue(
            mainTabBar.waitForExistence(timeout: 3),
            "键盘消失后当前页面应恢复 TabBar"
        )
        XCTAssertEqual(mainTabBar.frame.maxY, app.frame.maxY, accuracy: 1)

        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.navigationBars["设置"].waitForExistence(timeout: 3))
        XCTAssertTrue(mainTabBar.exists)
        XCTAssertEqual(mainTabBar.frame.maxY, app.frame.maxY, accuracy: 1)

        openAgentSettings("settings-agent-defaults", title: "默认参数")
        XCTAssertTrue(app.buttons["agent-defaults-agent-picker"].waitForExistence(timeout: 5))
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.navigationBars["设置"].waitForExistence(timeout: 3))

        openAgentSettings("settings-agent-roles", title: "角色分工")
        XCTAssertTrue(app.buttons["agent-role-title-agent"].waitForExistence(timeout: 5))
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.navigationBars["设置"].waitForExistence(timeout: 3))

        openAgentSettings("settings-agent-catalog", title: "目录")
        XCTAssertTrue(app.buttons["agent-catalog-check-versions"].waitForExistence(timeout: 5))
    }

    func testAgentManagementUpgradeAll() {
        launchDashboard()
        app.buttons["main-tab-3"].tap()
        XCTAssertTrue(app.navigationBars["设置"].waitForExistence(timeout: 3))
        app.buttons["settings-agent-catalog"].tap()
        XCTAssertTrue(app.navigationBars["目录"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["agent-catalog-upgrade-all"].waitForExistence(timeout: 5))

        app.buttons["agent-catalog-upgrade-all"].tap()
        XCTAssertTrue(app.navigationBars["更新全部 Agent？"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.staticTexts.matching(
            NSPredicate(format: "label CONTAINS %@", "TraeCode")
        ).firstMatch.exists)
        XCTAssertTrue(app.staticTexts.matching(
            NSPredicate(format: "label CONTAINS %@", "Codex")
        ).firstMatch.exists)
        XCTAssertFalse(app.staticTexts.matching(
            NSPredicate(format: "label CONTAINS %@", "Manual Agent")
        ).firstMatch.exists)
        app.buttons["agent-catalog-confirm"].tap()
        XCTAssertTrue(app.staticTexts["批量更新完成：2 个成功，0 个失败。"].waitForExistence(timeout: 8))
        XCTAssertTrue(app.descendants(matching: .any)["agent-catalog-result-trae"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["agent-catalog-result-codex"].exists)
        XCTAssertFalse(app.descendants(matching: .any)["agent-catalog-result-manual-agent"].exists)
        XCTAssertTrue(app.buttons["agent-catalog-upgrade-all"].isEnabled == false)
    }

    func testAgentManagementUpgradeAllContinuesAndStopsOnConflict() {
        app.launchArguments = ["--ui-testing-agent-upgrade-failures"]
        app.launch()
        XCTAssertTrue(app.buttons["main-tab-3"].waitForExistence(timeout: 5))
        app.buttons["main-tab-3"].tap()
        openAgentSettings("settings-agent-catalog", title: "目录")
        XCTAssertTrue(app.staticTexts["3 个可更新"].waitForExistence(timeout: 5))

        app.buttons["agent-catalog-upgrade-all"].tap()
        XCTAssertTrue(app.navigationBars["更新全部 Agent？"].waitForExistence(timeout: 3))
        app.buttons["agent-catalog-confirm"].tap()

        XCTAssertTrue(app.descendants(matching: .any).matching(
            NSPredicate(format: "label CONTAINS %@", "模拟网络错误：保留完整错误并继续后续 Agent。")
        ).firstMatch.waitForExistence(timeout: 8))
        XCTAssertTrue(app.descendants(matching: .any).matching(
            NSPredicate(format: "label CONTAINS %@", "HTTP 409")
        ).firstMatch.exists)
        XCTAssertTrue(app.descendants(matching: .any)["检测到另一个安装任务正在执行，已停止剩余更新。"].exists)
        XCTAssertFalse(app.descendants(matching: .any)["agent-catalog-result-after-conflict"].exists)
    }

    private func openAgentSettings(_ identifier: String, title: String) {
        let entry = app.buttons[identifier]
        XCTAssertTrue(entry.waitForExistence(timeout: 3))
        entry.tap()
        XCTAssertTrue(app.navigationBars[title].waitForExistence(timeout: 3))
    }

    func testChatEdgeSwipeBackThenOpensStats() {
        launchDashboard()

        for _ in 0..<3 {
            app.buttons["job-job-chat-running"].tap()
            XCTAssertTrue(app.navigationBars["优化 iOS 交互体验"].waitForExistence(timeout: 5))
            XCTAssertFalse(app.descendants(matching: .any)["main-tab-bar"].exists)

            let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.01, dy: 0.5))
            let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5))
            start.press(forDuration: 0.05, thenDragTo: end)

            XCTAssertTrue(app.buttons["main-tab-0"].waitForExistence(timeout: 3))
            XCTAssertFalse(app.navigationBars["优化 iOS 交互体验"].exists)

            app.buttons["main-tab-2"].tap()
            XCTAssertTrue(app.navigationBars["使用统计"].waitForExistence(timeout: 3))
            XCTAssertTrue(app.otherElements["stats-kpis"].exists)
            app.buttons["main-tab-0"].tap()
            XCTAssertTrue(app.buttons["job-job-chat-running"].waitForExistence(timeout: 3))
        }
    }

    func testOpeningChatDoesNotExposeBlackBottomBackground() {
        launchDashboard()

        app.buttons["job-job-chat-running"].tap()
        XCTAssertTrue(app.navigationBars["优化 iOS 交互体验"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.textFields["chat-composer"].waitForExistence(timeout: 2))
        XCTAssertFalse(app.descendants(matching: .any)["main-tab-bar"].exists)

        // The bug this guards was transient: the chat was laid out correctly from the first frame,
        // but the bottom strip stayed clipped to the old (tab bar era) height for a few frames and
        // rendered as bare canvas. A single settled screenshot cannot see it, so sample a burst.
        let rects = [
            CGRect(x: 0.05, y: 0.955, width: 0.25, height: 0.03),
            CGRect(x: 0.70, y: 0.955, width: 0.25, height: 0.03)
        ]
        var darkest: (index: Int, luminance: CGFloat, screenshot: XCUIScreenshot)?
        for index in 0..<12 {
            let shot = XCUIScreen.main.screenshot()
            let luminance = shot.averageLuminance(inNormalizedRects: rects)
            if darkest == nil || luminance < darkest!.luminance {
                darkest = (index, luminance, shot)
            }
        }

        guard let darkest else {
            return XCTFail("未能采集到聊天页截图")
        }

        let attachment = XCTAttachment(screenshot: darkest.screenshot)
        attachment.name = "聊天页底部最暗帧 #\(darkest.index)"
        attachment.lifetime = .keepAlways
        add(attachment)

        XCTAssertGreaterThan(
            darkest.luminance,
            0.10,
            "聊天页底部安全区在进入过程中露出了窗口背景（最暗帧 #\(darkest.index)，亮度 \(darkest.luminance)）"
        )
    }

    func testRootStatsDoesNotSwipeBackToRecentJobs() {
        launchDashboard()

        app.buttons["main-tab-2"].tap()
        XCTAssertTrue(app.navigationBars["使用统计"].waitForExistence(timeout: 3))

        let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.01, dy: 0.5))
        let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5))
        start.press(forDuration: 0.05, thenDragTo: end)

        XCTAssertTrue(app.navigationBars["使用统计"].waitForExistence(timeout: 2))
        XCTAssertTrue(app.otherElements["stats-kpis"].exists)
        XCTAssertFalse(app.buttons["new-conversation-button"].exists)
    }

    func testChatComposerConfigurationAndWorkspaceFooter() {
        launchDashboard()

        app.buttons["job-job-chat-running"].tap()
        XCTAssertTrue(app.navigationBars["优化 iOS 交互体验"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["chat-message-history"].exists)
        XCTAssertFalse(app.staticTexts["历史会话"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["workspace-footer"].exists)
        XCTAssertTrue(app.staticTexts["Quartet Studio："].exists)
        XCTAssertTrue(app.staticTexts["/workspace/quartet"].exists)
        XCTAssertTrue(app.staticTexts["main"].exists)

        app.buttons["chat-model-selector"].tap()
        XCTAssertTrue(app.navigationBars["选择模型"].waitForExistence(timeout: 2))
        XCTAssertTrue(app.buttons["GPT-5.4"].waitForExistence(timeout: 2))
        app.buttons["GPT-5.4"].tap()
        XCTAssertTrue(app.staticTexts["GPT-5.4"].waitForExistence(timeout: 2))

        app.buttons["chat-thought-level-selector"].tap()
        XCTAssertTrue(app.navigationBars["选择思考等级"].waitForExistence(timeout: 2))
        XCTAssertTrue(app.buttons["深入"].waitForExistence(timeout: 2))
        app.buttons["深入"].tap()
        XCTAssertTrue(app.staticTexts["深入"].waitForExistence(timeout: 2))
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
        let photoLibraryAction = app.buttons["相册"]
        let fileAction = app.buttons["文件"]
        XCTAssertTrue(cameraAction.waitForExistence(timeout: 2))
        XCTAssertTrue(photoLibraryAction.exists)
        XCTAssertTrue(fileAction.exists)
        XCTAssertLessThan(fileAction.frame.maxY, attachmentAnchorFrame.minY)
        XCTAssertLessThan(fileAction.frame.minX, attachmentAnchorFrame.midX)
        XCTAssertGreaterThan(fileAction.frame.maxX, attachmentAnchorFrame.midX)

        app.coordinate(withNormalizedOffset: CGVector(dx: 0.08, dy: 0.08)).tap()
        XCTAssertFalse(fileAction.waitForExistence(timeout: 1))
    }

    func testUsageStatsTabShowsDashboard() {
        launchDashboard()

        app.buttons["main-tab-2"].tap()
        XCTAssertTrue(app.navigationBars["使用统计"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.otherElements["stats-kpis"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.staticTexts["总耗时"].exists)
        XCTAssertTrue(app.staticTexts["总轮次"].exists)
        XCTAssertTrue(app.staticTexts["工作区"].exists)
        XCTAssertTrue(app.otherElements["stats-trend"].exists)
        let workspaceSection = app.staticTexts["按工作区"]
        for _ in 0..<4 where !workspaceSection.exists { app.swipeUp() }
        XCTAssertTrue(workspaceSection.waitForExistence(timeout: 2))
    }

    func testUsageStatsEnglishLocalization() {
        app.launchArguments = ["--ui-testing-dashboard", "--ui-testing-language-en"]
        app.launch()

        XCTAssertTrue(app.buttons["main-tab-2"].waitForExistence(timeout: 5))
        app.buttons["main-tab-2"].tap()
        XCTAssertTrue(app.navigationBars["Usage Statistics"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.otherElements["stats-kpis"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.staticTexts["Total time"].exists)
        XCTAssertTrue(app.staticTexts["Turns"].exists)
        XCTAssertTrue(app.staticTexts["Workspaces"].exists)
        XCTAssertTrue(app.staticTexts["Daily tokens"].exists)
        let workspaceSection = app.staticTexts["By Workspace"]
        for _ in 0..<4 where !workspaceSection.exists { app.swipeUp() }
        XCTAssertTrue(workspaceSection.waitForExistence(timeout: 2))
        XCTAssertTrue(app.staticTexts["Total"].exists)
        XCTAssertFalse(app.staticTexts["总耗时"].exists)
        XCTAssertFalse(app.staticTexts["总计"].exists)
    }

    func testRecentJobsScrollAboveDockedTabBar() {
        app.launchArguments = ["--ui-testing-docked-tabbar"]
        app.launch()

        let recentTab = app.buttons["main-tab-0"]
        let tabBar = app.descendants(matching: .any)["main-tab-bar"]
        let targetJob = app.buttons["job-job-tabbar-14"]
        XCTAssertTrue(recentTab.waitForExistence(timeout: 5))
        XCTAssertTrue(tabBar.exists)
        XCTAssertFalse(app.tabBars.firstMatch.exists, "系统 TabBar 不应残留背景或第二套按钮")
        XCTAssertEqual(tabBar.frame.maxY, app.frame.maxY, accuracy: 1, "自定义 TabBar 应贴住屏幕底边")

        let scrollView = app.scrollViews.firstMatch
        XCTAssertTrue(scrollView.exists)
        for _ in 0..<12 where !targetJob.isHittable {
            scrollView.swipeUp()
        }
        XCTAssertTrue(targetJob.waitForExistence(timeout: 2))
        XCTAssertTrue(targetJob.isHittable)

        let attachment = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        attachment.name = "最近任务与吸底实心 TabBar"
        attachment.lifetime = .keepAlways
        add(attachment)
        XCTAssertLessThanOrEqual(
            targetJob.frame.maxY,
            tabBar.frame.minY + 1,
            "最后一条任务应完整滚动到实心 TabBar 上方"
        )
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
            NSPredicate(format: "identifier BEGINSWITH 'job-' AND NOT identifier BEGINSWITH 'job-time-'")
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

    func testLiveBackendScheduledTaskCRUD() throws {
        #if !LIVE_SCHEDULE_E2E
            throw XCTSkip("Set QUARTET_LIVE_E2E=1 to run against the configured real backend.")
        #endif

        let originalName = "IOS_E2E_SCHEDULE_\(Int(Date().timeIntervalSince1970))"
        let updatedName = originalName + "_EDITED"
        app.launch()

        guard app.buttons["main-tab-1"].waitForExistence(timeout: 30) else {
            if app.buttons["connection-submit"].exists {
                throw XCTSkip("The device has no reusable Quartet login session.")
            }
            return XCTFail("The live Quartet dashboard did not become available.")
        }

        app.buttons["main-tab-1"].tap()
        XCTAssertTrue(app.navigationBars["定时任务"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.buttons["schedule-add"].waitForExistence(timeout: 15))
        removeLiveScheduleArtifacts(prefix: "IOS_E2E_SCHEDULE_")
        removeLiveScheduleJobs(prefix: "IOS_E2E_SCHEDULE_")
        addTeardownBlock { [self] in
            removeLiveScheduleArtifacts(prefix: "IOS_E2E_SCHEDULE_")
            removeLiveScheduleJobs(prefix: "IOS_E2E_SCHEDULE_")
        }
        app.buttons["schedule-add"].tap()

        XCTAssertTrue(app.navigationBars["新增定时任务"].waitForExistence(timeout: 5))
        let nameField = app.textFields["schedule-name"]
        XCTAssertTrue(nameField.waitForExistence(timeout: 5))
        nameField.tap()
        nameField.typeText(originalName)

        app.buttons["schedule-workflow-picker"].tap()
        let workflow = app.descendants(matching: .any).matching(
            NSPredicate(format: "identifier BEGINSWITH 'schedule-workflow-option-'")
        ).firstMatch
        guard workflow.waitForExistence(timeout: 10) else {
            throw XCTSkip("The live backend has no saved Graph Workflow available for schedule creation.")
        }
        workflow.tap()

        let save = app.buttons["schedule-save"]
        XCTAssertTrue(save.isEnabled)
        save.tap()
        XCTAssertTrue(app.staticTexts[originalName].waitForExistence(timeout: 20))

        app.buttons["schedule-more-\(originalName)"].tap()
        XCTAssertTrue(app.buttons["schedule-action-edit"].waitForExistence(timeout: 5))
        app.buttons["schedule-action-edit"].tap()
        XCTAssertTrue(app.navigationBars["编辑定时任务"].waitForExistence(timeout: 5))
        let editName = app.textFields["schedule-name"]
        editName.clearAndTypeText(updatedName)
        app.buttons["schedule-save"].tap()
        XCTAssertTrue(app.staticTexts[updatedName].waitForExistence(timeout: 20))

        app.buttons["schedule-more-\(updatedName)"].tap()
        app.buttons["schedule-action-toggle"].tap()
        let updatedRow = app.buttons["schedule-row-\(updatedName)"]
        XCTAssertTrue(updatedRow.waitForExistence(timeout: 15))
        XCTAssertTrue(waitForLabel(updatedRow, toContain: "已停用"))

        app.buttons["schedule-more-\(updatedName)"].tap()
        app.buttons["schedule-action-toggle"].tap()
        XCTAssertTrue(waitForLabel(updatedRow, toContain: "已启用"))

        app.buttons["schedule-more-\(updatedName)"].tap()
        XCTAssertTrue(app.buttons["schedule-action-run"].waitForExistence(timeout: 5))
        app.buttons["schedule-action-run"].tap()
        XCTAssertTrue(app.staticTexts.matching(
            NSPredicate(format: "label BEGINSWITH '已触发'")
        ).firstMatch.waitForExistence(timeout: 30))

        app.buttons["schedule-more-\(updatedName)"].tap()
        app.buttons["schedule-action-delete"].tap()
        XCTAssertTrue(app.buttons["schedule-delete-confirm"].waitForExistence(timeout: 5))
        app.buttons["schedule-delete-confirm"].tap()
        XCTAssertFalse(app.staticTexts[updatedName].waitForExistence(timeout: 15))
    }

    func testCleanupLiveScheduledTaskArtifacts() throws {
        #if !LIVE_SCHEDULE_E2E
            throw XCTSkip("Compile with LIVE_SCHEDULE_E2E to clean real-backend test artifacts.")
        #endif

        app.launch()
        guard app.buttons["main-tab-1"].waitForExistence(timeout: 30) else {
            throw XCTSkip("The device has no reusable Quartet login session.")
        }
        app.buttons["main-tab-1"].tap()
        guard app.navigationBars["定时任务"].waitForExistence(timeout: 10) else { return }
        removeLiveScheduleArtifacts(prefix: "IOS_E2E_SCHEDULE_")
        removeLiveScheduleJobs(prefix: "IOS_E2E_SCHEDULE_")
    }

    private func waitForLabel(_ element: XCUIElement, toContain text: String, timeout: TimeInterval = 15) -> Bool {
        let predicate = NSPredicate(format: "label CONTAINS %@", text)
        return XCTWaiter.wait(for: [XCTNSPredicateExpectation(predicate: predicate, object: element)], timeout: timeout) == .completed
    }

    private func removeLiveScheduleArtifacts(prefix: String) {
        if !app.navigationBars["定时任务"].exists {
            app.terminate()
            app.launch()
            guard app.buttons["main-tab-1"].waitForExistence(timeout: 20) else { return }
            app.buttons["main-tab-1"].tap()
            guard app.navigationBars["定时任务"].waitForExistence(timeout: 10) else { return }
        }

        for _ in 0..<10 {
            let menu = app.buttons.matching(
                NSPredicate(format: "identifier BEGINSWITH %@", "schedule-more-" + prefix)
            ).firstMatch
            guard menu.waitForExistence(timeout: 2) else { break }
            menu.tap()
            guard app.buttons["schedule-action-delete"].waitForExistence(timeout: 3) else { break }
            app.buttons["schedule-action-delete"].tap()
            guard app.buttons["schedule-delete-confirm"].waitForExistence(timeout: 3) else { break }
            app.buttons["schedule-delete-confirm"].tap()
            _ = menu.waitForNonExistence(timeout: 10)
        }
    }

    private func removeLiveScheduleJobs(prefix: String) {
        app.terminate()
        app.launch()
        guard app.buttons["main-tab-0"].waitForExistence(timeout: 20) else { return }
        app.buttons["main-tab-0"].tap()
        let scheduledVisibility = app.buttons["hide-scheduled-jobs-toggle"]
        if scheduledVisibility.waitForExistence(timeout: 5), scheduledVisibility.label.contains("显示定时任务") {
            scheduledVisibility.tap()
        }

        for _ in 0..<10 {
            let actions = app.buttons.matching(
                NSPredicate(format: "label CONTAINS %@ AND label CONTAINS %@", prefix, "任务操作")
            ).firstMatch
            guard actions.waitForExistence(timeout: 2) else { break }
            actions.tap()
            guard app.buttons["job-action-delete"].waitForExistence(timeout: 3) else { break }
            app.buttons["job-action-delete"].tap()
            guard app.buttons["job-delete-confirm"].waitForExistence(timeout: 3) else { break }
            app.buttons["job-delete-confirm"].tap()
            _ = actions.waitForNonExistence(timeout: 10)
        }
        if scheduledVisibility.exists, scheduledVisibility.label.contains("隐藏定时任务") {
            scheduledVisibility.tap()
        }

        if app.buttons["main-tab-1"].exists {
            app.buttons["main-tab-1"].tap()
            _ = app.navigationBars["定时任务"].waitForExistence(timeout: 10)
        }
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

private extension XCUIScreenshot {
    func averageLuminance(inNormalizedRects normalizedRects: [CGRect]) -> CGFloat {
        guard let image = UIImage(data: pngRepresentation)?.cgImage else {
            return 0
        }

        let width = image.width
        let height = image.height
        let bytesPerPixel = 4
        let bytesPerRow = width * bytesPerPixel
        var pixels = [UInt8](repeating: 0, count: height * bytesPerRow)
        guard let context = CGContext(
            data: &pixels,
            width: width,
            height: height,
            bitsPerComponent: 8,
            bytesPerRow: bytesPerRow,
            space: CGColorSpaceCreateDeviceRGB(),
            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
        ) else {
            return 0
        }
        context.draw(image, in: CGRect(x: 0, y: 0, width: width, height: height))

        var luminance: CGFloat = 0
        var sampleCount = 0
        for normalizedRect in normalizedRects {
            let minX = max(0, min(width - 1, Int(CGFloat(width) * normalizedRect.minX)))
            let maxX = max(minX, min(width - 1, Int(CGFloat(width) * normalizedRect.maxX)))
            let minY = max(0, min(height - 1, Int(CGFloat(height) * normalizedRect.minY)))
            let maxY = max(minY, min(height - 1, Int(CGFloat(height) * normalizedRect.maxY)))

            for y in minY...maxY {
                for x in minX...maxX {
                    let offset = y * bytesPerRow + x * bytesPerPixel
                    let red = CGFloat(pixels[offset]) / 255
                    let green = CGFloat(pixels[offset + 1]) / 255
                    let blue = CGFloat(pixels[offset + 2]) / 255
                    luminance += red * 0.2126 + green * 0.7152 + blue * 0.0722
                    sampleCount += 1
                }
            }
        }

        guard sampleCount > 0 else { return 0 }
        return luminance / CGFloat(sampleCount)
    }
}
import XCTest
import UIKit

/// Temporary README screenshot capture. Drives the real app against a live
/// backend supplied through TEST_RUNNER_* environment variables and writes PNGs
/// into the runner's temporary directory, printing every path it wrote.
@MainActor
final class QuartetScreenshotCapture: XCTestCase {
    private let app = XCUIApplication()

    private var outputDirectory: URL {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("quartet-shots", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    private func capture(_ name: String, settle: TimeInterval = 1.6) {
        Thread.sleep(forTimeInterval: settle)
        let shot = XCUIScreen.main.screenshot()
        let url = outputDirectory.appendingPathComponent("\(name).png")
        do {
            try shot.pngRepresentation.write(to: url)
            print("QUARTET_SHOT_WROTE \(url.path)")
        } catch {
            print("QUARTET_SHOT_FAILED \(name) \(error)")
        }
        let attachment = XCTAttachment(screenshot: shot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    func testCaptureReadmeScreenshots() throws {
        let env = ProcessInfo.processInfo.environment
        let server = env["QUARTET_SHOT_SERVER"] ?? "http://127.0.0.1:8090/"
        let user = env["QUARTET_SHOT_USER"] ?? "demo"
        let pass = env["QUARTET_SHOT_PASS"] ?? "QuartetDemo!2026"
        continueAfterFailure = true
        app.launch()

        let serverField = app.textFields["connection-server"]
        if serverField.waitForExistence(timeout: 20) {
            serverField.tap()
            serverField.press(forDuration: 0.9)
            if app.menuItems["全选"].waitForExistence(timeout: 1.2) {
                app.menuItems["全选"].tap()
            } else if app.menuItems["Select All"].waitForExistence(timeout: 1.2) {
                app.menuItems["Select All"].tap()
            }
            serverField.typeText(server)

            let userField = app.textFields["connection-username"]
            userField.tap()
            userField.typeText(user)

            let passField = app.secureTextFields["connection-password"]
            passField.tap()
            passField.typeText(pass)

            capture("ios-connect", settle: 0.8)

            app.buttons["connection-submit"].tap()
            let httpConfirm = app.buttons["继续连接"]
            if httpConfirm.waitForExistence(timeout: 4) {
                httpConfirm.tap()
            }
        }

        XCTAssertTrue(app.buttons["main-tab-0"].waitForExistence(timeout: 90), "dashboard did not load")
        Thread.sleep(forTimeInterval: 4)
        capture("ios-home")

        // Workspace filter sheet.
        if app.buttons["workspace-selector"].exists {
            app.buttons["workspace-selector"].tap()
            capture("ios-workspaces", settle: 1.2)
            if app.buttons["workspace-filter-all"].waitForExistence(timeout: 3) {
                app.buttons["workspace-filter-all"].tap()
            }
            Thread.sleep(forTimeInterval: 1)
        }

        // First interactive chat in the list.
        let jobButtons = app.buttons.matching(
            NSPredicate(format: "identifier BEGINSWITH 'job-' AND NOT identifier BEGINSWITH 'job-time-'")
        )
        var openedChat = false
        for index in 0..<min(jobButtons.count, 6) {
            let job = jobButtons.element(boundBy: index)
            guard job.exists, job.isHittable else { continue }
            job.tap()
            if app.textFields["chat-composer"].waitForExistence(timeout: 12) {
                openedChat = true
                Thread.sleep(forTimeInterval: 3)
                capture("ios-chat")
                app.swipeUp()
                capture("ios-chat-detail", settle: 1.2)
                break
            }
            if app.navigationBars.buttons.count > 0 {
                app.navigationBars.buttons.element(boundBy: 0).tap()
            }
            _ = app.buttons["main-tab-0"].waitForExistence(timeout: 5)
        }
        XCTAssertTrue(openedChat, "no interactive chat could be opened")
        if app.navigationBars.buttons.count > 0 {
            app.navigationBars.buttons.element(boundBy: 0).tap()
        }
        _ = app.buttons["main-tab-0"].waitForExistence(timeout: 8)

        // Graph run: scheduled jobs are hidden by default, so reveal them first.
        let scheduledToggle = app.buttons["hide-scheduled-jobs-toggle"]
        if scheduledToggle.waitForExistence(timeout: 5), scheduledToggle.label.contains("显示定时任务") {
            scheduledToggle.tap()
            Thread.sleep(forTimeInterval: 2)
        }
        let graphJobs = app.buttons.matching(
            NSPredicate(format: "identifier BEGINSWITH 'job-' AND NOT identifier BEGINSWITH 'job-time-'")
        )
        for index in 0..<min(graphJobs.count, 8) {
            let job = graphJobs.element(boundBy: index)
            guard job.exists, job.isHittable, job.label.contains("巡检") else { continue }
            job.tap()
            Thread.sleep(forTimeInterval: 4)
            capture("ios-graph")
            app.swipeUp()
            capture("ios-graph-detail", settle: 1.4)
            if app.navigationBars.buttons.count > 0 {
                app.navigationBars.buttons.element(boundBy: 0).tap()
            }
            _ = app.buttons["main-tab-0"].waitForExistence(timeout: 8)
            break
        }

        // Scheduled tasks.
        app.buttons["main-tab-1"].tap()
        Thread.sleep(forTimeInterval: 3)
        capture("ios-schedules")

        // Usage statistics.
        app.buttons["main-tab-2"].tap()
        Thread.sleep(forTimeInterval: 4)
        capture("ios-stats")
        app.swipeUp()
        capture("ios-stats-detail", settle: 1.4)

        // Settings.
        app.buttons["main-tab-3"].tap()
        Thread.sleep(forTimeInterval: 2.5)
        capture("ios-settings")

        // New conversation composer (a sheet, so capture it last).
        app.buttons["main-tab-0"].tap()
        if app.buttons["new-conversation-button"].waitForExistence(timeout: 6) {
            app.buttons["new-conversation-button"].tap()
            Thread.sleep(forTimeInterval: 3)
            capture("ios-new-task")
            let top = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.10))
            let bottom = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.98))
            top.press(forDuration: 0.05, thenDragTo: bottom)
            _ = app.buttons["main-tab-0"].waitForExistence(timeout: 8)
        }

        print("QUARTET_SHOT_DIR \(outputDirectory.path)")
    }

    /// Short follow-up pass for the root tabs. Assumes the app already holds a
    /// stored connection from a previous capture run.
    func testCaptureTabScreenshots() throws {
        continueAfterFailure = true
        app.launch()

        XCTAssertTrue(app.buttons["main-tab-0"].waitForExistence(timeout: 60), "dashboard did not load")
        Thread.sleep(forTimeInterval: 3)

        app.buttons["main-tab-1"].tap()
        Thread.sleep(forTimeInterval: 3)
        capture("ios-schedules")

        app.buttons["main-tab-2"].tap()
        Thread.sleep(forTimeInterval: 4)
        capture("ios-stats")

        app.buttons["main-tab-3"].tap()
        Thread.sleep(forTimeInterval: 2.5)
        capture("ios-settings")

        print("QUARTET_SHOT_DIR \(outputDirectory.path)")
    }
}
