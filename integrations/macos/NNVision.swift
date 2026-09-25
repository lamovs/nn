import AppKit
import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers
import Vision

// Exit codes: 0 ok, 1 failure, 2 usage, 3 no image on the clipboard.
private let exitFailure: Int32 = 1
private let exitUsage: Int32 = 2
private let exitNoImage: Int32 = 3

private let usage = """
usage: nn-vision ocr IMAGE [--langs ru-RU,en-US] [--language-correction]
       nn-vision langs
       nn-vision clipboard-image OUT.png
       nn-vision screen-access
"""

private struct HelperError: Error {
    let message: String
    let code: Int32

    init(_ message: String, code: Int32 = exitFailure) {
        self.message = message
        self.code = code
    }
}

private struct OCRBox: Encodable {
    let x: Double
    let y: Double
    let w: Double
    let h: Double
}

private struct OCRLine: Encodable {
    let text: String
    let box: OCRBox
    let confidence: Double
}

private struct OCRResult: Encodable {
    let engine: String
    let langs: [String]
    let width: Int
    let height: Int
    let lines: [OCRLine]
}

private func fail(_ error: HelperError) -> Never {
    FileHandle.standardError.write(Data("nn-vision: \(error.message)\n".utf8))
    exit(error.code)
}

private func parseLangs(_ value: String) -> [String] {
    value.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
}

private func newRequest() -> VNRecognizeTextRequest {
    let request = VNRecognizeTextRequest()
    request.recognitionLevel = .accurate
    return request
}

private func supportedLangs() throws -> [String] {
    try newRequest().supportedRecognitionLanguages()
}

private func loadImage(_ path: String) throws -> (CGImage, CGImagePropertyOrientation, Int, Int) {
    let url = URL(fileURLWithPath: path)
    guard FileManager.default.fileExists(atPath: url.path) else {
        throw HelperError("\(path): no such file")
    }
    guard let source = CGImageSourceCreateWithURL(url as CFURL, nil),
          let image = CGImageSourceCreateImageAtIndex(source, 0, nil) else {
        throw HelperError("\(path): not a readable image")
    }
    var orientation = CGImagePropertyOrientation.up
    if let props = CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any],
       let raw = props[kCGImagePropertyOrientation] as? UInt32,
       let parsed = CGImagePropertyOrientation(rawValue: raw) {
        orientation = parsed
    }
    var width = image.width
    var height = image.height
    switch orientation {
    case .left, .leftMirrored, .right, .rightMirrored:
        swap(&width, &height)
    default:
        break
    }
    return (image, orientation, width, height)
}

private func recognize(path: String, langs: [String], languageCorrection: Bool) throws -> OCRResult {
    let (image, orientation, width, height) = try loadImage(path)

    let request = newRequest()
    request.usesLanguageCorrection = languageCorrection
    if !langs.isEmpty {
        let supported = try request.supportedRecognitionLanguages()
        let unsupported = langs.filter { !supported.contains($0) }
        if !unsupported.isEmpty {
            throw HelperError("unsupported languages: \(unsupported.joined(separator: ", ")) (supported: \(supported.joined(separator: ", ")))")
        }
        request.recognitionLanguages = langs
        request.automaticallyDetectsLanguage = false
    }

    let handler = VNImageRequestHandler(cgImage: image, orientation: orientation, options: [:])
    try handler.perform([request])

    var lines: [OCRLine] = []
    for observation in request.results ?? [] {
        guard let candidate = observation.topCandidates(1).first else {
            continue
        }
        let text = candidate.string.trimmingCharacters(in: .whitespacesAndNewlines)
        if text.isEmpty {
            continue
        }
        // Vision boxes are normalized with the origin at the bottom left.
        let bb = observation.boundingBox
        let box = OCRBox(
            x: clamp(Double(bb.minX)),
            y: clamp(1 - Double(bb.maxY)),
            w: clamp(Double(bb.width)),
            h: clamp(Double(bb.height))
        )
        lines.append(OCRLine(text: text, box: box, confidence: Double(candidate.confidence)))
    }
    return OCRResult(
        engine: "vision",
        langs: langs.isEmpty ? request.recognitionLanguages : langs,
        width: width,
        height: height,
        lines: lines
    )
}

private func clamp(_ value: Double) -> Double {
    min(max(value, 0), 1)
}

