// Prints the text macOS Vision recognizes in a screenshot, one line per
// observation, so the mobile flow can check what a screen shows.
//
//   swift apps/mobile/e2e/ocr.swift screen.png
import AppKit
import Vision

guard CommandLine.arguments.count == 2,
      let image = NSImage(contentsOfFile: CommandLine.arguments[1]),
      let cg = image.cgImage(forProposedRect: nil, context: nil, hints: nil)
else {
  FileHandle.standardError.write("usage: ocr.swift <image>\n".data(using: .utf8)!)
  exit(2)
}

let request = VNRecognizeTextRequest()
request.recognitionLevel = .accurate
request.recognitionLanguages = ["zh-Hans", "en-US"]
request.usesLanguageCorrection = false
try VNImageRequestHandler(cgImage: cg).perform([request])
for observation in request.results ?? [] {
  if let text = observation.topCandidates(1).first?.string { print(text) }
}
