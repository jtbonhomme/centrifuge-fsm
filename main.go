package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/centrifugal/centrifuge"
	centrigo "github.com/centrifugal/centrifuge-go"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

func main() {
	var err error
	var wg sync.WaitGroup

	// Init log
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	output := zerolog.ConsoleWriter{
		Out:           os.Stderr,
		TimeFormat:    time.RFC3339,
		FormatMessage: func(i interface{}) string { return fmt.Sprintf("[main] %s", i) },
	}
	log = zerolog.New(output).With().Timestamp().Logger()

	node, err := centrifuge.New(centrifuge.Config{
		LogLevel: centrifuge.LogLevelDebug,
	})
	if err != nil {
		panic(fmt.Errorf("error instantiating new centrifuge node: %w", err))
	}

	node.OnConnect(func(client *centrifuge.Client) {
		// In our example transport will always be Websocket but it can also be SockJS.
		transportName := client.Transport().Name()
		// In our example clients connect with JSON protocol but it can also be Protobuf.
		transportProto := client.Transport().Protocol()
		log.Info().Msgf("client %s (%s) connected via %s (%s)", client.ID(), string(client.Info()), transportName, transportProto)

		client.OnSubscribe(func(e centrifuge.SubscribeEvent, cb centrifuge.SubscribeCallback) {
			log.Info().Msgf("client %s (%s) subscribes on channel %s", client.ID(), string(client.Info()), e.Channel)
			cb(centrifuge.SubscribeReply{}, nil)
		})

		client.OnPublish(func(e centrifuge.PublishEvent, cb centrifuge.PublishCallback) {
			log.Info().Msgf("client %s (%s) publishes into channel %s: %s", client.ID(), string(client.Info()), e.Channel, string(e.Data))
			cb(centrifuge.PublishReply{}, nil)
		})

		client.OnDisconnect(func(e centrifuge.DisconnectEvent) {
			log.Info().Msgf("client %s (%s) disconnected", client.ID(), string(client.Info()))
		})

		client.OnRPC(handleRPC)
		wg.Done()
	})

	err = node.Run()
	if err != nil {
		panic(fmt.Errorf("error running centrifuge node: %w", err))
	}

	// Configure HTTP routes.
	// Serve Websocket connections using WebsocketHandler.
	wsHandler := centrifuge.NewWebsocketHandler(node, centrifuge.WebsocketConfig{
		ReadBufferSize: 1024,
	})
	http.Handle("/connection/websocket", auth(wsHandler))

	// The second route is for serving index.html file.
	http.Handle("/", http.FileServer(http.Dir("./public")))

	go func() {
		log.Info().Msgf("Starting server, visit http://localhost:8000")
		if err := http.ListenAndServe(":8000", nil); err != nil {
			panic(fmt.Errorf("error listening on :8000: %w", err))
		}
	}()

	clients := make([]*centrigo.Client, 4)

	for i := 0; i < 4; i++ {
		log.Info().Msgf("create player %d", i)
		clients[i] = newClient(&log)
		wg.Add(1)
		err = clients[i].Connect()
		if err != nil {
			log.Panic().Msgf("connect client %d error: %s", i, err.Error())
		}
	}

	log.Info().Msgf("waiting for all clients to connected")

	wg.Wait()

	log.Info().Msgf("all client  connected")

	// Waiting signal
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	s := <-interrupt
	log.Info().Msg("received signal: " + s.String())

	log.Info().Msg("exit")
}

func handleRPC(e centrifuge.RPCEvent, c centrifuge.RPCCallback) {
	log.Info().Msgf("client RPC: %s %s", e.Method, string(e.Data))
}

func newClient(log *zerolog.Logger) *centrigo.Client {
	wsURL := "ws://localhost:8000/connection/websocket"

	c := centrigo.NewJsonClient(wsURL, centrigo.Config{
		Name:    "listening-go-client",
		Version: "0.0.1",
	})

	c.OnConnecting(func(_ centrigo.ConnectingEvent) {
		log.Info().Msg("Connecting")
	})

	c.OnConnected(func(_ centrigo.ConnectedEvent) {
		var err error
		log.Info().Msg("Connected")

		subServer, err := c.NewSubscription("com.jtbonhomme.server")
		if err != nil {
			log.Error().Msgf("subscription creation error: %s", err.Error())
		}

		subServer.OnJoin(func(e centrigo.JoinEvent) {
			log.Info().Msgf("[com.jtbonhomme.server] join event: %s", e.ClientInfo.Client)
		})

		subServer.OnError(func(e centrigo.SubscriptionErrorEvent) {
			log.Info().Msgf("[com.jtbonhomme.server] subscription error event: %s", e.Error.Error())
		})

		subServer.OnPublication(func(e centrigo.PublicationEvent) {
			log.Info().Msgf("[com.jtbonhomme.server] publication event: %s", string(e.Data))
		})

		subServer.OnSubscribing(func(e centrigo.SubscribingEvent) {
			log.Info().Msgf("[com.jtbonhomme.server] subscribing event: %s", e.Reason)
		})

		subServer.OnSubscribed(func(e centrigo.SubscribedEvent) {
			log.Info().Msgf("[com.jtbonhomme.server] subscribed event")
		})

		err = subServer.Subscribe()
		if err != nil {
			log.Error().Msgf("subscription error: %s", err.Error())
		}
	})

	c.OnDisconnected(func(e centrigo.DisconnectedEvent) {
		log.Info().Msgf("Disconnected event: %d %s", e.Code, e.Reason)
		// TODO automatic reconnect?
	})

	c.OnError(func(e centrigo.ErrorEvent) {
		log.Info().Msgf("error: %s", e.Error.Error())
	})

	c.OnMessage(func(e centrigo.MessageEvent) {
		log.Info().Msgf("Message received from server %s", string(e.Data))
	})

	return c
}

func auth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		// Put authentication Credentials into request Context.
		// Since we don't have any session backend here we simply
		// set user ID as empty string. Users with empty ID called
		// anonymous users, in real app you should decide whether
		// anonymous users allowed to connect to your server or not.
		cred := &centrifuge.Credentials{
			UserID: "",
		}
		newCtx := centrifuge.SetCredentials(ctx, cred)
		r = r.WithContext(newCtx)
		h.ServeHTTP(w, r)
	})
}
