package commands

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
)

var firstWords = []string{
	"Весёлый", "Хитрый", "Бодрый", "Ленивый", "Загадочный",
	"Дребезжащий", "Шипящий", "Свистящий", "Гудящий", "Потрескивающий",
	"Кряхтящий", "Фонящий", "Несущий", "Заземлённый", "Перегруженный",
	"Обесточенный", "Заряженный", "Разряженный", "Колеблющийся", "Резонансный",
	"Усиленный", "Ослабленный", "Согласованный", "Закорачивающий", "Искрящий",
	"Радиоактивный", "Самодельный", "Паяный", "Канифольный", "Импульсный",
}

var secondWords = []string{
	"Транзистор", "Конденсатор", "Резистор", "Диод", "Светодиод",
	"Дроссель", "Трансформатор", "Кварц", "Резонатор", "Гетеродин",
	"Смеситель", "Усилитель", "Генератор", "Модулятор", "Детектор",
	"Приёмник", "Передатчик", "Трансивер", "Фидер", "Диполь",
	"Штырь", "Сердечник", "Феррит", "Припой", "Эфир",
	"Паяльник", "Сигнал", "SNR", "Моксон",
}

type TicketStore struct {
	mu      sync.Mutex
	tickets map[string]string // senderKey -> phrase
	path    string
}

var ticketStore *TicketStore

// InitTicketStore loads (or creates) the ticket persistence file.
func InitTicketStore(path string) error {
	ts := &TicketStore{
		tickets: make(map[string]string),
		path:    path,
	}
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("opening ticket store: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		key, phrase, ok := strings.Cut(line, "\t")
		if !ok || key == "" || phrase == "" {
			continue
		}
		ts.tickets[key] = phrase
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	ticketStore = ts
	return nil
}

func (ts *TicketStore) getOrCreate(senderKey string) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	key := printableKey(senderKey)
	if phrase, ok := ts.tickets[key]; ok {
		return phrase, nil
	}
	phrase := firstWords[rand.IntN(len(firstWords))] + " " + secondWords[rand.IntN(len(secondWords))]
	ts.tickets[key] = phrase
	f, err := os.OpenFile(ts.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return "", fmt.Errorf("writing ticket store: %w", err)
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\t%s\n", key, phrase)
	return phrase, err
}

func (ts *TicketStore) random() (string, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.tickets) == 0 {
		return "", false
	}
	phrases := make([]string, 0, len(ts.tickets))
	for _, p := range ts.tickets {
		phrases = append(phrases, p)
	}
	return phrases[rand.IntN(len(phrases))], true
}

// printableKey returns senderKey as a readable string.
// bot.go passes raw public-key bytes cast to string for DMs, so we hex-encode
// those; plain channel names (already ASCII) pass through unchanged.
func printableKey(key string) string {
	for _, b := range []byte(key) {
		if b < 32 || b > 126 {
			return hex.EncodeToString([]byte(key))
		}
	}
	return key
}

// ProcessTicketCommand handles the .ticket command.
func ProcessTicketCommand(text, senderKey string) (string, error) {
	if ticketStore == nil {
		return "ticket store not initialized", nil
	}
	arg := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(text)), ".ticket"))
	if arg == "random" {
		phrase, ok := ticketStore.random()
		if !ok {
			return "Нет позывных", nil
		}
		return "Выбран " + phrase, nil
	}
	if senderKey == "" {
		return "Нет твоего ключа, отправь адверт", nil
	}
	phrase, err := ticketStore.getOrCreate(senderKey)
	if err != nil {
		return "", err
	}
	return "Твой позывной - " + phrase, nil
}
