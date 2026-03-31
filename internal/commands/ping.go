package commands

import (
	"math/rand"
	"time"
)

// PingMeta holds path, timing, and location metadata from the incoming ping message.
type PingMeta struct {
	PathLength int    // hop count from message wire header (0xFF = direct delivery)
	PathBytes  []byte // path node IDs from sender's contact (may be nil for channel messages)
	Timestamp  time.Time
	SenderLat  *float64     // sender GPS latitude (nil if unknown)
	SenderLon  *float64     // sender GPS longitude (nil if unknown)
	SelfLat    float64      // own GPS latitude (0 if unknown)
	SelfLon    float64      // own GPS longitude (0 if unknown)
	HopCoords  [][2]float64 // lat/lon for each intermediate hop; nil entry slot = unknown position
	HopKnown   []bool       // true when HopCoords[i] is valid
}

func getRandomString() string {
	strings := []string{
		"Pong",
		"Latency is high, please hold",
		"Acknowledgment packet loss",
		"200 OK",
		"Понг",
		"Я бы ответил, но у меня буферизация",
		"Ваш запрос принят, ожидайте ответа. Или нет.",
		"Биииип",
		"Вас слышно",
		"Есть контакт",
		"Ваш пинг достиг цели",
		"(отвечает эхом) Пинг... Пинг... Пинг...",
		"Связь есть",
		"Есть связь",
		"418",
		"Есть сигнал, качество - как у любви в 16 лет",
		"Понг, счет 15:40 в мою пользу",
		"(стучит азбукой Морзе) .--. --- -. --.",
		"Ваш пинг успешно поставлен в очередь",
		"А если не отвечу, пошлете еще один?",
		"(ловит пакет и прячет под подушку)",
		"502 Bad Gateway",
		"Принял. Теперь нам с этим жить.",
		"Понг, последний предупредительный.",
		"(поднимает табличку \"PONG\" и прячется обратно в кусты)",
		"Пинг принял, понг отправил",
		"Есть сигнал, шумность - как у соседа с перфоратором",
		"Принято, теперь он в коллекции \"1000 и 1 пинг\"",
		"(пересылает пинг кошке) Кошка сказала \"мяу\", что в переводе \"понг\"",
		"Понг. Но только половина. Остальное дойдет к утру.",
		"Пинг прошел через три стены, два репитера и одного кота. Ответ будет пушистым.",
		"Пинг успешно списан. Кэшбек - один понг.",
		"Ваш пинг прошел огонь, воду и медные трубы. Награда - понг.",
		"Ваш пинг выиграл в лотерею. Приз - понг.",
		"Пинг принят и отправлен на рецензию.",
		"Понг. Можете считать это официальным подтверждением.",
	}
	// инициализация генератора случайных чисел
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	idx := r.Intn(len(strings))
	return strings[idx]
}

func ProcessPingMessage() (string, error) {
	return getRandomString(), nil
}
