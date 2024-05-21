package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
)

var MV_USER string
var MV_PASS string
var TELEGRAM_TOKEN string

// map[uri]chatIDs
var subscriptions = make(map[string][]int64)
var subscriptionsLock = &sync.RWMutex{}
var report = make(chan NewMessage)

func init() {
	MV_USER = os.Getenv("MV_USER")
	MV_PASS = os.Getenv("MV_PASS")
	TELEGRAM_TOKEN = os.Getenv("TELEGRAM_TOKEN")
}

func main() {
	err := os.MkdirAll("saves", 0755)
	if err != nil {
		panic(err)
	}

	loadSubscriptions()

	for uri := range subscriptions {
		go NewMV(report, uri, 10*time.Second)
	}

	bot()
}

type NewMessage struct {
	URI     string
	Content Message
}

func bot() {
	b, err := gotgbot.NewBot(TELEGRAM_TOKEN, nil)
	if err != nil {
		panic("failed to create new bot: " + err.Error())
	}

	go func() {
		for newMsg := range report {
			subscriptionsLock.RLock()
			chatIDs := subscriptions[newMsg.URI]
			subscriptionsLock.RUnlock()
			for _, chatID := range chatIDs {
				_, err := b.SendMessage(chatID, fmt.Sprintf(`
%s

*%s*: 
%s
				`, newMsg.URI, newMsg.Content.Author, strings.TrimSpace(newMsg.Content.Body)), &gotgbot.SendMessageOpts{
					ParseMode: "markdown",
				})
				if err != nil {
					log.Println("failed to send message:", err)
				}
			}
		}
	}()

	dispatcher := ext.NewDispatcher(&ext.DispatcherOpts{
		Error: func(b *gotgbot.Bot, ctx *ext.Context, err error) ext.DispatcherAction {
			log.Println("an error occurred while handling update:", err.Error())
			return ext.DispatcherActionNoop
		},
		MaxRoutines: ext.DefaultMaxRoutines,
	})
	updater := ext.NewUpdater(dispatcher, nil)

	dispatcher.AddHandler(handlers.NewCommand("start", start))
	dispatcher.AddHandler(handlers.NewCommand("s", subscribe))
	dispatcher.AddHandler(handlers.NewCommand("u", unsubscribe))

	// Start receiving updates.
	err = updater.StartPolling(b, &ext.PollingOpts{
		DropPendingUpdates: true,
		GetUpdatesOpts: &gotgbot.GetUpdatesOpts{
			Timeout: 9,
			RequestOpts: &gotgbot.RequestOpts{
				Timeout: time.Second * 10,
			},
		},
	})
	if err != nil {
		panic("failed to start polling: " + err.Error())
	}
	log.Printf("%s has been started...\n", b.User.Username)
	updater.Idle()
}

func start(b *gotgbot.Bot, ctx *ext.Context) error {
	_, err := ctx.EffectiveMessage.Reply(b, `
Hola! Te notificaré de los hilos que me solicites.

Para recibir los mensajes enviados a un hilo, usa el comando /s seguido de la URL del hilo.

Para dejar de recibir notificaciones sobre un hilo, usa el comando /u seguido de la URL del hilo.
	`, &gotgbot.SendMessageOpts{
		ParseMode: "markdown",
	})
	if err != nil {
		return fmt.Errorf("failed to send start message: %w", err)
	}
	return nil
}

func subscribe(b *gotgbot.Bot, ctx *ext.Context) error {
	args := ctx.Args()
	log.Printf("args: %+v", args)
	if len(args) != 2 {
		_, err := ctx.EffectiveMessage.Reply(b, `
			Debes especificar un hilo para suscribirte.
		`, &gotgbot.SendMessageOpts{
			ParseMode: "markdown",
		})
		if err != nil {
			return fmt.Errorf("failed to send start message: %w", err)
		}
		return nil
	}
	uri := args[1]
	if uri[:30] != "https://www.mediavida.com/foro" || len(strings.Split(uri, "/")) < 6 {
		_, err := ctx.EffectiveMessage.Reply(b, `
			El hilo que has especificado no es válido.
		`, &gotgbot.SendMessageOpts{
			ParseMode: "text",
		})
		if err != nil {
			return fmt.Errorf("failed to send start message: %w", err)
		}
		return nil
	}
	// remove anything after the thread https://www.mediavida.com/foro/mafia/fortaleza-frontera-iv-remake-710835
	// for example https://www.mediavida.com/foro/mafia/fortaleza-frontera-iv-remake-710835/123
	uri = strings.Join(strings.Split(uri, "/")[:6], "/")

	subscriptionsLock.Lock()
	if _, ok := subscriptions[uri]; !ok {
		go NewMV(report, uri, 10*time.Second)
	}
	subscriptions[uri] = append(subscriptions[uri], ctx.EffectiveChat.Id)
	subscriptionsLock.Unlock()

	_, err := ctx.EffectiveMessage.Reply(b, fmt.Sprintf(`
		Subscripción exitosa al hilo %s
	`, uri), &gotgbot.SendMessageOpts{
		ParseMode: "markdown",
	})
	if err != nil {
		return fmt.Errorf("failed to send start message: %w", err)
	}

	persistSubscriptions()
	return nil
}

