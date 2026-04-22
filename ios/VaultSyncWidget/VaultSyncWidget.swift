import Foundation
import SwiftUI
import WidgetKit

private enum VaultSyncWidgetConstants {
    static let kind = "VaultSyncWidget"
    static let appGroupSuiteName = "group.eu.vaultsync.shared"
    static let snapshotKey = "vaultsync.widget.snapshot"
    static let syncURL = URL(string: "vaultsync://sync")!
}

private enum VaultSyncWidgetL10n {
    static func tr(_ key: String) -> String {
        NSLocalizedString(key, tableName: nil, bundle: .main, value: key, comment: "")
    }

    static func fmt(_ key: String, _ args: CVarArg...) -> String {
        String(format: tr(key), locale: Locale.current, arguments: args)
    }
}

private struct VaultSyncWidgetSnapshot: Codable, Equatable {
    let lastSyncTime: String
    let lastSyncDuration: Double
    let status: String
    let filesSynced: Int
    let folderCount: Int

    static let empty = VaultSyncWidgetSnapshot(
        lastSyncTime: "",
        lastSyncDuration: 0,
        status: "idle",
        filesSynced: 0,
        folderCount: 0
    )

    var lastSyncDate: Date? {
        guard !lastSyncTime.isEmpty else { return nil }
        return VaultSyncWidgetDateFormatters.parseISO8601(lastSyncTime)
    }

    var statusLabel: String {
        switch status {
        case "syncing":
            return VaultSyncWidgetL10n.tr("Syncing")
        case "error":
            return VaultSyncWidgetL10n.tr("Needs Attention")
        default:
            return VaultSyncWidgetL10n.tr("Idle")
        }
    }

    var statusSymbol: String {
        switch status {
        case "syncing":
            return "arrow.triangle.2.circlepath"
        case "error":
            return "exclamationmark.triangle.fill"
        default:
            return "checkmark.circle.fill"
        }
    }

    var statusColor: Color {
        switch status {
        case "syncing":
            return .blue
        case "error":
            return .orange
        default:
            return .green
        }
    }

    var lastSyncDescription: String {
        guard let lastSyncDate else { return VaultSyncWidgetL10n.tr("widget_no_sync_yet") }
        return VaultSyncWidgetDateFormatters.relativeDescription(for: lastSyncDate)
    }
}

private enum VaultSyncWidgetDateFormatters {
    static func parseISO8601(_ value: String) -> Date? {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.date(from: value)
    }

    static func iso8601String(from date: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.string(from: date)
    }

    static func relativeDescription(for date: Date) -> String {
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .short
        return formatter.localizedString(for: date, relativeTo: .now)
    }
}

private struct VaultSyncWidgetEntry: TimelineEntry {
    let date: Date
    let snapshot: VaultSyncWidgetSnapshot
}

private struct VaultSyncWidgetProvider: TimelineProvider {
    func placeholder(in context: Context) -> VaultSyncWidgetEntry {
        VaultSyncWidgetEntry(
            date: .now,
            snapshot: VaultSyncWidgetSnapshot(
                lastSyncTime: VaultSyncWidgetDateFormatters.iso8601String(from: .now.addingTimeInterval(-600)),
                lastSyncDuration: 12,
                status: "idle",
                filesSynced: 24,
                folderCount: 2
            )
        )
    }

    func getSnapshot(in context: Context, completion: @escaping (VaultSyncWidgetEntry) -> Void) {
        completion(VaultSyncWidgetEntry(date: .now, snapshot: loadSnapshot()))
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<VaultSyncWidgetEntry>) -> Void) {
        let entry = VaultSyncWidgetEntry(date: .now, snapshot: loadSnapshot())
        let refreshDate = Date().addingTimeInterval(15 * 60)
        completion(Timeline(entries: [entry], policy: .after(refreshDate)))
    }

    private func loadSnapshot() -> VaultSyncWidgetSnapshot {
        guard let defaults = UserDefaults(suiteName: VaultSyncWidgetConstants.appGroupSuiteName),
              let json = defaults.string(forKey: VaultSyncWidgetConstants.snapshotKey),
              let data = json.data(using: .utf8),
              let snapshot = try? JSONDecoder().decode(VaultSyncWidgetSnapshot.self, from: data) else {
            return .empty
        }
        return snapshot
    }
}

private struct VaultSyncWidgetEntryView: View {
    @Environment(\.widgetFamily) private var family

    let entry: VaultSyncWidgetEntry

    var body: some View {
        switch family {
        case .systemMedium:
            mediumWidget
        case .accessoryRectangular:
            accessoryWidget
        default:
            smallWidget
        }
    }

