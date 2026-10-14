// GoSTS Rig: gosts-rig as a Mac app. It checks what gosts-rig needs (gphoto2
// for a real camera; macOS 14 and Apple silicon for Build Model), starts the
// gosts-rig bundled beside it on a local port, and shows its page in a window.
// Quitting the app stops it (and if the app crashes, gosts-rig sees its stdin
// close and quits by itself: --exit-with-stdin).
//
// Built by build.sh (swiftc, no Xcode project): make, in the repository's root.

import AppKit
import Darwin
import WebKit

let appName = "GoSTS Rig"
let preferredPort = 8081 // gosts-rig's own default (another free one if it's taken)
let repoURL = URL(string: "https://github.com/frifox/gosts/tree/master/cmd/gosts-rig")!

// The places Homebrew (and MacPorts) put tools: an app started from the
// Finder gets only the system's PATH.
let toolDirs = ["/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"]

func searchPath() -> [String] {
    let sys = (ProcessInfo.processInfo.environment["PATH"] ?? "/usr/bin:/bin:/usr/sbin:/sbin").split(separator: ":").map(String.init)
    var seen = Set<String>()
    return (toolDirs + sys).filter { seen.insert($0).inserted }
}

func findTool(_ name: String) -> String? {
    for dir in searchPath() {
        let p = (dir as NSString).appendingPathComponent(name)
        if FileManager.default.isExecutableFile(atPath: p) { return p }
    }
    return nil
}

// MARK: - Dependencies

// A missing dependency: what it's for, and how to get it.
struct Missing {
    let key: String       // for "don't remind me"
    let title: String
    let detail: String
    let command: String?  // a Terminal command that installs it, if there is one
}

func isAppleSilicon() -> Bool {
    var v: Int32 = 0
    var size = MemoryLayout<Int32>.size
    return sysctlbyname("hw.optional.arm64", &v, &size, nil, 0) == 0 && v == 1
}

func checkDependencies() -> [Missing] {
    var out: [Missing] = []
    if findTool("gphoto2") == nil {
        if findTool("brew") != nil {
            out.append(Missing(key: "gphoto2", title: "gphoto2 isn't installed",
                               detail: "It's what talks to a real camera over USB (the simulated camera works without it). Install it with Homebrew, in Terminal:",
                               command: "brew install gphoto2"))
        } else {
            out.append(Missing(key: "gphoto2", title: "gphoto2 isn't installed",
                               detail: "It's what talks to a real camera over USB (the simulated camera works without it). Install Homebrew first (brew.sh), then in Terminal:",
                               command: "brew install gphoto2"))
        }
    }
    let os = ProcessInfo.processInfo.operatingSystemVersion
    if os.majorVersion < 14 {
        out.append(Missing(key: "macos14", title: "Build Model needs macOS 14 or later",
                           detail: "Apple's Object Capture (Build Model) came with macOS 14 Sonoma; this Mac has \(os.majorVersion).\(os.minorVersion). Everything else works.",
                           command: nil))
    }
    if !isAppleSilicon() {
        out.append(Missing(key: "arm64", title: "Build Model needs Apple silicon",
                           detail: "Apple's Object Capture (Build Model) runs only on Macs with Apple silicon (M1 or later). Everything else works.",
                           command: nil))
    }
    return out
}

// MARK: - Ports

// portFree says whether gosts-rig could listen on port: nothing answers
// there, and it can be bound (as gosts-rig binds: SO_REUSEADDR, so the
// connections a gosts-rig just quit left closing, TIME_WAIT, don't count).
func portFree(_ port: Int) -> Bool {
    if answers(port) { return false }
    let fd = socket(AF_INET, SOCK_STREAM, 0)
    if fd < 0 { return false }
    defer { close(fd) }
    var on: Int32 = 1
    setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &on, socklen_t(MemoryLayout<Int32>.size))
    var addr = loopback(port)
    return withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) == 0 }
    }
}

