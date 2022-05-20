This is an app I've been working on for months off and on, and it has taken
many forms before this one.  As a result, it is still a bit of a mess.  I'll
slap in a TODO some point soon with some of the required cleanup and ideas for
the future, but it frankly serves my purposes for now.

I suspect fanout and simplify will be on by default shortly.  I may even remove
the options, as this app is drifting towards making life easier for people
developing simple dashboards for what is going on in their Sonos system.

I also suspect I'll have to implement auth at some point as some of the cool
stuff that we want to display is locked down.  The good news is that only this
app cares, the clients will always get access via MQTT.


High level idea
---------------

I suppose I should start with describing what this actually does.  The general
idea is to discover Sonos players on the LAN, subscribe to events on all of the
group coordinators, and publish these events to a MQTT broker in a moderately
sane fashion.  The topics used will be listed at the end of this, but for now
the general idea is to:

  - Subscribe to {base}/+/groups or {base}/+/players
    
    This will give you the list of groups or players (the latter requires
    simplify config option).  The list can be used to look up the PlayerId of a
    player you care about events front.

  - Subscribe to {base}/+/{playerId}/{namespace}

    This is where you will get actual event from the player(s) you care about.


Config file
-----------

I'll just slap a commented config file here.  You need one.  The app defaults
to looking for config.yml in the working directory, but that can be overridden
on the command line via --cfgpath.


    # General options
    #
    # debug: optional, set to true in order get overly verbose debug messages
    debug: false

    # Sonos options
    #
    # apikey:       required, and can be obtained from Sonos
    # household:    optional, and if present only players from that household are tracked
    # subcriptions: optional. but playbackExtended is recommended for now
    # fanout:       optional, set to true if you want GC events to get copied to all GMs
    # simplify:     optional, set to true to simplify Muse events before publishing.
    # scantime:     optional, the number of seconds to wait for mDNS results.  Defaults to 5.
    sonos:
    apikey: "REDACTED"
    household: "REDACTED"
    subscriptions: 
        - playbackExtended
    fanout: true
    simplify: true

    # MQTT options
    #
    # Omitting any of this "works", but defeats the purpose of the application.
    #
    # broker:
    #   host:   required, hostname or IP of MQTT server
    #   port:   required, port of MQTT server
    #   client: required, name to use for this client on the MQTT server
    # topic:    required, base topic to put Sonos MQTT content on
    mqtt:
    broker:
        host: "127.0.0.1"
        port: 1883
        client: "sonosmqtt1"
    topic: "sonos"


MQTT topics used
----------------

  - {base}/{HouseholdId}/groups

    The latest GroupsResponse as delivered by a Sonos device.  The format is
    not documented here outside of the code I use internally, but it contains
    playerIds and groupIds.

    This is not evented if simplify is selected in config.yml, playersSimple is instead.

  - {base}/{HouseholdId}/{groupId}/{namespacesFromConfig}
    
    Example: {base}/{HouseholdId}/{GroupId}/extendedPlaybackStatus

    The latest group level events from the namespaces specified in config.yml.
    The one I've been testing with, as it is all I need at the moment, is
    extendedPlaybackStatus.  The events here are also not documented by me as
    they come from the Sonos player.

    This is not evented if fanout is selected in config.yml as we just send the events to the 
    player topics. 

  - {base}/{HouseholdId}/{PlayerId}/{namespacesFromConfig}

    If the fanout option is set in config.yml, all of the group events get
    copied to namespaces for the players as well.  This allows clients to
    simply subscribe to the player's MQTT topic if they want to see playback
    changes on a specific player, regardless of how it is grouped.

If the simplify option is selected in config.yml, a couple of different topics are used:

  - {base}/{HouseholdId}/playersSimple

    A list of players, with each entry being playerId, playerName.  Subscribing
    to this provides a trivial way to discover what players are in the
    household and find one by name so you can look up the playerId and
    subscribe to the player.

    {
        [
            { 
                "name": "Player Name", 
                "id": "PlayerId, suitable for use in creating the MQTT topic",
            },
        ]
    }

  - {base}/{HouseholdId}/{PlayerId}/extendedPlaybackStatusSimple

    { 
        "playbackState": "Playback state as sent by Sonos player",
        "artist": "ArtistName",
        "album": "AlbumName",
        "track": "TrackName",
        "imageUrl": "URL for album art",
    }