    private var smallWidget: some View {
        VStack(alignment: .leading, spacing: 10) {
            statusRow
            VStack(alignment: .leading, spacing: 4) {
                Text(VaultSyncWidgetL10n.tr("widget_last_sync"))
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                Text(entry.snapshot.lastSyncDescription)
                    .font(.headline)
                    .lineLimit(1)
                Text(VaultSyncWidgetL10n.fmt("widget_files_synced_format", entry.snapshot.filesSynced))
                    .font(.subheadline.weight(.semibold))
                Text(buttonLabel)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.blue)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 6)
                    .background(.blue.opacity(0.12), in: Capsule())
            }
            Spacer(minLength: 0)
        }
        .widgetURL(VaultSyncWidgetConstants.syncURL)
        .containerBackground(.fill.tertiary, for: .widget)
    }

    private var mediumWidget: some View {
        HStack(spacing: 16) {
            VStack(alignment: .leading, spacing: 10) {
                statusRow
                metric(label: VaultSyncWidgetL10n.tr("widget_last_sync"), value: entry.snapshot.lastSyncDescription)
                metric(label: VaultSyncWidgetL10n.tr("widget_files_synced"), value: "\(entry.snapshot.filesSynced)")
                metric(label: VaultSyncWidgetL10n.tr("widget_vaults"), value: "\(entry.snapshot.folderCount)")
            }

            Spacer(minLength: 0)

            VStack(alignment: .trailing, spacing: 12) {
                if let lastSyncDate = entry.snapshot.lastSyncDate {
                    Text(lastSyncDate, style: .time)
                        .font(.title3.monospacedDigit())
                } else {
                    Text(VaultSyncWidgetL10n.tr("widget_waiting"))
                        .font(.title3.weight(.semibold))
                }

                Text(durationLabel)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.trailing)

                Link(destination: VaultSyncWidgetConstants.syncURL) {
                    Label(buttonLabel, systemImage: "arrow.triangle.2.circlepath")
                        .font(.subheadline.weight(.semibold))
                        .padding(.horizontal, 12)
                        .padding(.vertical, 8)
                        .frame(maxWidth: .infinity)
                        .background(.blue, in: Capsule())
                        .foregroundStyle(.white)
                }
                .buttonStyle(.plain)
            }
            .frame(maxWidth: 110)
        }
        .widgetURL(VaultSyncWidgetConstants.syncURL)
        .containerBackground(.fill.tertiary, for: .widget)
    }

    private var accessoryWidget: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Image(systemName: entry.snapshot.statusSymbol)
                    .foregroundStyle(entry.snapshot.statusColor)
                Text(entry.snapshot.statusLabel)
                    .font(.caption.weight(.semibold))
                    .lineLimit(1)
            }
            Text(entry.snapshot.lastSyncDescription)
                .font(.caption2)
                .lineLimit(1)
            Text(VaultSyncWidgetL10n.fmt("widget_files_tap_to_sync", entry.snapshot.filesSynced))
                .font(.caption2)
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
        .widgetURL(VaultSyncWidgetConstants.syncURL)
        .containerBackground(.fill.tertiary, for: .widget)
    }

    private var statusRow: some View {
        HStack(spacing: 8) {
            Image(systemName: entry.snapshot.statusSymbol)
                .foregroundStyle(entry.snapshot.statusColor)
            Text(entry.snapshot.statusLabel)
                .font(.headline)
                .lineLimit(1)
        }
    }

    private func metric(label: String, value: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label)
                .font(.caption2)
                .foregroundStyle(.secondary)
            Text(value)
                .font(.body.weight(.semibold))
                .lineLimit(1)
        }
    }

    private var buttonLabel: String {
        entry.snapshot.status == "syncing"
            ? VaultSyncWidgetL10n.tr("Open VaultSync")
            : VaultSyncWidgetL10n.tr("widget_sync_now")
    }

    private var durationLabel: String {
        let seconds = Int(entry.snapshot.lastSyncDuration.rounded())
        guard seconds > 0 else { return VaultSyncWidgetL10n.tr("widget_duration_unavailable") }
        return VaultSyncWidgetL10n.fmt("widget_last_run_seconds", seconds)
    }
}

private struct VaultSyncWidgetDefinition: Widget {
    let kind = VaultSyncWidgetConstants.kind

    var body: some WidgetConfiguration {
        StaticConfiguration(kind: kind, provider: VaultSyncWidgetProvider()) { entry in
            VaultSyncWidgetEntryView(entry: entry)
        }
        .configurationDisplayName(VaultSyncWidgetL10n.tr("VaultSync"))
        .description(VaultSyncWidgetL10n.tr("widget_gallery_description"))
        .supportedFamilies([.systemSmall, .systemMedium, .accessoryRectangular])
    }
}

@main
struct VaultSyncWidgetBundle: WidgetBundle {
    var body: some Widget {
        VaultSyncWidgetDefinition()
    }
}

#Preview(as: .systemSmall) {
    VaultSyncWidgetDefinition()
} timeline: {
    VaultSyncWidgetEntry(
        date: .now,
        snapshot: VaultSyncWidgetSnapshot(
            lastSyncTime: VaultSyncWidgetDateFormatters.iso8601String(from: .now.addingTimeInterval(-420)),
            lastSyncDuration: 8,
            status: "idle",
            filesSynced: 17,
            folderCount: 2
        )
    )
}
