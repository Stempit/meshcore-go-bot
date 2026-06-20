// Package allcommands imports every command plugin for its registration side
// effect. Import it (blank) from main so all commands self-register.
//
// Adding a command is: create the folder + add one blank import line here.
//
// Note: the ".help" index lists commands in registration order, which the Go
// runtime fixes as lexicographic import-path order (not the source order below),
// so the index is alphabetical by trigger.
package allcommands

import (
	_ "meshbot/internal/commands/bbs"
	_ "meshbot/internal/commands/claw"
	_ "meshbot/internal/commands/dice"
	_ "meshbot/internal/commands/eightball"
	_ "meshbot/internal/commands/hang"
	_ "meshbot/internal/commands/help"
	_ "meshbot/internal/commands/last"
	_ "meshbot/internal/commands/llm"
	_ "meshbot/internal/commands/news"
	_ "meshbot/internal/commands/num"
	_ "meshbot/internal/commands/path"
	_ "meshbot/internal/commands/ping"
	_ "meshbot/internal/commands/stat"
	_ "meshbot/internal/commands/ticket"
	_ "meshbot/internal/commands/top"
	_ "meshbot/internal/commands/trace"
	_ "meshbot/internal/commands/weather"
	_ "meshbot/internal/commands/words"
)
