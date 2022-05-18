package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	zeroconf "github.com/grandcat/zeroconf"
	log "github.com/sirupsen/logrus"
)

type MDNSDevice struct {
	// Stuff from mDNS discovery
	IP      string
	Port    int
	InfoUrl string

	// Generated (and hacky, but it saves having to hit /info most of the time)
	BaseUrl string
	RestUrl string
}

func (p MDNSDevice) string() string {
	return fmt.Sprintf("ip=%v, port=%d, info=%s", p.IP, p.Port, p.InfoUrl)
}

func (p *MDNSDevice) init(e *zeroconf.ServiceEntry) {
	p.IP = e.AddrIPv4[0].String()
	p.Port = e.Port

	// Pull out the info Url
	for _, value := range e.Text {
		split := strings.Split(value, "=")
		if len(split) == 2 && split[0] == "info" {
			p.InfoUrl = split[1]
		}
	}

	// Generate the Base and REST Urls
	p.BaseUrl = fmt.Sprintf("https://%s:%d", p.IP, p.Port)
	p.RestUrl = fmt.Sprintf("%s/api", p.BaseUrl)
}

func scanForPlayers(scanTimeInSeconds uint) []MDNSDevice {
	// Discover all services on the network (e.g. _workstation._tcp)
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		log.Fatalln("Failed to initialize resolver:", err.Error())
	}

	// This is moderately sketch, but should be safe.  The goroutine below writes to
	// the slice and we don't read it until the browse operation completes.
	log.Infof("mDNS: start scan")

	devices := make([]MDNSDevice, 0, 32)
	entryChannel := make(chan *zeroconf.ServiceEntry)
	go func(results <-chan *zeroconf.ServiceEntry, players *[]MDNSDevice) {
		for entry := range results {
			var p MDNSDevice
			p.init(entry)
			log.Debugf("mDNS: %s", p.string())
			*players = append(*players, p)
		}
	}(entryChannel, &devices)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*time.Duration(scanTimeInSeconds))
	defer cancel()
	err = resolver.Browse(ctx, "_sonos._tcp", "local.", entryChannel)
	if err != nil {
		log.Fatalln("Failed to browse:", err.Error())
	}

	<-ctx.Done()

	log.Debugf("mDNS: done scan")

	return devices
}
