package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	monthNames   = []string{"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", "agosto", "setembro", "outubro", "novembro", "dezembro"}
	weekdayNames = []string{"domingo", "segunda-feira", "terça-feira", "quarta-feira", "quinta-feira", "sexta-feira", "sábado"}
)

type period struct {
	from, to time.Time
	title    string
	slug     string
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func dayPeriod(now time.Time) period {
	from := startOfDay(now)
	return period{from: from, to: from.AddDate(0, 0, 1),
		title: "Hoje, " + weekdayNames[now.Weekday()] + ", " + now.Format("02/01")}
}

func weekPeriod(now time.Time) period {
	from := startOfDay(now).AddDate(0, 0, -((int(now.Weekday()) + 6) % 7))
	to := from.AddDate(0, 0, 7)
	return period{from: from, to: to,
		title: "Semana de " + from.Format("02/01") + " a " + to.AddDate(0, 0, -1).Format("02/01")}
}

func monthPeriod(first time.Time) period {
	return period{from: first, to: first.AddDate(0, 1, 0),
		title: capitalize(monthNames[first.Month()-1]) + " de " + strconv.Itoa(first.Year()),
		slug:  first.Format("2006-01")}
}

func allTime() period {
	return period{from: time.Date(1, 1, 1, 0, 0, 0, 0, time.Local), to: time.Date(9999, 12, 31, 0, 0, 0, 0, time.Local),
		title: "Todos os gastos", slug: "tudo"}
}

func parseMonth(arg string, now time.Time) (period, error) {
	arg = strings.ToLower(strings.TrimSpace(arg))
	loc := now.Location()
	if arg == "" {
		return monthPeriod(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)), nil
	}
	for _, layout := range []string{"2006-01", "01/2006", "1/2006", "01-2006", "1-2006"} {
		if t, err := time.ParseInLocation(layout, arg, loc); err == nil {
			return monthPeriod(t), nil
		}
	}

	name, yearText, _ := strings.Cut(arg, " ")
	month := 0
	if n, err := strconv.Atoi(name); err == nil && n >= 1 && n <= 12 {
		month = n
	}
	plain := strings.NewReplacer("ç", "c").Replace(name)
	for i, m := range monthNames {
		full := strings.NewReplacer("ç", "c").Replace(m)
		if len(plain) >= 3 && strings.HasPrefix(full, plain) {
			month = i + 1
		}
	}
	if month == 0 {
		return period{}, fmt.Errorf("não entendi o mês %q: use 2026-10, 10/2026, 10 ou outubro", arg)
	}

	year := now.Year()
	if yearText = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(yearText), "de ")); yearText != "" {
		y, err := strconv.Atoi(yearText)
		if err != nil || y < 2000 || y > 2100 {
			return period{}, fmt.Errorf("não entendi o ano %q", yearText)
		}
		year = y
	} else if time.Month(month) > now.Month() {
		year--
	}
	return monthPeriod(time.Date(year, time.Month(month), 1, 0, 0, 0, 0, loc)), nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}
