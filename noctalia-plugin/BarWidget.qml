import QtQuick
import QtQuick.Layouts
import Quickshell
import qs.Commons
import qs.Widgets
import qs.Services.UI

// Bar pill: heartbeat icon + "done/total" for today's checklist, and how many tasks are due.
// While a task is being timed, a clock with the elapsed time is shown too.
// Left click: panel. Middle click: open the dashboard. Right click: menu.
Item {
  id: root

  property var pluginApi: null
  property ShellScreen screen
  property string widgetId: ""
  property string section: ""
  property int sectionWidgetIndex: -1
  property int sectionWidgetsCount: 0

  readonly property var main: pluginApi?.mainInstance
  readonly property bool online: main?.online ?? false
  readonly property bool showCount: pluginApi?.pluginSettings?.showCount ?? pluginApi?.manifest?.metadata?.defaultSettings?.showCount ?? true

  readonly property bool pillDirection: BarService.getPillDirection(root)
  readonly property string screenName: screen ? screen.name : ""
  readonly property string barPosition: Settings.getBarPositionForScreen(screenName)
  readonly property bool isVertical: barPosition === "left" || barPosition === "right"
  readonly property real capsuleHeight: Style.getCapsuleHeightForScreen(screenName)
  readonly property real barFontSize: Style.getBarFontSizeForScreen(screenName)

  readonly property string label: {
    if (!main || !online || !main.checklistItems.length) return "";
    let s = (main.doneCount + main.skipCount) + "/" + main.checklistItems.length;
    if (main.dueTasks.length) s += " · " + main.dueTasks.length;
    return s;
  }
  readonly property bool showLabel: showCount && !isVertical && label !== ""
  readonly property var tracking: main?.tracking ?? null
  readonly property bool showTimer: tracking !== null && !isVertical
  readonly property string elapsed: tracking ? main.fmtDur(main.now - tracking.start, true) : ""
  readonly property color accent: !online ? Color.mOnSurfaceVariant
                                  : (main && main.dueTasks.some(t => t.overdue)) ? Color.mError
                                  : (main && main.dayComplete) ? Color.mPrimary
                                  : Color.mOnSurface

  implicitWidth: showLabel || showTimer ? row.implicitWidth + Style.marginM * 2 : capsuleHeight
  implicitHeight: capsuleHeight

  Rectangle {
    x: Style.pixelAlignCenter(parent.width, width)
    y: Style.pixelAlignCenter(parent.height, height)
    width: root.implicitWidth
    height: root.capsuleHeight
    color: mouse.containsMouse ? Color.mHover : Style.capsuleColor
    radius: Style.radiusL
    border.color: Style.capsuleBorderColor
    border.width: Style.capsuleBorderWidth

    RowLayout {
      id: row
      anchors.centerIn: parent
      spacing: Style.marginS
      layoutDirection: root.pillDirection ? Qt.LeftToRight : Qt.RightToLeft

      NIcon {
        icon: main?.serverStarting ? "refresh" : (main?.dayComplete ? "circle-check" : "heartbeat")
        applyUiScale: false
        color: mouse.containsMouse ? Color.mOnHover : root.accent
        opacity: root.online ? 1 : 0.5
      }
      NText {
        visible: root.showLabel
        text: root.label
        pointSize: root.barFontSize
        font.weight: Style.fontWeightBold
        color: mouse.containsMouse ? Color.mOnHover : root.accent
      }
      NIcon {
        visible: root.showTimer
        icon: "clock-play"
        applyUiScale: false
        color: mouse.containsMouse ? Color.mOnHover : Color.mPrimary
      }
      NText {
        visible: root.showTimer
        text: root.elapsed
        pointSize: root.barFontSize
        font.weight: Style.fontWeightBold
        color: mouse.containsMouse ? Color.mOnHover : Color.mPrimary
      }
    }
  }

  NPopupContextMenu {
    id: menu
    model: {
      const items = [];
      if (root.tracking && root.tracking.uuid)
        items.push({ "label": "Stop timer · " + root.tracking.label, "action": "stop", "icon": "player-stop" });
      items.push({ "label": "Open dashboard", "action": "open", "icon": "external-link" },
                 { "label": "Refresh", "action": "refresh", "icon": "refresh" });
      if (main && !main.online) items.push({ "label": "Start server", "action": "start", "icon": "player-play" });
      else if (main && main.ownsServer && main.online) items.push({ "label": "Restart server", "action": "restart", "icon": "refresh" });
      items.push({ "label": "Settings", "action": "settings", "icon": "settings" });
      return items;
    }
    onTriggered: action => {
      menu.close();
      PanelService.closeContextMenu(screen);
      if (action === "settings") BarService.openPluginSettings(screen, pluginApi.manifest);
      else if (!main) return;
      else if (action === "stop") { if (root.tracking) main.stopTask(root.tracking.uuid); }
      else if (action === "open") main.openDashboard();
      else if (action === "refresh") main.refresh();
      else if (action === "start") main.startServer();
      else if (action === "restart") main.restartServer();
    }
  }

  MouseArea {
    id: mouse
    anchors.fill: parent
    hoverEnabled: true
    cursorShape: Qt.PointingHandCursor
    acceptedButtons: Qt.LeftButton | Qt.RightButton | Qt.MiddleButton
    onClicked: m => {
      if (m.button === Qt.LeftButton) { if (pluginApi) pluginApi.openPanel(root.screen, root); }
      else if (m.button === Qt.MiddleButton) { if (main) main.openDashboard(); }
      else PanelService.showContextMenu(menu, root, screen);
    }
  }
}
