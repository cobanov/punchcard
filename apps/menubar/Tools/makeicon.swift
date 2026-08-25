#!/usr/bin/env swift
//
// Draws the punchcard app icon.
//
// AppKit rather than a drawing tool: it is already on the machine, every size
// is drawn at full control, and the output is a .iconset folder ready for
// iconutil. No asset to keep in a repo, no export step to forget.
//
//   swift Tools/makeicon.swift build/PunchcardBar.iconset
//
import AppKit

// MARK: - Palette
//
// The same ink and paper as the landing page, so the icon and the site look
// like one product. Warm rather than blue: every other timer is blue.

let paper = NSColor(srgbRed: 0.98, green: 0.97, blue: 0.96, alpha: 1)
let ink = NSColor(srgbRed: 0.11, green: 0.10, blue: 0.09, alpha: 1)
let accent = NSColor(srgbRed: 0.71, green: 0.28, blue: 0.12, alpha: 1)
let accentSoft = NSColor(srgbRed: 0.89, green: 0.51, blue: 0.35, alpha: 1)

/// macOS icons leave transparent padding; on a 1024 canvas the visible plate is
/// about 824px. Break that ratio and the icon looks oversized next to its
/// neighbours in the Dock.
func drawIcon(size: CGFloat) -> NSImage {
    let image = NSImage(size: NSSize(width: size, height: size))
    image.lockFocus()
    defer { image.unlockFocus() }

    let scale = size / 1024
    let inset = 100 * scale
    let plate = NSRect(x: inset, y: inset, width: size - inset * 2, height: size - inset * 2)

    // Plate
    let radius = 180 * scale
    let path = NSBezierPath(roundedRect: plate, xRadius: radius, yRadius: radius)
    let gradient = NSGradient(colors: [ink.blended(withFraction: 0.10, of: .white) ?? ink, ink])!
    gradient.draw(in: path, angle: -90)

    // Punch holes down the left edge — the card the product is named after.
    let holeR = 26 * scale
    let holeX = plate.minX + 92 * scale
    for i in 0..<5 {
        let y = plate.minY + plate.height * (0.22 + 0.14 * CGFloat(i))
        let hole = NSBezierPath(ovalIn: NSRect(x: holeX - holeR, y: y - holeR,
                                               width: holeR * 2, height: holeR * 2))
        paper.withAlphaComponent(0.22).setFill()
        hole.fill()
    }

    // Clock hand sweeping from the centre — the timer half of the idea.
    let centre = NSPoint(x: plate.midX + 40 * scale, y: plate.midY)
    let handR = plate.width * 0.30

    let ring = NSBezierPath()
    ring.appendArc(withCenter: centre, radius: handR, startAngle: 0, endAngle: 360)
    ring.lineWidth = 30 * scale
    accent.withAlphaComponent(0.30).setStroke()
    ring.stroke()

    let sweep = NSBezierPath()
    sweep.appendArc(withCenter: centre, radius: handR, startAngle: 90, endAngle: -35, clockwise: true)
    sweep.lineWidth = 30 * scale
    sweep.lineCapStyle = .round
    accentSoft.setStroke()
    sweep.stroke()

    let hand = NSBezierPath()
    hand.move(to: centre)
    hand.line(to: NSPoint(x: centre.x + handR * 0.62 * cos(-35 * .pi / 180),
                          y: centre.y + handR * 0.62 * sin(-35 * .pi / 180)))
    hand.lineWidth = 34 * scale
    hand.lineCapStyle = .round
    paper.setStroke()
    hand.stroke()

    let hub = NSBezierPath(ovalIn: NSRect(x: centre.x - 26 * scale, y: centre.y - 26 * scale,
                                          width: 52 * scale, height: 52 * scale))
    paper.setFill()
    hub.fill()

    return image
}

// MARK: - Write the iconset

let output = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : "build/PunchcardBar.iconset"
try? FileManager.default.createDirectory(atPath: output, withIntermediateDirectories: true)

// The sizes iconutil expects, each at 1x and 2x.
let sizes: [(name: String, px: CGFloat)] = [
    ("icon_16x16", 16), ("icon_16x16@2x", 32),
    ("icon_32x32", 32), ("icon_32x32@2x", 64),
    ("icon_128x128", 128), ("icon_128x128@2x", 256),
    ("icon_256x256", 256), ("icon_256x256@2x", 512),
    ("icon_512x512", 512), ("icon_512x512@2x", 1024),
]

for (name, px) in sizes {
    let image = drawIcon(size: px)
    guard let tiff = image.tiffRepresentation,
          let rep = NSBitmapImageRep(data: tiff),
          let png = rep.representation(using: .png, properties: [:]) else {
        FileHandle.standardError.write(Data("could not render \(name)\n".utf8))
        exit(1)
    }
    try! png.write(to: URL(fileURLWithPath: "\(output)/\(name).png"))
}

print("wrote \(sizes.count) sizes to \(output)")
