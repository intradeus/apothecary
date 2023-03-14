package apothecary

// struct
type Grimoire struct {
	Name    string // Short name of the grimoire (init, start, stop, etc.)
	Rune    Rune
	context string
	potions []Potion // List of potions from the grimoire, in order
}

// Constructor for the Grimoire struct
func NewGrimoire(someParameter string) *Grimoire {
	p := new(Grimoire)
	p.Name = someParameter
	return p
}
