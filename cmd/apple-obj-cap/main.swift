// apple-obj-cap builds a 3D model from a folder of photos with Apple's Object
// Capture (RealityKit's PhotogrammetrySession), for gosts-rig: every option
// the API has is a flag, and it reports what it does as JSON, one event a
// line on stdout (see emit), so a program can follow it.
//
//	apple-obj-cap <folder> [flags]
//
// The photos (the folder's JPEG, PNG and HEIC files, by name; not its
// subfolders) are fed one by one, numbered from 0 in that order, so each
// event names its photo; with their EXIF, and gravity from gosts-rig's
// gravity.json (Apple Object Capture export) if there's one. The model goes
// to <folder>/model (OBJ, or USDZ with --format usdz), which must be
// empty (Object Capture won't write into a folder with anything in it);
// the rest to <folder>/model-build: poses.json (each photo's camera pose, as
// worked out), bounds.json and pointcloud.ply if asked for, and a
// checkpoint folder, so a run again (another detail level, say) is quicker.
//
// Ctrl-C (or SIGTERM) cancels. Exit status: 0 done, 1 failed, 2 canceled,
// 64 a bad flag.
//
// Build: swiftc -O -parse-as-library -swift-version 5 main.swift -o apple-obj-cap
import CoreMotion
import CoreVideo
import Foundation
import ImageIO
import RealityKit

// MARK: - options

struct Options {
	var folder = URL(fileURLWithPath: ".")
	var out: URL?
	var work: URL?
	var format = "obj" // obj (a folder) | usdz (a file)
	var detail = "full"
	var ordering = "sequential"
	var sensitivity = "normal"
	var masking = true
	var ignoreBoundingBox = false
	var mesh = "triangle"
	var checkpoint: URL?
	var noCheckpoint = false
	var maxPolygons: UInt?
	var textureDim: String?
	var textureMaps: [String]?
	var textureFormat: String?
	var jpegQuality: Float = 0.8
	var gravity: URL?
	var noGravity = false
	var bounds = false
	var pointCloud = false
}

let usage = """
	usage: apple-obj-cap <folder> [flags]

	  --out DIR            where the model goes: an empty folder (default <folder>/model)
	  --work DIR           poses.json, bounds.json, pointcloud.ply, the checkpoint (default <folder>/model-build)
	  --format obj|usdz    OBJ (a folder: mesh, materials, textures) or one USDZ file (default obj)
	  --detail D           preview | reduced | medium | full | raw | custom (default full)
	  --ordering O         sequential (neighboring photos overlap: a rig's rings) | unordered (default sequential)
	  --sensitivity S      normal | high (more features on plain objects; slower) (default normal)
	  --no-masking         keep the background (default: the object masked out of it)
	  --ignore-bbox        build all that's seen, not just the object found
	  --mesh M             triangle | quad (default triangle)
	  --checkpoint DIR     intermediate results, for a quicker run again (default <work>/checkpoint)
	  --no-checkpoint
	  --gravity FILE       gravity per photo: JSON [{"file": "...", "gravity": [x, y, z]}], in the camera's frame
	                       (default <folder>/gravity.json, if there)
	  --no-gravity
	  --bounds             also the object's bounding box (bounds.json)
	  --pointcloud         also a point cloud (pointcloud.ply)

	  with --detail custom:
	  --max-polygons N
	  --texture-dim T      1k | 2k | 4k | 8k | 16k
	  --texture-maps LIST  diffuse,normal,roughness,displacement,ao, or all
	  --texture-format F   png | jpeg
	  --jpeg-quality Q     0…1 (default 0.8)

	Events, one JSON object a line on stdout: start, loaded, invalid, skipped, progress,
	aligned, downsampling, stitchingIncomplete, result, error, cancelled, done.

	"""

func fail(_ msg: String) -> Never {
	FileHandle.standardError.write((msg + "\n\n" + usage).data(using: .utf8)!)
	exit(64)
}

