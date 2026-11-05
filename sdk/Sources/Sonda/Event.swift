import Foundation

/// One event on the wire, exactly as in .jerome/notes/ingest-contract.md (version 2).
/// Optional fields that are nil are omitted from the JSON. `environment` is always set by this
/// SDK; it is optional here only so queue lines written by SDK 0.1.0 (production only, no
/// field, which the server stores as production) still load and send.
struct WireEvent: Codable, Sendable, Equatable {
    var eventID: String
    var name: String
    /// Device time, milliseconds since the Unix epoch.
    var ts: Int64
    var userID: String
    var sessionID: String
    var appVersion: String?
    var build: String?
    var osVersion: String?
    var locale: String?
    var region: String?
    var storefront: String?
    var deviceModel: String?
    var deviceType: String?
    var environment: AppEnvironment?
    var props: [String: SondaValue]?

    enum CodingKeys: String, CodingKey {
        case eventID = "event_id"
        case name
        case ts
        case userID = "user_id"
        case sessionID = "session_id"
        case appVersion = "app_version"
        case build
        case osVersion = "os_version"
        case locale
        case region
        case storefront
        case deviceModel = "device_model"
        case deviceType = "device_type"
        case environment
        case props
    }
}

/// Client-side copy of the server rules (D11, D13), so a malformed event is dropped
/// on the device instead of being queued, sent and rejected.
enum EventRules {
    static let maxNameLength = 64
    static let maxPropKeyLength = 64
    static let maxPropsBytes = 2048
    static let maxFieldBytes = 64

    /// `area.action` in snake_case: `^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`.
    static func isValidName(_ name: String) -> Bool {
        guard !name.isEmpty, name.utf8.count <= maxNameLength else { return false }
        let parts = name.split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count == 2 else { return false }
        return parts.allSatisfy { isSnakeIdentifier(Substring($0)) }
    }

    /// `^[a-z][a-z0-9_]*$`, at most 64 characters.
    static func isValidPropKey(_ key: String) -> Bool {
        key.utf8.count <= maxPropKeyLength && isSnakeIdentifier(Substring(key))
    }

    private static func isSnakeIdentifier(_ s: Substring) -> Bool {
        guard let first = s.utf8.first, (UInt8(ascii: "a")...UInt8(ascii: "z")).contains(first) else {
            return false
        }
        return s.utf8.allSatisfy { b in
            (UInt8(ascii: "a")...UInt8(ascii: "z")).contains(b)
                || (UInt8(ascii: "0")...UInt8(ascii: "9")).contains(b)
                || b == UInt8(ascii: "_")
        }
    }

    /// Optional string fields: ≤ 64 bytes, no control characters; nil/empty -> nil.
    static func field(_ value: String?) -> String? {
        guard var v = value, !v.isEmpty else { return nil }
        v = String(v.unicodeScalars.filter { $0.value >= 0x20 && $0.value != 0x7f })
        while v.utf8.count > maxFieldBytes { v.removeLast() }
        return v.isEmpty ? nil : v
    }

    enum PropsError: Error, Equatable {
        case invalidKey(String)
        case nonFiniteNumber(String)
        case tooLarge(Int)
    }

    /// Checks keys, rejects NaN/infinity (not representable in JSON) and the 2 KB limit.
    static func validate(props: [String: SondaValue]) -> PropsError? {
        for (key, value) in props {
            if !isValidPropKey(key) { return .invalidKey(key) }
            if case .double(let d) = value, !d.isFinite { return .nonFiniteNumber(key) }
        }
        let size = serverEncodedSize(of: props)
        return size > maxPropsBytes ? .tooLarge(size) : nil
    }

    /// Size of the props as the server stores them (Go json.Marshal: compact, sorted keys,
    /// `<`, `>`, `&` escaped as \u00XX, U+2028/2029 escaped). Measured on our own compact
    /// encoding plus the extra bytes Go's escaping adds.
    static func serverEncodedSize(of props: [String: SondaValue]) -> Int {
        guard let data = try? JSONCoding.encoder.encode(props) else { return Int.max }
        var size = data.count
        for b in data where b == UInt8(ascii: "<") || b == UInt8(ascii: ">") || b == UInt8(ascii: "&") {
            size += 5
        }
        if let s = String(data: data, encoding: .utf8) {
            for scalar in s.unicodeScalars where scalar.value == 0x2028 || scalar.value == 0x2029 {
                size += 3
            }
        }
        // Control characters: count each as the longest escape (`\u00XX`, 6 bytes), whatever
        // our encoder wrote, so a value full of them cannot pass here and fail on the server.
        for case let .string(value) in props.values {
            for scalar in value.unicodeScalars where scalar.value < 0x20 {
                size += 6 - encodedLength(of: scalar)
            }
        }
        return size
    }

    /// Bytes our encoder writes for one control character inside a string (without quotes).
    private static func encodedLength(of scalar: Unicode.Scalar) -> Int {
        guard let data = try? JSONCoding.encoder.encode(String(Character(scalar))) else { return 6 }
        return data.count - 2
    }
}

enum JSONCoding {
    /// Compact, sorted keys, no escaped slashes: deterministic and close to Go's encoding.
    static var encoder: JSONEncoder {
        let e = JSONEncoder()
        e.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        return e
    }

    static var decoder: JSONDecoder { JSONDecoder() }
}
