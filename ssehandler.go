package ssehandler

import (
	"log"
	"net/http"
	"time"

	"github.com/manucorporat/sse"
)

type Event = sse.Event
type PublishChannel = chan<- *Event
type Client = eventChan

type ClientInitFunc = func(client Client, request *http.Request) error

type SSEHandler interface {
	http.Handler
	StartInit(ClientInitFunc) error
	Start() error
	Stop()
	PublishChannel() PublishChannel
}

type eventChan = chan *Event
type registration struct {
	client  Client
	request *http.Request
}

type ssehandler struct {
	eventInbound     eventChan
	registered       map[Client]interface{}
	register         chan registration
	unregister       chan Client
	stopNotification chan chan error
}

func (es *ssehandler) closeClient(client Client) {
	delete(es.registered, client)
	close(client)
	log.Printf("client unregistered, left %d clients", len(es.registered))
}

func (es *ssehandler) Start() error {
	return es.StartInit(func(c Client, r *http.Request) error { return nil })
}

func (es *ssehandler) StartInit(init ClientInitFunc) error {

	go func() {
	EventLoop:
		for {
			select {
			case registration := <-es.register:
				es.registered[registration.client] = struct{}{}
				init(registration.client, registration.request)
				log.Printf("new client registered")

			case client := <-es.unregister:
				es.closeClient(client)

			case e := <-es.eventInbound:
				for c := range es.registered {
					c <- e
				}

			case stoppedChan := <-es.stopNotification:
				for client := range es.registered {
					es.closeClient(client)
				}
				stoppedChan <- nil
				break EventLoop
			}
		}
	}()
	return nil
}

func (es *ssehandler) Stop() {
	e := make(chan error)
	es.stopNotification <- e
	<-e
}

func NewSSEHandler() SSEHandler {
	return &ssehandler{
		registered:       make(map[Client]interface{}),
		eventInbound:     make(Client, 20),
		register:         make(chan registration, 2),
		unregister:       make(chan Client, 2),
		stopNotification: make(chan chan error),
	}
}

func (es *ssehandler) PublishChannel() PublishChannel {
	return es.eventInbound
}

func (es *ssehandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	notifier, ok2 := w.(http.CloseNotifier)

	if !(ok && ok2) {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	eventChan := make(eventChan, 10)
	es.register <- registration{
		client:  eventChan,
		request: r,
	}

	// Listen to the closing of the http connection via the CloseNotifier
	go func() {
		<-notifier.CloseNotify()
		es.unregister <- eventChan
		log.Println("HTTP connection just closed.")
	}()

	// Set the headers related to event streaming.
	w.Header().Set("Content-Type", sse.ContentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// keep alive
	keepalive := time.NewTicker(time.Second * 15)
	defer keepalive.Stop()

EventLoop:
	for {
		select {
		// writing an sse comment
		case <-keepalive.C:
			w.Write([]byte(": ping\n"))
			flusher.Flush()

		// Read from our messageChan.
		case event, open := <-eventChan:
			if !open {
				break EventLoop
			}

			// Write to the ResponseWriter, `w`.
			sse.Encode(w, *event)
			flusher.Flush()
		}
	}

	// Done.
	log.Println("Finished HTTP request at ", r.URL.Path)
}