// answers says whether something accepts connections on 127.0.0.1:port.
func answers(_ port: Int) -> Bool {
    let fd = socket(AF_INET, SOCK_STREAM, 0)
    if fd < 0 { return false }
    defer { close(fd) }
    var addr = loopback(port)
    return withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) == 0 }
    }
}

func loopback(_ port: Int) -> sockaddr_in {
    var addr = sockaddr_in()
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_port = in_port_t(UInt16(port).bigEndian)
    addr.sin_addr.s_addr = inet_addr("127.0.0.1")
    return addr
}

func freePort() -> Int {
    let fd = socket(AF_INET, SOCK_STREAM, 0)
    defer { close(fd) }
    var addr = sockaddr_in()
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_port = 0
    addr.sin_addr.s_addr = inet_addr("127.0.0.1")
    _ = withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
    }
    var len = socklen_t(MemoryLayout<sockaddr_in>.size)
    _ = withUnsafeMutablePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(fd, $0, &len) }
    }
    return Int(UInt16(bigEndian: addr.sin_port))
}

// nextFreePort is the first free port after port (one nearby, so it's easy
// to tell apart; any free one if none is).
func nextFreePort(after port: Int) -> Int {
    for p in (port + 1)...(port + 100) where portFree(p) { return p }
    return freePort()
}

// isGostsRig says whether a gosts-rig answers at url (its page's title).
func isGostsRig(_ url: URL, done: @escaping (Bool) -> Void) {
    var req = URLRequest(url: url)
    req.timeoutInterval = 1.5
    URLSession.shared.dataTask(with: req) { data, _, _ in
        let page = data.flatMap { String(data: $0, encoding: .utf8) } ?? ""
        done(page.contains("Photogrammetry Rig"))
    }.resume()
}

// MARK: - The app

final class AppDelegate: NSObject, NSApplicationDelegate, WKNavigationDelegate, WKUIDelegate {
    var window: NSWindow!
    var web: WKWebView!
    var backend: Process?
    var lifeline: Pipe?   // gosts-rig's stdin: closed when the app goes
    var base: URL?        // where gosts-rig's page is
    var quitting = false

    // gosts-rig's output: a log a start (gosts-rig.<start time>.log), the
    // last keepLogs kept (see newLog).
    let logDir = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Logs/\(appName)")
    var logURL: URL?      // this start's
    let keepLogs = 10

    func applicationDidFinishLaunching(_ note: Notification) {
        buildMenu()
        makeWindow()
        showStatus("Starting…")
        NSApp.activate(ignoringOtherApps: true)
        DispatchQueue.main.async { self.launch() }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ app: NSApplication) -> Bool { true }

    // MARK: Window

