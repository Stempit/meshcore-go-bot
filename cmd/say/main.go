package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode"
)

func main() {
	filename := flag.String("file", "", "путь к файлу для отслеживания")
	flag.Parse()

	if *filename == "" {
		log.Fatal("укажите файл с помощью флага -file")
	}

	// Проверяем наличие утилиты синтеза речи
	if err := checkTTS(); err != nil {
		log.Fatalf("ошибка: %v", err)
	}

	// Обработка сигналов для graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Запускаем слежение за файлом
	go func() {
		if err := tailFile(*filename); err != nil {
			log.Printf("ошибка при слежении: %v", err)
			os.Exit(1)
		}
	}()

	<-sigChan
	log.Println("завершение работы")
}

// checkTTS проверяет наличие утилиты для синтеза речи
func checkTTS() error {
	var cmd string
	switch runtime.GOOS {
	case "linux":
		cmd = "espeak"
	case "darwin":
		cmd = "say"
	default:
		return fmt.Errorf("неподдерживаемая ОС: %s", runtime.GOOS)
	}
	_, err := exec.LookPath(cmd)
	if err != nil {
		return fmt.Errorf("утилита %s не найдена, установите её (например, 'sudo apt install espeak' для Linux)", cmd)
	}
	return nil
}

// speak произносит текст через динамики
func speak(text string) error {
	// "--" завершает список опций: иначе текст, начинающийся с '-',
	// будет воспринят say/espeak как флаг командной строки.
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("espeak", "--", text)
	case "darwin":
		cmd = exec.Command("say", "--", text)
	default:
		return fmt.Errorf("неподдерживаемая ОС")
	}
	return cmd.Run()
}

// tailFile следит за файлом и озвучивает новые строки.
//
// Файл переоткрывается по пути на каждой итерации, а позиция чтения хранится
// в виде логического смещения offset. Это важно, потому что rsync обновляет
// файл не дописыванием, а через временный файл + атомарный rename(), то есть
// каждый раз создаётся новый inode. Держать один открытый дескриптор нельзя —
// он остался бы на старом, уже удалённом inode и не видел бы обновлений.
func tailFile(filename string) error {
	var offset int64    // сколько байт уже обработано
	var leftover []byte // остаток от предыдущего чтения (неполная строка без '\n')

	// Стартуем с конца текущего файла, чтобы не озвучивать всю историю.
	if info, err := os.Stat(filename); err == nil {
		offset = info.Size()
	}

	for {
		info, err := os.Stat(filename)
		if err != nil {
			// Во время rename файл на мгновение может отсутствовать — ждём.
			if os.IsNotExist(err) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return err
		}
		size := info.Size()

		// Файл стал меньше (заменён/обрезан/ротация) — читаем с начала.
		if size < offset {
			offset = 0
			leftover = nil
		}

		if size > offset {
			data, n, err := readFrom(filename, offset, size-offset)
			if err != nil {
				return err
			}
			offset += int64(n)

			data = append(leftover, data...)
			leftover = nil

			// Разбиваем на строки. Полные строки (с '\n') озвучиваем,
			// хвост без '\n' сохраняем как остаток до следующего чтения.
			lines := splitLines(data)
			for _, line := range lines {
				if len(line) > 0 && line[len(line)-1] == '\n' {
					raw := strings.TrimRight(string(line), "\r\n")
					text := restoreCyrillic(formatLine(raw))
					if text != "" {
						log.Printf("Озвучиваю: %s", text)
						if err := speak(text); err != nil {
							log.Printf("ошибка озвучивания: %v", err)
						}
					}
				} else {
					// неполная строка — сохраняем как остаток
					leftover = line
				}
			}
		}

		time.Sleep(100 * time.Millisecond) // опрос с интервалом 100 мс
	}
}

// readFrom переоткрывает файл, читает не более max байт начиная с offset
// и возвращает прочитанные данные. Отдельная функция гарантирует, что
// дескриптор закрывается сразу после чтения (а не висит между итерациями).
func readFrom(filename string, offset, max int64) ([]byte, int, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, err
	}

	buf := make([]byte, max)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, 0, err
	}
	return buf[:n], n, nil
}

