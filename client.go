package pusher

import (
	"code.google.com/p/go.net/websocket"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/parnurzeal/gorequest"
	"log"
	"os"
	"time"
)

var Info *log.Logger

type Client struct {
	ws                 *websocket.Conn
	Events             chan *Event
	Stop               chan bool
	subscribedChannels *subscribedChannels
	binders            map[string]chan *Event

	PusherConn  *ConnectionData
	AuthUrl     string
	AuthHeaders map[string]string
}

// heartbeat send a ping frame to server each - TODO reconnect on disconnect
func (c *Client) heartbeat() {
	for {
		websocket.Message.Send(c.ws, `{"event":"pusher:ping","data":"{}"}`)
		time.Sleep(HEARTBEAT_RATE * time.Second)
	}
}

// listen to Pusher server and process/dispatch recieved events
func (c *Client) listen() {
	for {

		var message string
		websocket.Message.Receive(c.ws, &message)
		Info.Println(message)

		var event Event
		err := json.Unmarshal([]byte(message), &event)
		// err := websocket.JSON.Receive(c.ws, &event)
		if err != nil {
			Info.Println("Listen error : ", err)
		} else {
			//log.Println(event)
			switch event.Event {
			case "pusher:ping":
				websocket.Message.Send(c.ws, `{"event":"pusher:pong","data":"{}"}`)
			case "pusher:pong":
			case "pusher:error":
				Info.Println("Event error recieved: ", event.Data)
			default:
				_, ok := c.binders[event.Event]
				if ok {
					c.binders[event.Event] <- &event
				}
			}
		}
	}
}

func (c *Client) authorize(channel string) (*string, error) {
	req := gorequest.New()

	req = req.Post(c.AuthUrl).TLSClientConfig(&tls.Config{MaxVersion: tls.VersionTLS11, InsecureSkipVerify: true})

	//Set headers
	for k, v := range c.AuthHeaders {
		req = req.Set(k, v)
	}

	//Set post values
	postVal := fmt.Sprintf("socket_id=%s&channel_name=%s", c.PusherConn.SocketId, channel)

	Info.Println(fmt.Sprintf("Authorizing token: %s : %s", c.AuthUrl, postVal))

	_, body, errs := req.Send(postVal).End()

	if errs != nil {
		return nil, errs[0]
	}

	return &body, nil
}

func (c *Client) SubscribeWithAuthorization(channel string) (err error) {
	if c.subscribedChannels.contains(channel) {
		return errors.New(fmt.Sprintf("Channel %s already subscribed", channel))
	}

	data, err := c.authorize(channel)
	if err != nil {
		Info.Println(err)
		return err
	}

	//add channel info to the data
	var tmpData map[string]interface{}
	if err := json.Unmarshal([]byte(*data), &tmpData); err != nil {
		return errors.New("Body Json Malformed")
	}

	//update channel name
	tmpData["channel"] = channel

	//marshall back
	channelData, _ := json.Marshal(tmpData)
	//subscription logic
	eventData := fmt.Sprintf(`{"event":"pusher:subscribe","data":%s}`, channelData)
	Info.Println(eventData)

	err = websocket.Message.Send(c.ws, eventData)

	if err != nil {
		return err
	}
	return c.subscribedChannels.add(channel)
}

// Subsribe to a channel
func (c *Client) Subscribe(channel string) (err error) {
	// Already subscribed ?
	if c.subscribedChannels.contains(channel) {
		err = errors.New(fmt.Sprintf("Channel %s already subscribed", channel))
		return
	}
	err = websocket.Message.Send(c.ws, fmt.Sprintf(`{"event":"pusher:subscribe","data":{"channel":"%s"}}`, channel))
	if err != nil {
		return
	}
	err = c.subscribedChannels.add(channel)
	return
}

// Unsubscribe from a channel
func (c *Client) Unsubscribe(channel string) (err error) {
	// subscribed ?
	if !c.subscribedChannels.contains(channel) {
		err = errors.New(fmt.Sprintf("Client isn't subscrived to %s", channel))
		return
	}
	err = websocket.Message.Send(c.ws, fmt.Sprintf(`{"event":"pusher:unsubscribe","data":{"channel":"%s"}}`, channel))
	if err != nil {
		return
	}
	// Remove channel from subscribedChannels slice
	c.subscribedChannels.remove(channel)
	return
}

// Bind an event
func (c *Client) Bind(evt string) (dataChannel chan *Event, err error) {
	// Already binded
	_, ok := c.binders[evt]
	if ok {
		err = errors.New(fmt.Sprintf("Event %s already binded", evt))
		return
	}
	// New data channel
	dataChannel = make(chan *Event, EVENT_CHANNEL_BUFF_SIZE)
	c.binders[evt] = dataChannel
	return
}

// Unbind a event
func (c *Client) Unbind(evt string) {
	delete(c.binders, evt)
}

func (c *Client) Close() {
	Info.Println("Closing")
	c.ws.Close()
}

// NewClient initialize & return a Pusher client
func NewClient(appKey string) (*Client, error) {
	Info = log.New(os.Stdout, "PUSHER: ", log.Lshortfile)

	origin := "http://localhost/"
	url := "wss://ws.pusherapp.com:443/app/" + appKey + "?protocol=" + PROTOCOL_VERSION

	Info.Println("Connecting: " + url)
	ws, err := websocket.Dial(url, "", origin)
	if err != nil {
		return nil, err
	}
	var resp = make([]byte, 11000) // Pusher max message size is 10KB
	n, err := ws.Read(resp)
	if err != nil {
		return nil, err
	}
	var event Event
	err = json.Unmarshal(resp[0:n], &event)
	if err != nil {
		return nil, err
	}

	var connData *ConnectionData
	err = json.Unmarshal([]byte(event.Data), &connData)
	if err != nil {
		return nil, err
	}

	switch event.Event {
	case "pusher:error":
		var data eventError
		err = json.Unmarshal([]byte(event.Data), &data)
		if err != nil {
			return nil, err
		}
		err = errors.New(fmt.Sprintf("Pusher return error : code : %d, message %s", data.code, data.message))
		return nil, err
	case "pusher:connection_established":
		log.Println("Pusher connection established.")
		sChannels := new(subscribedChannels)
		sChannels.channels = make([]string, 0)

		pClient := Client{ws,
			make(chan *Event, EVENT_CHANNEL_BUFF_SIZE),
			make(chan bool),
			sChannels,
			make(map[string]chan *Event),
			connData, "",
			map[string]string{},
		}

		go pClient.heartbeat()
		go pClient.listen()
		return &pClient, nil
	}
	return nil, errors.New("Ooooops something wrong happen")
}
