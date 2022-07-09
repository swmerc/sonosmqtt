package sonos

import (
	"context"
	"fmt"
	"strings"

	zeroconf "github.com/grandcat/zeroconf"
	log "github.com/sirupsen/logrus"
)

// DiscoveryData is an interface for the data we read from the mDNS responses.  We may eventually support
// other discovery mechanisms, but mDNS works fine and is the most modern option.
type DiscoveryData interface {
	GetHouseholdId() (string, error)
	GetInfoUrl() (string, error)
}

// ScanForPlayers does an active scan for Sonos devices over mDNS and sends the data
// down the responseChannel as it comes in.
func ScanForPlayers(ctx context.Context, responseChannel chan DiscoveryData) {

	go func() {

		log.Debugf("mDNS: start scan")

		// Discover all services on the network (e.g. _workstation._tcp)
		resolver, err := zeroconf.NewResolver(nil)
		if err != nil {
			log.Fatalln("mDNS: failed to initialize resolver:", err.Error())
		}

		// Grab data on one channel, translate, send it on another.
		serviceEntryChannel := make(chan *zeroconf.ServiceEntry)
		go func(serviceEntries <-chan *zeroconf.ServiceEntry) {
			for entry := range serviceEntries {
				// NOTE: I hate the context switch, but like the API.  I think I'd have to pass
				//       in the serviceEntryChannel to avoid it,  but I don't want code outside
				//       of this module dealing with the Sonos-isms (or mDNS).
				responseChannel <- mDNSDataFromServiceEntry(entry)
			}
		}(serviceEntryChannel)

		// Kick off the actual browse
		err = resolver.Browse(ctx, "_sonos._tcp", "local.", serviceEntryChannel)
		if err != nil {
			log.Errorf("mDNS: failed to browse: %s", err.Error())
		}

		log.Debugf("mDNS: done scan")
	}()

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
		return ConvertToApiVersion1(fmt.Sprintf("https://%s:%d%s", resp.IP, resp.Port, data)), nil
	}
	return "", fmt.Errorf("%s", "mDNS: No info found")
}

// mDNSDataFromServiceEntry creates a proper MDNSData struct from the raw mDNS data provided
// in a service record.
func mDNSDataFromServiceEntry(e *zeroconf.ServiceEntry) DiscoveryData {
	// Simple stuff
	data := &mDNSResponse{
		IP:      e.AddrIPv4[0].String(),
		Port:    e.Port,
		records: map[string]string{},
	}

	// Parse the TXT records
	data.records = make(map[string]string)
	for _, value := range e.Text {

		split := strings.Split(value, "=")
		if len(split) != 2 {
			continue
		}

		data.records[split[0]] = split[1]
	}

	return data
}
