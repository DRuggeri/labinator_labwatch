package switchman

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// parsePortStatus extracts port status information from the HTML table
func (m *SwitchManager) parsePortStatus(htmlContent string) (SwitchStatus, error) {
	status := SwitchStatus{}

	// Parse the HTML document
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return status, fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Find the status table (second table with port information)
	tables := m.findTables(doc)
	if len(tables) < 2 {
		return status, fmt.Errorf("expected at least 2 tables, found %d", len(tables))
	}

	// Parse the second table which contains the port status
	statusTable := tables[1]
	rows := m.findTableRows(statusTable)

	// Skip header rows (first 2 rows) and parse data rows
	for i := 2; i < len(rows); i++ {
		cells := m.findTableCells(rows[i])
		if len(cells) < 3 {
			continue // Skip rows without enough cells
		}

		// Extract port number from first cell (e.g., "Port 1")
		portText := m.getTextContent(cells[0])
		portNum := m.extractPortNumber(portText)
		if portNum == 0 {
			continue // Skip if we can't determine port number
		}

		// Extract state from third cell (e.g., "Enabled" or "Disabled")
		stateText := strings.TrimSpace(strings.ToLower(m.getTextContent(cells[2])))
		isEnabled := stateText == "enabled"

		// Set the appropriate field based on port number
		m.setPortStatus(&status, portNum, isEnabled)
	}

	return status, nil
}

// Helper function to find all table elements
func (m *SwitchManager) findTables(n *html.Node) []*html.Node {
	var tables []*html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "table" {
			tables = append(tables, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(n)
	return tables
}

// Helper function to find all tr elements in a table
func (m *SwitchManager) findTableRows(table *html.Node) []*html.Node {
	var rows []*html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			rows = append(rows, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(table)
	return rows
}

// Helper function to find all td/th elements in a row
func (m *SwitchManager) findTableCells(row *html.Node) []*html.Node {
	var cells []*html.Node
	for c := row.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && (c.Data == "td" || c.Data == "th") {
			cells = append(cells, c)
		}
	}
	return cells
}

// Helper function to extract text content from a node
func (m *SwitchManager) getTextContent(n *html.Node) string {
	var text strings.Builder
	var extract func(*html.Node)
	extract = func(n *html.Node) {
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extract(c)
		}
	}
	extract(n)
	return strings.TrimSpace(text.String())
}

// Helper function to extract port number from text like "Port 1"
func (m *SwitchManager) extractPortNumber(text string) int {
	re := regexp.MustCompile(`Port\s+(\d+)`)
	matches := re.FindStringSubmatch(text)
	if len(matches) >= 2 {
		if num, err := strconv.Atoi(matches[1]); err == nil {
			return num
		}
	}
	return 0
}

// Helper function to set port status based on port number
func (m *SwitchManager) setPortStatus(status *SwitchStatus, portNum int, isEnabled bool) {
	switch portNum {
	case 1:
		status.Port1 = isEnabled
	case 2:
		status.Port2 = isEnabled
	case 3:
		status.Port3 = isEnabled
	case 4:
		status.Port4 = isEnabled
	case 5:
		status.Port5 = isEnabled
	case 6:
		status.Port6 = isEnabled
	case 7:
		status.Port7 = isEnabled
	case 8:
		status.Port8 = isEnabled
	}
}
