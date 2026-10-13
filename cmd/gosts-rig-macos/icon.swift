// icon.swift OUT.iconset: draws GoSTS Rig's icon (the rig's turntable, an
// object on it and the camera's path round it) at every size an .icns needs.
// build.sh runs it, then iconutil.

import AppKit

let out = CommandLine.arguments[1]
try? FileManager.default.createDirectory(atPath: out, withIntermediateDirectories: true)

func draw(_ px: Int) -> Data {
    let s = CGFloat(px)
    let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: px, pixelsHigh: px, bitsPerSample: 8, samplesPerPixel: 4,
                               hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
    let u = s / 1024 // (drawn on a 1024 grid)

    // The tile: macOS' rounded square, inset as Apple's icons are.
    let tile = NSRect(x: 100 * u, y: 100 * u, width: 824 * u, height: 824 * u)
    let shape = NSBezierPath(roundedRect: tile, xRadius: 185 * u, yRadius: 185 * u)
    NSGraphicsContext.current?.saveGraphicsState()
    let shadow = NSShadow()
    shadow.shadowColor = NSColor.black.withAlphaComponent(0.35)
    shadow.shadowOffset = NSSize(width: 0, height: -10 * u)
    shadow.shadowBlurRadius = 24 * u
    shadow.set()
    NSColor(srgbRed: 0.09, green: 0.11, blue: 0.15, alpha: 1).setFill()
    shape.fill()
    NSGraphicsContext.current?.restoreGraphicsState()
    NSGradient(starting: NSColor(srgbRed: 0.13, green: 0.16, blue: 0.22, alpha: 1),
               ending: NSColor(srgbRed: 0.06, green: 0.07, blue: 0.10, alpha: 1))!.draw(in: shape, angle: -90)

    let c = NSPoint(x: 512 * u, y: 470 * u)
    // The camera's path: a tilted ring of shots round the object.
    let ring = NSRect(x: c.x - 300 * u, y: c.y + 10 * u, width: 600 * u, height: 250 * u)
    let path = NSBezierPath(ovalIn: ring)
    path.lineWidth = 10 * u
    NSColor(srgbRed: 0.36, green: 0.55, blue: 1, alpha: 0.55).setStroke()
    path.setLineDash([2 * u, 34 * u], count: 2, phase: 0)
    path.lineCapStyle = .round
    path.lineWidth = 16 * u
    path.stroke()

    // The turntable.
    let top = NSRect(x: c.x - 230 * u, y: c.y - 150 * u, width: 460 * u, height: 130 * u)
    let side = NSBezierPath()
    side.appendOval(in: top.offsetBy(dx: 0, dy: -40 * u))
    NSColor(srgbRed: 0.20, green: 0.23, blue: 0.30, alpha: 1).setFill()
    side.fill()
    NSRect(x: top.minX, y: top.midY - 40 * u, width: top.width, height: 40 * u).fill()
    NSColor(srgbRed: 0.30, green: 0.34, blue: 0.43, alpha: 1).setFill()
    NSBezierPath(ovalIn: top).fill()

    // The object: a little orange print, a box with a cabin.
    let orange = NSColor(srgbRed: 0.82, green: 0.54, blue: 0.17, alpha: 1)
    let hull = NSBezierPath()
    hull.move(to: NSPoint(x: c.x - 150 * u, y: c.y - 60 * u))
    hull.line(to: NSPoint(x: c.x + 170 * u, y: c.y - 60 * u))
    hull.line(to: NSPoint(x: c.x + 210 * u, y: c.y + 40 * u))
    hull.line(to: NSPoint(x: c.x - 190 * u, y: c.y + 40 * u))
    hull.close()
    orange.setFill()
    hull.fill()
    NSColor(srgbRed: 0.92, green: 0.64, blue: 0.25, alpha: 1).setFill()
    NSBezierPath(roundedRect: NSRect(x: c.x - 110 * u, y: c.y + 40 * u, width: 160 * u, height: 150 * u), xRadius: 14 * u, yRadius: 14 * u).fill()
    NSBezierPath(roundedRect: NSRect(x: c.x - 10 * u, y: c.y + 190 * u, width: 50 * u, height: 70 * u), xRadius: 10 * u, yRadius: 10 * u).fill()
    NSColor(srgbRed: 0.13, green: 0.16, blue: 0.22, alpha: 1).setFill()
    NSBezierPath(roundedRect: NSRect(x: c.x - 75 * u, y: c.y + 75 * u, width: 50 * u, height: 70 * u), xRadius: 22 * u, yRadius: 22 * u).fill()

    // The camera, on its ring at the front right.
    let cam = NSPoint(x: c.x + 250 * u, y: c.y + 70 * u)
    NSColor(srgbRed: 0.36, green: 0.55, blue: 1, alpha: 1).setFill()
    NSBezierPath(roundedRect: NSRect(x: cam.x - 55 * u, y: cam.y - 38 * u, width: 110 * u, height: 76 * u), xRadius: 16 * u, yRadius: 16 * u).fill()
    NSColor(srgbRed: 0.09, green: 0.11, blue: 0.15, alpha: 1).setFill()
    NSBezierPath(ovalIn: NSRect(x: cam.x - 24 * u, y: cam.y - 24 * u, width: 48 * u, height: 48 * u)).fill()

    NSGraphicsContext.restoreGraphicsState()
    return rep.representation(using: .png, properties: [:])!
}

for (pt, scales) in [(16, [1, 2]), (32, [1, 2]), (128, [1, 2]), (256, [1, 2]), (512, [1, 2])] {
    for sc in scales {
        let name = sc == 1 ? "icon_\(pt)x\(pt).png" : "icon_\(pt)x\(pt)@2x.png"
        try! draw(pt * sc).write(to: URL(fileURLWithPath: out).appendingPathComponent(name))
    }
}