func parse() -> Options {
	var o = Options()
	var args = Array(CommandLine.arguments.dropFirst())
	var folder: String?
	func value(_ flag: String) -> String {
		guard !args.isEmpty else { fail("\(flag) needs a value") }
		return args.removeFirst()
	}
	func oneOf(_ flag: String, _ v: String, _ allowed: [String]) -> String {
		guard allowed.contains(v) else { fail("\(flag): \(v)? one of \(allowed.joined(separator: ", "))") }
		return v
	}
	while !args.isEmpty {
		let a = args.removeFirst()
		switch a {
		case "-h", "--help": print(usage); exit(0)
		case "--out": o.out = URL(fileURLWithPath: value(a), isDirectory: true) // (a folder, though not made yet: else Object Capture takes it for a file)
		case "--work": o.work = URL(fileURLWithPath: value(a), isDirectory: true)
		case "--format": o.format = oneOf(a, value(a), ["obj", "usdz"])
		case "--detail": o.detail = oneOf(a, value(a), ["preview", "reduced", "medium", "full", "raw", "custom"])
		case "--ordering": o.ordering = oneOf(a, value(a), ["sequential", "unordered"])
		case "--sensitivity": o.sensitivity = oneOf(a, value(a), ["normal", "high"])
		case "--no-masking": o.masking = false
		case "--ignore-bbox": o.ignoreBoundingBox = true
		case "--mesh": o.mesh = oneOf(a, value(a), ["triangle", "quad"])
		case "--checkpoint": o.checkpoint = URL(fileURLWithPath: value(a), isDirectory: true)
		case "--no-checkpoint": o.noCheckpoint = true
		case "--gravity": o.gravity = URL(fileURLWithPath: value(a))
		case "--no-gravity": o.noGravity = true
		case "--bounds": o.bounds = true
		case "--pointcloud": o.pointCloud = true
		case "--max-polygons":
			guard let n = UInt(value(a)), n > 0 else { fail("--max-polygons: a whole number above 0") }
			o.maxPolygons = n
		case "--texture-dim": o.textureDim = oneOf(a, value(a), ["1k", "2k", "4k", "8k", "16k"])
		case "--texture-maps":
			let maps = value(a).split(separator: ",").map { String($0).trimmingCharacters(in: .whitespaces) }
			for m in maps { _ = oneOf(a, m, ["diffuse", "normal", "roughness", "displacement", "ao", "all"]) }
			o.textureMaps = maps
		case "--texture-format": o.textureFormat = oneOf(a, value(a), ["png", "jpeg"])
		case "--jpeg-quality":
			guard let q = Float(value(a)), q >= 0, q <= 1 else { fail("--jpeg-quality: 0…1") }
			o.jpegQuality = q
		default:
			if a.hasPrefix("-") { fail("\(a)? not a flag") }
			guard folder == nil else { fail("one folder only") }
			folder = a
		}
	}
	guard let f = folder else { fail("which folder?") }
	o.folder = URL(fileURLWithPath: f).standardizedFileURL
	return o
}

// MARK: - events

let outLock = NSLock()

// emit writes one event: a JSON object on a line of its own.
func emit(_ event: String, _ fields: [String: Any] = [:]) {
	var obj = fields
	obj["event"] = event
	guard let data = try? JSONSerialization.data(withJSONObject: obj, options: [.sortedKeys]) else { return }
	outLock.lock()
	FileHandle.standardOutput.write(data)
	FileHandle.standardOutput.write("\n".data(using: .utf8)!)
	outLock.unlock()
}

func requestName(_ r: PhotogrammetrySession.Request) -> String {
	switch r {
	case .modelFile: return "model"
	case .modelEntity: return "entity"
	case .bounds: return "bounds"
	case .pointCloud: return "pointcloud"
	case .poses: return "poses"
	@unknown default: return "other"
	}
}

func stageName(_ s: PhotogrammetrySession.Output.ProcessingStage) -> String {
	switch s {
	case .preProcessing: return "preProcessing"
	case .imageAlignment: return "imageAlignment"
	case .pointCloudGeneration: return "pointCloudGeneration"
	case .meshGeneration: return "meshGeneration"
	case .textureMapping: return "textureMapping"
	case .optimization: return "optimization"
	@unknown default: return "other"
	}
}

// MARK: - photos

let imageExts: Set<String> = ["jpg", "jpeg", "png", "heic", "heif"]

