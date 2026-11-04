import Foundation

/// An event waiting to be sent: its id, device time and the exact JSON that goes on the wire.
/// The JSON is produced once, when the event is created, so every retry sends the same bytes
/// (and the same `event_id`, which the server uses to skip duplicates).
struct QueuedEvent: Sendable, Equatable {
    let id: String
    let ts: Int64
    let json: Data
}

/// Disk queue: one JSON event per line (JSON Lines) in a single file.
/// Appending a line is cheap and happens on every event; the file is rewritten atomically
/// only when events are removed (sent, dropped, trimmed). A torn last line after a crash is
/// skipped on load. Not thread-safe: owned by the `Engine` actor.
final class EventStore {
    let fileURL: URL
    let maxEvents: Int
    private(set) var events: [QueuedEvent] = []

    init(fileURL: URL, maxEvents: Int) {
        self.fileURL = fileURL
        self.maxEvents = maxEvents
        load()
    }

    var count: Int { events.count }

    func append(_ event: QueuedEvent) {
        events.append(event)
        if events.count > maxEvents {
            // Cap reached: drop the oldest events and rewrite.
            events.removeFirst(events.count - maxEvents)
            persistAll()
            return
        }
        appendLine(event.json)
    }

    func remove(ids: Set<String>) {
        guard !ids.isEmpty else { return }
        let before = events.count
        events.removeAll { ids.contains($0.id) }
        if events.count != before { persistAll() }
    }

    /// Drops events older than `cutoff` (ms). Returns how many were dropped.
    @discardableResult
    func removeOlder(than cutoff: Int64) -> Int {
        let before = events.count
        events.removeAll { $0.ts < cutoff }
        let dropped = before - events.count
        if dropped > 0 { persistAll() }
        return dropped
    }

    // MARK: - File I/O

    private func load() {
        guard let data = try? Data(contentsOf: fileURL) else { return }
        var loaded: [QueuedEvent] = []
        var corrupt = false
        for line in data.split(separator: UInt8(ascii: "\n"), omittingEmptySubsequences: true) {
            let json = Data(line)
            guard let ev = try? JSONCoding.decoder.decode(WireEvent.self, from: json) else {
                corrupt = true
                continue
            }
            loaded.append(QueuedEvent(id: ev.eventID, ts: ev.ts, json: json))
        }
        if loaded.count > maxEvents {
            loaded.removeFirst(loaded.count - maxEvents)
            corrupt = true
        }
        events = loaded
        if corrupt { persistAll() }
    }

    private func appendLine(_ json: Data) {
        var line = json
        line.append(UInt8(ascii: "\n"))
        if FileManager.default.fileExists(atPath: fileURL.path),
           let handle = try? FileHandle(forWritingTo: fileURL) {
            defer { try? handle.close() }
            do {
                try handle.seekToEnd()
                try handle.write(contentsOf: line)
                return
            } catch {
                // fall through to a full rewrite
            }
        }
        persistAll()
    }

    private func persistAll() {
        var data = Data()
        for ev in events {
            data.append(ev.json)
            data.append(UInt8(ascii: "\n"))
        }
        do {
            try FileManager.default.createDirectory(
                at: fileURL.deletingLastPathComponent(), withIntermediateDirectories: true)
            #if os(iOS)
            // Background sends happen while the device may be locked.
            try data.write(to: fileURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
            #else
            try data.write(to: fileURL, options: [.atomic])
            #endif
        } catch {
            Log.sdk.error("queue write failed: \(error.localizedDescription, privacy: .public)")
        }
    }

    /// Default location: Application Support/Sonda/queue.jsonl, excluded from backups.
    static func defaultURL() -> URL {
        let fm = FileManager.default
        let base = (try? fm.url(for: .applicationSupportDirectory, in: .userDomainMask,
                                appropriateFor: nil, create: true))
            ?? fm.temporaryDirectory
        var dir = base
        #if os(macOS)
        // Outside the App Sandbox Application Support is shared by all apps.
        dir = dir.appendingPathComponent(Bundle.main.bundleIdentifier ?? "unknown", isDirectory: true)
        #endif
        dir = dir.appendingPathComponent("Sonda", isDirectory: true)
        try? fm.createDirectory(at: dir, withIntermediateDirectories: true)
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try? dir.setResourceValues(values)
        return dir.appendingPathComponent("queue.jsonl")
    }
}
