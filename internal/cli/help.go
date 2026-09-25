package cli

import "strings"

type Help struct {
	Verb string

	// Alias is a short alternate name for the command, such as "a" for "add".
	Alias string

	Summary string

	Examples []Example

	Sections []HelpSection

	SeeAlso []string
}

type HelpSection struct {
	Title string
	Items []string
}

type Example struct {
	Cmd  string
	What string
}

func (h Help) label() string {
	if h.Alias == "" {
		return h.Verb
	}
	return h.Verb + " (" + h.Alias + ")"
}

func (h Help) Render(p Palette) []string {
	lines := []string{p.Bold("nn "+h.label()) + " - " + h.Summary}

	if len(h.Examples) > 0 {
		lines = append(lines, "", p.Dim("Examples"))
		cmdWidth := 0
		for _, e := range h.Examples {
			cmdWidth = max(cmdWidth, DisplayWidth(e.Cmd))
		}

		const indent = "  "
		const gap = 2
		if len(indent)+cmdWidth+gap+minDescription > Width {

			for _, e := range h.Examples {
				lines = append(lines, indent+p.Bold(e.Cmd))
				for _, line := range Wrap(e.What, Width-len(indent)-gap) {
					lines = append(lines, indent+strings.Repeat(" ", gap)+line)
				}
			}
			return h.tail(lines, p)
		}
		for _, e := range h.Examples {
			head := indent + pad(e.Cmd, cmdWidth, p.Bold) + strings.Repeat(" ", gap)

			hang := strings.Repeat(" ", len(indent)+cmdWidth+gap)
			wrapped := Wrap(e.What, Width-DisplayWidth(hang))
			lines = append(lines, strings.TrimRight(head+wrapped[0], " "))
			for _, more := range wrapped[1:] {
				lines = append(lines, hang+more)
			}
		}
	}
	return h.tail(lines, p)
}

const minDescription = 30

func (h Help) tail(lines []string, p Palette) []string {
	for _, section := range h.Sections {
		if strings.TrimSpace(section.Title) == "" || len(section.Items) == 0 {
			continue
		}
		lines = append(lines, "", p.Dim(section.Title))
		lines = appendHelpItems(lines, section.Items)
	}
	if len(h.SeeAlso) > 0 {
		siblings := make([]string, len(h.SeeAlso))
		for i, v := range h.SeeAlso {
			siblings[i] = "nn " + v
		}
		lines = append(lines, "", p.Dim("See also"))
		lines = appendHelpItems(lines, []string{strings.Join(siblings, ", ")})
	}
	return lines
}

func appendHelpItems(lines, items []string) []string {
	const head = "  - "
	hang := strings.Repeat(" ", len(head))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		wrapped := Wrap(item, Width-DisplayWidth(head))
		lines = append(lines, head+wrapped[0])
		for _, more := range wrapped[1:] {
			lines = append(lines, hang+more)
		}
	}
	return lines
}

type IndexGroup struct {
	Title string
	Verbs []Help
}

func RenderIndex(intro string, groups []IndexGroup, footer string, p Palette) []string {
	width := 0
	for _, g := range groups {
		for _, h := range g.Verbs {
			width = max(width, DisplayWidth(h.label()))
		}
	}

	var lines []string
	if intro != "" {
		lines = append(lines, Wrap(intro, Width)...)
	}
	for _, g := range groups {
		if len(g.Verbs) == 0 {
			continue
		}
		lines = append(lines, "", p.Dim(g.Title))
		for _, h := range g.Verbs {
			plain := h.label()
			styled := p.Bold(h.Verb)
			if h.Alias != "" {
				styled += p.Dim(" (" + h.Alias + ")")
			}
			fill := width - DisplayWidth(plain)
			if fill < 0 {
				fill = 0
			}
			head := "  " + styled + strings.Repeat(" ", fill) + "  "
			hang := strings.Repeat(" ", DisplayWidth("  ")+width+2)
			wrapped := Wrap(h.Summary, Width-DisplayWidth(hang))
			lines = append(lines, strings.TrimRight(head+wrapped[0], " "))
			for _, more := range wrapped[1:] {
				lines = append(lines, hang+more)
			}
		}
	}
	if footer != "" {
		lines = append(lines, "")
		lines = append(lines, Wrap(footer, Width)...)
	}
	return lines
}
