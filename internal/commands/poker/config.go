package poker

import (
	"fmt"

	"github.com/stempit/meshcore-go-bot/internal/config"
)

// cfg is the .poker config section (yaml key "poker"). Opt-in via Enabled
// (same convention as .words/.vote); int fields default at their use sites
// when unset/zero (see the getters below).
type cfg struct {
	Enabled        bool `yaml:"enabled"`          // opt-in: .poker stays off unless true
	StartingStack  int  `yaml:"starting_stack"`   // chips per player, default 200
	SmallBet       int  `yaml:"small_bet"`        // bet size preflop/flop, default 10
	BigBet         int  `yaml:"big_bet"`          // bet size turn/river, default 20
	MaxRaises      int  `yaml:"max_raises"`       // raises per street, default 3
	MaxPlayers     int  `yaml:"max_players"`      // table size, default 6 (2–6)
	JoinSec        int  `yaml:"join_sec"`         // initial join window, default 60
	TurnTimeoutSec int  `yaml:"turn_timeout_sec"` // auto check-else-fold, default 90
	DealPauseSec   int  `yaml:"deal_pause_sec"`   // join window between hands, default 20
	IdleTimeoutSec int  `yaml:"idle_timeout_sec"` // table idle close, default 900
}

var conf cfg

func (plugin) ConfigKey() string { return "poker" }

func (plugin) Configure(s config.Sectioner) error {
	if err := s.Section("poker", &conf); err != nil {
		return err
	}
	if conf.StartingStack < 0 || conf.SmallBet < 0 || conf.BigBet < 0 || conf.MaxRaises < 0 ||
		conf.MaxPlayers < 0 || conf.JoinSec < 0 || conf.TurnTimeoutSec < 0 ||
		conf.DealPauseSec < 0 || conf.IdleTimeoutSec < 0 {
		return fmt.Errorf("negative values are not allowed")
	}
	if conf.SmallBet > 0 && conf.BigBet > 0 && conf.BigBet < conf.SmallBet {
		return fmt.Errorf("big_bet (%d) must be >= small_bet (%d)", conf.BigBet, conf.SmallBet)
	}
	if conf.StartingStack > 0 && conf.SmallBet > 0 && conf.StartingStack < conf.SmallBet {
		return fmt.Errorf("starting_stack (%d) must cover small_bet (%d)", conf.StartingStack, conf.SmallBet)
	}
	if conf.MaxPlayers > 0 && (conf.MaxPlayers < 2 || conf.MaxPlayers > 6) {
		return fmt.Errorf("max_players must be within 2–6, got %d", conf.MaxPlayers)
	}
	return nil
}

func (plugin) Enabled() bool { return conf.Enabled }

// Blind/bet/limit getters with defaults applied when unset.
func startingStack() int {
	if conf.StartingStack > 0 {
		return conf.StartingStack
	}
	return 200
}

func smallBet() int {
	if conf.SmallBet > 0 {
		return conf.SmallBet
	}
	return 10
}

func bigBet() int {
	if conf.BigBet > 0 {
		return conf.BigBet
	}
	return 20
}

func blindSB() int { return smallBet() / 2 }
func blindBB() int { return smallBet() }

func maxRaises() int {
	if conf.MaxRaises > 0 {
		return conf.MaxRaises
	}
	return 3
}

func maxPlayers() int {
	if conf.MaxPlayers > 0 {
		return conf.MaxPlayers
	}
	return 6
}

func joinSec() int {
	if conf.JoinSec > 0 {
		return conf.JoinSec
	}
	return 60
}

func turnTimeoutSec() int {
	if conf.TurnTimeoutSec > 0 {
		return conf.TurnTimeoutSec
	}
	return 90
}

func dealPauseSec() int {
	if conf.DealPauseSec > 0 {
		return conf.DealPauseSec
	}
	return 20
}

func idleTimeoutSec() int {
	if conf.IdleTimeoutSec > 0 {
		return conf.IdleTimeoutSec
	}
	return 900
}
