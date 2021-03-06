This is an app I've been working on for months off and on, and it has taken
many forms before this one.  As a result, it is still a bit of a mess.  I'll
slap in a TODO some point soon with some of the required cleanup and ideas for
the future, but it frankly serves my purposes for now.

I suspect I'll have to implement auth at some point as some of the cool
stuff that we want to display is locked down.  The good news is that only this
app cares, the clients will always get access via MQTT.


High level idea
---------------

I suppose I should start with describing what this actually does.  The general
idea is to discover Sonos players on the LAN, subscribe to events on all of the
group coordinators, and publish these events to a MQTT broker in a moderately
sane fashion.  The topics used will be listed at the end of this, but for now
the general idea is to subscribe to a topic for discovery and use the data
provided to find the other topics you want to subscribe to.  In each of the
cases below, you can pick what you want by setting the simplify config
option.

  - Subscribe to {base}/groups or {base}/players
    
    This will give you the list of groups or players. See below for more
    detail.

  - Subscribe to {base}/group/{groupId}/{eventType} or {base}/player/{playerId}/{eventType}

    This is where you will get actual event from the player(s) you care about.
    At the moment I only support groupId targeted events, so you will get the
    former when simplify is not set and the latter when it is set.
    
    Note that in the current implementation I can't see any reason to deal
    with {base}/group/# for anything.  You get the same content in 
    {base}/player/{playerId}/#, and you can use any PlayerId in the group.


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
    # simplify:     optional, set to true to simplify Muse events before publishing.
    # scantime:     optional, the number of seconds to wait for mDNS results.  Defaults to 5.
    sonos:
    apikey: "REDACTED"
    household: "REDACTED"
    subscriptions: 
        - playbackExtended
    simplify: true

    # MQTT options
    #
    # Omitting any of this "works", but defeats the purpose of the application.
    #
    # broker:
    #   host:     required, hostname or IP of MQTT server
    #   port:     required, port of MQTT server
    #   client:   required, name to use for this client on the MQTT server
    #   tls:      optional, and setting to true enables tls
    #   username: optional, and only valid if tls is true
    #   password: optional, and only valid if tls is true
    # topic:    required, base topic to put Sonos MQTT content on
    mqtt:
    broker:
        host: "127.0.0.1"
        port: 1883
        client: "sonosmqtt1"
    topic: "sonos"


MQTT topics used
----------------

  Simplify disabled
  -----------------

  When simplify is disabled, events come in exactly as sent by the Sonos
  devices.  As a result, I will only document the topic paths and not the
  content outside of mentioning the content type.  I don't want to have to
  adjust the docs every time the format changes, nor do I want to document the
  Sonos formats in gory detail.
  
  - {base}/groups

    The latest GroupsResponse as delivered by a Sonos device.  This contains
    all of the current groups, all of the current players, and some mapping
    between them.  There is also a LOT of detail available for the players if
    one cares (capabilities, protocol versions, etc).
    

  - {base}/group/{groupCoordinatorId}/{eventType} or {base}/player/{playerId}/{eventType}
    
    Examples:    
      - {base}/{GroupCoordinatorId}/extendedPlaybackStatus
      - {base}/{PlayerId}/extendedPlaybackStatus

    The latest group level events from the namespaces specified in config.yml.
    The one I've been testing with, as it is all I need at the moment, is
    extendedPlaybackStatus.

    Note that I'm also not going to document the relationship between subscribing 
    to a namespace and the events that are received.  At the moment subscribing to
    playbackExtended only events extendedPlaybackStatus, but I have no insight
    into how that may change over time.


  Simplify enabled
  ----------------

  - {base}/groups
  
    At the moment, this is a list of groups.  Each group contains an id and a list
    of players.  This can be used by more complete controllers, while /players is 
    more suitable for dump single-player displays.
    
    {
      [
        { 
	  "id":   "GroupId1",
	  [
       	    { "id: "PlayerId 1", "name": "Player Name 1" },
	  ], 
      	},
      ]
    }
    
  
  
  - {base}/players

   At the moment it is a simple list of players, with playerName and playerId
   exposed:
    

    {
      [
        { 
	  "id":   "PlayerId1",
       	  "name": "Player Name 1", 
      	},
        ...
	{ 
	  "id":   "PlayerIdX",
       	  "name": "Player Name X", 
          
      	},
      ]
    }

  - {base}/{PlayerId}/extendedPlaybackStatusSimple

    If you subscribe to playbackExtended via the config file, all responses
    will get reduced to the following and will be published to
    {base}/{playerId}/extendedPlaybackStatusSimple for every player in the
    group when the group metadata or playback state changes.

    It looks like this:
    
    { 
        "playbackState": "Playback state as sent by Sonos player",
        "artist":        "ArtistName",
        "album":         "AlbumName",
        "track":         "TrackName",
        "imageUrl":      "URL for album art",
    }