// lineRe разбирает строку лога вида
//
//	<< 2026-06-17 06:52:46 [xISPx]: Пинг
//	>> 2026-06-17 06:52:59 [Me]: @[xISPx] TTL не истёк.
//
// на: направление (<< входящее / >> исходящее), имя отправителя и текст.
var lineRe = regexp.MustCompile(`^(<<|>>)\s+\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}\s+\[(.+?)\]:\s*(.*)$`)

// mentionRe выделяет обращение в начале текста: @[xISPx] или @xISPx.
var mentionRe = regexp.MustCompile(`^@\[?([^\]\s]+)\]?\s*(.*)$`)

// latToCyrMap — обратное к cyrLatMap из internal/shrink: латинские буквы-двойники
// возвращаются в кириллицу. shrink заменяет кириллицу на визуально идентичную
// латиницу ради экономии байт, из-за чего синтезатор читает буквы по-английски.
// Преобразование частично теряет регистр-нейтральные различия (ё→е), что для
// озвучивания несущественно.
var latToCyrMap = map[rune]rune{
	'a': 'а', 'e': 'е', 'o': 'о', 'c': 'с', 'p': 'р', 'y': 'у', 'x': 'х',
	'A': 'А', 'B': 'В', 'E': 'Е', 'K': 'К', 'M': 'М', 'H': 'Н',
	'O': 'О', 'P': 'Р', 'C': 'С', 'T': 'Т', 'Y': 'У', 'X': 'Х',
}

func isCyrillic(r rune) bool {
	return r >= 0x0400 && r <= 0x04FF
}

// restoreCyrillic возвращает латинские буквы-двойники обратно в кириллицу внутри
// русских слов, не трогая настоящие латинские слова (pong, TTL, ник xISPx).
// Слово конвертируется, если содержит хотя бы одну кириллическую букву либо
// является одиночной буквой-двойником (предлоги «с», «у», «о»…).
func restoreCyrillic(s string) string {
	var b strings.Builder
	var word []rune

	flush := func() {
		if len(word) == 0 {
			return
		}
		hasCyr := false
		for _, r := range word {
			if isCyrillic(r) {
				hasCyr = true
				break
			}
		}
		convert := hasCyr
		if !convert && len(word) == 1 {
			_, convert = latToCyrMap[word[0]]
		}
		for _, r := range word {
			if convert {
				if c, ok := latToCyrMap[r]; ok {
					r = c
				}
			}
			b.WriteRune(r)
		}
		word = word[:0]
	}

	for _, r := range s {
		if unicode.IsLetter(r) {
			word = append(word, r)
		} else {
			flush()
			b.WriteRune(r)
		}
	}
	flush()
	return b.String()
}

// formatLine превращает строку лога в человекочитаемую фразу для озвучивания.
// Нераспознанные строки возвращаются как есть.
func formatLine(line string) string {
	m := lineRe.FindStringSubmatch(line)
	if m == nil {
		return line
	}
	// Направление (<< / >>) игнорируем — об ответе говорит только обращение @.
	name, msg := m[2], strings.TrimSpace(m[3])

	// "Me" — это наш бот, озвучиваем как Йорик.
	if name == "Me" {
		name = "Йорик"
	}

	// Если сообщение начинается с обращения @, это ответ —
	// проговариваем, кому именно отвечаем.
	if mm := mentionRe.FindStringSubmatch(msg); mm != nil {
		target := mm[1]
		rest := strings.TrimSpace(mm[2])
		return fmt.Sprintf("%s ответил %s %s", name, target, rest)
	}

	return fmt.Sprintf("%s написал %s", name, msg)
}

// splitLines разбивает данные на строки, сохраняя завершающий символ '\n'.
// Возвращает срез байтовых срезов, каждый элемент — строка с '\n' в конце,
// кроме, возможно, последнего, который может быть без '\n' (неполная строка).
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i+1])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
