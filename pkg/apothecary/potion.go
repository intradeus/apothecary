package apothecary

import (
	"intradeus/apothecary/pkg/plugins"
)

// struct
type Potion struct {
	Name        string
	Label       string
	FailStop    bool
	Incantation string
	Type        plugins.PluginInterface
}

// Constructor for the Potion struct
func NewPotion(someParameter string) *Potion {
	p := new(Potion)
	p.Name = someParameter
	return p
}
