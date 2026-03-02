package main

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Quit     key.Binding
	NextTab  key.Binding
	PrevTab  key.Binding
	Tab1     key.Binding
	Tab2     key.Binding
	Tab3     key.Binding
	Tab4     key.Binding
	Tab5     key.Binding
	Tab6     key.Binding
	Refresh  key.Binding
	Enter    key.Binding
	Back     key.Binding
	Delete   key.Binding
	Launch   key.Binding
	Help     key.Binding
	Filter   key.Binding
	Create   key.Binding
	Edit key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		NextTab:  key.NewBinding(key.WithKeys("tab"), key.WithHelp("Tab", "next tab")),
		PrevTab:  key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("S-Tab", "prev tab")),
		Tab1:     key.NewBinding(key.WithKeys("1")),
		Tab2:     key.NewBinding(key.WithKeys("2")),
		Tab3:     key.NewBinding(key.WithKeys("3")),
		Tab4:     key.NewBinding(key.WithKeys("4")),
		Tab5:     key.NewBinding(key.WithKeys("5")),
		Tab6:     key.NewBinding(key.WithKeys("6")),
		Refresh:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Enter:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "detail")),
		Back:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", "back")),
		Delete:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		Launch:   key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "launch")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Filter:   key.NewBinding(key.WithKeys("/")),
		Create:   key.NewBinding(key.WithKeys("c")),
		Edit: key.NewBinding(key.WithKeys("e")),
	}
}
