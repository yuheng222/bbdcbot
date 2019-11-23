package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"github.com/joho/godotenv"
)

func main() {
	if os.Getenv("IS_HEROKU") != "TRUE" {
		loadEnvironmentalVariables()
	}

	//set up telegram info
	bot, err := tgbotapi.NewBotAPI(os.Getenv("TELEGRAM_TOKEN"))
	errCheck(err, "Failed to start telegram bot")
	log.Printf("Authorized on account %s", bot.Self.UserName)
	chatIDs, err := parseChatIDList((os.Getenv("CHAT_ID")))
	errCheck(err, "Failed to fetch chat IDs")

	client := &http.Client{}
	jar := &myjar{} // add cookie jar so you can store the session ID cookie
	jar.jar = make(map[string][]*http.Cookie)
	client.Jar = jar

	tgclient := AlertService{Bot: bot, ReceiverIDs: chatIDs}

	//for heroku
	go func() {
		http.ListenAndServe(":"+os.Getenv("PORT"),
			http.HandlerFunc(http.NotFound))
	}()
	for {
		//fetching cookies
		log.Println("Logging in")
		err = logIn(os.Getenv("NRIC"), os.Getenv("PASSWORD"), client)
		errCheck(err, "Error logging in")

		rawSlots, err := slotPage(os.Getenv("ACCOUNT_ID"), client)
		errCheck(err, "Error getting slot page")

		slots, err := extractSlots(rawSlots)
		errCheck(err, "Error parsing slot page")

		valids := validSlots(slots)
		for _, validSlot := range valids { //for all the slots which meet the rule (i.e. within 10 days of now)
			tgclient.MessageAll("Slot available on " + validSlot.Date.Format("_2 Jan 2006 (Mon)") + " " + os.Getenv("SESSION_"+validSlot.SessionNumber))
		}
		if len(valids) != 0 {
			tgclient.MessageAll("Finished getting slots")
		}

		r := rand.Intn(300) + 120
		time.Sleep(time.Duration(r) * time.Second)
	}

}

// returns slots that should be autobooked/alerted about
func validSlots(slots []DrivingSlot) []DrivingSlot {
	valids := make([]DrivingSlot, 0)
	for _, slot := range slots {
		if slot.Date.Sub(time.Now()) < 10*(24*time.Hour) { // if slot is within 10 days of now
			if slot.Date.Sub(time.Now()) > 1*(24*time.Hour) { // if slot is more than 24 hours from now
				if slot.SessionNumber != "1" { // slot can't be today
					valids = append(valids, slot)
				}
			}

		}
	}
	return valids
}

type myjar struct {
	jar map[string][]*http.Cookie
}

func (p *myjar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	fmt.Printf("The URL is : %s\n", u.String())
	fmt.Printf("The cookie being set is : %s\n", cookies)
	p.jar[u.Host] = cookies
}

func (p *myjar) Cookies(u *url.URL) []*http.Cookie {
	fmt.Printf("The URL is : %s\n", u.String())
	fmt.Printf("Cookie being returned is : %s\n", p.jar[u.Host])
	return p.jar[u.Host]
}

// DrivingSlot represents a CDC slot to go for driving lessons
type DrivingSlot struct {
	Date          time.Time
	SessionNumber string
}

// AlertService is a service for alerting many telegram users
type AlertService struct {
	Bot         *tgbotapi.BotAPI
	ReceiverIDs []int64
}

// MessageAll sends a message to all chats in the alert service
func (as *AlertService) MessageAll(msg string) {
	for _, chatID := range as.ReceiverIDs {
		alert(msg, as.Bot, chatID)
	}
}

func parseChatIDList(list string) ([]int64, error) {
	chatIDStrings := strings.Split(list, ",")
	chatIDs := make([]int64, len(chatIDStrings))
	for i, chatIDString := range chatIDStrings {
		chatID, err := strconv.ParseInt(strings.TrimSpace(chatIDString), 10, 64)
		chatIDs[i] = chatID
		if err != nil {
			return nil, err
		}
	}
	return chatIDs, nil
}

