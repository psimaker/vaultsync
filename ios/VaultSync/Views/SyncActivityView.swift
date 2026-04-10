import SwiftUI

struct SyncActivityView: View {
    let events: [SyncEventItem]

    var body: some View {
        List {
            if events.isEmpty {
                ContentUnavailableView(
                    "No Activity Yet",
                    systemImage: "clock.arrow.circlepath",
                    description: Text("Recent scan, sync, connection, and error events will appear here.")
                )
            } else {
                ForEach(events) { event in
                    HStack(alignment: .top, spacing: 12) {
                        Image(systemName: event.symbolName)
                            .font(.body.weight(.semibold))
                            .foregroundStyle(event.isError ? .red : .blue)
                            .accessibilityHidden(true)

                        VStack(alignment: .leading, spacing: 3) {
                            Text(event.title)
                                .font(.subheadline.weight(.semibold))
                            if !event.detail.isEmpty {
                                Text(event.detail)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                            Text(event.date, style: .relative)
                                .font(.caption2)
                                .foregroundStyle(.tertiary)
                        }

                        Spacer(minLength: 0)
                    }
                    .padding(.vertical, 2)
                    .accessibilityElement(children: .combine)
                }
            }
        }
        .navigationTitle("Sync Activity")
        .navigationBarTitleDisplayMode(.inline)
    }
}
