package event

// Form is what a question asks: ACP's elicitation/create form — itself MCP
// elicitation's restricted JSON Schema, a flat object of primitive fields —
// held as an ORDERED list, because the order is the order the human reads
// them in and a JSON object would lose it. The vocabulary is the standard
// one, so a harness's own question tool (arriving over ACP) and ask_human
// ask the same kinds of question:
//
//	string            free text (format, pattern, min/max length)
//	string + options  one of them                 (schema: oneOf/enum)
//	array  + options  any number of them          (schema: items.anyOf/enum)
//	number, integer   a number (minimum/maximum)
//	boolean           yes or no
//
// The answer is accept with a value per field (PingAnswered.Content), or
// decline with the human's note — "none of these fit" is a decline, not an
// option.
type Form struct {
	Fields []Field `json:"fields"`
}

// Field types.
const (
	FieldString  = "string"
	FieldNumber  = "number"
	FieldInteger = "integer"
	FieldBoolean = "boolean"
	FieldArray   = "array"
)

// Field is one property of the form. ID is the property name the answer is
// keyed by; Title is what the human reads (defaults to the ID).
type Field struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	// Options are the choices of a string (pick one) or an array (pick any).
	Options []Option `json:"options,omitempty"`
	// string
	Format    string `json:"format,omitempty"` // email|uri|date|date-time
	Pattern   string `json:"pattern,omitempty"`
	MinLength *int   `json:"min_length,omitempty"`
	MaxLength *int   `json:"max_length,omitempty"`
	// number, integer
	Minimum *float64 `json:"minimum,omitempty"`
	Maximum *float64 `json:"maximum,omitempty"`
	// array
	MinItems *int `json:"min_items,omitempty"`
	MaxItems *int `json:"max_items,omitempty"`
	// Default pre-fills the field: a string, number, bool, or (array) a list
	// of option ids.
	Default any `json:"default,omitempty"`
}

// Label is what the human reads for the field.
func (f Field) Label() string {
	if f.Title != "" {
		return f.Title
	}
	return f.ID
}

// Form answers. Accept carries Content; decline carries the human's note.
const (
	FormAccept  = "accept"
	FormDecline = "decline"
)
