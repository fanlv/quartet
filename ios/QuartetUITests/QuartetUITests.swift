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
        app.swipeDown()

        let job = app.buttons["job-job-chat-running"]
        XCTAssertTrue(job.waitForExistence(timeout: 2))
        job.swipeLeft()
        XCTAssertTrue(app.buttons["job-swipe-pin-job-chat-running"].waitForExistence(timeout: 2))
        XCTAssertTrue(app.buttons["job-swipe-rename-job-chat-running"].exists)
        XCTAssertTrue(app.buttons["job-swipe-delete-job-chat-running"].exists)
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

        let screenshot = XCUIScreen.main.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = "聊天页底部背景"
        attachment.lifetime = .keepAlways
        add(attachment)

        let bottomLuminance = screenshot.averageLuminance(inNormalizedRects: [
            CGRect(x: 0.05, y: 0.955, width: 0.25, height: 0.03),
            CGRect(x: 0.70, y: 0.955, width: 0.25, height: 0.03)
        ])
        XCTAssertGreaterThan(
            bottomLuminance,
            0.02,
            "聊天页底部安全区不应露出纯黑窗口背景"
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
        XCTAssertTrue(app.staticTexts["按工作区"].exists)
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
            NSPredicate(format: "identifier BEGINSWITH 'job-' AND NOT identifier BEGINSWITH 'job-time-' AND NOT identifier BEGINSWITH 'job-swipe-'")
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
