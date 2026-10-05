package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/bavix/sol/internal/domain/wol"
	"github.com/bavix/sol/internal/infra/network"
)

var ifacesJSON bool

var ifacesCmd = &cobra.Command{
	Use:   "ifaces",
	Short: "List local network interfaces and their auto-selection eligibility",
	Long: "List every local network interface with its MAC/IPv4 and whether it would be\n" +
		"selected by auto mode (up, non-loopback, has a MAC and is not virtual).",
	RunE: func(_ *cobra.Command, _ []string) error {
		ifaces, err := network.List()
		if err != nil {
			return err
		}

		if ifacesJSON {
			return printIfacesJSON(ifaces)
		}

		printIfacesTable(ifaces)

		return nil
	},
}

func init() {
	rootCmd.AddCommand(ifacesCmd)
	ifacesCmd.Flags().BoolVar(&ifacesJSON, "json", false, "Print the interface list as JSON")
}

// ifaceTablePadding is the minimum column spacing of the `sol ifaces` table.
const ifaceTablePadding = 2

type ifaceJSON struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"`
	MAC    string `json:"mac"`
	IPv4   string `json:"ipv4"`
	Auto   bool   `json:"auto"`
}

func printIfacesTable(ifaces []wol.IfaceInfo) {
	writer := tabwriter.NewWriter(os.Stdout, 0, 0, ifaceTablePadding, ' ', 0)

	fmt.Fprintln(writer, "NAME\tTYPE\tSTATUS\tMAC\tIPV4\tAUTO")

	for _, iface := range ifaces {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			iface.Name,
			ifaceType(iface),
			ifaceStatus(iface),
			iface.MAC,
			dash(formatIPv4(iface.IPv4())),
			autoMark(iface.Eligible),
		)
	}

	_ = writer.Flush()
}

func printIfacesJSON(ifaces []wol.IfaceInfo) error {
	items := make([]ifaceJSON, 0, len(ifaces))

	for _, iface := range ifaces {
		items = append(items, ifaceJSON{
			Name:   iface.Name,
			Type:   ifaceType(iface),
			Status: ifaceStatus(iface),
			MAC:    iface.MAC.String(),
			IPv4:   formatIPv4(iface.IPv4()),
			Auto:   iface.Eligible,
		})
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)

	return encoder.Encode(items)
}

func ifaceType(iface wol.IfaceInfo) string {
	switch {
	case iface.Loopback:
		return "loopback"
	case iface.Virtual:
		return "virtual"
	default:
		return "physical"
	}
}

func ifaceStatus(iface wol.IfaceInfo) string {
	if iface.Up {
		return "up"
	}

	return "down"
}

func autoMark(eligible bool) string {
	if eligible {
		return "yes"
	}

	return "no"
}

func formatIPv4(ip net.IP) string {
	if ip == nil {
		return ""
	}

	return ip.String()
}

func dash(value string) string {
	if value == "" {
		return "-"
	}

	return value
}