func logIn(nric string, pwd string, client *http.Client) error {
	loginForm := url.Values{}
	loginForm.Add("txtNRIC", nric)
	loginForm.Add("txtpassword", pwd)
	loginForm.Add("btnLogin", "ACCESS+TO+BOOKING+SYSTEM")
	req, err := http.NewRequest("POST", "http://www.bbdc.sg/bbdc/bbdc_web/header2.asp", strings.NewReader(loginForm.Encode()))
	if err != nil {
		return errors.New("Error creating request: " + err.Error())
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	_, err = client.Do(req)
	if err != nil { // not checking for incorrect password, for fully secure version do check that in the response
		return errors.New("Error sending request: " + err.Error())
	}
	return nil
}

func extractSlots(slotPage string) ([]DrivingSlot, error) {
	// parse booking page to get booking dates
	// The data is hidden away in the following function call in the HTML page
	// fetched:
	// doTooltipV(event,0, "03/05/2019 (Fri)","3","11:30","13:10","BBDC");

	slotSections := strings.Split(slotPage, "doTooltipV(")[1:]
	slots := make([]DrivingSlot, 0)
	for _, slotSection := range slotSections {
		bookingData := strings.Split(slotSection, ",")[0:6]
		sessionNum := strings.Split(bookingData[3], "\"")[1] // strip of quotes
		rawDay := bookingData[2]                             // looks like  "03/05/2019 (Fri)"
		layout := "02/01/2006"
		day, err := time.Parse(layout, strings.Split(strings.Split(rawDay, "\"")[1], " ")[0]) // strip of quotes and remove the `(Fri)`
		if err != nil {
			return nil, errors.New("Error parsing date: " + err.Error())
		}
		slots = append(slots, DrivingSlot{Date: day, SessionNumber: sessionNum})
	}

	return slots, nil
}

func slotPage(accountID string, client *http.Client) (string, error) {
	req, err := http.NewRequest("POST", "http://www.bbdc.sg/bbdc/b-3c-pLessonBooking1.asp",
		strings.NewReader(bookingForm(accountID).Encode()))
	if err != nil {
		return "", errors.New("Error creating request: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("Error sending request: " + err.Error())
	}
	body, _ := ioutil.ReadAll(resp.Body)
	// ioutil.WriteFile("booking.txt", body, 0644)
	return string(body), nil
}

func alert(msg string, bot *tgbotapi.BotAPI, chatID int64) {
	telegramMsg := tgbotapi.NewMessage(chatID, msg)
	bot.Send(telegramMsg)
	log.Println("Sent message to " + strconv.FormatInt(chatID, 10) + ": " + msg)
}

func loadEnvironmentalVariables() {
	err := godotenv.Load()
	if err != nil {
		log.Print("Error loading environmental variables: ")
		log.Fatal(err.Error())
	}
}

func fetchCookies() (*http.Cookie, *http.Cookie) {
	resp, err := http.Get("http://www.bbdc.sg/bbweb/default.aspx")
	errCheck(err, "Error fetching cookies")
	aspxanon := resp.Cookies()[0]
	resp, err = http.Get("http://www.bbdc.sg/bbdc/bbdc_web/newheader.asp")
	errCheck(err, "Error fetching cookies (sessionID)")
	sessionID := resp.Cookies()[0]
	return aspxanon, sessionID
}

func paymentForm(slotID string) url.Values {
	form := url.Values{}
	form.Add("accId", os.Getenv("ACCOUNT_ID"))
	form.Add("slot", slotID)

	return form
}

func bookingForm(accountID string) url.Values {
	bookingForm := url.Values{}
	bookingForm.Add("accId", os.Getenv(accountID))
	bookingForm.Add("Month", "Nov/2019")
	bookingForm.Add("Month", "Dec/2019")
	bookingForm.Add("Month", "Jan/2020")
	bookingForm.Add("Session", "1")
	bookingForm.Add("Session", "2")
	bookingForm.Add("Session", "3")
	bookingForm.Add("Session", "4")
	bookingForm.Add("Session", "5")
	bookingForm.Add("Session", "6")
	bookingForm.Add("Session", "7")
	bookingForm.Add("Session", "8")
	bookingForm.Add("allSes", "on")
	bookingForm.Add("Day", "2")
	bookingForm.Add("Day", "3")
	bookingForm.Add("Day", "4")
	bookingForm.Add("Day", "5")
	bookingForm.Add("Day", "6")
	bookingForm.Add("Day", "7")
	bookingForm.Add("Day", "1")
	bookingForm.Add("allDay", "")
	bookingForm.Add("defPLVenue", "1")
	bookingForm.Add("optVenue", "1")

	return bookingForm
}

func errCheck(err error, msg string) {
	if err != nil {
		log.Fatal(msg + ": " + err.Error())
	}
}
