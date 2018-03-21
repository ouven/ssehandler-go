package ssehandler

import (
	"log"
	"net/http"
	"time"

	"github.com/manucorporat/sse"
)

type Event = sse.Event
type PublishChannel = chan<- *Event

type ClientInitFunc = func(client client, request *http.Request) error

type SSEHandler interface {
	http.Handler
	StartInit(ClientInitFunc) error
	Start() error
	Stop()
	PublishChannel() PublishChannel
}

type eventChan = chan *Event
type client = eventChan
type registration struct {
	client  client
	request *http.Request
}

type ssehandler struct {
	eventInbound     eventChan
	registered       map[client]interface{}
	register         chan registration
	unregister       chan client
	stopNotification chan chan error
}

func (es *ssehandler) closeClient(client client) {
	delete(es.registered, client)
	close(client)
	log.Printf("client unregistered, left %d clients", len(es.registered))
}

func (es *ssehandler) Start() error {
	return es.StartInit(func(c client, r *http.Request) error { return nil })
}

func (es *ssehandler) StartInit(init ClientInitFunc) error {

	go func() {
	EventLoop:
		for {
			select {
			case registration := <-es.register:
				es.registered[registration.client] = nil
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
		registered:       make(map[client]interface{}),
		eventInbound:     make(client, 20),
		register:         make(chan registration, 2),
		unregister:       make(chan client, 2),
		stopNotification: make(chan chan error),
	}
}

func (es *ssehandler) PublishChannel() PublishChannel {
	return es.eventInbound
}

func (es *ssehandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	eventChan := make(eventChan, 10)
	es.register <- registration{
		client:  eventChan,
		request: r,
	}

	// Listen to the closing of the http connection via the CloseNotifier
	notify := w.(http.CloseNotifier).CloseNotify()
	go func() {
		<-notify
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
			f.Flush()

		// Read from our messageChan.
		case event, open := <-eventChan:
			if !open {
				break EventLoop
			}

			// Write to the ResponseWriter, `w`.
			sse.Encode(w, *event)
			f.Flush()
		}
	}

	// Done.
	log.Println("Finished HTTP request at ", r.URL.Path)
}
