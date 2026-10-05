package main

import (
	"charm.land/lipgloss/v2"

	"github.com/solev/hibernaut/internal/hibapi"
)

var (
	good    = lipgloss.NewStyle().Foreground(lipgloss.Green)
	waiting = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	failing = lipgloss.NewStyle().Foreground(lipgloss.Red)
)

var stateStyles = map[string]lipgloss.Style{
	"running":                    good,
	"ok":                         good,
	"paused":                     waiting,
	string(hibapi.CodexDead):     failing,
	string(hibapi.CodexStuck):    failing,
	"down":                       failing,
	string(hibapi.SecretRevoked): failing,
	string(hibapi.SecretFailed):  failing,
}

func (a *app) style() hibapi.Style {
	if !a.colour || a.json {
		return nil
	}
	return func(state string) string {
		if s, ok := stateStyles[state]; ok {
			return s.Render(state)
		}
		return state
	}
}