func photoFiles(in folder: URL) -> [URL] {
	let urls = (try? FileManager.default.contentsOfDirectory(at: folder, includingPropertiesForKeys: [.isRegularFileKey], options: [.skipsHiddenFiles])) ?? []
	return urls.filter { imageExts.contains($0.pathExtension.lowercased()) && ((try? $0.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) ?? false) }
		.sorted { $0.lastPathComponent < $1.lastPathComponent }
}

// loadGravity reads gosts-rig's gravity.json: file name → gravity (g, the
// camera's frame: x right, y up, z towards the viewer).
func loadGravity(_ url: URL) -> [String: CMAcceleration] {
	struct Entry: Decodable { let file: String; let gravity: [Double] }
	guard let data = try? Data(contentsOf: url), let entries = try? JSONDecoder().decode([Entry].self, from: data) else { return [:] }
	var m: [String: CMAcceleration] = [:]
	for e in entries where e.gravity.count == 3 {
		m[e.file] = CMAcceleration(x: e.gravity[0], y: e.gravity[1], z: e.gravity[2])
	}
	return m
}

// sample is photo id: its pixels (32-bit BGRA), EXIF and gravity.
func sample(id: Int, url: URL, gravity: CMAcceleration?) -> PhotogrammetrySample? {
	guard let src = CGImageSourceCreateWithURL(url as CFURL, nil),
		let cg = CGImageSourceCreateImageAtIndex(src, 0, [kCGImageSourceShouldCacheImmediately: true] as CFDictionary)
	else { return nil }
	let w = cg.width, h = cg.height
	var pb: CVPixelBuffer?
	let attrs: [CFString: Any] = [kCVPixelBufferCGImageCompatibilityKey: true, kCVPixelBufferCGBitmapContextCompatibilityKey: true,
		kCVPixelBufferIOSurfacePropertiesKey: [:] as CFDictionary]
	guard CVPixelBufferCreate(kCFAllocatorDefault, w, h, kCVPixelFormatType_32BGRA, attrs as CFDictionary, &pb) == kCVReturnSuccess, let buf = pb else { return nil }
	CVPixelBufferLockBaseAddress(buf, [])
	defer { CVPixelBufferUnlockBaseAddress(buf, []) }
	guard let ctx = CGContext(data: CVPixelBufferGetBaseAddress(buf), width: w, height: h, bitsPerComponent: 8,
		bytesPerRow: CVPixelBufferGetBytesPerRow(buf), space: CGColorSpaceCreateDeviceRGB(),
		bitmapInfo: CGImageAlphaInfo.premultipliedFirst.rawValue | CGBitmapInfo.byteOrder32Little.rawValue)
	else { return nil }
	ctx.draw(cg, in: CGRect(x: 0, y: 0, width: w, height: h))
	var s = PhotogrammetrySample(id: id, image: buf)
	if let props = CGImageSourceCopyPropertiesAtIndex(src, 0, nil) as? [String: Any] {
		s.metadata = props
	}
	if let g = gravity {
		s.gravity = g
	}
	return s
}

// MARK: - results

func writePoses(_ poses: PhotogrammetrySession.Poses, files: [URL], to url: URL) {
	var out: [[String: Any]] = []
	for (id, p) in poses.posesBySample.sorted(by: { $0.key < $1.key }) {
		let q = p.rotation.vector
		out.append(["id": id, "file": id < files.count ? files[id].lastPathComponent : "",
			"translation": [p.translation.x, p.translation.y, p.translation.z],
			"rotation": [q.x, q.y, q.z, q.w]]) // (a quaternion: x, y, z, w)
	}
	if let data = try? JSONSerialization.data(withJSONObject: out, options: [.prettyPrinted, .sortedKeys]) {
		try? data.write(to: url)
	}
}

func writePointCloud(_ pc: PhotogrammetrySession.PointCloud, to url: URL) {
	var s = "ply\nformat ascii 1.0\nelement vertex \(pc.points.count)\n" +
		"property float x\nproperty float y\nproperty float z\nproperty uchar red\nproperty uchar green\nproperty uchar blue\nend_header\n"
	for p in pc.points {
		s += "\(p.position.x) \(p.position.y) \(p.position.z) \(p.color.x) \(p.color.y) \(p.color.z)\n"
	}
	try? s.write(to: url, atomically: true, encoding: .utf8)
}

// MARK: - main