func unsubscribe(b *gotgbot.Bot, ctx *ext.Context) error {
	args := ctx.Args()
	if len(args) != 2 {
		_, err := ctx.EffectiveMessage.Reply(b, `
			Debes especificar un hilo para desuscribirte.
		`, &gotgbot.SendMessageOpts{
			ParseMode: "markdown",
		})
		if err != nil {
			return fmt.Errorf("failed to send start message: %w", err)
		}
		return nil
	}
	uri := args[1]
	if uri[:30] != "https://www.mediavida.com/foro" || len(strings.Split(uri, "/")) < 6 {
		_, err := ctx.EffectiveMessage.Reply(b, `
			El hilo que has especificado no es válido.
		`, &gotgbot.SendMessageOpts{
			ParseMode: "markdown",
		})
		if err != nil {
			return fmt.Errorf("failed to send start message: %w", err)
		}
		return nil
	}
	uri = strings.Join(strings.Split(uri, "/")[:6], "/")

	subscriptionsLock.Lock()
	for i, chatID := range subscriptions[uri] {
		if chatID == ctx.EffectiveChat.Id {
			subscriptions[uri] = append(subscriptions[uri][:i], subscriptions[uri][i+1:]...)
			break
		}
	}
	subscriptionsLock.Unlock()

	_, err := ctx.EffectiveMessage.Reply(b, fmt.Sprintf(`
		Desuscripción exitosa al hilo %s
	`, uri), &gotgbot.SendMessageOpts{
		ParseMode: "markdown",
	})
	if err != nil {
		return fmt.Errorf("failed to send start message: %w", err)
	}

	persistSubscriptions()
	return nil
}

// Persist subscriptions
func persistSubscriptions() {
	subscriptionsLock.RLock()
	defer subscriptionsLock.RUnlock()

	f, err := os.Create("saves/subscriptions")
	if err != nil {
		log.Println("failed to create subscriptions file:", err)
		return
	}
	defer f.Close()

	for uri, chatIDs := range subscriptions {
		var ids string
		for _, id := range chatIDs {
			ids += fmt.Sprintf("%d,", id)
		}
		_, err := f.WriteString(fmt.Sprintf("%s %v\n", uri, ids))
		if err != nil {
			log.Println("failed to write to subscriptions file:", err)
			return
		}
	}

	log.Println("subscriptions saved")
}

// Load subscriptions
func loadSubscriptions() {
	f, err := os.Open("saves/subscriptions")
	if err != nil {
		log.Println("failed to open subscriptions file:", err)
		return
	}
	defer f.Close()

	subscriptionsLock.Lock()
	var uri string
	var chatIDsString string
	for {
		_, err := fmt.Fscanf(f, "%s %s\n", &uri, &chatIDsString)
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("failed to read from subscriptions file: %v", err)
			continue
		}
		chatIDs := strings.Split(chatIDsString, ",")
		var chatIDsInt []int64
		for _, id := range chatIDs {
			if id == "" {
				continue
			}
			chatID, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				log.Printf("failed to parse chat ID: %v", err)
				continue
			}
			chatIDsInt = append(chatIDsInt, chatID)
		}
		if uri == "" {
			continue
		}
		subscriptions[uri] = chatIDsInt
	}
	subscriptionsLock.Unlock()

	for uri, chatIDs := range subscriptions {
		log.Printf("loaded subscription: %s for %d users", uri, len(chatIDs))
	}
	log.Println("loaded", len(subscriptions), "subscriptions")
}
