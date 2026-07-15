import Darwin
import Foundation

enum DiagnosticsUploadFileStore {
    static func createImmutable(
        folderPath: String,
        components: [String],
        data: Data
    ) throws {
        guard !folderPath.isEmpty,
              components.count == 5,
              !data.isEmpty,
              data.count <= DiagnosticsDeterministicCBOR.maximumMessageBytes,
              components.allSatisfy({
                  !$0.isEmpty && $0 != "." && $0 != ".." && !$0.contains("/")
              }) else {
            throw DiagnosticsProtocolError.unsupported
        }
        let root = open(folderPath, O_RDONLY | O_DIRECTORY | O_CLOEXEC | O_NOFOLLOW)
        guard root >= 0 else { throw mappedError(errno) }
        var directories = [root]
        defer { directories.reversed().forEach { close($0) } }

        var parent = root
        for component in components.dropLast() {
            let descriptor = component.withCString {
                openat(parent, $0, O_RDONLY | O_DIRECTORY | O_CLOEXEC | O_NOFOLLOW)
            }
            guard descriptor >= 0 else { throw mappedError(errno) }
            var status = stat()
            guard fstat(descriptor, &status) == 0,
                  (status.st_mode & S_IFMT) == S_IFDIR else {
                close(descriptor)
                throw DiagnosticsProtocolError.conflict
            }
            directories.append(descriptor)
            parent = descriptor
        }

        let filename = components.last!
        let file = filename.withCString {
            openat(
                parent,
                $0,
                O_RDWR | O_CREAT | O_EXCL | O_CLOEXEC | O_NOFOLLOW,
                mode_t(S_IRUSR | S_IWUSR)
            )
        }
        guard file >= 0 else { throw mappedError(errno) }
        var committed = false
        defer {
            close(file)
            if !committed {
                _ = filename.withCString { unlinkat(parent, $0, 0) }
                _ = fsync(parent)
            }
        }

        try data.withUnsafeBytes { buffer in
            guard let base = buffer.baseAddress else {
                throw DiagnosticsProtocolError.invalidMessage
            }
            var offset = 0
            while offset < buffer.count {
                let written = Darwin.write(file, base.advanced(by: offset), buffer.count - offset)
                if written < 0, errno == EINTR { continue }
                guard written > 0 else { throw mappedError(errno) }
                offset += written
            }
        }
        guard fsync(file) == 0 else { throw mappedError(errno) }

        var status = stat()
        guard fstat(file, &status) == 0,
              (status.st_mode & S_IFMT) == S_IFREG,
              status.st_nlink == 1,
              status.st_size == data.count else {
            throw DiagnosticsProtocolError.conflict
        }
        var verified = Data(count: data.count)
        let readCount = verified.withUnsafeMutableBytes { buffer in
            pread(file, buffer.baseAddress, buffer.count, 0)
        }
        guard readCount == data.count, verified == data, fsync(parent) == 0 else {
            throw DiagnosticsProtocolError.conflict
        }
        committed = true
    }

    private static func mappedError(_ code: Int32) -> DiagnosticsProtocolError {
        switch code {
        case EEXIST, ELOOP:
            return .conflict
        case ENOENT, ENOTDIR, EACCES, EPERM:
            return .unavailable
        default:
            return .unsupported
        }
    }
}
