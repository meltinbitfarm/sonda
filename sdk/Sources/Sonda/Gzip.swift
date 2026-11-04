import Foundation

/// gzip (RFC 1952) without dependencies: Foundation's `.zlib` compression produces a raw
/// DEFLATE stream (RFC 1951); we add the 10-byte gzip header and the CRC-32/size trailer.
enum Gzip {
    static func compress(_ data: Data) -> Data? {
        guard let deflated = try? (data as NSData).compressed(using: .zlib) as Data else { return nil }
        var out = Data(capacity: deflated.count + 18)
        // ID1 ID2, CM=8 (deflate), FLG=0, MTIME=0 (4 bytes), XFL=0, OS=255 (unknown)
        out.append(contentsOf: [0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff])
        out.append(deflated)
        appendLittleEndian(crc32(data), to: &out)
        appendLittleEndian(UInt32(truncatingIfNeeded: data.count), to: &out)
        return out
    }

    /// Inverse of `compress` for streams with no optional header fields. Used by tests.
    static func decompress(_ data: Data) -> Data? {
        guard data.count >= 18, data[data.startIndex] == 0x1f, data[data.startIndex + 1] == 0x8b,
              data[data.startIndex + 3] == 0 else { return nil }
        let body = data.subdata(in: (data.startIndex + 10)..<(data.endIndex - 8))
        guard let inflated = try? (body as NSData).decompressed(using: .zlib) as Data else { return nil }
        let trailer = data.suffix(8)
        let crc = readLittleEndian(trailer.prefix(4))
        let size = readLittleEndian(trailer.suffix(4))
        guard crc == crc32(inflated), size == UInt32(truncatingIfNeeded: inflated.count) else { return nil }
        return inflated
    }

    static func crc32(_ data: Data) -> UInt32 {
        var crc: UInt32 = 0xffff_ffff
        for byte in data {
            crc = table[Int((crc ^ UInt32(byte)) & 0xff)] ^ (crc >> 8)
        }
        return crc ^ 0xffff_ffff
    }

    private static let table: [UInt32] = (0..<256).map { i -> UInt32 in
        var c = UInt32(i)
        for _ in 0..<8 {
            c = (c & 1) != 0 ? (0xedb8_8320 ^ (c >> 1)) : (c >> 1)
        }
        return c
    }

    private static func appendLittleEndian(_ value: UInt32, to data: inout Data) {
        for shift in stride(from: 0, to: 32, by: 8) {
            data.append(UInt8((value >> UInt32(shift)) & 0xff))
        }
    }

    private static func readLittleEndian(_ bytes: Data) -> UInt32 {
        var v: UInt32 = 0
        for (i, b) in bytes.enumerated() { v |= UInt32(b) << UInt32(8 * i) }
        return v
    }
}
