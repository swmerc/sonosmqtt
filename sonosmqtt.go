package main

import (
	"flag"
	"fmt"
	"os"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	log "github.com/sirupsen/logrus"

	"gopkg.in/yaml.v2"
)

func main() {
	var config AppConfig
	var client mqtt.Client
	var err error

	// Command line args
	cfgPath := flag.String("cfgpath", "config.yml", "Path to config file for the server")
	flag.Parse()

	// Config file
	if config, err = loadConfigFile(*cfgPath); err != nil {
		log.Errorf("Unable to load config from %s (%s)", *cfgPath, err.Error())
		return
	}

	// Handle log level now that we've read the config
	if config.Debug {
		log.SetLevel(log.DebugLevel)
	}

	// MQTT client
	if client, err = initMQTTClient(config.MQTT.Config); err != nil {
		log.Errorf("Unable to init MQTT client (%s)", err.Error())
		return
	}

	// Fire up the actual app
	app := NewApp(config, client)
	app.run()
}

func loadConfigFile(cfgPath string) (AppConfig, error) {
	var err error

	// Apply defaults
	config := AppConfig{}
	config.Sonos.ScanTime = 5

	// Pull in content from the file
	f, err := os.Open(cfgPath)
	if err != nil {
		return config, err
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	err = decoder.Decode(&config)

	// Manually check the required stuff.  Shame this is not built in.
	if err == nil {
		if len(config.Sonos.ApiKey) == 0 {
			err = fmt.Errorf("API key must be present in the configuration file.")
		}
	}

	return config, err
}