@main
struct Tool {
	static func main() async {
		let o = parse()
		guard PhotogrammetrySession.isSupported else {
			emit("error", ["message": "Object Capture isn't supported on this Mac"])
			exit(1)
		}
		let files = photoFiles(in: o.folder)
		guard !files.isEmpty else {
			emit("error", ["message": "no photos (JPEG, PNG or HEIC) in \(o.folder.path)"])
			exit(1)
		}
		let limit = PhotogrammetrySession.limits.maximumNumberOfInputImages
		guard files.count <= limit else {
			emit("error", ["message": "\(files.count) photos: Object Capture takes up to \(limit)"])
			exit(1)
		}
		let out = o.out ?? o.folder.appendingPathComponent("model", isDirectory: true)
		let work = o.work ?? o.folder.appendingPathComponent("model-build", isDirectory: true)
		try? FileManager.default.createDirectory(at: out, withIntermediateDirectories: true)
		try? FileManager.default.createDirectory(at: work, withIntermediateDirectories: true)
		if let left = try? FileManager.default.contentsOfDirectory(atPath: out.path).filter({ !$0.hasPrefix(".") }), !left.isEmpty {
			emit("error", ["message": "\(out.path) isn't empty: Object Capture writes its model into an empty folder"])
			exit(1)
		}

		var gravity: [String: CMAcceleration] = [:]
		var gravityFile: URL?
		if !o.noGravity {
			let g = o.gravity ?? o.folder.appendingPathComponent("gravity.json")
			if FileManager.default.fileExists(atPath: g.path) {
				gravity = loadGravity(g)
				gravityFile = g
			}
		}

		var cfg = PhotogrammetrySession.Configuration()
		cfg.sampleOrdering = o.ordering == "sequential" ? .sequential : .unordered
		cfg.featureSensitivity = o.sensitivity == "high" ? .high : .normal
		cfg.isObjectMaskingEnabled = o.masking
		cfg.ignoreBoundingBox = o.ignoreBoundingBox
		cfg.meshPrimitive = o.mesh == "quad" ? .quad : .triangle
		if !o.noCheckpoint {
			let cp = o.checkpoint ?? work.appendingPathComponent("checkpoint", isDirectory: true)
			try? FileManager.default.createDirectory(at: cp, withIntermediateDirectories: true)
			cfg.checkpointDirectory = cp
		}
		if o.detail == "custom" {
			var c = PhotogrammetrySession.Configuration.CustomDetailSpecification()
			if let n = o.maxPolygons { c.maximumPolygonCount = n }
			switch o.textureDim {
			case "1k": c.maximumTextureDimension = .oneK
			case "2k": c.maximumTextureDimension = .twoK
			case "4k": c.maximumTextureDimension = .fourK
			case "8k": c.maximumTextureDimension = .eightK
			case "16k": c.maximumTextureDimension = .sixteenK
			default: break
			}
			if let maps = o.textureMaps {
				var m: PhotogrammetrySession.Configuration.CustomDetailSpecification.TextureMapOutputs = []
				for name in maps {
					switch name {
					case "diffuse": m.insert(.diffuseColor)
					case "normal": m.insert(.normal)
					case "roughness": m.insert(.roughness)
					case "displacement": m.insert(.displacement)
					case "ao": m.insert(.ambientOcclusion)
					default: m.insert(.all)
					}
				}
				c.outputTextureMaps = m
			}
			if o.textureFormat == "png" { c.textureFormat = .png }
			if o.textureFormat == "jpeg" { c.textureFormat = .jpeg(compressionQuality: o.jpegQuality) }
			cfg.customDetailSpecification = c
		}

		// The photos, loaded as the session asks for them (all at once
		// wouldn't fit: 24 MP each), each event naming its file.
		var next = 0
		let samples = AnySequence<PhotogrammetrySample> {
			AnyIterator {
				while next < files.count {
					let id = next, url = files[id]
					next += 1
					if let s = sample(id: id, url: url, gravity: gravity[url.lastPathComponent]) {
						emit("loaded", ["id": id, "file": url.lastPathComponent])
						return s
					}
					emit("invalid", ["id": id, "file": url.lastPathComponent, "reason": "couldn't be read"])
				}
				return nil
			}
		}

		let session: PhotogrammetrySession
		do {
			session = try PhotogrammetrySession(input: samples, configuration: cfg)
		} catch {
			emit("error", ["message": "starting: \(error.localizedDescription)"])
			exit(1)
		}

		let detail: PhotogrammetrySession.Request.Detail = [
			"preview": .preview, "reduced": .reduced, "medium": .medium, "full": .full, "raw": .raw, "custom": .custom,
		][o.detail]!
		let modelURL = o.format == "usdz" ? out.appendingPathComponent("model.usdz") : out
		var requests: [PhotogrammetrySession.Request] = [.poses, .modelFile(url: modelURL, detail: detail)]
		if o.bounds { requests.append(.bounds) }
		if o.pointCloud { requests.append(.pointCloud) }

		// Ctrl-C (SIGINT) or SIGTERM: the session canceled, cleanly.
		signal(SIGINT, SIG_IGN)
		signal(SIGTERM, SIG_IGN)
		var sources: [DispatchSourceSignal] = []
		for sig in [SIGINT, SIGTERM] {
			let s = DispatchSource.makeSignalSource(signal: sig, queue: .main)
			s.setEventHandler { session.cancel() }
			s.resume()
			sources.append(s)
		}

		emit("start", ["files": files.map { $0.lastPathComponent }, "out": out.path, "work": work.path, "detail": o.detail, "format": o.format,
			"gravity": gravityFile?.path ?? "", "gravityPhotos": gravity.count])
		do {
			try session.process(requests: requests)
		} catch {
			emit("error", ["message": "processing: \(String(describing: error))"])
			exit(1)
		}

		var failed = false
		do {
			for try await output in session.outputs {
				switch output {
				case .inputComplete:
					emit("inputComplete")
				case .requestProgress(let r, let fraction):
					emit("progress", ["request": requestName(r), "fraction": fraction])
				case .requestProgressInfo(let r, let info):
					var f: [String: Any] = ["request": requestName(r)]
					if let s = info.processingStage { f["stage"] = stageName(s) }
					if let t = info.estimatedRemainingTime { f["remaining"] = t }
					emit("progress", f)
				case .requestComplete(let r, let result):
					switch result {
					case .modelFile(let url):
						emit("result", ["request": requestName(r), "path": url.path])
					case .poses(let poses):
						writePoses(poses, files: files, to: work.appendingPathComponent("poses.json"))
						let ids = poses.posesBySample.keys.sorted()
						emit("aligned", ["ids": ids, "files": ids.map { $0 < files.count ? files[$0].lastPathComponent : "" }])
					case .bounds(let b):
						let j: [String: Any] = ["min": [b.min.x, b.min.y, b.min.z], "max": [b.max.x, b.max.y, b.max.z]]
						if let data = try? JSONSerialization.data(withJSONObject: j, options: [.prettyPrinted]) {
							try? data.write(to: work.appendingPathComponent("bounds.json"))
						}
						emit("result", ["request": "bounds", "min": j["min"]!, "max": j["max"]!])
					case .pointCloud(let pc):
						let url = work.appendingPathComponent("pointcloud.ply")
						writePointCloud(pc, to: url)
						emit("result", ["request": "pointcloud", "path": url.path, "points": pc.points.count])
					case .modelEntity:
						break
					@unknown default:
						break
					}
				case .requestError(let r, let error):
					failed = true
					emit("error", ["request": requestName(r), "message": String(describing: error)])
				case .processingComplete:
					emit("done", ["ok": !failed])
					exit(failed ? 1 : 0)
				case .processingCancelled:
					emit("cancelled")
					exit(2)
				case .invalidSample(let id, let reason):
					emit("invalid", ["id": id, "file": id < files.count ? files[id].lastPathComponent : "", "reason": reason])
				case .skippedSample(let id):
					emit("skipped", ["id": id, "file": id < files.count ? files[id].lastPathComponent : ""])
				case .automaticDownsampling:
					emit("downsampling")
				case .stitchingIncomplete:
					emit("stitchingIncomplete")
				@unknown default:
					break
				}
			}
		} catch {
			emit("error", ["message": error.localizedDescription])
			exit(1)
		}
		_ = sources
		exit(failed ? 1 : 0)
	}
}