private func runOCR(_ arguments: [String]) throws {
    var path: String?
    var langs: [String] = []
    var languageCorrection = false
    var index = 0
    while index < arguments.count {
        let argument = arguments[index]
        switch argument {
        case "--langs":
            index += 1
            guard index < arguments.count else {
                throw HelperError("--langs needs a value\n\(usage)", code: exitUsage)
            }
            langs = parseLangs(arguments[index])
        case "--language-correction":
            languageCorrection = true
        case "--":
            if index + 1 < arguments.count, path == nil {
                path = arguments[index + 1]
                index += 1
            }
        default:
            if argument.hasPrefix("--langs=") {
                langs = parseLangs(String(argument.dropFirst("--langs=".count)))
            } else if argument.hasPrefix("-") && argument != "-" {
                throw HelperError("unknown flag \(argument)\n\(usage)", code: exitUsage)
            } else if path == nil {
                path = argument
            } else {
                throw HelperError("unexpected argument \(argument)\n\(usage)", code: exitUsage)
            }
        }
        index += 1
    }
    guard let path else {
        throw HelperError("ocr needs an image path\n\(usage)", code: exitUsage)
    }

    let result = try recognize(path: path, langs: langs, languageCorrection: languageCorrection)
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.withoutEscapingSlashes]
    var data = try encoder.encode(result)
    data.append(0x0A)
    FileHandle.standardOutput.write(data)
}

private func pngData(fromImageAt url: URL) -> Data? {
    guard let source = CGImageSourceCreateWithURL(url as CFURL, nil),
          let type = CGImageSourceGetType(source) else {
        return nil
    }
    if (type as String) == UTType.png.identifier {
        return try? Data(contentsOf: url)
    }
    guard let image = CGImageSourceCreateImageAtIndex(source, 0, nil) else {
        return nil
    }
    return NSBitmapImageRep(cgImage: image).representation(using: .png, properties: [:])
}

private func pngData(from pasteboard: NSPasteboard) -> Data? {
    let types = pasteboard.types ?? []

    // A file copied in Finder carries its icon as TIFF; prefer the file itself.
    if types.contains(.fileURL),
       let urls = pasteboard.readObjects(forClasses: [NSURL.self], options: [
           .urlReadingFileURLsOnly: true,
           .urlReadingContentsConformToTypes: [UTType.image.identifier],
       ]) as? [URL],
       let url = urls.first,
       let data = pngData(fromImageAt: url) {
        return data
    }
    if types.contains(.png), let data = pasteboard.data(forType: .png) {
        return data
    }
    guard NSImage.canInit(with: pasteboard),
          let image = NSImage(pasteboard: pasteboard),
          let tiff = image.tiffRepresentation,
          let rep = NSBitmapImageRep(data: tiff) else {
        return nil
    }
    return rep.representation(using: .png, properties: [:])
}

private func runClipboardImage(_ arguments: [String]) throws {
    guard arguments.count == 1, !arguments[0].isEmpty else {
        throw HelperError("clipboard-image needs an output path\n\(usage)", code: exitUsage)
    }
    guard let data = pngData(from: NSPasteboard.general) else {
        throw HelperError("no image on the clipboard", code: exitNoImage)
    }
    do {
        try data.write(to: URL(fileURLWithPath: arguments[0]), options: .atomic)
    } catch {
        throw HelperError("write \(arguments[0]): \(error.localizedDescription)")
    }
}

private func runScreenAccess(_ arguments: [String]) throws {
    guard arguments.isEmpty else {
        throw HelperError("screen-access takes no arguments\n\(usage)", code: exitUsage)
    }
    print(CGPreflightScreenCaptureAccess() ? "granted" : "denied")
}

private func runLangs(_ arguments: [String]) throws {
    guard arguments.isEmpty else {
        throw HelperError("langs takes no arguments\n\(usage)", code: exitUsage)
    }
    for lang in try supportedLangs() {
        print(lang)
    }
}

@main
private enum NNVision {
    static func main() {
        let arguments = Array(CommandLine.arguments.dropFirst())
        guard let command = arguments.first else {
            fail(HelperError(usage, code: exitUsage))
        }
        let rest = Array(arguments.dropFirst())
        do {
            switch command {
            case "ocr":
                try runOCR(rest)
            case "langs":
                try runLangs(rest)
            case "clipboard-image":
                try runClipboardImage(rest)
            case "screen-access":
                try runScreenAccess(rest)
            case "-h", "--help", "help":
                print(usage)
            default:
                throw HelperError("unknown command \(command)\n\(usage)", code: exitUsage)
            }
        } catch let error as HelperError {
            fail(error)
        } catch {
            fail(HelperError(error.localizedDescription))
        }
    }
}
