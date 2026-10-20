import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import Quickshell
import qs.Commons
import qs.Widgets
import qs.Services.UI

// Today at a glance: a task timer, the checklist (done / skip with an optional reason) and tasks due today.
Item {
  id: root

  property var pluginApi: null
  readonly property var geometryPlaceholder: panelContainer
  readonly property bool allowAttach: true
  property real contentPreferredWidth: 420 * Style.uiScaleRatio
  property real contentPreferredHeight: Math.min(640 * Style.uiScaleRatio, layout.implicitHeight + Style.marginM * 2)

  readonly property var main: pluginApi?.mainInstance
  readonly property bool online: main?.online ?? false

  // Which row's reason box is open ("c:<item>" or "t:<uuid>"), and its text. Kept here so
  // the 30 s refresh, which rebuilds the rows, doesn't lose what you're typing.
  property string asking: ""
  property string draft: ""
  function ask(key, initial) { draft = initial || ""; asking = key; }

  // Task picked in the timer's dropdown (kept across refreshes).
  property string pickUuid: ""

  anchors.fill: parent
  Component.onCompleted: if (main) main.refresh()

  component ReasonBox: RowLayout {
    id: box
    property string confirmText: "Save"
    property string placeholder: "Why? (optional)"
    signal confirmed(string note)
    Layout.fillWidth: true
    spacing: Style.marginS
    NTextInput {
      id: input
      Layout.fillWidth: true
      placeholderText: box.placeholder
      showClearButton: false
      Component.onCompleted: { text = root.draft; inputItem.forceActiveFocus(); inputItem.selectAll(); }
      onTextChanged: root.draft = text
      onAccepted: box.confirmed(text.trim())
    }
    NButton { text: box.confirmText; fontSize: Style.fontSizeS; onClicked: box.confirmed(input.text.trim()) }
    NIconButton { icon: "x"; baseSize: Style.baseWidgetSize * 0.8; tooltipText: "Cancel"; onClicked: root.asking = "" }
  }

  Rectangle {
    id: panelContainer
    anchors.fill: parent
    color: "transparent"

    ColumnLayout {
      id: layout
      anchors.fill: parent
      anchors.margins: Style.marginM
      spacing: Style.marginM

      // Header
      RowLayout {
        Layout.fillWidth: true
        spacing: Style.marginS
        NIcon { icon: "heartbeat"; color: Color.mPrimary }
        NText { text: "Today"; pointSize: Style.fontSizeL; font.weight: Style.fontWeightBold; color: Color.mOnSurface }
        NText {
          Layout.fillWidth: true
          text: !main ? "" : main.online ? (main.dayComplete ? "· day complete ✓" : "") : (main.serverStarting ? "· starting server…" : main.reachable ? "· not connected" : "· offline")
          pointSize: Style.fontSizeS
          color: main && main.online && main.dayComplete ? Color.mPrimary : Color.mOnSurfaceVariant
        }
        NIconButton { icon: "refresh"; tooltipText: "Refresh"; onClicked: if (main) main.refresh() }
        NIconButton { icon: "external-link"; tooltipText: "Open dashboard"; onClicked: { if (main) main.openDashboard(); if (pluginApi) pluginApi.closePanel(pluginApi.panelOpenScreen); } }
      }

      // Offline
      NBox {
        visible: !root.online
        Layout.fillWidth: true
        implicitHeight: offline.implicitHeight + Style.marginM * 2
        ColumnLayout {
          id: offline
          anchors.fill: parent
          anchors.margins: Style.marginM
          spacing: Style.marginS
          NText {
            Layout.fillWidth: true
            wrapMode: Text.Wrap
            text: !main ? "Loading…"
                  : main.serverStarting ? "Starting the dashboard server (the first start can take a minute while Nix builds it)…"
                  : (main.lastError || "The dashboard isn't running.")
            color: Color.mOnSurfaceVariant
            pointSize: Style.fontSizeS
          }
          NButton {
            visible: main && !main.reachable && !main.serverStarting
            text: "Start server"
            icon: "player-play"
            onClicked: main.startServer()
          }
        }
      }

      NScrollView {
        visible: root.online
        Layout.fillWidth: true
        Layout.fillHeight: true
        Layout.preferredHeight: content.implicitHeight
        horizontalPolicy: ScrollBar.AlwaysOff

        ColumnLayout {
          id: content
          width: parent.width
          spacing: Style.marginXS

          // ---------- timer ----------
          NText {
            text: "Timer"
            pointSize: Style.fontSizeS
            font.weight: Style.fontWeightBold
            color: Color.mOnSurfaceVariant
          }
          // Running: what's being timed, the live clock, and Stop.
          RowLayout {
            visible: main && main.tracking !== null
            Layout.fillWidth: true
            spacing: Style.marginS
            NIcon { icon: "clock-play"; color: Color.mPrimary }
            NText {
              Layout.fillWidth: true
              text: main && main.tracking ? main.tracking.label : ""
              elide: Text.ElideRight
              color: Color.mOnSurface
            }
            NText {
              text: main && main.tracking ? main.fmtDur(main.now - main.tracking.start, true) : ""
              pointSize: Style.fontSizeL
              font.weight: Style.fontWeightBold
              color: Color.mPrimary
            }
            NButton {
              visible: main && main.tracking && main.tracking.uuid !== ""
              text: "Stop"
              icon: "player-stop"
              fontSize: Style.fontSizeS
              onClicked: main.stopTask(main.tracking.uuid)
            }
          }
          // Idle: pick any pending task and start it.
          RowLayout {
            visible: main && main.tracking === null
            Layout.fillWidth: true
            spacing: Style.marginS
            NComboBox {
              Layout.fillWidth: true
              minimumWidth: 260
              baseSize: 0.9
              placeholder: main && main.pendingTasks.length ? "Pick a task to time…" : "No pending tasks"
              model: main ? main.pendingTasks.map(t => ({ key: t.uuid, name: t.project ? `${t.description} · ${t.project}` : t.description })) : []
              currentKey: root.pickUuid
              onSelected: key => root.pickUuid = key
            }
            NButton {
              text: "Start"
              icon: "player-play"
              fontSize: Style.fontSizeS
              enabled: main && main.pendingTasks.some(t => t.uuid === root.pickUuid)
              onClicked: { main.startTask(root.pickUuid); root.pickUuid = ""; }
            }
          }
          NText {
            Layout.fillWidth: true
            wrapMode: Text.Wrap
            text: !main ? ""
                  : main.timeAvailable ? `${main.fmtDur(main.todayMs)} today · ${main.fmtDur(main.weekMs)} this week`
                  : "Timewarrior isn't available, so time isn't being logged. Start the dashboard with nix run."
            pointSize: Style.fontSizeXS
            color: Color.mOnSurfaceVariant
          }

          Item { implicitHeight: Style.marginM }

          // ---------- checklist ----------
          NText {
            text: main && main.checklistItems.length
                  ? `Checklist · ${main.doneCount + main.skipCount} of ${main.checklistItems.length}` + (main.skipCount ? ` (${main.skipCount} skipped)` : "")
                  : "Checklist"
            pointSize: Style.fontSizeS
            font.weight: Style.fontWeightBold
            color: Color.mOnSurfaceVariant
          }
          NText {
            visible: main && !main.checklistItems.length
            text: "No checklist items. Add some on the dashboard."
            pointSize: Style.fontSizeS
            color: Color.mOnSurfaceVariant
          }
          Repeater {
            model: main ? main.checklistItems : []
            delegate: ColumnLayout {
              required property var modelData
              readonly property string key: "c:" + modelData.name
              readonly property bool skipped: modelData.state === "skip"
              Layout.fillWidth: true
              spacing: 0

              RowLayout {
                Layout.fillWidth: true
                NCheckbox {
                  Layout.fillWidth: true
                  label: modelData.name
                  labelSize: Style.fontSizeM
                  checked: modelData.state === "done"
                  opacity: skipped ? 0.6 : 1
                  onToggled: c => main.setCheck(modelData.name, c ? "done" : "", "")
                }
                NButton {
                  text: skipped ? "skipped" : "skip"
                  fontSize: Style.fontSizeXS
                  outlined: !skipped
                  backgroundColor: skipped ? Color.mTertiary : Color.mOnSurfaceVariant
                  textColor: skipped ? Color.mOnTertiary : Color.mOnSurfaceVariant
                  tooltipText: skipped ? "Un-skip" : "Skip today (still counts toward the day)"
                  onClicked: {
                    if (skipped) { main.setCheck(modelData.name, "", ""); if (root.asking === key) root.asking = ""; }
                    else { main.setCheck(modelData.name, "skip", ""); root.ask(key, ""); }
                  }
                }
              }
              // Reason line: click to add or change it.
              NText {
                visible: skipped && root.asking !== key
                Layout.leftMargin: Style.marginXL
                Layout.fillWidth: true
                wrapMode: Text.Wrap
                text: modelData.note ? "↳ " + modelData.note : "+ add a reason"
                pointSize: Style.fontSizeXS
                color: modelData.note ? Color.mTertiary : Color.mOnSurfaceVariant
                MouseArea { anchors.fill: parent; cursorShape: Qt.PointingHandCursor; onClicked: root.ask(key, modelData.note) }
              }
              Loader {
                active: root.asking === key
                visible: active
                Layout.leftMargin: Style.marginXL
                Layout.fillWidth: true
                sourceComponent: ReasonBox {
                  onConfirmed: note => { main.setCheck(modelData.name, "skip", note); root.asking = ""; }
                }
              }
            }
          }

          Item { implicitHeight: Style.marginM }

          // ---------- tasks ----------
          NText {
            text: main && main.dueTasks.length ? `Due today · ${main.dueTasks.length}` : "Due today"
            pointSize: Style.fontSizeS
            font.weight: Style.fontWeightBold
            color: Color.mOnSurfaceVariant
          }
          NText {
            visible: main && !main.dueTasks.length
            text: "Nothing due. 🎉"
            pointSize: Style.fontSizeS
            color: Color.mOnSurfaceVariant
          }
          Repeater {
            model: main ? main.dueTasks : []
            delegate: ColumnLayout {
              required property var modelData
              readonly property string key: "t:" + modelData.uuid
              // Like the dashboard: skip every missed copy, or just today's.
              readonly property var skipUuids: modelData.overdueUuids.length ? modelData.overdueUuids : [modelData.uuid]
              Layout.fillWidth: true
              spacing: 0

              RowLayout {
                Layout.fillWidth: true
                spacing: Style.marginS
                ColumnLayout {
                  Layout.fillWidth: true
                  spacing: 0
                  NText { Layout.fillWidth: true; text: modelData.description; elide: Text.ElideRight; color: Color.mOnSurface }
                  NText {
                    Layout.fillWidth: true
                    visible: text !== ""
                    text: [modelData.project,
                           modelData.overdue ? (modelData.overdueUuids.length > 1 ? `${modelData.overdueUuids.length} missed` : "overdue") : ""]
                          .filter(s => s).join(" · ")
                    pointSize: Style.fontSizeXS
                    color: modelData.overdue ? Color.mError : Color.mOnSurfaceVariant
                  }
                }
                NIconButton {
                  icon: modelData.active ? "player-stop" : "player-play"
                  tooltipText: modelData.active ? "Stop timer" : "Start timer"
                  colorBg: modelData.active ? Color.mPrimary : Color.smartAlpha(Color.mSurfaceVariant)
                  colorFg: modelData.active ? Color.mOnPrimary : Color.mPrimary
                  onClicked: modelData.active ? main.stopTask(modelData.active) : main.startTask(modelData.uuid)
                }
                // Asks how long it took, unless it was timed with Start (that time is already logged).
                NIconButton {
                  icon: "check"
                  tooltipText: "Done"
                  onClicked: modelData.active ? main.completeTask(modelData.uuid)
                             : root.asking === "d:" + modelData.uuid ? root.asking = "" : root.ask("d:" + modelData.uuid, "30")
                }
                NIconButton {
                  icon: "player-skip-forward"
                  tooltipText: skipUuids.length > 1 ? `Skip ${skipUuids.length} missed` : "Skip"
                  onClicked: root.asking === key ? root.asking = "" : root.ask(key, "")
                }
              }
              Loader {
                active: root.asking === key
                visible: active
                Layout.leftMargin: Style.marginXL
                Layout.fillWidth: true
                sourceComponent: ReasonBox {
                  confirmText: skipUuids.length > 1 ? `Skip ${skipUuids.length}` : "Skip"
                  onConfirmed: note => { main.skipTask(skipUuids, note); root.asking = ""; }
                }
              }
              Loader {
                active: root.asking === "d:" + modelData.uuid
                visible: active
                Layout.leftMargin: Style.marginXL
                Layout.fillWidth: true
                sourceComponent: ReasonBox {
                  confirmText: "Done"
                  placeholder: "Took… e.g. 30, 45m, 1h 30m (blank = don't log)"
                  onConfirmed: text => {
                    const m = main.parseMins(text);
                    if (m !== null && !(m >= 1 && m <= 1440))
                      return ToastService.showError("Health Dashboard", "Time taken: use minutes or hours, like 30, 45m, 1h 30m");
                    main.completeTask(modelData.uuid, m);
                    root.asking = "";
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}
