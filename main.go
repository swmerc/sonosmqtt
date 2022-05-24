package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	log "github.com/sirupsen/logrus"

	"gopkg.in/yaml.v2"
)

// Config defines the server options we support in the config file.  Who knew?
type Config struct {
	// Log level
	Debug bool `yaml:"debug"`

	// Sonos options
	Sonos struct {
		ApiKey      string `yaml:"apikey"`
		HouseholdId string `yaml:"household"` // Filter to households with this if provided

		// Things to subscribe to, and how to handle the data
		Subscriptions []string `yaml:"subscriptions"`
		Simplify      bool     `yaml:"simplify"`

		// Geekier stuff.  May go away.
		ScanTime uint `yaml:"scantime"` // Time to wait for mDNS responses.  Defaults to 5 seconds.
		FanOut   bool `yaml:"fanout"`   // True to copy coordinator events to players
	} `yaml:"sonos"`

	// MQTT broker-isms
	MQTT struct {
		Config MQTTConfig `yaml:"broker"`
		Topic  string     `yaml:"topic"`
	} `yaml:"mqtt"`
}

// main entry point.  It just handles loading config and firing up the MQTT client
func main() {
	var config Config
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

// loadConfigFile loads the config file from the given path and applies
// defaults
func loadConfigFile(cfgPath string) (Config, error) {
	var err error

	// Apply defaults
	config := Config{}
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

	// Automatically flip fanout if simplify is selected (for now)
	//
	// I'll pull fanout out of the code once I'm sure this is how I want it to work.
	if config.Sonos.Simplify {
		if !config.Sonos.FanOut {
			log.Infof("app: Setting fanout since simplify is set.")
			config.Sonos.FanOut = true
		}
	}

	return config, err
}

// MQTTConfig is the section of a config file that describes how to connect to a MQTT broker
type MQTTConfig struct {
	Client   string `yaml:"client"`
	Host     string `yaml:"host"`
	Port     uint32 `yaml:"port"`
	TLS      bool   `yaml:"tls"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// initMQTTClient actually initializes the client
func initMQTTClient(config MQTTConfig) (mqtt.Client, error) {
	if len(config.Host) == 0 || len(config.Client) == 0 || config.Port == 0 {
		log.Infof("mqtt: not configured")
		return nil, nil
	}

	opts := mqtt.NewClientOptions()
	opts.CleanSession = false

	opts.SetClientID(config.Client)

	// Make sure username/password is secure
	if config.TLS == false && (len(config.Username)+len(config.Password) > 0) {
		log.Fatalf("mqtt: username/password auth with no TLS? Can't let you do it.")
	}

	if (len(config.Username) > 0) != (len(config.Password) > 0) {
		log.Fatalf("mqtt: username/password must both be set or cleared.")
	}

	// While this supports TLS, it does not support client certs yet
	if config.TLS {
		tlsConfig := &tls.Config{InsecureSkipVerify: false, ClientAuth: tls.NoClientCert}
		opts.SetTLSConfig(tlsConfig)
		opts.AddBroker(fmt.Sprintf("ssl://%s:%d", config.Host, config.Port))
	} else {
		opts.AddBroker(fmt.Sprintf("tcp://%s:%d", config.Host, config.Port))
	}

	// We already checked that user and password are both set or both cleared, so
	// we only need to check one here.
	if len(config.Username) > 0 {
		opts.SetUsername(config.Username)
		opts.SetPassword(config.Password)
	}

	//
	// We block if the broker is down. The only downside is that we hang here if we have a
	// misconfigured MQTT broker.
	//
	client := mqtt.NewClient(opts)
	for true {
		if token := client.Connect(); token.Wait() && token.Error() != nil {
			log.Infof("mqtt: error connecting to broker %s:%d at start: %s", config.Host, config.Port, token.Error())
			time.Sleep(time.Duration(1) * time.Minute)
		} else {
			break
		}
	}

	log.Infof("mqtt: connected")

	return client, nil
}
