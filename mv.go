package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

var mvclient *MVClient

type MVClient struct {
	Http *http.Client
	User string
	Pass string
}

type Message struct {
	Author string
	Body   string
}

type Game struct {
	Frequency         time.Duration
	Client            *MVClient
	Uri               string
	ThreadID          string
	SubforumID        string
	LastPageProcessed int
	LastMsg           int
	LastPage          int
	Messages          []Message
	Report            chan<- NewMessage
}

func NewMV(report chan<- NewMessage, uri string, frequency time.Duration) {
	log.Printf("Starting MV client for %v", uri)
	if mvclient == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			panic(err)
		}
		client := &http.Client{
			Jar: jar,
		}
		mvclient = &MVClient{
			Http: client,
			User: MV_USER,
			Pass: MV_PASS,
		}
		mvclient.Login()
	}
	g := &Game{
		Uri:       uri,
		Client:    mvclient,
		LastMsg:   1,
		Frequency: frequency,
		Report:    report,
	}
	g.Load()
	g.Info()
	time.Sleep(1 * time.Second)
	g.Poll()
}

func (c *MVClient) Login() {
	resp, err := c.Http.Get("https://www.mediavida.com/login")
	if err != nil {
		panic(err)
	}
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		panic(err)
	}
	crsftoken, ok := doc.Find("#_token").Attr("value")
	if !ok {
		panic("No crsf token found")
	}
	_, err = c.Http.PostForm("https://www.mediavida.com/login", url.Values{
		"name":     {c.User},
		"password": {c.Pass},
		"cookie":   {"1"},
		"_token":   {crsftoken},
	})
	if err != nil {
		log.Fatal(err)
	}
	// TODO: check login success
	log.Println("Logged in")
}

func (g *Game) Info() (fid, tid, pagina, token string) {
	resp, err := g.Client.Http.Get(g.Uri)
	if err != nil {
		fmt.Println("Uri:", g.Uri)
		panic(err)
	}
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		panic(err)
	}
	fid, _ = doc.Find("#fid").Attr("value")
	tid, _ = doc.Find("#tid").Attr("value")
	pagina, _ = doc.Find("#pagina").Attr("value")
	token, _ = doc.Find("#token").Attr("value")
	g.SubforumID = fid
	g.ThreadID = tid
	if token == "" {
		fmt.Println(g.Uri)
		fmt.Println(doc.Find("#token").Html())
		log.Println("Token not found for", g.Uri)
	}
	return
}

func (g *Game) ReadThread(page int) error {
	fmt.Println("Reading page", page)
	resp, err := g.Client.Http.Get(fmt.Sprintf(g.Uri+"/%v", page))
	if err != nil {
		g.LastPageProcessed = 0
		log.Printf("Error reading page %v: (Client.Http.Get) %v", page, err)
		return err
	}
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		g.LastPageProcessed = 0
		log.Printf("Error reading page %v: (goquery.NewDocumentFromReader) %v", page, err)
		return err
	}
	pag, _ := doc.Find("#pagina").Attr("value")
	pagina, err := strconv.Atoi(pag)
	if err != nil {
		g.LastPageProcessed = 0
		log.Printf("Error reading page %v: (strconv.Atoi) %v", page, err)
		return err
	}
	if pagina != page {
		log.Printf("Finished reading thread, last page: %d", g.LastPageProcessed)
		return fmt.Errorf("EOF")
	}
	doc.Find(".cf.post").Each(func(i int, s *goquery.Selection) {
		author, _ := s.Attr("data-autor")
		snum, _ := s.Attr("data-num")
		num, err := strconv.Atoi(snum)
		if err != nil {
			panic(err)
		}
		if num <= g.LastMsg {
			return
		}
		g.Messages = append(g.Messages, Message{
			Author: strings.ToLower(author),
			Body:   s.Find(".post-contents").Text(),
		})
		g.LastMsg = num
	})
	g.LastPageProcessed = page
	return g.ReadThread(page + 1)
}

func (g *Game) moar() (bool, error) {
	_, tid, _, crsftoken := g.Info()
	url := fmt.Sprintf("https://www.mediavida.com/foro/moar.php?token=%v&tid=%v&last=%v", crsftoken, tid, g.LastMsg)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest") // this is the magic
	resp, err := g.Client.Http.Do(req)
	if err != nil {
		return false, err
	}
	var data map[string]interface{}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("error reading body: %v", err)
	}
	err = json.Unmarshal(body, &data)
	if err != nil {
		return false, fmt.Errorf("error unmarshaling json: %v body: %v", err, string(body))
	}
	moar := data["moar"].(float64)
	newMessages := false
	log.Println(string(body))
	if moar != 0 {
		page := g.LastPageProcessed
		if page == 0 {
			page = 1
		}
		g.Messages = []Message{}
		err = g.ReadThread(page)
		if err != nil && err.Error() != "EOF" {
			log.Printf("Error reading thread: %v", err)
		}
		newMessages = true
	}
	return newMessages, nil
}

func (g *Game) Poll() {
	for {
		msgs, err := g.moar()
		if err != nil && err.Error() != "EOF" {
			log.Printf("Error reading thread: %v", err)
		}
		if msgs {
			log.Println("New messages")
			// Send the last 10 messages, we do not want to broadcast a whole Tabern
			messages := g.Messages
			if len(messages) > 10 {
				messages = messages[len(messages)-10:]
			}
			for _, msg := range messages {
				g.Report <- NewMessage{
					URI:     g.Uri,
					Content: msg,
				}
			}
		}
		g.Persist()
		time.Sleep(g.Frequency)
		subscriptionsLock.RLock()
		ids, ok := subscriptions[g.Uri]
		subscriptionsLock.RUnlock()
		if !ok || len(ids) == 0 {
			log.Printf("No more subscriptions for %v, shutting down client", g.Uri)
			return
		}
	}
}

// Persist saves the g.LastMsg and g.LastPageProcessed to a file which is named by hashing the g.Uri
func (g *Game) Persist() {
	hash := generateHash(g.Uri)
	fileName := hash + ".save"
	file, err := os.Create(fmt.Sprintf("saves/%v", fileName))
	if err != nil {
		log.Printf("Error creating file: %v", err)
		return
	}
	defer file.Close()
	_, err = file.WriteString(fmt.Sprintf("%v\n%v", g.LastMsg, g.LastPageProcessed))
	if err != nil {
		log.Printf("Error writing to file: %v", err)
		return
	}
}

func (g *Game) Load() {
	hash := generateHash(g.Uri)
	fileName := hash + ".save"
	file, err := os.Open(fmt.Sprintf("saves/%v", fileName))
	if err == nil {
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			log.Printf("Error reading file: %v", err)
			return
		}
		lines := strings.Split(string(data), "\n")
		if len(lines) >= 2 {
			lastMsg, err := strconv.Atoi(lines[0])
			if err != nil {
				log.Printf("Error converting last message to int: %v", err)
				return
			}
			g.LastMsg = lastMsg
			lastPageProcessed, err := strconv.Atoi(lines[1])
			if err != nil {
				log.Printf("Error converting last page processed to int: %v", err)
				return
			}
			g.LastPageProcessed = lastPageProcessed
		}
	}
}

// generateHash generates a hash of the given string using MD5 algorithm
func generateHash(s string) string {
	h := md5.New()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}
