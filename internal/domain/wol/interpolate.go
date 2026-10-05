package wol

import (
	"fmt"
	"net"
	"strings"
	"text/template"
	"time"
)

// Vars are the values an action may interpolate; anything else is rejected (§4.3).
// Both exec commands (§4.3) and outbound HTTP actions (§18.2) render their templates
// with these values.
type Vars struct {
	Action    string
	SrcIP     string
	SrcPort   int
	DstPort   int
	Interface string
	MAC       string
	Time      string
	// Arg holds the validated arguments of a remote command ({{.Arg.<name>}}, §21).
	Arg map[string]string
}

// EventVars collects the interpolation values of one trigger.
func EventVars(action Action, ev Event) Vars {
	return Vars{
		Action:    string(action),
		SrcIP:     ipString(ev.SrcIP),
		SrcPort:   ev.SrcPort,
		DstPort:   ev.DstPort,
		Interface: ev.Interface,
		MAC:       ev.TargetMAC.String(),
		Time:      time.Now().Format(time.RFC3339),
		Arg:       ev.Args,
	}
}

// Interpolate renders one template with the whitelisted values.
func (v Vars) Interpolate(text string) (string, error) {
	tmpl, err := template.New("value").Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("invalid template %q: %w", text, err)
	}

	var buf strings.Builder

	if err := tmpl.Execute(&buf, v); err != nil {
		return "", fmt.Errorf("interpolate %q: %w", text, err)
	}

	return buf.String(), nil
}

// InterpolateAll renders a list of templates.
func (v Vars) InterpolateAll(templates []string) ([]string, error) {
	out := make([]string, 0, len(templates))

	for _, text := range templates {
		rendered, err := v.Interpolate(text)
		if err != nil {
			return nil, err
		}

		out = append(out, rendered)
	}

	return out, nil
}

// ParseTemplates parses templates at startup so that syntax errors fail fast instead
// of at trigger time.
func ParseTemplates(templates []string) error {
	for _, text := range templates {
		if _, err := template.New("value").Option("missingkey=error").Parse(text); err != nil {
			return fmt.Errorf("invalid template %q: %w", text, err)
		}
	}

	return nil
}

func ipString(ip net.IP) string {
	if ip == nil {
		return ""
	}

	return ip.String()
}
