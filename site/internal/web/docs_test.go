package web

import (
	"strings"
	"testing"
)

func TestDocsTemplatesRender(t *testing.T) {
	ui := &UI{templates: newTemplateSet("../../web")}
	links := []docLink{
		{Name: "overview.md", Title: "Overview", Note: true, First: true},
		{Name: "ideas.md", Title: "Ideas", Note: true, Open: true, Last: true},
		{Name: "dashboard.md", Title: "Dashboard guide", FirstGuide: true},
	}
	for _, view := range []docsView{
		{Docs: links, Open: "ideas.md", Title: "Ideas", Note: true, HasNotes: true, HasGuides: true, Content: "<p>hi</p>"},
		{Docs: links, Open: "ideas.md", Title: "Ideas", Note: true, HasNotes: true, HasGuides: true, Editing: true, Source: "# Ideas\n<b>"},
		{Docs: links[2:], Open: "dashboard.md", Title: "Dashboard guide", HasGuides: true, Content: "<p>guide</p>"},
	} {
		html, err := ui.renderToHTML("docs", view)
		if err != nil {
			t.Fatalf("%+v: %v", view, err)
		}
		page := string(html)
		switch {
		case view.Editing:
			if !strings.Contains(page, `name="content"`) || !strings.Contains(page, "# Ideas\n&lt;b&gt;") || !strings.Contains(page, "/docs/delete") {
				t.Errorf("editor:\n%s", page)
			}
		case view.Note:
			if !strings.Contains(page, "edit=1") || !strings.Contains(page, "Move down") || strings.Count(page, `class="new-note"`) != 1 {
				t.Errorf("note:\n%s", page)
			}
		default:
			if strings.Contains(page, "edit=1") {
				t.Errorf("a guide shouldn't have Edit:\n%s", page)
			}
		}
	}
}
