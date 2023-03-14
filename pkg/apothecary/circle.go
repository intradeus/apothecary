package apothecary

// struct
type Circle struct {
	Name      string
	Grimoires []Grimoire
}

// Constructor for the Circle struct
func NewCircle(someParameter string) *Circle {
	p := new(Circle)
	p.Name = someParameter
	return p
}
