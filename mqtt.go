package main

import (
	"fmt"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	log "github.com/sirupsen/logrus"
)

// MQTTConfig is the section of a config file that describes how to connect to a MQTT broker
type MQTTConfig struct {
	Client string `yaml:"client"`
	Host   string `yaml:"host"`
	Port   uint32 `yaml:"port"`
}

func initMQTTClient(config MQTTConfig) (mqtt.Client, error) {
	if len(config.Host) == 0 || len(config.Client) == 0 || config.Port == 0 {
		log.Infof("mqtt: not configured")
		return nil, nil
	}

	opts := mqtt.NewClientOptions()
	opts.CleanSession = false

	opts.SetClientID(config.Client)
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", config.Host, config.Port))

	//
	// We block if the broker is down. The only downside is that we hang here if we have a
	// misconfigured MQTT broker.
	//
	client := mqtt.NewClient(opts)
	for true {
		if token := client.Connect(); token.Wait() && token.Error() != nil {
			log.Infof("Error connecting to broker %s:%d at start: %s", config.Host, config.Port, token.Error())
			time.Sleep(time.Duration(1) * time.Minute)
		} else {
			break
		}
	}

	log.Infof("mqtt: connected")

	return client, nil
}
