// Package eightball answers .8ball with a random Magic 8-Ball phrase.
package eightball

import (
	"math/rand"
	"time"

	"meshbot/internal/support/registry"
)

// eightBallAnswers are the 20 classic Magic 8-Ball responses, translated to
// Russian. The original English phrase is kept in a comment after each one.
var eightBallAnswers = []string{
	// --- Affirmative ---
	"Бесспорно",                 // It is certain
	"Предрешено",                // It is decidedly so
	"Никаких сомнений",          // Without a doubt
	"Определённо да",            // Yes definitely
	"Можешь быть уверен в этом", // You may rely on it
	"Как я вижу, да",            // As I see it, yes
	"Скорее всего",              // Most likely
	"Хорошие перспективы",       // Outlook good
	"Да",                        // Yes
	"Знаки говорят — да",        // Signs point to yes

	// --- Non-committal ---
	"Пока не ясно, попробуй снова", // Reply hazy, try again
	"Спроси позже",                 // Ask again later
	"Лучше не рассказывать сейчас", // Better not tell you now
	"Сейчас нельзя предсказать",    // Cannot predict now
	"Сосредоточься и спроси опять", // Concentrate and ask again

	// --- Negative ---
	"Даже не надейся",      // Don't count on it
	"Мой ответ — нет",      // My reply is no
	"По моим данным — нет", // My sources say no
	"Перспективы не очень", // Outlook not so good
	"Весьма сомнительно",   // Very doubtful
}

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "8ball",
		Triggers:  []string{".8ball"},
		Match:     registry.Prefix,
		HelpShort: "magic 8-ball",
		HelpLong:  ".8ball <вопрос>\nмагический шар отвечает да/нет",
		Group:     "Other",
	}
}

// Handle answers a yes/no question with a random Magic 8-Ball phrase. The
// question text itself is ignored — like the classic toy.
func (plugin) Handle(c registry.Ctx) ([]string, error) {
	question := c.Arg(".8ball")
	if question == "" {
		return []string{"задай вопрос: .8ball <вопрос>"}, nil
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return []string{"🎱 " + eightBallAnswers[r.Intn(len(eightBallAnswers))]}, nil
}
