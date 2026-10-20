import QtQuick
import QtQuick.Layouts
import Quickshell
import qs.Commons
import qs.Widgets

ColumnLayout {
  id: root
  spacing: Style.marginL

  property var pluginApi: null
  readonly property var defaults: pluginApi?.manifest?.metadata?.defaultSettings || ({})
  function get(k) { return pluginApi?.pluginSettings?.[k] ?? defaults[k]; }

  property string editUrl: get("url")
  property string editSiteDir: get("siteDir")
  property string editOpenCommand: get("openCommand")
  property bool editAutoStart: get("autoStart")
  property bool editShowCount: get("showCount")

  // Called by Noctalia's settings dialog.
  function saveSettings() {
    if (!pluginApi) return;
    pluginApi.pluginSettings.url = editUrl.trim() || defaults.url;
    pluginApi.pluginSettings.siteDir = editSiteDir.trim();
    pluginApi.pluginSettings.openCommand = editOpenCommand.trim() || defaults.openCommand;
    pluginApi.pluginSettings.autoStart = editAutoStart;
    pluginApi.pluginSettings.showCount = editShowCount;
    pluginApi.saveSettings();
    pluginApi.mainInstance?.refresh();
  }

  NTextInput {
    Layout.fillWidth: true
    label: "Dashboard URL"
    description: "Where the dashboard server listens. The port is also used when starting it."
    text: root.editUrl
    defaultValue: root.defaults.url
    onTextChanged: root.editUrl = text
  }
  NTextInput {
    Layout.fillWidth: true
    label: "Site folder"
    description: "The folder with flake.nix and server.py, where the server is started with `nix run`. Leave empty to use the site/ folder next to this plugin."
    text: root.editSiteDir
    defaultValue: root.defaults.siteDir
    onTextChanged: root.editSiteDir = text
  }
  NTextInput {
    Layout.fillWidth: true
    label: "Open command"
    description: "How to open the dashboard. {url} is replaced with the URL. Use `xdg-open {url}` for a normal browser tab."
    text: root.editOpenCommand
    defaultValue: root.defaults.openCommand
    onTextChanged: root.editOpenCommand = text
  }
  NToggle {
    label: "Start the server automatically"
    description: "If the dashboard isn't reachable, run it in the background. It stops when the shell exits."
    checked: root.editAutoStart
    defaultValue: root.defaults.autoStart
    onToggled: c => root.editAutoStart = c
  }
  NToggle {
    label: "Show counts on the bar"
    description: "Checklist progress (done/total) and the number of tasks due today."
    checked: root.editShowCount
    defaultValue: root.defaults.showCount
    onToggled: c => root.editShowCount = c
  }
}