    func makeWindow() {
        let conf = WKWebViewConfiguration()
        conf.preferences.javaScriptCanOpenWindowsAutomatically = false
        web = WKWebView(frame: .zero, configuration: conf)
        web.navigationDelegate = self
        web.uiDelegate = self
        web.allowsMagnification = true
        if #available(macOS 13.3, *) { web.isInspectable = true }
        web.setValue(false, forKey: "drawsBackground") // (the window's color till the page paints: no white flash)

        window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1440, height: 900),
                          styleMask: [.titled, .closable, .miniaturizable, .resizable],
                          backing: .buffered, defer: false)
        window.title = appName
        window.minSize = NSSize(width: 1000, height: 640)
        window.backgroundColor = NSColor(name: nil) { a in
            a.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua ? NSColor(srgbRed: 0x0f / 255, green: 0x12 / 255, blue: 0x18 / 255, alpha: 1)
                                                                 : NSColor(srgbRed: 0xf4 / 255, green: 0xf5 / 255, blue: 0xf7 / 255, alpha: 1)
        }
        window.contentView = web
        window.center()
        window.setFrameAutosaveName("Main")
        window.tabbingMode = .disallowed
        window.makeKeyAndOrderFront(nil)
    }

    // showStatus shows a message in the window (starting, or what went wrong).
    // showStatus fills the window with a message (and a button: a link to
    // gostsrig:<action>, see the navigation policy).
    func showStatus(_ text: String, detail: String = "", button: (title: String, action: String)? = nil) {
        let esc = { (s: String) in s.replacingOccurrences(of: "&", with: "&amp;").replacingOccurrences(of: "<", with: "&lt;") }
        web.loadHTMLString("""
        <!doctype html><meta charset="utf-8"><style>
        :root { color-scheme: light dark; --bg: #f4f5f7; --text: #1b1f27; --muted: #6b7280; }
        @media (prefers-color-scheme: dark) { :root { --bg: #0f1218; --text: #e6e8ec; --muted: #8b93a3; } }
        html, body { height: 100%; margin: 0; background: var(--bg); color: var(--text); font: 14px -apple-system, sans-serif; }
        body { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px; }
        b { font-size: 17px; font-weight: 600; } p { color: var(--muted); margin: 0; max-width: 520px; text-align: center; white-space: pre-wrap; }
        a.btn { margin-top: 10px; padding: 7px 16px; border-radius: 8px; background: #2f6fed; color: #fff; text-decoration: none; font-weight: 500; }
        a.btn:hover { filter: brightness(1.08); }
        </style><b>\(esc(text))</b><p>\(esc(detail))</p>\(button.map { "<a class=\"btn\" href=\"gostsrig:\($0.action)\">\(esc($0.title))</a>" } ?? "")
        """, baseURL: nil)
    }

    // MARK: Starting gosts-rig

    func launch() {
        let missing = checkDependencies().filter { !UserDefaults.standard.bool(forKey: "skip-\($0.key)") }
        if !missing.isEmpty && !showMissing(missing, atLaunch: true) {
            NSApp.terminate(nil)
            return
        }
        // Its port taken: by a gosts-rig (from Terminal, say, or another copy
        // of the app), or by something else. Asked: quit, use that gosts-rig,
        // or start another on the next free port (two would fight over the
        // rig and the camera: one of them should be on the simulators).
        let usual = URL(string: "http://127.0.0.1:\(preferredPort)/")!
        if portFree(preferredPort) {
            startBackend(port: preferredPort)
            return
        }
        isGostsRig(usual) { yes in
            DispatchQueue.main.async {
                let next = nextFreePort(after: preferredPort)
                let a = NSAlert()
                if yes {
                    a.messageText = "gosts-rig is already running on port \(preferredPort)"
                    a.informativeText = "Another gosts-rig is serving there (started from Terminal, or another GoSTS Rig, perhaps). Connect to it, or start a new one on port \(next): only one of them can use the rig and the camera at a time."
                    a.addButton(withTitle: "Connect to It")
                } else {
                    a.messageText = "Port \(preferredPort) is taken"
                    a.informativeText = "Something other than gosts-rig is using it. Start gosts-rig on port \(next) instead?"
                }
                a.addButton(withTitle: "Start New on \(next)")
                a.addButton(withTitle: "Quit")
                let r = a.runModal()
                let connect = yes && r == .alertFirstButtonReturn
                let start = yes ? r == .alertSecondButtonReturn : r == .alertFirstButtonReturn
                if connect {
                    self.base = usual
                    self.web.load(URLRequest(url: usual))
                } else if start {
                    self.startBackend(port: portFree(next) ? next : nextFreePort(after: next))
                } else {
                    NSApp.terminate(nil)
                }
            }
        }
    }

    func startBackend(port: Int) {
        guard let exe = Bundle.main.url(forAuxiliaryExecutable: "gosts-rig") else {
            showStatus("gosts-rig is missing", detail: "The app is incomplete: build it again (make).")
            return
        }
        let logURL = newLog()
        self.logURL = logURL
        let log = try? FileHandle(forWritingTo: logURL)

        let p = Process()
        p.executableURL = exe
        p.arguments = ["--listen", "127.0.0.1:\(port)", "--exit-with-stdin"]
        // GOSTS_RIG_ARGS: more flags for gosts-rig, space separated (trying
        // the app out on the simulators: "--rig sim --camera sim --config /tmp/sim.toml").
        if let more = ProcessInfo.processInfo.environment["GOSTS_RIG_ARGS"] {
            p.arguments! += more.split(separator: " ").map(String.init)
        }
        var env = ProcessInfo.processInfo.environment
        env["PATH"] = searchPath().joined(separator: ":") // (gphoto2 from Homebrew)
        p.environment = env
        p.currentDirectoryURL = FileManager.default.homeDirectoryForCurrentUser
        let life = Pipe()
        p.standardInput = life
        p.standardOutput = log
        p.standardError = log
        p.terminationHandler = { [weak self] proc in
            DispatchQueue.main.async { self?.backendEnded(proc) }
        }
        do {
            try p.run()
        } catch {
            showStatus("gosts-rig didn't start", detail: error.localizedDescription)
            return
        }
        backend = p
        lifeline = life
        let url = URL(string: "http://127.0.0.1:\(port)/")!
        base = url
        window.title = port == preferredPort ? appName : "\(appName) (port \(port))" // (another beside the usual one: told apart)
        waitForBackend(url, tries: 100)
    }

    // waitForBackend loads the page once gosts-rig answers (about 15 s at most).
    func waitForBackend(_ url: URL, tries: Int) {
        guard backend?.isRunning == true else { return }
        var req = URLRequest(url: url)
        req.timeoutInterval = 1
        URLSession.shared.dataTask(with: req) { _, resp, _ in
            DispatchQueue.main.async {
                if (resp as? HTTPURLResponse)?.statusCode == 200 {
                    self.web.load(URLRequest(url: url))
                } else if tries > 0 {
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.15) { self.waitForBackend(url, tries: tries - 1) }
                } else {
                    self.showStatus("gosts-rig isn't answering", detail: "Help → Show Log has what it said.")
                }
            }
        }.resume()
    }

    func backendEnded(_ p: Process) {
        backend = nil
        lifeline = nil
        if quitting { return }
        if p.terminationReason == .exit && p.terminationStatus == 0 { // (it quit as asked: Settings' Exit Backend)
            showStatus(appName, detail: "gosts-rig isn't running.", button: ("Start GoSTS Rig", "start"))
            return
        }
        let tail = logTail(12)
        showStatus("gosts-rig stopped", detail: tail)
        let a = NSAlert()
        a.alertStyle = .warning
        a.messageText = "gosts-rig stopped (exit status \(p.terminationStatus))"
        a.informativeText = tail.isEmpty ? "It said nothing." : tail
        a.addButton(withTitle: "Restart")
        a.addButton(withTitle: "Show Log")
        a.addButton(withTitle: "Quit")
        switch a.runModal() {
        case .alertFirstButtonReturn: showStatus("Starting…"); launch()
        case .alertSecondButtonReturn: showLog(nil)
        default: NSApp.terminate(nil)
        }
    }

    // newLog makes this start's log, gosts-rig.<start time>.log (local time,
    // e.g. gosts-rig.2026-10-14_12-00-00.log: they sort by name), and deletes
    // all but the last keepLogs. Logs from before (gosts-rig.log, .previous)
    // are named after their times first, to be kept with the rest.
    func newLog() -> URL {
        let fm = FileManager.default
        try? fm.createDirectory(at: logDir, withIntermediateDirectories: true)
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "yyyy-MM-dd_HH-mm-ss"
        let named = { (d: Date, extra: Int) in
            self.logDir.appendingPathComponent("gosts-rig.\(f.string(from: d))\(extra > 0 ? "-\(extra)" : "").log")
        }
        for legacy in ["gosts-rig.previous.log", "gosts-rig.log"] {
            let u = logDir.appendingPathComponent(legacy)
            if let d = (try? fm.attributesOfItem(atPath: u.path))?[.modificationDate] as? Date {
                var to = named(d, 0), k = 0
                while fm.fileExists(atPath: to.path) { k += 1; to = named(d, k) }
                try? fm.moveItem(at: u, to: to)
            }
        }
        var url = named(Date(), 0), k = 0
        while fm.fileExists(atPath: url.path) { k += 1; url = named(Date(), k) } // (two starts in a second)
        fm.createFile(atPath: url.path, contents: nil)
        let logs = ((try? fm.contentsOfDirectory(atPath: logDir.path)) ?? [])
            .filter { $0.hasPrefix("gosts-rig.") && $0.hasSuffix(".log") }
            .sorted()
        for old in logs.dropLast(keepLogs) {
            try? fm.removeItem(at: logDir.appendingPathComponent(old))
        }
        return url
    }

    func logTail(_ n: Int) -> String {
        guard let logURL, let s = try? String(contentsOf: logURL, encoding: .utf8) else { return "" }
        return s.split(separator: "\n").suffix(n).joined(separator: "\n")
    }

    // MARK: Missing dependencies

    // showMissing tells what's missing and how to get it; false: quit.
    @discardableResult
    func showMissing(_ missing: [Missing], atLaunch: Bool) -> Bool {
        let a = NSAlert()
        a.alertStyle = missing.contains { $0.command != nil } ? .warning : .informational
        a.messageText = missing.count == 1 ? missing[0].title : "Some things GoSTS Rig uses are missing"
        a.informativeText = missing.map { m in
            (missing.count == 1 ? "" : "• \(m.title)\n") + m.detail + (m.command.map { "\n\n    \($0)" } ?? "")
        }.joined(separator: "\n\n")
        let commands = missing.compactMap(\.command)
        a.addButton(withTitle: atLaunch ? "Continue" : "OK")
        if !commands.isEmpty { a.addButton(withTitle: "Copy Command") }
        if atLaunch { a.addButton(withTitle: "Quit") }
        if atLaunch {
            a.showsSuppressionButton = true
            a.suppressionButton?.title = "Don't remind me at launch"
        }
        let r = a.runModal()
        if a.suppressionButton?.state == .on {
            for m in missing { UserDefaults.standard.set(true, forKey: "skip-\(m.key)") }
        }
        if !commands.isEmpty && r == .alertSecondButtonReturn {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(commands.joined(separator: " && "), forType: .string)
            return true
        }
        let quit = atLaunch && r == (commands.isEmpty ? .alertSecondButtonReturn : .alertThirdButtonReturn)
        return !quit
    }

    @objc func checkDependenciesNow(_ sender: Any?) {
        for k in ["gphoto2", "macos14", "arm64"] { UserDefaults.standard.removeObject(forKey: "skip-\(k)") }
        let missing = checkDependencies()
        if missing.isEmpty {
            let a = NSAlert()
            a.messageText = "All set"
            a.informativeText = "gphoto2 is installed (\(findTool("gphoto2") ?? "")), and this Mac can run Build Model."
            a.runModal()
        } else {
            showMissing(missing, atLaunch: false)
        }
    }

    // MARK: Quitting

    func applicationShouldTerminate(_ app: NSApplication) -> NSApplication.TerminateReply {
        if quitting || backend == nil { return .terminateNow }
        whenIdle(title: "Quit GoSTS Rig?", action: "Quit", what: "quitting stops it") { ok in
            if !ok {
                app.reply(toApplicationShouldTerminate: false)
                return
            }
            self.stopBackend { app.reply(toApplicationShouldTerminate: true) }
        }
        return .terminateLater
    }

    // Settings… (⌘,): the page's own (its header's gear: the captures' and
    // the settings' folders), so they're the same in a browser.
    @objc func showSettings(_ sender: Any?) {
        window.makeKeyAndOrderFront(nil)
        web.evaluateJavaScript("typeof openAppSettings === 'function' && openAppSettings()", completionHandler: nil)
    }


    // whenIdle calls done(true) at once if no capture or model build is going
    // on, else asks first (done(false): canceled).
    func whenIdle(title: String, action: String, what: String, _ done: @escaping (Bool) -> Void) {
        web.evaluateJavaScript("(typeof state === 'object') && !!(state.cap?.running || state.model?.running)") { v, _ in
            guard (v as? Bool) == true else { done(true); return }
            let a = NSAlert()
            a.alertStyle = .warning
            a.messageText = title
            a.informativeText = "A capture or a model build is still going: \(what)."
            a.addButton(withTitle: action)
            a.addButton(withTitle: "Cancel")
            done(a.runModal() == .alertFirstButtonReturn)
        }
    }

    // stopBackend asks gosts-rig to quit (it lets go of the rig and the
    // camera), and kills it if it hasn't after 5 s.
    func stopBackend(_ done: @escaping () -> Void) {
        quitting = true
        guard let p = backend, p.isRunning else { done(); return }
        p.interrupt()
        DispatchQueue.global().async {
            let deadline = Date().addingTimeInterval(5)
            while p.isRunning && Date() < deadline { usleep(50_000) }
            if p.isRunning { kill(p.processIdentifier, SIGKILL) }
            DispatchQueue.main.async(execute: done)
        }
    }

    // MARK: Web view

    // Links away from gosts-rig's page open in the default browser.
    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        if let url = action.request.url, url.scheme == "gostsrig" { // (showStatus' button)
            decisionHandler(.cancel)
            if url.absoluteString == "gostsrig:start", backend == nil {
                showStatus("Starting…")
                launch()
            }
            return
        }
        if let url = action.request.url, let base, ["http", "https"].contains(url.scheme ?? ""),
           url.host != base.host || url.port != base.port {
            NSWorkspace.shared.open(url)
            decisionHandler(.cancel)
            return
        }
        decisionHandler(.allow)
    }

    func webView(_ webView: WKWebView, createWebViewWith conf: WKWebViewConfiguration, for action: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        if let url = action.request.url { NSWorkspace.shared.open(url) }
        return nil
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) { webView.reload() }

    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
        let a = NSAlert()
        a.messageText = message
        a.beginSheetModal(for: window) { _ in completionHandler() }
    }

    func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (Bool) -> Void) {
        let a = NSAlert()
        a.messageText = message
        a.addButton(withTitle: "OK")
        a.addButton(withTitle: "Cancel")
        a.beginSheetModal(for: window) { completionHandler($0 == .alertFirstButtonReturn) }
    }

    func webView(_ webView: WKWebView, runJavaScriptTextInputPanelWithPrompt prompt: String, defaultText: String?, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (String?) -> Void) {
        let a = NSAlert()
        a.messageText = prompt
        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 280, height: 24))
        field.stringValue = defaultText ?? ""
        a.accessoryView = field
        a.addButton(withTitle: "OK")
        a.addButton(withTitle: "Cancel")
        a.beginSheetModal(for: window) { completionHandler($0 == .alertFirstButtonReturn ? field.stringValue : nil) }
    }

    func webView(_ webView: WKWebView, runOpenPanelWith parameters: WKOpenPanelParameters, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping ([URL]?) -> Void) {
        let p = NSOpenPanel()
        p.allowsMultipleSelection = parameters.allowsMultipleSelection
        p.canChooseDirectories = parameters.allowsDirectories
        p.beginSheetModal(for: window) { completionHandler($0 == .OK ? p.urls : nil) }
    }

    // MARK: Menu

    @objc func reloadPage(_ sender: Any?) {
        if let base, web.url == nil || web.url?.scheme == "about" { web.load(URLRequest(url: base)) } else { web.reload() }
    }
    @objc func actualSize(_ sender: Any?) { web.pageZoom = 1 }
    @objc func zoomIn(_ sender: Any?) { web.pageZoom = min(web.pageZoom + 0.1, 2) }
    @objc func zoomOut(_ sender: Any?) { web.pageZoom = max(web.pageZoom - 0.1, 0.5) }
    @objc func openInBrowser(_ sender: Any?) { if let base { NSWorkspace.shared.open(base) } }
    @objc func showLog(_ sender: Any?) {
        if let logURL, FileManager.default.fileExists(atPath: logURL.path) { NSWorkspace.shared.activateFileViewerSelecting([logURL]) }
        else { NSWorkspace.shared.open(logDir) }
    }
    @objc func openHelp(_ sender: Any?) { NSWorkspace.shared.open(repoURL) }

    func buildMenu() {
        let main = NSMenu()
        func item(_ title: String, _ action: Selector?, _ key: String = "", _ mods: NSEvent.ModifierFlags = .command, target: AnyObject? = nil) -> NSMenuItem {
            let i = NSMenuItem(title: title, action: action, keyEquivalent: key)
            i.keyEquivalentModifierMask = mods
            i.target = target
            return i
        }
        func submenu(_ title: String, _ items: [NSMenuItem]) -> NSMenu {
            let m = NSMenu(title: title)
            items.forEach(m.addItem)
            let top = NSMenuItem(title: title, action: nil, keyEquivalent: "")
            top.submenu = m
            main.addItem(top)
            return m
        }
        _ = submenu(appName, [
            item("About \(appName)", #selector(NSApplication.orderFrontStandardAboutPanel(_:)), ""),
            item("Settings…", #selector(showSettings(_:)), ",", target: self),
            item("Check Dependencies…", #selector(checkDependenciesNow(_:)), "", target: self),
            .separator(),
            item("Hide \(appName)", #selector(NSApplication.hide(_:)), "h"),
            item("Hide Others", #selector(NSApplication.hideOtherApplications(_:)), "h", [.command, .option]),
            item("Show All", #selector(NSApplication.unhideAllApplications(_:))),
            .separator(),
            item("Quit \(appName)", #selector(NSApplication.terminate(_:)), "q"),
        ])
        _ = submenu("Edit", [ // (the page's text fields need these to copy and paste)
            item("Undo", Selector(("undo:")), "z"),
            item("Redo", Selector(("redo:")), "z", [.command, .shift]),
            .separator(),
            item("Cut", #selector(NSText.cut(_:)), "x"),
            item("Copy", #selector(NSText.copy(_:)), "c"),
            item("Paste", #selector(NSText.paste(_:)), "v"),
            item("Select All", #selector(NSText.selectAll(_:)), "a"),
        ])
        _ = submenu("View", [
            item("Reload", #selector(reloadPage(_:)), "r", target: self),
            .separator(),
            item("Actual Size", #selector(actualSize(_:)), "0", target: self),
            item("Zoom In", #selector(zoomIn(_:)), "+", target: self),
            item("Zoom Out", #selector(zoomOut(_:)), "-", target: self),
            .separator(),
            item("Enter Full Screen", #selector(NSWindow.toggleFullScreen(_:)), "f", [.command, .control]),
        ])
        let win = submenu("Window", [
            item("Minimize", #selector(NSWindow.performMiniaturize(_:)), "m"),
            item("Zoom", #selector(NSWindow.performZoom(_:))),
        ])
        NSApp.windowsMenu = win
        _ = submenu("Help", [
            item("Open in Browser", #selector(openInBrowser(_:)), "", target: self),
            item("Show Log", #selector(showLog(_:)), "", target: self),
            .separator(),
            item("\(appName) Help", #selector(openHelp(_:)), "?", target: self),
        ])
        NSApp.mainMenu = main
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.run()
