package sonos

import (
	"context"
	"fmt"
	"strings"
	"time"

	zeroconf "github.com/grandcat/zeroconf"
	log "github.com/sirupsen/logrus"
)

// MDNSData is an interface for the data we read from the mDNS responses.
type MDNSData interface {
	GetHouseholdId() (string, error)
	GetInfoUrl() (string, error)
}

// ScanForPlayersViaMDNS does an active scan for Sonos devices over mDNS
// and returns an array of the ones it finds.
func ScanForPlayersViaMDNS(scanTimeInSeconds uint) []MDNSData {
	// Discover all services on the network (e.g. _workstation._tcp)
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		log.Fatalln("mDNS: failed to initialize resolver:", err.Error())
	}

	// This is moderately sketch, but should be safe.  The goroutine below writes to
	// the slice and we don't read it until the browse operation completes.

	devices := make([]MDNSData, 0, 32)
	entryChannel := make(chan *zeroconf.ServiceEntry)
	go func(results <-chan *zeroconf.ServiceEntry, players *[]MDNSData) {
		for entry := range results {
			var p mDNSResponse
			p.loadFromServiceEntry(entry)
			*players = append(*players, &p)
		}
	}(entryChannel, &devices)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*time.Duration(scanTimeInSeconds))
	defer cancel()
	err = resolver.Browse(ctx, "_sonos._tcp", "local.", entryChannel)
	if err != nil {
		log.Fatalln("mDNS: failed to browse:", err.Error())
	}

	<-ctx.Done()

	log.Debugf("mDNS: done scan")

	return devices
}

// mDNSResponse is our internal data format.
type mDNSResponse struct {
	// Stuff we get from the fact they responded
	IP   string
	Port int

	// Addition values from the TXT records
	records map[string]string
}

// GetHouseholdId returns the hhid record if it exists.
//
// Required for the interface
func (resp *mDNSResponse) GetHouseholdId() (string, error) {
	if data, ok := resp.records["hhid"]; ok {
		return data, nil
	}
	return "", fmt.Errorf("mDNS: %s", "No hhid found")
}

// GetInfoUrl returns the full /info URL if it exists
//
// Required for the interface
func (resp *mDNSResponse) GetInfoUrl() (string, error) {
	if data, ok := resp.records["info"]; ok {
		return fmt.Sprintf("https://%s:%d%s", resp.IP, resp.Port, data), nil
	}
	return "", fmt.Errorf("%s", "mDNS: No info found")
}

// loadFromServiceEntry is a private function that initializes our internal
// structure from the IP/port and TXT records
func (resp *mDNSResponse) loadFromServiceEntry(e *zeroconf.ServiceEntry) {
	// Simple stuff
	resp.IP = e.AddrIPv4[0].String()
	resp.Port = e.Port

	log.Debugf("mDNS: %s:%d: %s", resp.IP, resp.Port, strings.Join(e.Text, ","))

	// Parse the TXT records
	resp.records = make(map[string]string)
	for _, value := range e.Text {

		split := strings.Split(value, "=")
		if len(split) != 2 {
			continue
		}

		resp.records[split[0]] = split[1]
	}
}
