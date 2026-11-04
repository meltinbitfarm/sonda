import Foundation

/// What to do with a batch after a send attempt (table "Responses" in the ingest contract).
enum SendOutcome: Equatable, Sendable {
    /// 200: remove the whole batch (accepted, duplicates and rejected alike).
    case delivered(accepted: Int, duplicates: Int, rejected: Int)
    /// 400, 415 and other permanent 4xx: the batch will never succeed, drop it.
    case dropBatch(status: Int)
    /// 401: keep the queue, stop sending until the next launch.
    case unauthorized
    /// 413: the batch is too large, split it and retry.
    case tooLarge
    /// 429: retry the same batch after the given delay (seconds), or the backoff if nil.
    case rateLimited(retryAfter: TimeInterval?)
    /// 5xx, 408, network errors: retry with exponential backoff.
    case retry
}

enum Transport {
    /// Server limits (contract): ≤ 100 events per request, ≤ 256 KB body compressed or not.
    static let maxBatchEvents = 100
    /// Uncompressed body budget, under the server's 256 KB so the decompressed check passes too.
    static let maxBodyBytes = 250 * 1024

    /// `{"events":[e1,e2,...]}` built from the stored JSON of each event (byte-identical on retry).
    static func body(for events: [QueuedEvent]) -> Data {
        var data = Data(#"{"events":["#.utf8)
        for (i, ev) in events.enumerated() {
            if i > 0 { data.append(UInt8(ascii: ",")) }
            data.append(ev.json)
        }
        data.append(contentsOf: Array("]}".utf8))
        return data
    }

    /// The first events of the queue that fit in one request.
    static func nextBatch(from events: [QueuedEvent], limit: Int) -> [QueuedEvent] {
        var batch: [QueuedEvent] = []
        var size = #"{"events":[]}"#.utf8.count
        for ev in events.prefix(max(1, min(limit, maxBatchEvents))) {
            let added = ev.json.count + (batch.isEmpty ? 0 : 1)
            if !batch.isEmpty && size + added > maxBodyBytes { break }
            batch.append(ev)
            size += added
        }
        return batch
    }

    /// `<endpoint>/v1/events`, unless the endpoint already ends with it.
    static func eventsURL(endpoint: URL) -> URL {
        if endpoint.path.hasSuffix("/v1/events") { return endpoint }
        return endpoint.appendingPathComponent("v1").appendingPathComponent("events")
    }

    static func request(endpoint: URL, ingestKey: String, events: [QueuedEvent]) -> URLRequest {
        var req = URLRequest(url: eventsURL(endpoint: endpoint))
        req.httpMethod = "POST"
        req.timeoutInterval = 30
        req.setValue("Bearer \(ingestKey)", forHTTPHeaderField: "Authorization")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let json = body(for: events)
        if let gz = Gzip.compress(json) {
            req.setValue("gzip", forHTTPHeaderField: "Content-Encoding")
            req.httpBody = gz
        } else {
            req.httpBody = json
        }
        return req
    }

    static func outcome(status: Int, headers: [AnyHashable: Any], body: Data, now: Date) -> SendOutcome {
        switch status {
        case 200..<300:
            let r = try? JSONDecoder().decode(IngestResponse.self, from: body)
            if let errors = r?.errors, !errors.isEmpty {
                for e in errors.prefix(10) {
                    Log.sdk.debug("event \(e.index) rejected: \(e.reason, privacy: .public)")
                }
            }
            return .delivered(accepted: r?.accepted ?? 0, duplicates: r?.duplicates ?? 0,
                              rejected: r?.rejected ?? 0)
        case 401, 403:
            return .unauthorized
        case 413:
            return .tooLarge
        case 429:
            return .rateLimited(retryAfter: retryAfter(headers: headers, now: now))
        case 408:
            return .retry
        case 400..<500:
            return .dropBatch(status: status)
        default:
            return .retry
        }
    }

    /// `Retry-After` as delta-seconds or an HTTP date (RFC 9110).
    static func retryAfter(headers: [AnyHashable: Any], now: Date) -> TimeInterval? {
        let value = headers.first { ($0.key as? String)?.lowercased() == "retry-after" }?.value as? String
        guard let raw = value?.trimmingCharacters(in: .whitespaces), !raw.isEmpty else { return nil }
        if let seconds = TimeInterval(raw), seconds >= 0 { return seconds }
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = TimeZone(identifier: "GMT")
        f.dateFormat = "EEE, dd MMM yyyy HH:mm:ss zzz"
        if let date = f.date(from: raw) { return max(0, date.timeIntervalSince(now)) }
        return nil
    }
}

struct IngestResponse: Decodable {
    struct RejectedEvent: Decodable {
        let index: Int
        let reason: String
    }

    let accepted: Int?
    let duplicates: Int?
    let rejected: Int?
    let errors: [RejectedEvent]?
}
