package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

var (
	db *sql.DB
	mu sync.Mutex
)

// Card represents a playing card.
type Card struct {
	Code  string `json:"code"`
	Rank  string `json:"rank"`
	Suit  string `json:"suit"`
	Image string `json:"image"`
}

// DrawnCard represents a drawn card with the draw time.
type DrawnCard struct {
	Code string `json:"code"`
	Time string `json:"time"`
}

// Deck represents a card deck.
type Deck struct {
	ID        string `json:"deck_id"`
	Cards     []Card `json:"cards,omitempty"`
	Remaining int    `json:"remaining"`
}

// Request represents a request for deck operations.
type Request struct {
	Type    string
	DeckID  string
	Params  []string
	ReplyCh chan Response
}

// Response represents a response from deck operations.
type Response struct {
	Deck  Deck
	Drawn []DrawnCard
	Error error
}

func main() {
	var err error
	db, err = sql.Open("sqlite3", "./deck.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	createTable()

	http.HandleFunc("/deck/new/", createDeck)
	http.HandleFunc("/deck/", handleDeckRequests)

	go handleRequests()

	log.Fatal(http.ListenAndServe(":8080", nil))
}

func handleRequests() {
	for req := range requestChannel {
		switch req.Type {
		case "draw":
			drawCards(req)
		case "shuffle":
			shuffleDeck(req)
		}
	}
}

var requestChannel = make(chan Request)

func createTable() {
	sqlStmt := `CREATE TABLE IF NOT EXISTS decks (
		id TEXT PRIMARY KEY,
		cards TEXT,
		piged TEXT,  -- Drawn cards
		upcoming TEXT -- Upcoming cards to be drawn
	);`
	_, err := db.Exec(sqlStmt)
	if err != nil {
		log.Fatalf("Error creating table: %v", err)
	}
}

func createDeck(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()

	nbrPaquet := 1
	jokers := false

	// Read parameters from URL
	params := r.URL.Path[len("/deck/new/"):]
	parts := strings.Split(params, "/")

	if len(parts) > 0 {
		if p, err := strconv.Atoi(parts[0]); err == nil {
			nbrPaquet = p
		}
	}
	if len(parts) > 1 {
		jokers = parts[1] == "true"
	}

	if nbrPaquet > 10 {

		http.Error(w, "Too many Deckes", http.StatusInternalServerError)
		return
	}

	deckID := uuid.New().String()
	cards := generateCards(nbrPaquet, jokers)

	cardsJSON, _ := json.Marshal(cards)
	_, err := db.Exec("INSERT INTO decks (id, cards, piged, upcoming) VALUES (?, ?, ?, ?)", deckID, string(cardsJSON), "[]", string(cardsJSON))
	if err != nil {
		http.Error(w, "Error creating deck", http.StatusInternalServerError)
		return
	}

	response := Deck{
		ID:        deckID,
		Cards:     cards,
		Remaining: len(cards),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func generateCards(nbrPaquet int, jokers bool) []Card {
	var cards []Card
	ranks := []string{"2", "3", "4", "5", "6", "7", "8", "9", "10", "j", "q", "k", "a"}
	suits := []string{"h", "d", "c", "s"}

	for i := 0; i < nbrPaquet; i++ {
		for _, suit := range suits {
			for _, rank := range ranks {
				code := rank + suit
				cards = append(cards, Card{
					Code:  code,
					Rank:  rank,
					Suit:  suit,
					Image: fmt.Sprintf("/static/%s.svg", code),
				})
			}
		}
		if jokers {
			cards = append(cards, Card{Code: "joker", Rank: "joker", Suit: "", Image: "/static/joker.svg"})
			cards = append(cards, Card{Code: "joker", Rank: "joker", Suit: "", Image: "/static/joker.svg"})
		}
	}
	return cards
}

func handleDeckRequests(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/deck/"), "/")

	if len(parts) < 1 {
		http.Error(w, "Invalid deck ID", http.StatusBadRequest)
		return
	}

	deckID := parts[0]

	switch r.Method {
	case http.MethodPost:
		if len(parts) > 1 && parts[1] == "add" {
			addCards(w, deckID, r.URL.Query().Get("cards"))
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

	case http.MethodGet:
		if len(parts) > 1 {
			action := parts[1]
			switch action {
			case "draw":
				if len(parts) < 3 {
					http.Error(w, "Draw action requires parameters", http.StatusBadRequest)
					return
				}
				drawReq := Request{
					Type:    "draw",
					DeckID:  deckID,
					Params:  []string{parts[2]},
					ReplyCh: make(chan Response),
				}
				requestChannel <- drawReq
				resp := <-drawReq.ReplyCh
				handleResponse(w, resp)
				return
			case "shuffle":
				shuffleReq := Request{
					Type:    "shuffle",
					DeckID:  deckID,
					ReplyCh: make(chan Response),
				}
				requestChannel <- shuffleReq
				resp := <-shuffleReq.ReplyCh
				handleResponse(w, resp)
				return
			case "show":
				if len(parts) < 4 {
					http.Error(w, "Show action requires parameters", http.StatusBadRequest)
					return
				}
				showType := parts[2]
				countStr := parts[3]
				if showType == "0" {
					showDrawnCards(w, deckID, countStr)
				} else if showType == "1" {
					showUpcomingCards(w, deckID, countStr)
				} else {
					http.Error(w, "Invalid show type", http.StatusBadRequest)
				}
				return
			}
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func drawCards(req Request) {
	mu.Lock()
	defer mu.Unlock()

	nbrCarte, err := strconv.Atoi(req.Params[0])
	if err != nil || nbrCarte < 1 {
		req.ReplyCh <- Response{Error: fmt.Errorf("Invalid number of cards")}
		return
	}

	var upcomingJSON string
	row := db.QueryRow("SELECT upcoming FROM decks WHERE id = ?", req.DeckID)
	if err := row.Scan(&upcomingJSON); err != nil {
		req.ReplyCh <- Response{Error: fmt.Errorf("Deck not found")}
		return
	}

	var upcomingCards []Card
	if err := json.Unmarshal([]byte(upcomingJSON), &upcomingCards); err != nil {
		req.ReplyCh <- Response{Error: fmt.Errorf("Error parsing upcoming cards")}
		return
	}

	if len(upcomingCards) == 0 {
		req.ReplyCh <- Response{Error: fmt.Errorf("Deck empty")}
		return
	}

	if nbrCarte > len(upcomingCards) {
		nbrCarte = len(upcomingCards)
	}

	drawnCards := upcomingCards[:nbrCarte]
	upcomingCards = upcomingCards[nbrCarte:]

	var drawnWithTime []DrawnCard
	for _, card := range drawnCards {
		drawnWithTime = append(drawnWithTime, DrawnCard{
			Code: card.Code,
			Time: time.Now().Format(time.RFC3339),
		})
	}

	// Update the database with the new upcoming cards
	updatedUpcomingJSON, err := json.Marshal(upcomingCards)
	if err != nil {
		req.ReplyCh <- Response{Error: fmt.Errorf("Error marshalling upcoming cards")}
		return
	}

	if _, err := db.Exec("UPDATE decks SET upcoming = ? WHERE id = ?", string(updatedUpcomingJSON), req.DeckID); err != nil {
		req.ReplyCh <- Response{Error: fmt.Errorf("Error updating deck")}
		return
	}

	response := Deck{
		ID:        req.DeckID,
		Cards:     drawnCards,
		Remaining: len(upcomingCards),
	}

	req.ReplyCh <- Response{Deck: response}
}

func shuffleDeck(req Request) {
	mu.Lock()
	defer mu.Unlock()

	var upcomingJSON string
	row := db.QueryRow("SELECT upcoming FROM decks WHERE id = ?", req.DeckID)
	if err := row.Scan(&upcomingJSON); err != nil {
		req.ReplyCh <- Response{Error: fmt.Errorf("Deck not found")}
		return
	}

	var upcomingCards []Card
	if err := json.Unmarshal([]byte(upcomingJSON), &upcomingCards); err != nil {
		req.ReplyCh <- Response{Error: fmt.Errorf("Error parsing upcoming cards")}
		return
	}

	// Shuffle the cards
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(upcomingCards), func(i, j int) {
		upcomingCards[i], upcomingCards[j] = upcomingCards[j], upcomingCards[i]
	})

	updatedUpcomingJSON, err := json.Marshal(upcomingCards)
	if err != nil {
		req.ReplyCh <- Response{Error: fmt.Errorf("Error marshalling upcoming cards")}
		return
	}

	if _, err := db.Exec("UPDATE decks SET upcoming = ? WHERE id = ?", string(updatedUpcomingJSON), req.DeckID); err != nil {
		req.ReplyCh <- Response{Error: fmt.Errorf("Error updating deck")}
		return
	}

	response := Deck{
		ID:        req.DeckID,
		Cards:     upcomingCards,
		Remaining: len(upcomingCards),
	}

	req.ReplyCh <- Response{Deck: response}
}

func addCards(w http.ResponseWriter, deckID string, cardsStr string) {
	mu.Lock()
	defer mu.Unlock()

	var existingCards []Card
	var upcomingCards []Card
	row := db.QueryRow("SELECT cards, upcoming FROM decks WHERE id = ?", deckID)
	var cardsJSON, upcomingJSON string
	if err := row.Scan(&cardsJSON, &upcomingJSON); err != nil {
		http.Error(w, "Deck not found", http.StatusNotFound)
		return
	}

	json.Unmarshal([]byte(cardsJSON), &existingCards)
	json.Unmarshal([]byte(upcomingJSON), &upcomingCards)

	newCards := parseCards(cardsStr)
	upcomingCards = append(upcomingCards, newCards...)

	updatedUpcomingJSON, _ := json.Marshal(upcomingCards)
	_, err := db.Exec("UPDATE decks SET upcoming = ? WHERE id = ?", string(updatedUpcomingJSON), deckID)
	if err != nil {
		http.Error(w, "Error adding cards", http.StatusInternalServerError)
		return
	}

	allCards := append(existingCards, upcomingCards...)

	response := Deck{
		ID:        deckID,
		Cards:     allCards,
		Remaining: len(upcomingCards),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func parseCards(cardsStr string) []Card {
	var cards []Card
	for _, card := range strings.Split(cardsStr, ",") {
		cards = append(cards, Card{Code: card})
	}
	return cards
}

func showDrawnCards(w http.ResponseWriter, deckID string, countStr string) {
	mu.Lock()
	defer mu.Unlock()

	var drawnJSON string
	row := db.QueryRow("SELECT piged FROM decks WHERE id = ?", deckID)
	if err := row.Scan(&drawnJSON); err != nil {
		http.Error(w, "Deck not found", http.StatusNotFound)
		return
	}

	var drawnCards []DrawnCard
	if err := json.Unmarshal([]byte(drawnJSON), &drawnCards); err != nil {
		http.Error(w, "Error parsing drawn cards", http.StatusInternalServerError)
		return
	}

	count, err := strconv.Atoi(countStr)
	if err != nil || count < 0 || count > len(drawnCards) {
		http.Error(w, "Invalid count", http.StatusBadRequest)
		return
	}

	response := drawnCards
	if count > 0 {
		response = drawnCards[len(drawnCards)-count:]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func showUpcomingCards(w http.ResponseWriter, deckID string, countStr string) {
	mu.Lock()
	defer mu.Unlock()

	var upcomingJSON string
	row := db.QueryRow("SELECT upcoming FROM decks WHERE id = ?", deckID)
	if err := row.Scan(&upcomingJSON); err != nil {
		http.Error(w, "Deck not found", http.StatusNotFound)
		return
	}

	var upcomingCards []Card
	if err := json.Unmarshal([]byte(upcomingJSON), &upcomingCards); err != nil {
		http.Error(w, "Error parsing upcoming cards", http.StatusInternalServerError)
		return
	}

	count, err := strconv.Atoi(countStr)
	if err != nil || count < 0 || count > len(upcomingCards) {
		http.Error(w, "Invalid count", http.StatusBadRequest)
		return
	}

	response := upcomingCards
	if count > 0 {
		response = upcomingCards[:count]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func handleResponse(w http.ResponseWriter, resp Response) {
	if resp.Error != nil {
		http.Error(w, resp.Error.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp.Deck)
}
