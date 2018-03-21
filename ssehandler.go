package ssehandler

import (
	"log"
	"net/http"
	"time"

	"github.com/manucorporat/sse"
)

type Event = sse.Event
type PublishChannel = chan<- *Event

type SSEHandler interface {
	http.Handler
	Start() error
	Stop()
	PublishChannel() PublishChannel
}

type eventChan = chan *Event

type ssehandler struct {
	eventInbound eventChan
	registered   map[eventChan]interface{}
	register     chan eventChan
	unregister   chan eventChan
}

func (es *ssehandler) Start() error {

	go func() {
		for {
			select {
			case reg := <-es.register:
				es.registered[reg] = nil
				log.Printf("new client registered")

			case awayClient := <-es.unregister:
				delete(es.registered, awayClient)
				close(awayClient)
				log.Printf("client unregistered, left %d clients", len(es.registered))

			case e := <-es.eventInbound:
				for c := range es.registered {
					c <- e
				}
			}
		}
	}()
	return nil
}

func (es *ssehandler) Stop() {
}

func NewSSEHandler() SSEHandler {
	return &ssehandler{
		registered:   make(map[eventChan]interface{}),
		eventInbound: make(eventChan, 20),
		register:     make(chan eventChan, 2),
		unregister:   make(chan eventChan, 2),
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
	es.register <- eventChan

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
